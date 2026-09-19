package syncer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"icloud-api/internal/domain"
)

func activeTestReceiver(t *testing.T, watch MailboxWatchFunc, fetch func(context.Context, int64) error) (*ActiveReceiver, *demandRepoFake) {
	t.Helper()
	repo := &demandRepoFake{fakeRepo: newFakeRepo(domain.Account{ID: 1, Enabled: true}, domain.Account{ID: 2, Enabled: true})}
	for id := int64(1); id <= 40; id++ {
		repo.aliases[1] = append(repo.aliases[1], domain.Alias{ID: id, AccountID: 1, Enabled: true})
	}
	repo.aliases[2] = []domain.Alias{{ID: 100, AccountID: 2, Enabled: true}}
	m := New(repo, demandCipherFake{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Minute, 2)
	r := NewActiveReceiver(context.Background(), m, watch)
	r.syncAccount = fetch
	t.Cleanup(r.Close)
	return r, repo
}

func TestActiveReceiverSharesFortyReadersAndCache(t *testing.T) {
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	r, _ := activeTestReceiver(t, nil, func(ctx context.Context, _ int64) error {
		if calls.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	results := make(chan error, 40)
	for id := int64(1); id <= 40; id++ {
		go func(id int64) { results <- r.SyncAlias(context.Background(), id) }(id)
	}
	eventReceive(t, entered)
	once.Do(func() { close(release) })
	for i := 0; i < 40; i++ {
		if err := eventReceive(t, results); err != nil {
			t.Fatal(err)
		}
	}
	for id := int64(1); id <= 40; id++ {
		if err := r.SyncAlias(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("forty aliases used %d fetches", calls.Load())
	}
}

func TestActiveReceiverCancellationDoesNotCancelSharedFetchOrOtherAccount(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	r, _ := activeTestReceiver(t, nil, func(ctx context.Context, id int64) error {
		if id != 1 {
			return nil
		}
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { first <- r.SyncAlias(ctx, 1) }()
	eventReceive(t, entered)
	cancel()
	if err := eventReceive(t, first); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	second := make(chan error, 1)
	go func() { second <- r.SyncAlias(context.Background(), 2) }()
	if err := r.SyncAlias(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
	if err := eventReceive(t, second); err != nil {
		t.Fatal(err)
	}
}

func TestActiveReceiverInflightNotificationAndContinuousArrivals(t *testing.T) {
	watches := make(chan eventWatchCall, 1)
	entered, release := make(chan struct{}, 8), make(chan struct{}, 8)
	r, _ := activeTestReceiver(t, captureEventWatches(watches), func(ctx context.Context, _ int64) error {
		entered <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	result := make(chan error, 1)
	go func() { result <- r.SyncAlias(context.Background(), 1) }()
	eventReceive(t, entered)
	w := eventReceive(t, watches)
	w.notify()
	release <- struct{}{}
	if err := eventReceive(t, result); err != nil {
		t.Fatal(err)
	}
	eventReceive(t, entered)
	w.notify()
	release <- struct{}{}
	eventReceive(t, entered)
	release <- struct{}{}
	eventEventually(t, func() bool { r.mu.Lock(); defer r.mu.Unlock(); w := r.workers[1]; return w.committed == w.generation })
	if err := r.SyncAlias(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
		t.Fatal("warm reader triggered duplicate fetch")
	default:
	}
}

func TestActiveReceiverFailureBackoffDoesNotSlideWithRequests(t *testing.T) {
	var calls atomic.Int32
	want := errors.New("fixture fetch failure")
	r, _ := activeTestReceiver(t, nil, func(context.Context, int64) error { calls.Add(1); return want })
	r.retryMinimum = time.Hour
	if err := r.SyncAlias(context.Background(), 1); !errors.Is(err, want) {
		t.Fatal(err)
	}
	r.mu.Lock()
	deadline := r.workers[1].nextTry
	r.mu.Unlock()
	for id := int64(2); id <= 40; id++ {
		if err := r.SyncAlias(context.Background(), id); !errors.Is(err, want) {
			t.Fatal(err)
		}
	}
	r.mu.Lock()
	after := r.workers[1].nextTry
	r.mu.Unlock()
	if calls.Load() != 1 || !after.Equal(deadline) {
		t.Fatalf("failure retried or cooldown extended: %d", calls.Load())
	}
}

func TestActiveReceiverSleepWakeAndDisable(t *testing.T) {
	var calls atomic.Int32
	watches := make(chan eventWatchCall, 4)
	r, repo := activeTestReceiver(t, captureEventWatches(watches), func(context.Context, int64) error { calls.Add(1); return nil })
	r.idleTTL = 40 * time.Millisecond
	if err := r.SyncAlias(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	w := eventReceive(t, watches)
	eventReceive(t, w.stopped)
	eventEventually(t, func() bool { r.mu.Lock(); defer r.mu.Unlock(); return len(r.workers) == 0 })
	if err := r.SyncAlias(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	w = eventReceive(t, watches)
	repo.mu.Lock()
	repo.accounts[0].Enabled = false
	repo.mu.Unlock()
	if err := r.SyncAlias(context.Background(), 1); err == nil {
		t.Fatal("disabled account used cached data")
	}
	eventReceive(t, w.stopped)
	if calls.Load() != 2 {
		t.Fatalf("wake fetches: %d", calls.Load())
	}
}

func TestActiveReceiverCredentialChangeAndClose(t *testing.T) {
	watches := make(chan eventWatchCall, 4)
	r, repo := activeTestReceiver(t, captureEventWatches(watches), func(context.Context, int64) error { return nil })
	if err := r.SyncAlias(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	w := eventReceive(t, watches)
	repo.mu.Lock()
	repo.accounts[0].PasswordCiphertext = "new"
	repo.mu.Unlock()
	if err := r.SyncAlias(context.Background(), 1); !errors.Is(err, ErrSyncDeferred) {
		t.Fatalf("old connection reused: %v", err)
	}
	eventReceive(t, w.stopped)
	eventEventually(t, func() bool { r.mu.Lock(); defer r.mu.Unlock(); return len(r.workers) == 0 })
	if err := r.SyncAlias(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	w = eventReceive(t, watches)
	r.Close()
	eventReceive(t, w.stopped)
	if err := r.SyncAlias(context.Background(), 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestActiveReceiverDisconnectedFreshnessAndNoPeriodicFetch(t *testing.T) {
	var calls atomic.Int32
	watches := make(chan eventWatchCall, 1)
	r, _ := activeTestReceiver(t, captureEventWatches(watches), func(context.Context, int64) error {
		calls.Add(1)
		return nil
	})
	if err := r.SyncAlias(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	watch := eventReceive(t, watches)
	watch.notify()
	if err := r.SyncAlias(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	w := r.workers[1]
	w.lastSync = time.Now().Add(-6 * time.Second)
	r.mu.Unlock()
	before := calls.Load()
	if err := r.SyncAlias(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != before {
		t.Fatal("connected watch ignored its freshness window")
	}
	// A disconnect invalidates the observation immediately; a half-open
	// connection is still covered by the longer connected freshness window.
	r.notify(w, false)
	if err := r.SyncAlias(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != before+1 {
		t.Fatal("disconnect did not catch up")
	}
	r.mu.Lock()
	w.lastSync = time.Now().Add(-6 * time.Second)
	r.mu.Unlock()
	before = calls.Load()
	time.Sleep(30 * time.Millisecond)
	if calls.Load() != before {
		t.Fatal("stale observation triggered an unconditional poll")
	}
	if err := r.SyncAlias(context.Background(), 4); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != before+1 {
		t.Fatal("disconnected reader reused stale observation")
	}
	r.mu.Lock()
	w.connected = true
	w.lastSync = time.Now().Add(-31 * time.Second)
	r.mu.Unlock()
	if err := r.SyncAlias(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != before+2 {
		t.Fatal("silent watch bypassed bounded freshness check")
	}
}

func TestActiveReceiverCapacityDoesNotBlockExistingReaders(t *testing.T) {
	r, _ := activeTestReceiver(t, nil, func(context.Context, int64) error { return nil })
	r.maxWorkers = 1
	if err := r.SyncAlias(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if err := r.SyncAlias(context.Background(), 100); !errors.Is(err, ErrSyncDeferred) {
		t.Fatalf("excess worker admission: %v", err)
	}
	if err := r.SyncAlias(context.Background(), 2); err != nil {
		t.Fatalf("existing worker admission: %v", err)
	}
}

func TestActiveReceiverDeletedAccountStopsBeforeIdleExpiry(t *testing.T) {
	watches := make(chan eventWatchCall, 1)
	r, repo := activeTestReceiver(t, captureEventWatches(watches), func(context.Context, int64) error { return nil })
	r.idleTTL = 100 * time.Millisecond
	if err := r.SyncAlias(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	watch := eventReceive(t, watches)
	r.mu.Lock()
	// Keep the worker active: removal, not idle expiry, must close the watch.
	r.workers[1].lastAccess = time.Now().Add(time.Minute)
	r.mu.Unlock()
	repo.mu.Lock()
	repo.accounts = repo.accounts[1:]
	repo.mu.Unlock()
	eventReceive(t, watch.stopped)
	eventEventually(t, func() bool { r.mu.Lock(); defer r.mu.Unlock(); return len(r.workers) == 0 })
}
