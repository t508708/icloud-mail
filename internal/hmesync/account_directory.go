package hmesync

import (
	"context"
	"errors"
	"net/http"

	"icloud-api/internal/apple"
	"icloud-api/internal/domain"
	"icloud-api/internal/store"
)

type accountDirectoryLister interface {
	ListAccountAliases(context.Context, apple.AccountSession) (apple.ListResult, apple.AccountSession, error)
}

type syncDirectory struct {
	list        apple.ListResult
	web         apple.Session
	fromAccount bool
}

// Read directories before acquiring the publication lock. Explicit Web auth
// failures and an unresolved 421 permit the independent Account read channel.
func (s *Service) readSyncDirectory(ctx context.Context, account domain.Account) (syncDirectory, error) {
	appleCtx, err := s.accountProxyForAccount(ctx, account)
	if err != nil {
		return syncDirectory{}, err
	}
	ctx = appleCtx
	record, session, err := s.loadSession(ctx, account.ID)
	if err != nil {
		if errors.Is(err, ErrSessionExpired) {
			if expireErr := s.removeSessionForIdentity(ctx, account.ID, identityOf(account)); expireErr != nil {
				return syncDirectory{}, errors.Join(err, expireErr)
			}
		}
		if errors.Is(err, ErrLoginRequired) || errors.Is(err, ErrSessionExpired) {
			return s.readAccountDirectoryForSync(ctx, account, err)
		}
		return syncDirectory{}, err
	}
	validated, err := s.client.Validate(ctx, session)
	if err != nil {
		mapped := mapAppleError(err, false)
		if explicitWebFailure(err, apple.ErrInvalidSession) {
			if expireErr := s.removeSessionForIdentity(ctx, account.ID, identityOf(account)); expireErr != nil {
				return syncDirectory{}, errors.Join(mapped, expireErr)
			}
			return s.readAccountDirectoryForSync(ctx, account, mapped)
		}
		// A failed trusted-token exchange (421) leaves the Web checkpoint intact.
		// The independent Account directory may still serve this read.
		var upstream *apple.Error
		if errors.As(err, &upstream) && upstream.StatusCode == http.StatusMisdirectedRequest &&
			!apple.IsRateLimited(err) && ctx.Err() == nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			return s.readAccountDirectoryForSync(ctx, account, mapped)
		}
		return syncDirectory{}, mapped
	}
	normalizeSession(&validated, record.AppleID, session.Region, s.now())
	list, updated, err := s.client.ListAliases(ctx, validated)
	if err != nil {
		mapped := mapAppleError(err, false)
		if explicitWebFailure(err, apple.ErrHMEAuthentication) {
			if checkpointErr := s.preserveValidatedDirectorySession(ctx, account, record, session, validated, false); checkpointErr != nil {
				return syncDirectory{}, errors.Join(wrapPersistenceError(checkpointErr), mapped)
			}
			return s.readAccountDirectoryForSync(ctx, account, mapped)
		}
		if explicitWebFailure(err, apple.ErrInvalidSession) {
			if expireErr := s.removeSessionForIdentity(ctx, account.ID, identityOf(account)); expireErr != nil {
				return syncDirectory{}, errors.Join(mapped, expireErr)
			}
		}
		return syncDirectory{}, mapped
	}
	normalizeSession(&updated, record.AppleID, validated.Region, s.now())
	return syncDirectory{list: list, web: updated}, nil
}

func explicitWebFailure(err, kind error) bool {
	if !errors.Is(err, kind) || apple.IsRateLimited(err) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var upstream *apple.Error
	if errors.As(err, &upstream) && (upstream.StatusCode == http.StatusTooManyRequests || upstream.StatusCode >= 500) {
		return false
	}
	return true
}

func (s *Service) readAccountDirectoryForSync(ctx context.Context, account domain.Account, webErr error) (syncDirectory, error) {
	appleCtx, err := s.accountProxyForAccount(ctx, account)
	if err != nil {
		return syncDirectory{}, err
	}
	client, ok := s.client.(accountDirectoryLister)
	if !ok {
		return syncDirectory{}, webErr
	}
	// Adapters without independent storage must retain the original Web error;
	// their legacy save path authenticates the outer Web envelope.
	if _, ok := s.repo.(accountSessionRepository); !ok {
		return syncDirectory{}, webErr
	}
	managed, err := s.readAccountManagementSession(ctx, account.ID)
	if errors.Is(err, store.ErrNotFound) || Code(err) == CodeAccountSessionExpired {
		return syncDirectory{}, webErr
	}
	if err != nil {
		return syncDirectory{}, err
	}
	if managed == nil || managed.APIKey == "" || managed.SCNT == "" || managed.RefreshRejected {
		return syncDirectory{}, webErr
	}
	current, err := s.repo.GetAccount(ctx, account.ID)
	if err != nil {
		return syncDirectory{}, err
	}
	if !sameIdentity(identityOf(current), identityOf(account)) {
		return syncDirectory{}, wrapError(CodeAccountChanged, ErrAccountChanged, nil)
	}
	list, returned, readErr := client.ListAccountAliases(appleCtx, *managed)
	acceptedCheckpoint := returned.UpdatedAt.After(managed.UpdatedAt)
	if readErr == nil || acceptedCheckpoint || returned.AppleID != "" || returned.Region != "" {
		if err := validateAccountDirectorySession(*managed, returned); err != nil {
			return syncDirectory{}, err
		}
	}
	if explicitWebFailure(readErr, apple.ErrInvalidSession) {
		if !acceptedCheckpoint {
			returned = *managed
		}
		returned.RefreshRejected, returned.UpdatedAt = true, s.now()
		acceptedCheckpoint = true
	}
	if readErr == nil || acceptedCheckpoint {
		// Accepted cookie/token rotations survive later read failures, including
		// cancellation. The Account checkpoint never rewrites Web authentication.
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), autoCreatePersistTimeout)
		defer cancel()
		if err := s.persistAccountManagementSession(persistCtx, account.ID, identityOf(account), returned); err != nil {
			if readErr != nil {
				return syncDirectory{}, errors.Join(mapAppleError(readErr, false), wrapPersistenceError(err))
			}
			return syncDirectory{}, wrapPersistenceError(err)
		}
	}
	if readErr != nil {
		return syncDirectory{}, mapAppleError(readErr, false)
	}
	return syncDirectory{list: list, fromAccount: true}, nil
}

func validateAccountDirectorySession(expected, returned apple.AccountSession) error {
	expectedRegion, expectedErr := normalizeRegion(expected.Region)
	returnedRegion, returnedErr := normalizeRegion(returned.Region)
	if !sameEmail(expected.AppleID, returned.AppleID) || expectedErr != nil || returnedErr != nil || expectedRegion != returnedRegion {
		return wrapError(CodeAccountMismatch, ErrAccountMismatch, nil)
	}
	return nil
}
