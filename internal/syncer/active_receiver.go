package syncer

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"time"

	"icloud-api/internal/domain"
	"icloud-api/internal/store"
)

// ActiveReceiver shares a bounded account-wide receive worker between all
// authenticated alias readers. HTTP cancellation never cancels shared work.
type ActiveReceiver struct {
	manager           *Manager
	watch             MailboxWatchFunc
	ctx               context.Context
	mu                sync.Mutex
	workers           map[int64]*activeReceiverWorker
	wg                sync.WaitGroup
	closed            bool
	idleTTL           time.Duration
	freshness         time.Duration
	fallbackFreshness time.Duration
	retryMinimum      time.Duration
	maxWorkers        int
	syncAccount       func(context.Context, int64) error
}

type activeReceiverWorker struct {
	account    domain.Account
	ctx        context.Context
	cancel     context.CancelFunc
	wake       chan struct{}
	changed    chan struct{}
	lastAccess time.Time
	lastSync   time.Time
	generation uint64
	committed  uint64
	connected  bool
	err        error
	nextTry    time.Time
	closed     bool
}

func NewActiveReceiver(ctx context.Context, manager *Manager, watch MailboxWatchFunc) *ActiveReceiver {
	return &ActiveReceiver{ctx: ctx, manager: manager, watch: watch,
		workers: make(map[int64]*activeReceiverWorker), idleTTL: 10 * time.Minute,
		// A live socket is not proof of prompt delivery: Apple can omit or delay
		// EXISTS. Bound observation age on demand even while IDLE is connected.
		freshness: 5 * time.Second, fallbackFreshness: 5 * time.Second,
		retryMinimum: 2 * time.Second, maxWorkers: 128, syncAccount: manager.SyncActiveAccount}
}

func (r *ActiveReceiver) Close() {
	r.mu.Lock()
	r.closed = true
	for _, w := range r.workers {
		w.cancel()
	}
	r.mu.Unlock()
	r.wg.Wait()
}

func (r *ActiveReceiver) SyncAlias(ctx context.Context, aliasID int64) error {
	repo, ok := r.manager.repo.(aliasDemandRepository)
	if !ok {
		return errors.New("active receiver persistence unavailable")
	}
	a, err := repo.GetAlias(ctx, aliasID)
	if err != nil {
		return err
	}
	account, err := r.manager.repo.GetAccount(ctx, a.AccountID)
	if err != nil {
		return err
	}
	if !a.Enabled || !account.Enabled {
		return store.ErrAccountDisabled
	}
	transport, err := accountMailTransport(ctx, r.manager.repo, account.ID)
	if err != nil {
		return err
	}
	if transport == store.MailTransportWebmail {
		return r.manager.SyncAliasOnDemand(ctx, aliasID)
	}
	if domain.IsIMAPAuthenticationFailure(account.LastSyncError) {
		return domain.ErrIMAPAuthenticationPaused
	}
	r.mu.Lock()
	if r.closed || r.ctx.Err() != nil {
		r.mu.Unlock()
		return context.Canceled
	}
	w := r.workers[account.ID]
	if w != nil && mailboxWatchIdentity(w.account) != mailboxWatchIdentity(account) {
		w.cancel()
		r.mu.Unlock()
		return ErrSyncDeferred
	}
	if w == nil {
		if len(r.workers) >= r.maxWorkers {
			r.mu.Unlock()
			return ErrSyncDeferred
		}
		workerCtx, cancel := context.WithCancel(r.ctx)
		w = &activeReceiverWorker{account: account, ctx: workerCtx, cancel: cancel,
			wake: make(chan struct{}, 1), changed: make(chan struct{}), generation: 1}
		r.workers[account.ID] = w
		r.wg.Add(1)
		go r.run(w)
	}
	w.lastAccess = time.Now()
	freshness := r.fallbackFreshness
	if w.connected {
		freshness = r.freshness
	}
	if w.committed == w.generation && (w.lastSync.IsZero() || time.Since(w.lastSync) >= freshness || a.CreatedAt.After(w.lastSync)) {
		w.generation++
	}
	// Wait for the generation observed on admission, not a moving target when
	// unrelated messages keep arriving during the shared read.
	required := w.generation
	select {
	case w.wake <- struct{}{}:
	default:
	}
	for {
		if w.closed {
			r.mu.Unlock()
			return ErrSyncDeferred
		}
		if w.err != nil && time.Now().Before(w.nextTry) {
			err := w.err
			r.mu.Unlock()
			return err
		}
		if w.err == nil && w.committed >= required && !w.lastSync.IsZero() {
			r.mu.Unlock()
			return nil
		}
		changed := w.changed
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w.ctx.Done():
			return ErrSyncDeferred
		case <-changed:
		}
		r.mu.Lock()
	}
}

func (r *ActiveReceiver) notify(w *activeReceiverWorker, connected bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if w.closed {
		return
	}
	w.connected = connected
	w.generation++
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (r *ActiveReceiver) watchAccount(w *activeReceiverWorker) {
	if r.watch == nil {
		return
	}
	retry := 30 * time.Second
	for w.ctx.Err() == nil {
		password, err := r.manager.cipher.Decrypt(w.account.PasswordCiphertext)
		if err != nil {
			return
		}
		started := time.Now()
		err = r.watch(w.ctx, w.account, password, func() { r.notify(w, true) })
		password = ""
		if w.ctx.Err() != nil {
			return
		}
		r.notify(w, false)
		if err != nil && domain.IsIMAPAuthenticationFailure(err.Error()) {
			return
		}
		r.manager.logger.Warn("活跃主号通知连接中断，将退避重连", "account_id", w.account.ID,
			"operation", "active_mail_watch_retry", "retry_after_seconds", int(retry.Seconds()))
		if !waitForInterval(w.ctx, retry+time.Duration(rand.Int64N(int64(retry/4)+1))) {
			return
		}
		if time.Since(started) > 10*time.Minute {
			retry = 30 * time.Second
		} else {
			retry = min(2*retry, 5*time.Minute)
		}
	}
}

func (r *ActiveReceiver) run(w *activeReceiverWorker) {
	defer r.wg.Done()
	receiveCtx := w.ctx
	if owner, ok := r.manager.fetcher.(interface {
		OpenActiveAccount(context.Context, int64) (context.Context, func())
	}); ok {
		var closeConnection func()
		receiveCtx, closeConnection = owner.OpenActiveAccount(w.ctx, w.account.ID)
		defer closeConnection()
	}
	watchDone := make(chan struct{})
	go func() { defer close(watchDone); r.watchAccount(w) }()
	defer func() {
		w.cancel()
		<-watchDone
		r.mu.Lock()
		w.closed = true
		delete(r.workers, w.account.ID)
		close(w.changed)
		r.mu.Unlock()
		r.manager.logger.Info("活跃主号收件已休眠", "account_id", w.account.ID, "operation", "active_mail_sleep")
	}()
	r.manager.logger.Info("取件唤醒主号共享收件", "account_id", w.account.ID, "operation", "active_mail_wake")
	tick := time.NewTicker(min(15*time.Second, r.idleTTL/2))
	defer tick.Stop()
	failures := 0
	for {
		r.mu.Lock()
		if time.Since(w.lastAccess) >= r.idleTTL {
			r.mu.Unlock()
			return
		}
		pending := w.generation != w.committed
		delay := time.Until(w.nextTry)
		generation := w.generation
		r.mu.Unlock()
		if pending && delay <= 0 {
			started := time.Now()
			ctx, cancel := context.WithTimeout(receiveCtx, r.manager.syncTimeout)
			err := r.syncAccount(ctx, w.account.ID)
			cancel()
			r.mu.Lock()
			w.err = err
			if err == nil {
				w.committed = generation
				w.lastSync = started
				w.nextTry = time.Time{}
				failures = 0
			} else {
				failures++
				backoff := min(r.retryMinimum*time.Duration(1<<min(failures-1, 4)), 30*time.Second)
				if domain.IsIMAPAuthenticationFailure(err.Error()) {
					backoff = r.idleTTL
				}
				w.nextTry = time.Now().Add(backoff + time.Duration(rand.Int64N(int64(backoff/4)+1)))
			}
			close(w.changed)
			w.changed = make(chan struct{})
			r.mu.Unlock()
			continue
		}
		if !pending {
			delay = r.idleTTL
		}
		timer := time.NewTimer(max(delay, time.Millisecond))
		select {
		case <-w.ctx.Done():
			timer.Stop()
			return
		case <-w.wake:
		case <-timer.C:
		case <-tick.C:
			account, err := r.manager.repo.GetAccount(w.ctx, w.account.ID)
			if errors.Is(err, store.ErrNotFound) {
				timer.Stop()
				return
			}
			if err == nil {
				transport, transportErr := accountMailTransport(w.ctx, r.manager.repo, account.ID)
				if !account.Enabled || mailboxWatchIdentity(account) != mailboxWatchIdentity(w.account) ||
					domain.IsIMAPAuthenticationFailure(account.LastSyncError) || transportErr == nil && transport == store.MailTransportWebmail {
					timer.Stop()
					return
				}
			}
		}
		timer.Stop()
	}
}

type activeReceiveContextKey struct{}

type activeAccountFetcher interface {
	FetchActiveIncremental(context.Context, domain.Account, string, []domain.Alias, *domain.IMAPSyncState, map[int64]domain.MailboxSnapshotPosition) (domain.MailboxSyncResult, error)
}

func (m *Manager) SyncActiveAccount(ctx context.Context, accountID int64) error {
	ctx = context.WithValue(ctx, activeReceiveContextKey{}, true)
	for batch := 0; batch < 8; batch++ {
		err := m.SyncAccountFromNotification(ctx, accountID)
		if !errors.Is(err, ErrSyncPending) {
			return err
		}
	}
	return ErrSyncPending
}
