package syncer

import (
	"context"
	"time"

	"icloud-api/internal/domain"
)

type WebMailFetcher func(context.Context, domain.Account, domain.Alias, []domain.Alias) (domain.MailboxSyncResult, error)

type mailTransportRepository interface {
	GetAccountMailTransport(context.Context, int64) (string, error)
}

type webMailRepository interface {
	ApplyAliasWebMailboxSync(context.Context, time.Time, domain.Alias, domain.MailboxSyncResult, time.Time) error
}

// SetWebMailFetcher installs the alternative receiver before workers start.
func (m *Manager) SetWebMailFetcher(fetch WebMailFetcher) { m.webMailFetcher = fetch }

func accountMailTransport(ctx context.Context, repo any, accountID int64) (string, error) {
	if r, ok := repo.(mailTransportRepository); ok {
		return r.GetAccountMailTransport(ctx, accountID)
	}
	return "imap", nil
}
