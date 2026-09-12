package syncer

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"icloud-api/internal/domain"
)

type MailboxEventRepository interface {
	ListEnabledAccounts(context.Context) ([]domain.Account, error)
}

type MailboxWatchFunc func(context.Context, domain.Account, string, func()) error
type MailboxEventSyncFunc func(context.Context, int64) error

type mailboxWatchState struct {
	connected atomic.Bool
	synced    atomic.Bool
}

// MailboxEvents owns one notification-only connection per enabled account.
// Actual reads use the existing synchronizer and its account/global limits.
type MailboxEvents struct {
	repo              MailboxEventRepository
	cipher            CredentialCipher
	watch             MailboxWatchFunc
	syncAccount       MailboxEventSyncFunc
	logger            *slog.Logger
	reconcileInterval time.Duration
	retryMinimum      time.Duration
	retryMaximum      time.Duration
	eventCooldown     time.Duration
	statesMu          sync.RWMutex
	states            map[int64]*mailboxWatchState
}

func NewMailboxEvents(repo MailboxEventRepository, cipher CredentialCipher, watch MailboxWatchFunc, syncAccount MailboxEventSyncFunc, logger *slog.Logger) *MailboxEvents {
	if logger == nil {
		logger = slog.Default()
	}
	return &MailboxEvents{repo: repo, cipher: cipher, watch: watch, syncAccount: syncAccount, logger: logger,
		states:            make(map[int64]*mailboxWatchState),
		reconcileInterval: 30 * time.Second, retryMinimum: time.Minute, retryMaximum: 5 * time.Minute, eventCooldown: 10 * time.Second}
}

func mailboxWatchIdentity(account domain.Account) [32]byte {
	return sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s\x00%s\x00%s", account.IMAPHost, account.IMAPPort, account.IMAPUsername, account.Email, account.PasswordCiphertext)))
}

// Healthy means notifications are connected and the initial catch-up succeeded.
// API cache reads can then avoid opening another upstream connection per poll.
func (m *MailboxEvents) Healthy(accountID int64) bool {
	m.statesMu.RLock()
	state := m.states[accountID]
	m.statesMu.RUnlock()
	return state != nil && state.connected.Load() && state.synced.Load()
}

func (m *MailboxEvents) Run(ctx context.Context) {
	type worker struct {
		identity [32]byte
		cancel   context.CancelFunc
		done     chan struct{}
	}
	workers := map[int64]worker{}
	defer func() {
		for _, w := range workers {
			w.cancel()
		}
		for _, w := range workers {
			<-w.done
		}
		m.statesMu.Lock()
		clear(m.states)
		m.statesMu.Unlock()
	}()
	reconcile := func() {
		accounts, err := m.repo.ListEnabledAccounts(ctx)
		if err != nil {
			if ctx.Err() == nil {
				m.logger.Warn("读取邮件通知订阅配置失败，稍后重试")
			}
			return
		}
		next := make(map[int64]domain.Account, len(accounts))
		for _, account := range accounts {
			if account.Enabled {
				next[account.ID] = account
			}
		}
		for id, w := range workers {
			account, exists := next[id]
			if !exists || w.identity != mailboxWatchIdentity(account) {
				w.cancel()
				<-w.done
				delete(workers, id)
				m.statesMu.Lock()
				delete(m.states, id)
				m.statesMu.Unlock()
			}
		}
		for id, account := range next {
			if _, exists := workers[id]; exists {
				continue
			}
			workerCtx, cancel := context.WithCancel(ctx)
			w := worker{identity: mailboxWatchIdentity(account), cancel: cancel, done: make(chan struct{})}
			workers[id] = w
			state := &mailboxWatchState{}
			m.statesMu.Lock()
			m.states[id] = state
			m.statesMu.Unlock()
			go func() { defer close(w.done); m.runAccount(workerCtx, account, state) }()
		}
	}
	ticker := time.NewTicker(m.reconcileInterval)
	defer ticker.Stop()
	reconcile()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcile()
		}
	}
}

func (m *MailboxEvents) runAccount(ctx context.Context, account domain.Account, state *mailboxWatchState) {
	ctx, cancel := context.WithCancel(ctx)
	notifications := make(chan struct{}, 1)
	notify := func() {
		select {
		case notifications <- struct{}{}:
		default:
		}
	}
	var background sync.WaitGroup
	background.Add(1)
	go func() {
		defer background.Done()
		var lastAttempt time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-notifications:
			}
			if delay := m.eventCooldown - time.Since(lastAttempt); delay > 0 && !waitForInterval(ctx, delay) {
				return
			}
			lastAttempt = time.Now()
			for attempt := 0; attempt < 3 && ctx.Err() == nil; attempt++ {
				err := m.syncAccount(ctx, account.ID)
				state.synced.Store(err == nil)
				if err == nil || ctx.Err() != nil {
					break
				}
				m.logger.Warn("通知触发的邮件同步未完成，将退避重试并保留低频补偿", "account_id", account.ID, "attempt", attempt+1)
				if attempt < 2 && !waitForInterval(ctx, m.retryMinimum*time.Duration(attempt+1)) {
					return
				}
			}
		}
	}()
	defer func() {
		state.connected.Store(false)
		cancel()
		background.Wait()
	}()
	retry := m.retryMinimum
	for ctx.Err() == nil {
		password, err := m.cipher.Decrypt(account.PasswordCiphertext)
		if err != nil {
			m.logger.Warn("邮件通知订阅凭据读取失败", "account_id", account.ID)
			if !waitForInterval(ctx, m.retryMaximum) {
				return
			}
			continue
		}
		var connected sync.Once
		state.synced.Store(false)
		started := time.Now()
		_ = m.watch(ctx, account, password, func() {
			connected.Do(func() {
				state.connected.Store(true)
				m.logger.Info("IMAP IDLE 通知连接已建立", "account_id", account.ID)
			})
			notify()
		})
		state.connected.Store(false)
		password = ""
		if ctx.Err() != nil {
			return
		}
		// An unsupported/disconnected watcher must still have a bounded fallback.
		// The normal manager's long interval is not sufficient during an outage.
		notify()
		m.logger.Warn("IMAP 通知连接已断开或暂不可用，低频同步后重连", "account_id", account.ID, "retry_after_seconds", int(retry.Seconds()))
		if !waitForInterval(ctx, retry) {
			return
		}
		if time.Since(started) > 10*time.Minute {
			retry = m.retryMinimum
		} else {
			retry = min(retry*2, m.retryMaximum)
		}
	}
}
