package hmesync

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"sync"
	"time"

	"icloud-api/internal/apple"
	"icloud-api/internal/domain"
)

// AliasRetirementTarget retains the original identity even after the local
// alias has disappeared. Reconciliation never reloads or mutates that alias.
type AliasRetirementTarget struct {
	AliasID   int64
	AccountID int64
	Address   string
}

// AliasRetirementObservation only proves absence when Err is nil.
type AliasRetirementObservation struct {
	AliasID int64
	Present bool
	Err     error
}

// ReconcileRetirementAliases reads one freshly validated, complete directory
// per account under the same locks as deletion. It never imports or deletes
// aliases. Throttles and failed reads stop that account without an immediate
// reread, preserving Apple's Retry-After in the typed error chain.
func (s *Service) ReconcileRetirementAliases(ctx context.Context, targets []AliasRetirementTarget) ([]AliasRetirementObservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	observations := make([]AliasRetirementObservation, len(targets))
	byAccount := make(map[int64][]int)
	seen := make(map[int64]struct{}, len(targets))
	for index, target := range targets {
		observations[index].AliasID = target.AliasID
		address := domain.NormalizeEmail(target.Address)
		parsed, addressErr := mail.ParseAddress(address)
		switch {
		case target.AliasID < 1 || target.AccountID < 1:
			observations[index].Err = errors.New("retirement alias and account IDs must be positive")
		case addressErr != nil || address == "" || parsed.Name != "" || domain.NormalizeEmail(parsed.Address) != address:
			observations[index].Err = errors.New("retirement alias address is invalid")
		default:
			if _, duplicate := seen[target.AliasID]; duplicate {
				observations[index].Err = errors.New("duplicate retirement alias ID")
				continue
			}
			seen[target.AliasID] = struct{}{}
			byAccount[target.AccountID] = append(byAccount[target.AccountID], index)
		}
	}

	slots := make(chan struct{}, 2)
	var workers sync.WaitGroup
	var failureMu sync.Mutex
	var batchErr error
	for accountID, indexes := range byAccount {
		workers.Go(func() {
			err, panicked := runAliasDeletionSafely(func() error {
				return s.withAliasDeletionAccount(ctx, accountID, slots, func(_ func(context.Context, time.Duration) error) error {
					batch, err := s.retirementDirectory(ctx, accountID)
					if err != nil {
						return err
					}
					for _, index := range indexes {
						_, present, err := batch.find(targets[index].Address)
						observations[index].Present = present
						observations[index].Err = retirementObservationError(err)
					}
					return nil
				})
			})
			if err != nil {
				err = retirementObservationError(err)
				for _, index := range indexes {
					observations[index].Present = false
					observations[index].Err = err
				}
			}
			if panicked {
				failureMu.Lock()
				if batchErr == nil {
					batchErr = err
				}
				failureMu.Unlock()
			}
		})
	}
	workers.Wait()
	return observations, batchErr
}

func (s *Service) retirementDirectory(ctx context.Context, accountID int64) (*aliasDeletionBatch, error) {
	account, err := s.repo.GetAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	appleCtx, err := s.accountProxyForAccount(ctx, account)
	if err != nil {
		return nil, err
	}
	ctx = appleCtx
	if !account.Enabled {
		return nil, wrapError(CodeAccountDisabled, ErrAccountDisabled, nil)
	}
	if account.ID != accountID || !sameIdentity(identityOf(account), identityOf(account)) {
		return nil, wrapError(CodeAccountMismatch, ErrAccountMismatch, nil)
	}
	if err := s.retirementReadCooldown(accountID); err != nil {
		return nil, err
	}
	// Leave recovery nil even if the caller enabled deletion recovery. Its
	// beforeRequest requires a surviving local alias, unlike retirement reads.
	batch := &aliasDeletionBatch{s: s, accountID: accountID, account: account, appleCtx: appleCtx}
	batch.record, batch.session, err = s.loadSession(ctx, accountID)
	if err != nil {
		if errors.Is(err, ErrSessionExpired) {
			err = batch.expire(ctx, err)
		}
		return nil, err
	}
	if batch.record.AccountID != accountID || strings.TrimSpace(batch.record.AppleID) == "" {
		return nil, wrapError(CodeAccountMismatch, ErrAccountMismatch, nil)
	}
	checkAccount := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := s.repo.GetAccount(ctx, accountID)
		if err != nil {
			return err
		}
		if !current.Enabled {
			return wrapError(CodeAccountDisabled, ErrAccountDisabled, nil)
		}
		if current.ID != accountID || !sameIdentity(identityOf(current), identityOf(account)) || !sameAccountProxy(current.Proxy, account.Proxy) {
			return wrapError(CodeAccountChanged, ErrAccountChanged, nil)
		}
		return nil
	}
	accept := func(returned apple.Session, remoteErr error) error {
		s.rememberRetirementReadCooldown(accountID, remoteErr)
		prepared, err := s.prepareAliasDeletionSession(batch.record, returned, batch.session)
		if err != nil {
			return errors.Join(err, mapAppleError(remoteErr, false))
		}
		if strings.TrimSpace(batch.session.DSID) != "" {
			if err := validateSessionDSID(batch.session.DSID, prepared); err != nil {
				return errors.Join(err, mapAppleError(remoteErr, false))
			}
		}
		err, _ = batch.acceptResponse(ctx, prepared, remoteErr)
		return err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	validated, remoteErr := s.client.Validate(appleCtx, batch.session)
	if err := accept(validated, remoteErr); err != nil {
		return nil, err
	}
	if err := checkAccount(); err != nil {
		return nil, err
	}
	directory, returned, remoteErr := s.client.ListAliases(appleCtx, batch.session)
	if err := accept(returned, remoteErr); err != nil {
		return nil, err
	}
	if err := checkAccount(); err != nil {
		return nil, err
	}
	if _, _, err := filterAliases(directory, account.Email); err != nil {
		return nil, err
	}
	batch.directory = make(map[string]apple.Alias, len(directory.Aliases))
	for _, remote := range directory.Aliases {
		batch.directory[domain.NormalizeEmail(remote.HME)] = remote
	}
	batch.valid = true
	s.operationMu.Lock()
	delete(s.retirementReadCooldowns, accountID)
	s.operationMu.Unlock()
	return batch, nil
}

// Account operation locks serialize reads; operationMu only guards this small
// independent map so cooling retirement reads never affect creation channels.
func (s *Service) retirementReadCooldown(accountID int64) error {
	now := s.now()
	s.operationMu.Lock()
	until := s.retirementReadCooldowns[accountID]
	if !until.After(now) {
		delete(s.retirementReadCooldowns, accountID)
	}
	s.operationMu.Unlock()
	if !until.After(now) {
		return nil
	}
	return wrapError(CodeRateLimited, ErrRateLimited, &apple.Error{
		Op: "reconcile Hide My Email aliases", Kind: apple.ErrService,
		StatusCode: 429, RetryAfter: until.Sub(now),
	})
}

func (s *Service) rememberRetirementReadCooldown(accountID int64, err error) {
	delay := apple.RetryDelay(err)
	if apple.IsRateLimited(err) {
		delay = max(delay, time.Minute)
	}
	if delay <= 0 {
		return
	}
	until := s.now().Add(delay)
	s.operationMu.Lock()
	if s.retirementReadCooldowns == nil {
		s.retirementReadCooldowns = make(map[int64]time.Time)
	}
	if until.After(s.retirementReadCooldowns[accountID]) {
		s.retirementReadCooldowns[accountID] = until
	}
	s.operationMu.Unlock()
}

// Error strings are public diagnostics; preserve typed causes for callers
// without including transport response bodies or credential-bearing text.
func retirementObservationError(err error) error {
	if err == nil || err == context.Canceled || err == context.DeadlineExceeded {
		return err
	}
	if code := Code(err); code != "" {
		return wrapError(code, nil, err)
	}
	return wrapError(CodeUpstreamError, ErrUpstream, err)
}
