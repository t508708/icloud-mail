package hmesync

import (
	"context"
	"encoding/json"
	"errors"

	"icloud-api/internal/apple"
	"icloud-api/internal/store"
)

// Automatic expiry invalidates service access but retains the Apple-issued
// browser trust token for the next explicit login. Explicit logout still deletes it.
// The caller holds the account publication lock.
func (s *Service) expireWebSessionPreservingTrust(ctx context.Context, id int64) error {
	record, err := s.repo.GetAppleWebSession(ctx, id)
	if err != nil {
		return err
	}
	previous, err := s.decryptSession(record)
	if err != nil || previous.TrustToken == "" {
		return s.deleteWebSessionPreservingAccount(ctx, id)
	}
	if _, supported := s.repo.(accountSessionRepository); supported {
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
	trusted := apple.Session{AppleID: previous.AppleID, Region: previous.Region, ClientID: previous.ClientID, TrustToken: previous.TrustToken}
	if _, supported := s.repo.(accountSessionRepository); !supported {
		trusted.Account = previous.Account
	}
	payload, err := json.Marshal(trusted)
	if err != nil {
		return err
	}
	record.Ciphertext, err = s.cipher.EncryptAppleSession(string(payload))
	if err != nil {
		return err
	}
	record.Authenticated = false
	_, err = s.repo.UpsertAppleWebSession(ctx, record)
	return err
}
