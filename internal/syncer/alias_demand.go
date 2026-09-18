package syncer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"icloud-api/internal/domain"
	"icloud-api/internal/store"
)

type aliasDemandRepository interface {
	GetAlias(context.Context, int64) (domain.Alias, error)
	GetAliasIMAPSyncState(context.Context, int64) (domain.IMAPSyncState, error)
	ApplyAliasMailboxSync(context.Context, time.Time, domain.Alias, domain.MailboxSyncResult, time.Time) error
}

type aliasDemandFetcher interface {
	FetchAliasIncremental(context.Context, domain.Account, string, domain.Alias, []domain.Alias, *domain.IMAPSyncState, map[int64]domain.MailboxSnapshotPosition) (domain.MailboxSyncResult, error)
}

type aliasDemandFlight struct {
	done     chan struct{}
	err      error
	finished time.Time
}

const aliasDemandSyncReuseInterval = 2 * time.Second

// SyncAliasOnDemand runs at most one bounded batch for an authenticated read.
// Concurrent readers share the result, and repeated refreshes within 2 seconds
// read the cache. No continuation or retry is scheduled after this returns.
func (m *Manager) SyncAliasOnDemand(ctx context.Context, aliasID int64) error {
	if aliasID < 1 || ctx == nil {
		return errors.New("alias demand requires an ID and context")
	}
	if m.stopping.Load() {
		return context.Canceled
	}
	m.demandMu.Lock()
	if flight := m.demandFlights[aliasID]; flight != nil && (flight.finished.IsZero() || time.Since(flight.finished) < aliasDemandSyncReuseInterval) {
		m.demandMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-flight.done:
			return flight.err
		}
	}
	if m.demandFlights == nil {
		m.demandFlights = make(map[int64]*aliasDemandFlight)
	}
	if len(m.demandFlights) >= 1024 {
		for id, flight := range m.demandFlights {
			if !flight.finished.IsZero() && time.Since(flight.finished) >= aliasDemandSyncReuseInterval {
				delete(m.demandFlights, id)
			}
		}
	}
	if len(m.demandFlights) >= 4096 {
		m.demandMu.Unlock()
		return ErrSyncDeferred
	}
	flight := &aliasDemandFlight{done: make(chan struct{})}
	m.demandFlights[aliasID] = flight
	m.demandMu.Unlock()
	err := m.syncAliasDemand(ctx, aliasID)
	m.demandMu.Lock()
	flight.err, flight.finished = err, time.Now()
	close(flight.done)
	m.demandMu.Unlock()
	return err
}

func (m *Manager) syncAliasDemand(ctx context.Context, aliasID int64) (retErr error) {
	failedOperation := "load_alias"
	var accountID int64
	var transport string
	var sensitive []string
	defer func() {
		if retErr == nil || m == nil || m.logger == nil {
			return
		}
		m.logger.Warn("单邮箱按需获取失败", "account_id", accountID, "alias_id", aliasID, "transport", transport,
			"request_id", domain.MailboxRequestID(ctx), "operation", "alias_demand_sync",
			"failed_operation", failedOperation,
			"error_context", redactSyncLogText(retErr.Error(), sensitive...))
	}()
	repo, ok := m.repo.(aliasDemandRepository)
	if !ok {
		return errors.New("alias demand persistence unavailable")
	}
	fetcher, ok := m.fetcher.(aliasDemandFetcher)
	if !ok {
		return errors.New("alias demand fetcher unavailable")
	}
	alias, err := repo.GetAlias(ctx, aliasID)
	if err != nil {
		return err
	}
	if !alias.Enabled {
		return store.ErrAccountDisabled
	}
	accountID = alias.AccountID
	sensitive = append(sensitive, alias.Address, alias.CredentialCiphertext, alias.APIKeyPrefix)
	failedOperation = "wait_for_account"
	ctx, cancel := context.WithTimeout(ctx, m.syncTimeout)
	defer cancel()
	return m.WithAccountIMAPSlot(ctx, alias.AccountID, func() error {
		failedOperation = "load_account"
		account, err := m.repo.GetAccount(ctx, alias.AccountID)
		if err != nil {
			return err
		}
		sensitive = append(sensitive, account.Email, account.IMAPUsername, account.PasswordCiphertext)
		current, err := repo.GetAlias(ctx, aliasID)
		if err != nil {
			return err
		}
		if !account.Enabled || !current.Enabled || current.AccountID != account.ID {
			return store.ErrAccountDisabled
		}
		transport, err = accountMailTransport(ctx, m.repo, account.ID)
		if err != nil {
			return err
		}
		useWebMail := transport == store.MailTransportWebmail
		if !useWebMail && domain.IsIMAPAuthenticationFailure(account.LastSyncError) {
			return domain.ErrIMAPAuthenticationPaused
		}
		failedOperation = "wait_for_fetch_interval"
		if err := m.waitForFetchInterval(ctx, account.ID); err != nil {
			return err
		}
		failedOperation = "load_mailbox_state"
		state, err := repo.GetAliasIMAPSyncState(ctx, aliasID)
		var previous *domain.IMAPSyncState
		if err == nil {
			previous = &state
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		allPositions, err := m.repo.ListMailboxSnapshotPositions(ctx, account.ID)
		if err != nil {
			return err
		}
		positions := make(map[int64]domain.MailboxSnapshotPosition)
		if position, exists := allPositions[aliasID]; exists {
			positions[aliasID] = position
		}
		knownAliases, err := m.repo.ListEnabledAliasesByAccount(ctx, account.ID)
		if err != nil {
			return err
		}
		m.logger.Debug("取件请求触发单邮箱获取", "account_id", account.ID, "alias_id", aliasID, "operation", "alias_demand_sync", "transport", transport)
		var result domain.MailboxSyncResult
		failedOperation = "fetch"
		if useWebMail {
			if m.webMailFetcher == nil {
				return errors.New("web mail receiver unavailable")
			}
			result, err = m.webMailFetcher(ctx, account, current, knownAliases)
		} else {
			failedOperation = "decrypt_credentials"
			password, decryptErr := m.cipher.Decrypt(account.PasswordCiphertext)
			if decryptErr != nil {
				return fmt.Errorf("decrypt demand mailbox credentials: %w", decryptErr)
			}
			sensitive = append(sensitive, password)
			failedOperation = "fetch"
			result, err = fetcher.FetchAliasIncremental(ctx, account, password, current, knownAliases, previous, positions)
			password = ""
		}
		if err != nil {
			if useWebMail || domain.IsIMAPAuthenticationFailure(err.Error()) {
				failures := newFailureRecorder(m, ctx, account.ID, account.UpdatedAt)
				defer failures.close()
				failures.record(err)
			}
			return err
		}
		failedOperation = "publish"
		if useWebMail {
			webRepo, ok := m.repo.(webMailRepository)
			if !ok {
				return errors.New("web mail persistence unavailable")
			}
			err = webRepo.ApplyAliasWebMailboxSync(ctx, account.UpdatedAt, current, result, time.Now().UTC())
		} else {
			err = repo.ApplyAliasMailboxSync(ctx, account.UpdatedAt, current, result, time.Now().UTC())
		}
		if err != nil {
			if errors.Is(err, store.ErrAliasMailboxSyncStale) {
				return ErrSyncDeferred
			}
			return err
		}
		m.logger.Debug("单邮箱按需获取完成", "account_id", account.ID, "alias_id", aliasID, "operation", "alias_demand_sync", "transport", transport, "message_count", len(result.ArchivedMessages), "has_more", result.HasMore)
		return nil
	})
}
