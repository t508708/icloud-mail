package hmesync

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"icloud-api/internal/apple"
	"icloud-api/internal/domain"
)

// RunAccountSessionRenewal checks local deadlines; only due, enabled accounts
// make management requests. It never reads mail or creates an address.
func (s *Service) RunAccountSessionRenewal(ctx context.Context, logger *slog.Logger) {
	repo, ok := s.repo.(interface {
		ListAccounts(context.Context) ([]domain.Account, error)
	})
	if !ok {
		return
	}
	// Leave scheduling headroom around the four-minute renewal deadline.
	// This tick only reads local state; requests are sent only when due.
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		accounts, err := repo.ListAccounts(ctx)
		if err != nil && ctx.Err() == nil && logger != nil {
			logger.Warn("读取 Apple Account 续期计划暂未完成", "operation", "apple_account_session_renew_list")
		}
		for _, account := range accounts {
			if ctx.Err() != nil {
				return
			}
			if !account.Enabled || domain.NormalizeMailboxType(account.MailboxType) != domain.MailboxTypeICloud {
				continue
			}
			callCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
			err := s.keepAliveAccountSession(callCtx, account.ID, true, logger)
			cancel()
			if err != nil && ctx.Err() == nil && logger != nil {
				mapped := mapAppleError(err, false)
				code := Code(mapped)
				if errors.Is(err, apple.ErrInvalidSession) {
					code = CodeAccountSessionExpired
				}
				attrs := []any{"account_id", account.ID, "operation", "apple_account_session_renew", "error_code", code}
				var upstream *apple.Error
				if errors.As(err, &upstream) {
					attrs = append(attrs, "http_status", upstream.StatusCode, "upstream_operation", upstream.Op, "service_code", upstream.ServiceCode)
				}
				logger.Warn("Apple Account 会话续期暂未完成", attrs...)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
