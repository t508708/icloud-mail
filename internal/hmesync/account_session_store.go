package hmesync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"icloud-api/internal/apple"
	"icloud-api/internal/domain"
	"icloud-api/internal/store"
)

type accountSessionRepository interface {
	GetAppleAccountSession(context.Context, int64) (domain.AppleWebSession, error)
	UpsertAppleAccountSession(context.Context, domain.AppleWebSession) (domain.AppleWebSession, error)
	DeleteAppleAccountSession(context.Context, int64) error
}

// A Web challenge can predate a successful ACC re-login. Do not publish the
// management cookies captured by that older challenge over the newer login.
func (s *Service) attachManagementForWebLogin(ctx context.Context, id int64, web *apple.Session) error {
	if _, ok := s.repo.(accountSessionRepository); !ok {
		return nil
	}
	managed, err := s.readAccountManagementSession(ctx, id)
	if err != nil && !errors.Is(err, store.ErrNotFound) && Code(err) != CodeAccountSessionExpired {
		return err
	}
	web.Account = nil
	if err == nil && sameEmail(web.AppleID, managed.AppleID) {
		web.Account = managed
	}
	return nil
}

// Older installations kept management cookies inside the Web envelope.
// Prefer the newest matching checkpoint without requiring a working Web login.
func (s *Service) readAccountManagementSession(ctx context.Context, id int64) (*apple.AccountSession, error) {
	var managed *apple.AccountSession
	var current domain.AppleWebSession
	if repo, ok := s.repo.(accountSessionRepository); ok {
		record, err := repo.GetAppleAccountSession(ctx, id)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		if err == nil {
			if !record.Authenticated {
				return nil, store.ErrNotFound
			}
			envelope, err := s.decryptSession(record)
			if err != nil {
				return nil, wrapError(CodeAccountSessionExpired, ErrSessionExpired, err)
			}
			managed, current = envelope.Account, record
		}
	}
	record, err := s.repo.GetAppleWebSession(ctx, id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if err == nil {
		web, decodeErr := s.decryptSession(record)
		if decodeErr == nil && web.Account != nil && sameEmail(web.AppleID, web.Account.AppleID) &&
			(managed == nil || sameEmail(managed.AppleID, web.Account.AppleID) && record.UpdatedAt.After(current.UpdatedAt)) {
			managed = web.Account
		}
	}
	if managed == nil {
		return nil, store.ErrNotFound
	}
	return managed, nil
}

func (s *Service) saveAccountManagementSession(ctx context.Context, id int64, managed apple.AccountSession) error {
	repo, ok := s.repo.(accountSessionRepository)
	if !ok {
		// Compatibility for adapters implementing only the original repository.
		_, web, err := s.loadSession(ctx, id)
		if err != nil {
			return err
		}
		web.Account = &managed
		_, err = s.saveSession(ctx, id, web)
		return err
	}
	region, err := normalizeRegion(managed.Region)
	if err != nil {
		return err
	}
	managed.Region = region
	payload, err := json.Marshal(apple.Session{AppleID: managed.AppleID, Region: region, Account: &managed})
	if err != nil {
		return err
	}
	ciphertext, err := s.cipher.EncryptAppleSession(string(payload))
	if err != nil {
		return fmt.Errorf("encrypt Apple Account session: %w", err)
	}
	now := s.now().UTC()
	_, err = repo.UpsertAppleAccountSession(ctx, domain.AppleWebSession{
		AccountID: id, Ciphertext: ciphertext, AppleID: managed.AppleID,
		Region: publicRegion(string(region)), Authenticated: true, LastValidatedAt: &now,
	})
	return err
}

func (s *Service) persistAccountManagementSession(ctx context.Context, id int64, expected accountIdentity, managed apple.AccountSession) error {
	return s.locker.WithAccountLock(ctx, id, func() error {
		account, err := s.repo.GetAccount(ctx, id)
		if err != nil {
			return err
		}
		if !sameIdentity(identityOf(account), expected) {
			return wrapError(CodeAccountChanged, ErrAccountChanged, nil)
		}
		return s.saveAccountManagementSession(ctx, id, managed)
	})
}

// Call under the account lock. Expiring Web cookies must not log out ACC.
func (s *Service) deleteWebSessionPreservingAccount(ctx context.Context, id int64) error {
	if _, ok := s.repo.(accountSessionRepository); ok {
		managed, err := s.readAccountManagementSession(ctx, id)
		if err != nil && !errors.Is(err, store.ErrNotFound) && Code(err) != CodeAccountSessionExpired {
			return err
		}
		if err == nil {
			if err := s.saveAccountManagementSession(ctx, id, *managed); err != nil {
				return err
			}
		}
	}
	return s.repo.DeleteAppleWebSession(ctx, id)
}
