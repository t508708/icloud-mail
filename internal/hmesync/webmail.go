package hmesync

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"icloud-api/internal/apple"
	"icloud-api/internal/domain"
	"icloud-api/internal/store"
)

var ErrWebMailLoginRequired = errors.New("WEB_MAIL_AUTH_REQUIRED: 网页收件登录待恢复，请重新登录 iCloud Web 目录连接")

type webMailReader interface {
	ReadAliasMail(context.Context, apple.Session, string) (apple.WebMailResult, apple.Session, error)
}

// ReadAliasWebMailLocked is called with the sync manager's account lock held.
// It never starts a password login, refreshes a directory, or takes that lock again.
func (s *Service) ReadAliasWebMailLocked(ctx context.Context, account domain.Account, alias domain.Alias) (apple.WebMailResult, error) {
	if !account.Enabled || !alias.Enabled || alias.AccountID != account.ID {
		return apple.WebMailResult{}, store.ErrAccountDisabled
	}
	if domain.NormalizeMailboxType(account.MailboxType) != domain.MailboxTypeICloud || domain.UsesForwardedICloudIMAP(account) {
		return apple.WebMailResult{}, store.ErrICloudMailboxRequired
	}
	reader, ok := s.client.(webMailReader)
	if !ok {
		return apple.WebMailResult{}, errors.New("web mail reader unavailable")
	}
	record, session, err := s.loadSession(ctx, account.ID)
	if err != nil {
		if errors.Is(err, ErrLoginRequired) || errors.Is(err, ErrSessionExpired) {
			return apple.WebMailResult{}, ErrWebMailLoginRequired
		}
		return apple.WebMailResult{}, err
	}
	// A failed mail authentication pauses mail requests until a later trusted
	// Web login/validation. Keep the independently usable HME/Account sessions.
	if strings.HasPrefix(account.LastSyncError, "WEB_MAIL_AUTH_REQUIRED") && account.LastSyncedAt != nil &&
		(record.LastValidatedAt == nil || !record.LastValidatedAt.After(*account.LastSyncedAt)) {
		return apple.WebMailResult{}, ErrWebMailLoginRequired
	}
	result, updated, err := reader.ReadAliasMail(ctx, session, alias.Address)
	if err != nil {
		if errors.Is(err, apple.ErrWebMailAuthentication) {
			return apple.WebMailResult{}, ErrWebMailLoginRequired
		}
		return apple.WebMailResult{}, fmt.Errorf("网页按需收件未完成: %w", err)
	}
	if err := validateSessionDSID(session.DSID, updated); err != nil {
		return apple.WebMailResult{}, err
	}
	if !sameEmail(updated.AppleID, record.AppleID) {
		return apple.WebMailResult{}, ErrAccountMismatch
	}
	if _, err := s.saveSession(ctx, account.ID, updated); err != nil {
		return apple.WebMailResult{}, err
	}
	return result, nil
}
