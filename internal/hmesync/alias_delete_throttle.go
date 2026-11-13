package hmesync

import (
	"context"
	"errors"
	"strings"
	"time"

	"icloud-api/internal/apple"
	"icloud-api/internal/domain"
	"icloud-api/internal/store"
)

// CodeBatchDeferred identifies items which were not attempted after an account's
// recovery budget was exhausted. Its cause can still identify the Apple throttle.
const CodeBatchDeferred = "APPLE_BATCH_DEFERRED"

var ErrBatchDeferred = errors.New("Apple batch item deferred without execution")

const (
	aliasDeletionRequestInterval = time.Second
	aliasDeletionMaxRecoveries   = 3
	aliasDeletionRecoveryLimit   = 2 * time.Hour
)

// AliasDeletionWait is transient account wait state, not an item outcome. Attempt
// counts throttle recoveries across the account group (1..3); it is zero before
// any throttle recovery if only a server-requested reconciliation delay applies.
type AliasDeletionWait struct {
	AccountID   int64
	AliasID     int64
	Operation   string // validate, list, deactivate or delete
	RetryAt     time.Time
	Attempt     int
	MaxAttempts int
	Waiting     bool
	HTTPStatus  int
	ServiceCode string
}

type aliasDeletionRecoveryKey struct{}

// WithAliasDeletionRecovery opts DeleteAliases into paced, bounded throttle
// recovery. DeleteAlias and batches without this context retain their original
// semantics. A nil report still enables recovery, without wait notifications.
// Reports run synchronously while account locks may be held and can arrive
// concurrently for different accounts: callers must synchronize shared state
// and must not re-enter account operations. Every wait start is paired with
// Waiting=false on exit, including cancellation. No item result is reported
// merely because it is waiting.
func WithAliasDeletionRecovery(ctx context.Context, report func(AliasDeletionWait)) context.Context {
	return context.WithValue(ctx, aliasDeletionRecoveryKey{}, report)
}

// WithAliasDeletionWaiter replaces the context-aware timer for opt-in batch
// pacing and recovery. It is intended for tests, usually together with WithClock.
// A nil waiter keeps the production timer. The waiter must respect cancellation;
// returning nil means the requested delay has elapsed (virtual time is allowed).
// It may be called concurrently for different accounts.
func WithAliasDeletionWaiter(wait func(context.Context, time.Duration) error) Option {
	return func(service *Service) {
		service.aliasDeletionWaiter = wait
	}
}

// Each instance belongs to one locked account group. Neither successful reads
// nor advancing to a new alias resets attempts: a persistent mutation throttle
// must not get another three retries for every input item.
type aliasDeletionRecovery struct {
	report          func(AliasDeletionWait)
	aliasID         int64
	address         string
	attempts        int
	lastRequest     time.Time
	requested       bool
	nextServerDelay time.Duration
	lastError       error
}

func newAliasDeletionRecovery(ctx context.Context) *aliasDeletionRecovery {
	report, enabled := ctx.Value(aliasDeletionRecoveryKey{}).(func(AliasDeletionWait))
	if !enabled {
		return nil
	}
	return &aliasDeletionRecovery{report: report}
}

func (s *Service) waitAliasDeletion(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay <= 0 {
		return nil
	}
	if s.aliasDeletionWaiter != nil {
		if err := s.aliasDeletionWaiter(ctx, delay); err != nil {
			return err
		}
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

// beforeRequest is shared by validation, reads, mutations and reconciliation.
// Pacing alone is not a throttle event and does not publish wait status.
func (b *aliasDeletionBatch) beforeRequest(ctx context.Context, operation string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r := b.recovery
	if r == nil {
		return nil
	}
	if err := b.waitServerDelay(ctx, operation); err != nil {
		return err
	}
	if r.requested {
		if err := b.s.waitAliasDeletion(ctx, aliasDeletionRequestInterval-b.s.now().Sub(r.lastRequest)); err != nil {
			return err
		}
	}
	// A long cooldown must not make earlier identity/pending checks stale.
	account, err := b.s.repo.GetAccount(ctx, b.accountID)
	if err != nil {
		return err
	}
	if !sameIdentity(identityOf(account), identityOf(b.account)) || !sameAccountProxy(account.Proxy, b.account.Proxy) {
		b.stopped = wrapError(CodeAccountChanged, ErrAccountChanged, nil)
		return b.stopped
	}
	alias, err := b.repo.GetAlias(ctx, r.aliasID)
	if err != nil {
		return err
	}
	if alias.AccountID != b.accountID || domain.NormalizeEmail(alias.Address) != r.address {
		b.stopped = wrapError(CodeAccountChanged, ErrAccountChanged, nil)
		return b.stopped
	}
	if !alias.Enabled && strings.TrimSpace(alias.LastSyncError) == domain.AppleAliasConfirmationPending {
		return wrapError(CodeAliasConfirmationPending, ErrAliasConfirmationPending, store.ErrAliasConfirmationPending)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.lastRequest = b.s.now()
	r.requested = true
	return nil
}

func (b *aliasDeletionBatch) canRecover(err error) bool {
	// A joined rate-limit cause must never override checkpoint, identity or
	// session failures. Those set stopped and are permanent for this group.
	return b.recovery != nil && b.stopped == nil && Code(err) == CodeRateLimited
}

func (b *aliasDeletionBatch) recoverThrottle(ctx context.Context, operation string, cause error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r := b.recovery
	if r.attempts >= aliasDeletionMaxRecoveries {
		// Return the actual throttle to this item, but explicitly distinguish all
		// subsequent unattempted items. Never clear a stopped account to retry.
		b.stopped = wrapError(CodeBatchDeferred, ErrBatchDeferred, cause)
		return cause
	}
	r.attempts++
	delay := max(time.Minute<<uint(r.attempts-1), r.nextServerDelay)
	r.nextServerDelay = 0
	b.valid = false
	return b.reportWait(ctx, operation, cause, delay)
}

// A Retry-After on a non-throttle error does not permit mutation retries, but
// even its read-only reconciliation (or the next item's read) must respect the
// hint. This shares the same wait path without granting another recovery budget.
func (b *aliasDeletionBatch) waitServerDelay(ctx context.Context, operation string) error {
	r := b.recovery
	if r == nil || r.nextServerDelay <= 0 {
		return nil
	}
	delay := r.nextServerDelay
	r.nextServerDelay = 0
	return b.reportWait(ctx, operation, r.lastError, delay)
}

func (b *aliasDeletionBatch) reportWait(ctx context.Context, operation string, cause error, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r := b.recovery
	state := AliasDeletionWait{
		AccountID: b.accountID, AliasID: r.aliasID, Operation: operation,
		RetryAt: b.s.now().Add(delay), Attempt: r.attempts,
		MaxAttempts: aliasDeletionMaxRecoveries, Waiting: true,
	}
	var upstream *apple.Error
	if errors.As(cause, &upstream) && upstream != nil {
		state.HTTPStatus, state.ServiceCode = upstream.StatusCode, upstream.ServiceCode
	}
	if r.report != nil {
		defer func() {
			state.Waiting = false
			r.report(state)
		}()
		r.report(state)
	}
	err := b.waitCooldown(ctx, delay)
	if err != nil && ctx.Err() == nil && b.stopped == nil {
		// A failing injected waiter must not let the next item bypass a cooldown.
		b.stopped = err
	}
	return err
}
