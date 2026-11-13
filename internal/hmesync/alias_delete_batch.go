package hmesync

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"icloud-api/internal/apple"
	"icloud-api/internal/domain"
	"icloud-api/internal/store"
)

type aliasDeletionProgressKey struct{}

// WithAliasDeletionProgress reports each input item exactly once as DeleteAliases
// finishes it, including invalid, duplicate, failed and cancelled items. Reports
// may arrive out of input order and concurrently for different accounts; callers
// must synchronize shared state. The callback runs synchronously while account
// locks may be held, so it should be brief and must not re-enter account operations.
// A nil report disables reporting. DeleteAliases returns results in input order.
// During DeleteAliases, a callback panic stops new work and returns a typed
// upstream batch error without changing completed item results or invoking that
// item's callback a second time.
func WithAliasDeletionProgress(ctx context.Context, report func(AliasDeletionOutcome)) context.Context {
	return context.WithValue(ctx, aliasDeletionProgressKey{}, report)
}

// ReportAliasDeletionProgress invokes the context's callback, if any. Adapters
// implementing a batch with single DeleteAlias calls can use this after each
// completed item. DeleteAlias does not report by itself; DeleteAliases already
// reports exactly once per input. The concurrency/locking contract is documented
// on WithAliasDeletionProgress. No error causes or session data are logged here.
func ReportAliasDeletionProgress(ctx context.Context, outcome AliasDeletionOutcome) {
	if report, _ := ctx.Value(aliasDeletionProgressKey{}).(func(AliasDeletionOutcome)); report != nil {
		report(outcome)
	}
}

type aliasDeletionItem struct {
	index   int
	aliasID int64
}

type aliasDeletionGroup struct {
	accountID int64
	items     []aliasDeletionItem
}

// DeleteAliases holds the Apple operation lock for each account's entire group,
// sharing one validation, directory and rolling session. With AccountLockAcquirer,
// the shared account lock is released during recovery cooldowns so local settings
// remain responsive. Up to two account groups actively execute within a batch;
// a cooling account yields its execution slot so unrelated accounts can proceed.
// Each account's Apple operations remain strictly serial.
// Item failures do not discard already completed results or stop other accounts.
func (s *Service) DeleteAliases(ctx context.Context, aliasIDs []int64) ([]AliasDeletionOutcome, error) {
	if len(aliasIDs) == 0 {
		return nil, errors.New("at least one alias ID is required")
	}
	if _, enabled := ctx.Value(aliasDeletionRecoveryKey{}).(func(AliasDeletionWait)); enabled {
		var cancelRecovery context.CancelFunc
		ctx, cancelRecovery = context.WithTimeout(ctx, aliasDeletionRecoveryLimit)
		defer cancelRecovery()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var failureMu sync.Mutex
	var batchErr error
	fail := func(err error) {
		failureMu.Lock()
		if batchErr == nil {
			batchErr = err // Keep one redacted error, not thousands of panic causes.
		}
		failureMu.Unlock()
		cancel()
	}
	outcomes := make([]AliasDeletionOutcome, len(aliasIDs))
	completed := make([]bool, len(aliasIDs))
	complete := func(item aliasDeletionItem, err error) {
		if completed[item.index] {
			return
		}
		outcome := AliasDeletionOutcome{AliasID: item.aliasID, Err: err}
		outcomes[item.index] = outcome // Each index belongs to exactly one worker.
		completed[item.index] = true
		if err, panicked := runAliasDeletionSafely(func() error {
			ReportAliasDeletionProgress(ctx, outcome)
			return nil
		}); panicked {
			fail(err)
		}
	}
	deleteRepo, repoOK := s.repo.(AliasDeletionRepository)
	deleteClient, clientOK := s.client.(AliasDeletionClient)
	var unavailable error
	if !repoOK {
		unavailable = errors.New("alias deletion persistence is unavailable")
	} else if !clientOK {
		unavailable = errors.New("Apple alias deletion client is unavailable")
	}

	seen := make(map[int64]struct{}, len(aliasIDs))
	byAccount := make(map[int64]*aliasDeletionGroup)
	var groups []*aliasDeletionGroup
	for index, aliasID := range aliasIDs {
		item := aliasDeletionItem{index: index, aliasID: aliasID}
		if aliasID < 1 {
			complete(item, errors.New("alias ID must be positive"))
			continue
		}
		if _, exists := seen[aliasID]; exists {
			complete(item, errors.New("duplicate alias ID"))
			continue
		}
		seen[aliasID] = struct{}{}
		if err := ctx.Err(); err != nil {
			complete(item, err)
			continue
		}
		if unavailable != nil {
			complete(item, unavailable)
			continue
		}
		// This lookup is only for scheduling. Re-read ownership and pending state
		// under both locks before using the shared Apple session or directory.
		var alias domain.Alias
		err, panicked := runAliasDeletionSafely(func() error {
			var err error
			alias, err = deleteRepo.GetAlias(ctx, aliasID)
			return err
		})
		if panicked {
			fail(err)
		}
		if err != nil {
			complete(item, err)
			continue
		}
		if alias.AccountID < 1 {
			complete(item, errors.New("alias account ID must be positive"))
			continue
		}
		group := byAccount[alias.AccountID]
		if group == nil {
			group = &aliasDeletionGroup{accountID: alias.AccountID}
			byAccount[alias.AccountID] = group
			groups = append(groups, group)
		}
		group.items = append(group.items, item)
	}

	// Each account keeps its own suspended flow (including its rolling session
	// and recovery budget). Limit active execution, not the number of flows:
	// otherwise two cooling accounts would block every healthy queued account.
	slots := make(chan struct{}, 2)
	var workers sync.WaitGroup
	for _, group := range groups {
		workers.Go(func() {
			// Recover inside the goroutine: an HTTP caller's recovery boundary
			// cannot catch worker panics. Defers release both account locks first.
			if err, panicked := runAliasDeletionSafely(func() error {
				err, panicked := runAliasDeletionSafely(func() error {
					return s.withAliasDeletionAccount(ctx, group.accountID, slots, func(waitCooldown func(context.Context, time.Duration) error) error {
						batch := aliasDeletionBatch{s: s, repo: deleteRepo, client: deleteClient, accountID: group.accountID, waitCooldown: waitCooldown}
						batch.recovery = newAliasDeletionRecovery(ctx)
						for _, item := range group.items {
							complete(item, batch.delete(ctx, item.aliasID))
						}
						return nil
					})
				})
				if panicked {
					fail(err)
				}
				for _, item := range group.items {
					if !completed[item.index] {
						complete(item, err)
					}
				}
				return nil
			}); panicked {
				fail(err)
			}
		})
	}
	workers.Wait()
	// A worker infrastructure panic can leave an item/group unclaimed. Finalize
	// it after all workers stop, preserving every result and callback already sent.
	for index, aliasID := range aliasIDs {
		if !completed[index] {
			if batchErr == nil {
				fail(wrapError(CodeUpstreamError, ErrUpstream, errors.New("alias deletion worker stopped before completion")))
			}
			complete(aliasDeletionItem{index: index, aliasID: aliasID}, batchErr)
		}
	}
	return outcomes, batchErr
}

func runAliasDeletionSafely(operation func() error) (err error, panicked bool) {
	defer func() {
		if recover() != nil {
			// Never include the panic value: transports/adapters can panic with
			// response bodies, session tokens or other credential-bearing data.
			err = wrapError(CodeUpstreamError, ErrUpstream, errors.New("alias deletion operation panicked"))
			panicked = true
		}
	}()
	return operation(), false
}

// aliasDeletionExecutionSlot is confined to one account flow. A nil pool is
// used by single deletion, which has no batch-level concurrency limit.
type aliasDeletionExecutionSlot struct {
	pool chan struct{}
	held bool
}

func (slot *aliasDeletionExecutionSlot) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if slot.pool == nil {
		return nil
	}
	select {
	case slot.pool <- struct{}{}:
		slot.held = true
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (slot *aliasDeletionExecutionSlot) release() {
	if slot.held {
		<-slot.pool
		slot.held = false
	}
}

func (s *Service) withAliasDeletionAccount(ctx context.Context, accountID int64, slots chan struct{}, operation func(func(context.Context, time.Duration) error) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	releaseOperation, err := s.acquireOperation(ctx, accountID)
	if err != nil {
		return err
	}
	defer releaseOperation()
	// A second batch waiting on this account's Apple operation must not take
	// an execution slot needed by that batch's unrelated healthy accounts.
	slot := aliasDeletionExecutionSlot{pool: slots}
	if err := slot.acquire(ctx); err != nil {
		return err
	}
	defer slot.release()
	waitWithoutSlot := func(waitCtx context.Context, delay time.Duration) error {
		slot.release()
		if err := s.waitAliasDeletion(waitCtx, delay); err != nil {
			return err
		}
		return slot.acquire(waitCtx)
	}
	guarded := func(waitCooldown func(context.Context, time.Duration) error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return operation(waitCooldown)
	}
	if acquirer, ok := s.locker.(AccountLockAcquirer); ok {
		releaseAccount, err := acquirer.AcquireAccountLock(ctx, accountID)
		if err != nil {
			return err
		}
		defer func() {
			if releaseAccount != nil {
				releaseAccount()
			}
		}()
		return guarded(func(waitCtx context.Context, delay time.Duration) error {
			// Responses have already checkpointed the rolling Apple session.
			// Keep its operation lock, but let local settings use the account
			// boundary while no remote request or publication is in flight.
			releaseAccount()
			releaseAccount = nil
			if err := waitWithoutSlot(waitCtx, delay); err != nil {
				return err
			}
			var err error
			releaseAccount, err = acquirer.AcquireAccountLock(waitCtx, accountID)
			return err
		})
	}
	// Callback-only embedders do not expose a safe release/reacquire boundary.
	return s.locker.WithAccountLock(ctx, accountID, func() error { return guarded(waitWithoutSlot) })
}

// aliasDeletionBatch is confined to one worker under the Apple operation lock.
// Requests and local publication also hold the shared account lock. Recovery
// invalidates the directory and rechecks local identity/pending state after a
// cooldown, since settings can change while the shared account lock is released.
type aliasDeletionBatch struct {
	s            *Service
	repo         AliasDeletionRepository
	client       AliasDeletionClient
	accountID    int64
	appleCtx     context.Context
	account      domain.Account
	record       domain.AppleWebSession
	session      apple.Session
	ready        bool
	stopped      error
	directory    map[string]apple.Alias // Includes foreign aliases for ownership checks.
	valid        bool
	recovery     *aliasDeletionRecovery // Only opt-in DeleteAliases, never DeleteAlias.
	waitCooldown func(context.Context, time.Duration) error
}

func (b *aliasDeletionBatch) delete(ctx context.Context, aliasID int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	alias, err := b.repo.GetAlias(ctx, aliasID)
	if err != nil {
		return err
	}
	if alias.AccountID != b.accountID {
		return wrapError(CodeAccountChanged, ErrAccountChanged, nil)
	}
	if !alias.Enabled && strings.TrimSpace(alias.LastSyncError) == domain.AppleAliasConfirmationPending {
		return wrapError(CodeAliasConfirmationPending, ErrAliasConfirmationPending, store.ErrAliasConfirmationPending)
	}
	if b.stopped != nil {
		return b.stopped
	}
	if b.recovery != nil {
		if b.recovery.aliasID == aliasID && b.recovery.address != domain.NormalizeEmail(alias.Address) {
			b.stopped = wrapError(CodeAccountChanged, ErrAccountChanged, nil)
			return b.stopped
		}
		b.recovery.aliasID = aliasID
		b.recovery.address = domain.NormalizeEmail(alias.Address)
	}
	account, err := b.s.repo.GetAccount(ctx, b.accountID)
	if err != nil {
		return err
	}
	if b.appleCtx == nil {
		b.appleCtx, err = b.s.accountProxyForAccount(ctx, account)
		if err != nil {
			return err
		}
	}
	ctx = b.appleCtx
	if b.ready && (!sameIdentity(identityOf(account), identityOf(b.account)) || !sameAccountProxy(account.Proxy, b.account.Proxy)) {
		b.stopped = wrapError(CodeAccountChanged, ErrAccountChanged, nil)
		return b.stopped
	}
	if !b.ready {
		b.account = account
		b.account.Proxy = cloneAccountProxy(account.Proxy)
		if err := b.initialize(ctx); err != nil {
			if b.stopped == nil {
				b.stopped = err
			}
			return err
		}
	}

	fresh := !b.valid
	if fresh {
		if err := b.refresh(ctx); err != nil {
			return err
		}
	}
	remote, found, err := b.find(alias.Address)
	if err != nil {
		return err
	}
	if !found && !fresh {
		// Absence in an earlier item's cache is not evidence for local deletion.
		// Recheck now, including aliases absent from the initial directory.
		if err := b.refresh(ctx); err != nil {
			return err
		}
		remote, found, err = b.find(alias.Address)
		if err != nil {
			return err
		}
	}
	if !found {
		return b.s.deleteLocalAliasAfterApple(ctx, b.repo, aliasID)
	}
	remoteID := strings.TrimSpace(remote.AnonymousID)
	if remoteID == "" {
		return wrapError(CodeUpstreamError, ErrUpstream, errors.New("Apple alias omitted its remote ID"))
	}
	if remote.IsActive {
		if err := b.beforeRequest(ctx, "deactivate"); err != nil {
			return err
		}
		returned, remoteErr := b.client.DeactivateAlias(b.appleCtx, b.session, remoteID)
		mapped, fatal := b.acceptResponse(ctx, returned, remoteErr)
		if mapped != nil {
			b.valid = false
			if fatal {
				return mapped
			}
			if b.canRecover(mapped) {
				if err := b.recoverThrottle(ctx, "deactivate", mapped); err != nil {
					return err
				}
				// Re-enter with an invalid directory, not by replaying the mutation.
				// The shared three-recovery budget bounds this re-entry. A fresh read
				// decides whether deactivation succeeded and supplies the current ID.
				return b.delete(ctx, aliasID)
			}
			reconciled, present, err := b.reconcile(ctx, alias.Address)
			if err != nil {
				return errors.Join(err, mapped)
			}
			if !present {
				return b.s.deleteLocalAliasAfterApple(ctx, b.repo, aliasID)
			}
			if reconciled.IsActive || b.stopped != nil {
				return mapped
			}
			remoteID = strings.TrimSpace(reconciled.AnonymousID)
			if remoteID == "" {
				return errors.Join(mapped, wrapError(CodeUpstreamError, ErrUpstream,
					errors.New("Apple alias omitted its remote ID")))
			}
		} else {
			remote.IsActive = false
			b.directory[domain.NormalizeEmail(alias.Address)] = remote
		}
	}
	// Even when read-only reconciliation finishes after cancellation, do not
	// start another irreversible request. Checkpoints/local confirmed cleanup
	// still use their bounded, cancellation-independent persistence contexts.
	if err := b.beforeRequest(ctx, "delete"); err != nil {
		return err
	}
	returned, remoteErr := b.client.DeleteAlias(b.appleCtx, b.session, remoteID)
	mapped, fatal := b.acceptResponse(ctx, returned, remoteErr)
	if mapped != nil {
		b.valid = false
		if fatal {
			return mapped
		}
		if b.canRecover(mapped) {
			if err := b.recoverThrottle(ctx, "delete", mapped); err != nil {
				return err
			}
			return b.delete(ctx, aliasID)
		}
		_, present, err := b.reconcile(ctx, alias.Address)
		if err != nil {
			return errors.Join(err, mapped)
		}
		if present {
			return mapped
		}
	} else {
		delete(b.directory, domain.NormalizeEmail(alias.Address))
	}
	return b.s.deleteLocalAliasAfterApple(ctx, b.repo, aliasID)
}

func (b *aliasDeletionBatch) initialize(ctx context.Context) error {
	var err error
	b.record, b.session, err = b.s.loadSession(ctx, b.accountID)
	if err != nil {
		if errors.Is(err, ErrSessionExpired) {
			return b.expire(ctx, err)
		}
		return err
	}
	for {
		if err := b.beforeRequest(ctx, "validate"); err != nil {
			return err
		}
		returned, remoteErr := b.s.client.Validate(b.appleCtx, b.session)
		if err, _ := b.acceptResponse(ctx, returned, remoteErr); err != nil {
			if !b.canRecover(err) {
				return err
			}
			if err := b.recoverThrottle(ctx, "validate", err); err != nil {
				return err
			}
			continue // Keep the rolling session; never reload its initial record.
		}
		break
	}
	b.ready = true
	return nil
}

// acceptResponse advances memory and the durable checkpoint on both successful
// and failed requests. A failed checkpoint or identity check stops the account;
// no subsequent item reloads an older session and sends it back to Apple.
func (b *aliasDeletionBatch) acceptResponse(ctx context.Context, returned apple.Session, remoteErr error) (error, bool) {
	mapped := mapAppleError(remoteErr, false)
	if b.recovery != nil && apple.IsRateLimited(remoteErr) {
		// A 429 header is authoritative even when reading its body timed out.
		// mapAppleError preserves context-shaped transport causes for legacy
		// callers; recovery must retain the throttle and its Retry-After instead.
		// Explicit session/account-fatal classifications still take precedence.
		switch {
		case errors.Is(remoteErr, apple.ErrInvalidSession):
			mapped = wrapError(CodeSessionExpired, ErrSessionExpired, remoteErr)
		case errors.Is(remoteErr, apple.ErrAuthentication):
			mapped = wrapError(CodeCredentialsInvalid, ErrCredentialsInvalid, remoteErr)
		case errors.Is(remoteErr, apple.ErrTermsRequired):
			mapped = wrapError(CodeAccountActionRequired, ErrAccountActionRequired, remoteErr)
		case errors.Is(remoteErr, apple.ErrTwoFactorCode):
			mapped = wrapError(CodeVerificationInvalid, ErrVerificationInvalid, remoteErr)
		default:
			mapped = wrapError(CodeRateLimited, ErrRateLimited, remoteErr)
		}
	}
	if errors.Is(mapped, ErrSessionExpired) {
		return b.expire(ctx, mapped), true
	}
	prepared, err := b.s.prepareAliasDeletionSession(b.record, returned, b.session)
	if err == nil {
		b.session = prepared
		err = b.s.checkpointAliasDeletionSession(ctx, b.accountID, b.session)
	}
	if err != nil {
		b.stopped = errors.Join(err, mapped)
		return b.stopped, true
	}
	if b.recovery != nil {
		b.recovery.nextServerDelay = apple.RetryDelay(remoteErr)
		b.recovery.lastError = remoteErr
	}
	if (errors.Is(mapped, ErrRateLimited) && b.recovery == nil) || errors.Is(mapped, ErrCredentialsInvalid) || errors.Is(mapped, ErrAccountActionRequired) {
		// Reconcile an already-started mutation, but do not hammer a blocked
		// account with the rest of the batch. Other accounts remain independent.
		b.stopped = mapped
	}
	return mapped, false
}

func (b *aliasDeletionBatch) expire(ctx context.Context, mapped error) error {
	b.valid = false
	b.session = apple.Session{}
	b.stopped = b.s.expireAliasDeletionSession(ctx, b.accountID, mapped)
	result := b.stopped
	if result == mapped {
		// Successful cleanup leaves later items with the same missing-session
		// classification as a subsequent single deletion, without reloading it.
		b.stopped = wrapError(CodeLoginRequired, ErrLoginRequired, store.ErrNotFound)
	}
	return result
}

func (b *aliasDeletionBatch) refresh(ctx context.Context) error {
	return b.refreshDirectory(ctx, false)
}

func (b *aliasDeletionBatch) refreshDirectory(ctx context.Context, reconcile bool) error {
	b.valid = false
	var directory apple.ListResult
	for {
		// Consume an existing server hint under the original job context before
		// detaching a bounded reconciliation read from cancellation below.
		if err := b.waitServerDelay(ctx, "list"); err != nil {
			return err
		}
		err := func() error {
			requestContext := ctx
			if reconcile {
				// Preserve bounded, cancellation-independent confirmation of an
				// ambiguous mutation. Only the request is detached, never a cooldown.
				var cancel context.CancelFunc
				requestContext, cancel = context.WithTimeout(context.WithoutCancel(b.appleCtx), aliasDeletePersistTimeout)
				defer cancel()
			}
			if err := b.beforeRequest(requestContext, "list"); err != nil {
				return err
			}
			var returned apple.Session
			var remoteErr error
			directory, returned, remoteErr = b.s.client.ListAliases(requestContext, b.session)
			err, _ := b.acceptResponse(requestContext, returned, remoteErr)
			return err
		}()
		if err == nil {
			break
		}
		if !b.canRecover(err) {
			return err
		}
		// Use the original job context and shared recovery budget, outside the
		// short request deadline. No reconciliation request can skip this wait.
		if err := b.recoverThrottle(ctx, "list", err); err != nil {
			return err
		}
	}
	if _, _, err := filterAliases(directory, b.account.Email); err != nil {
		return err
	}
	b.directory = make(map[string]apple.Alias, len(directory.Aliases))
	for _, remote := range directory.Aliases {
		b.directory[domain.NormalizeEmail(remote.HME)] = remote
	}
	b.valid = true
	return nil
}

func (b *aliasDeletionBatch) find(address string) (apple.Alias, bool, error) {
	remote, found := b.directory[domain.NormalizeEmail(address)]
	if found && !sameEmail(remote.ForwardToEmail, b.account.Email) {
		return apple.Alias{}, false, wrapError(CodeAccountMismatch, ErrAccountMismatch, nil)
	}
	return remote, found, nil
}

func (b *aliasDeletionBatch) reconcile(ctx context.Context, address string) (apple.Alias, bool, error) {
	// Refresh the whole directory, not only the current alias: another cached
	// alias may also have changed. A failed read leaves the cache invalid and
	// still checkpoints any returned session for the next item's fresh read.
	if err := b.refreshDirectory(ctx, true); err != nil {
		return apple.Alias{}, false, err
	}
	return b.find(address)
}
