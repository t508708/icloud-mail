package syncer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"icloud-api/internal/domain"
)

type eventWatchCall struct {
	account  domain.Account
	password string
	notify   func()
	stopped  <-chan struct{}
}

func eventReceive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("event worker timed out")
		var zero T
		return zero
	}
}

func eventEventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("event condition timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

func eventTestManager(repo *fakeRepo, watch MailboxWatchFunc, syncAccount MailboxEventSyncFunc) *MailboxEvents {
	m := NewMailboxEvents(repo, cipherFunc(func(value string) (string, error) { return "clear-" + value, nil }), watch, syncAccount, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.reconcileInterval = 10 * time.Millisecond
	m.retryMinimum = 30 * time.Millisecond
	m.retryMaximum = 120 * time.Millisecond
	m.eventCooldown = 15 * time.Millisecond
	return m
}

func runEventTest(t *testing.T, m *MailboxEvents) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	t.Cleanup(func() { cancel(); eventReceive(t, done) })
	return cancel
}

func captureEventWatches(calls chan<- eventWatchCall) MailboxWatchFunc {
	return func(ctx context.Context, account domain.Account, password string, notify func()) error {
		calls <- eventWatchCall{account, password, notify, ctx.Done()}
		<-ctx.Done()
		return ctx.Err()
	}
}

func TestMailboxEventsQuietAndCoalescesWithoutLosingInflightArrival(t *testing.T) {
	repo := newFakeRepo(domain.Account{ID: 1, Enabled: true, PasswordCiphertext: "cipher"})
	watches := make(chan eventWatchCall, 4)
	syncs := make(chan int64, 20)
	release := make(chan struct{}, 10)
	m := eventTestManager(repo, captureEventWatches(watches), func(ctx context.Context, id int64) error {
		syncs <- id
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	runEventTest(t, m)
	w := eventReceive(t, watches)
	if w.password != "clear-cipher" {
		t.Fatal("wrong decrypted credential")
	}
	select {
	case <-syncs:
		t.Fatal("quiet watcher started an IMAP fetch")
	case <-time.After(25 * time.Millisecond):
	}
	if m.Healthy(1) {
		t.Fatal("uninitialized watcher is healthy")
	}
	w.notify()
	if id := eventReceive(t, syncs); id != 1 {
		t.Fatalf("sync account = %d", id)
	}
	for i := 0; i < 100; i++ {
		w.notify()
	}
	release <- struct{}{}
	eventReceive(t, syncs)
	release <- struct{}{}
	eventEventually(t, func() bool { return m.Healthy(1) })
	select {
	case <-syncs:
		t.Fatal("notification burst was not coalesced")
	case <-time.After(40 * time.Millisecond):
	}
}

func TestMailboxEventsReconcilesCredentialsAndDisabledAccounts(t *testing.T) {
	repo := newFakeRepo(domain.Account{ID: 1, Enabled: true, Email: "first", PasswordCiphertext: "old"}, domain.Account{ID: 2, Enabled: true, PasswordCiphertext: "other"})
	watches := make(chan eventWatchCall, 10)
	syncs := make(chan int64, 10)
	m := eventTestManager(repo, captureEventWatches(watches), func(_ context.Context, id int64) error { syncs <- id; return nil })
	runEventTest(t, m)
	a, b := eventReceive(t, watches), eventReceive(t, watches)
	if a.account.ID != 1 {
		a, b = b, a
	}
	a.notify()
	b.notify()
	eventReceive(t, syncs)
	eventReceive(t, syncs)
	eventEventually(t, func() bool { return m.Healthy(1) && m.Healthy(2) })
	repo.mu.Lock()
	repo.accounts[0].PasswordCiphertext = "new"
	repo.accounts[1].Enabled = false
	repo.mu.Unlock()
	c := eventReceive(t, watches)
	if c.account.ID != 1 || c.password != "clear-new" {
		t.Fatal("wrong replacement subscription")
	}
	eventReceive(t, a.stopped)
	eventReceive(t, b.stopped)
	eventEventually(t, func() bool { return !m.Healthy(1) && !m.Healthy(2) })
	c.notify()
	eventReceive(t, syncs)
	repo.mu.Lock()
	repo.accounts[0].Email = "renamed"
	repo.mu.Unlock()
	d := eventReceive(t, watches)
	if d.account.Email != "renamed" {
		t.Fatal("fallback username change did not restart watcher")
	}
	eventReceive(t, c.stopped)
}

func TestMailboxEventsDisconnectBackoffAndFallback(t *testing.T) {
	repo := newFakeRepo(domain.Account{ID: 1, Enabled: true})
	attempts := make(chan time.Time, 10)
	syncs := make(chan int64, 10)
	m := eventTestManager(repo, func(context.Context, domain.Account, string, func()) error {
		attempts <- time.Now()
		return errors.New("disconnected")
	}, func(_ context.Context, id int64) error { syncs <- id; return nil })
	cancel := runEventTest(t, m)
	a := eventReceive(t, attempts)
	eventReceive(t, syncs)
	b := eventReceive(t, attempts)
	eventReceive(t, syncs)
	c := eventReceive(t, attempts)
	if b.Sub(a) < m.retryMinimum || c.Sub(b) < 2*m.retryMinimum {
		t.Fatal("reconnects ignored backoff")
	}
	if m.Healthy(1) {
		t.Fatal("disconnected watcher is healthy")
	}
	cancel()
}

func TestMailboxEventsRetriesFailedNotificationSyncWithoutNewMail(t *testing.T) {
	repo := newFakeRepo(domain.Account{ID: 1, Enabled: true})
	watches := make(chan eventWatchCall, 4)
	var attempts atomic.Int32
	m := eventTestManager(repo, captureEventWatches(watches), func(context.Context, int64) error {
		if attempts.Add(1) == 1 {
			return errors.New("transient fetch error")
		}
		return nil
	})
	runEventTest(t, m)
	w := eventReceive(t, watches)
	w.notify()
	eventEventually(t, func() bool { return attempts.Load() == 2 && m.Healthy(1) })
	time.Sleep(40 * time.Millisecond)
	if attempts.Load() != 2 {
		t.Fatal("successful retry caused periodic fetches")
	}
}

func TestMailboxEventsInvalidCipherDoesNotConnect(t *testing.T) {
	repo := newFakeRepo(domain.Account{ID: 1, Enabled: true, PasswordCiphertext: "broken"})
	var decrypts, watches, syncs atomic.Int32
	m := eventTestManager(repo, func(context.Context, domain.Account, string, func()) error { watches.Add(1); return nil }, func(context.Context, int64) error { syncs.Add(1); return nil })
	m.cipher = cipherFunc(func(string) (string, error) { decrypts.Add(1); return "", errors.New("private-error") })
	runEventTest(t, m)
	eventEventually(t, func() bool { return decrypts.Load() > 0 })
	time.Sleep(30 * time.Millisecond)
	if watches.Load() != 0 || syncs.Load() != 0 || decrypts.Load() != 1 {
		t.Fatal("invalid credential caused network work or hot retries")
	}
}

func TestMailboxEventsPausesAuthenticationFailureUntilCredentialChanges(t *testing.T) {
	repo := newFakeRepo(
		domain.Account{ID: 1, Enabled: true, PasswordCiphertext: "bad"},
		domain.Account{ID: 2, Enabled: true, PasswordCiphertext: "good"},
	)
	var watches, syncs [3]atomic.Int32
	m := eventTestManager(repo, func(ctx context.Context, account domain.Account, _ string, _ func()) error {
		watches[account.ID].Add(1)
		if account.ID == 1 {
			return errors.New("AUTHENTICATIONFAILED")
		}
		<-ctx.Done()
		return ctx.Err()
	}, func(_ context.Context, id int64) error {
		syncs[id].Add(1)
		return errors.New("AUTHENTICATIONFAILED")
	})
	runEventTest(t, m)
	eventually := func() { eventEventually(t, func() bool { return syncs[1].Load() == 1 && watches[2].Load() == 1 }) }
	eventually()
	time.Sleep(60 * time.Millisecond)
	if watches[1].Load() != 1 || syncs[1].Load() != 1 {
		t.Fatalf("auth account retried: watches=%d syncs=%d", watches[1].Load(), syncs[1].Load())
	}
	if watches[2].Load() != 1 {
		t.Fatal("unrelated account watcher did not continue")
	}
	repo.mu.Lock()
	repo.accounts[0].PasswordCiphertext = "new"
	repo.accounts[0].LastSyncError = ""
	repo.mu.Unlock()
	eventEventually(t, func() bool { return watches[1].Load() == 2 })
}

func TestMailboxEventsSkipsPersistedAuthenticationFailures(t *testing.T) {
	repo := newFakeRepo(domain.Account{ID: 1, Enabled: true, LastSyncError: "IMAP AUTHENTICATIONFAILED"}, domain.Account{ID: 2, Enabled: true})
	var watches [3]atomic.Int32
	m := eventTestManager(repo, func(ctx context.Context, account domain.Account, _ string, _ func()) error {
		watches[account.ID].Add(1)
		<-ctx.Done()
		return ctx.Err()
	}, func(context.Context, int64) error { return nil })
	runEventTest(t, m)
	eventEventually(t, func() bool { return watches[2].Load() == 1 })
	time.Sleep(40 * time.Millisecond)
	if watches[1].Load() != 0 {
		t.Fatal("persisted authentication failure started watcher")
	}
}

func TestMailboxEventsStopsWatcherWhenAuthenticationFailureIsPersisted(t *testing.T) {
	repo := newFakeRepo(domain.Account{ID: 1, Enabled: true})
	watches := make(chan eventWatchCall, 2)
	m := eventTestManager(repo, captureEventWatches(watches), func(context.Context, int64) error { return nil })
	runEventTest(t, m)
	w := eventReceive(t, watches)
	w.notify()
	eventEventually(t, func() bool { return m.Healthy(1) })
	repo.mu.Lock()
	repo.accounts[0].LastSyncError = "IMAP AUTHENTICATIONFAILED"
	repo.mu.Unlock()
	eventReceive(t, w.stopped)
	eventEventually(t, func() bool { return !m.Healthy(1) })
	select {
	case unexpected := <-watches:
		t.Fatalf("paused account watcher restarted: %#v", unexpected)
	case <-time.After(40 * time.Millisecond):
	}
}

func TestMailboxEventsDefersNoProgressWithoutRetryOrHealthyState(t *testing.T) {
	repo := newFakeRepo(domain.Account{ID: 1, Enabled: true})
	watches := make(chan eventWatchCall, 2)
	var syncs atomic.Int32
	m := eventTestManager(repo, captureEventWatches(watches), func(context.Context, int64) error {
		syncs.Add(1)
		return ErrSyncDeferred
	})
	runEventTest(t, m)
	w := eventReceive(t, watches)
	w.notify()
	eventEventually(t, func() bool { return syncs.Load() == 1 })
	time.Sleep(50 * time.Millisecond)
	if syncs.Load() != 1 || m.Healthy(1) {
		t.Fatalf("deferred notification state: calls=%d healthy=%v", syncs.Load(), m.Healthy(1))
	}
}

func TestNotificationSyncUsesManagerLimitsAndClearsProgress(t *testing.T) {
	repo := newFakeRepo(domain.Account{ID: 1, Enabled: true})
	var manager *Manager
	var fetched bool
	fetcher := fetcherFunc(func(ctx context.Context, account domain.Account, _ string, _ []domain.Alias, _ *domain.IMAPSyncState) (domain.MailboxSyncResult, error) {
		fetched = true
		progress, active := manager.AccountProgress(account.ID)
		if !active || progress.Trigger != domain.MailboxSyncTriggerNotification {
			t.Error("notification sync reported the wrong source")
		}
		if len(manager.syncSlots) != 1 {
			t.Error("notification bypassed global IMAP slot")
		}
		return domain.MailboxSyncResult{}, nil
	})
	manager = New(repo, cipherFunc(func(string) (string, error) { return "pass", nil }), fetcher, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Minute, 1)
	if err := manager.SyncAccountFromNotification(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if !fetched {
		t.Fatal("notification did not reach the fetcher")
	}
	if _, active := manager.AccountProgress(1); active {
		t.Fatal("completed notification left active progress")
	}
	if len(manager.syncSlots) != 0 {
		t.Fatal("notification leaked a sync slot")
	}
}
