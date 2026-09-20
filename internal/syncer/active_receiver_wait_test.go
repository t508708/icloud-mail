package syncer

import (
	"context"
	"testing"
	"time"
)

func TestMailboxChangesDoesNotCreateWorker(t *testing.T) {
	r, _ := activeTestReceiver(t, nil, func(context.Context, int64) error { return nil })
	if ch, d := r.MailboxChanges(1); ch != nil || d != 0 {
		t.Fatalf("missing worker = (%v, %v)", ch, d)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.workers) != 0 {
		t.Fatal("observation created a worker")
	}
}

func TestMailboxChangesPendingUsesSyncTimeout(t *testing.T) {
	r, _ := activeTestReceiver(t, nil, func(context.Context, int64) error { return nil })
	changed := make(chan struct{})
	r.mu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r.workers[1] = &activeReceiverWorker{changed: changed, cancel: cancel, ctx: ctx, generation: 2, committed: 1}
	r.mu.Unlock()
	ch, d := r.MailboxChanges(1)
	if ch != changed || d != r.manager.syncTimeout {
		t.Fatalf("pending = (%v, %v), want channel and %v", ch, d, r.manager.syncTimeout)
	}
}

func TestMailboxChangesErrorClampsRetry(t *testing.T) {
	r, _ := activeTestReceiver(t, nil, func(context.Context, int64) error { return nil })
	changed := make(chan struct{})
	r.mu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r.workers[1] = &activeReceiverWorker{changed: changed, cancel: cancel, ctx: ctx, err: context.Canceled, nextTry: time.Now().Add(-time.Second)}
	r.mu.Unlock()
	ch, d := r.MailboxChanges(1)
	if ch != changed || d != 0 {
		t.Fatalf("expired retry = (%v, %v)", ch, d)
	}
}

func TestMailboxChangesCommitCloseAndFixedFreshness(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	r, _ := activeTestReceiver(t, nil, func(ctx context.Context, _ int64) error {
		close(entered)
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
	changed, delay := r.MailboxChanges(1)
	if changed == nil || delay != r.manager.syncTimeout {
		t.Fatal("pending observer is not bounded by the shared sync")
	}
	close(release)
	eventReceive(t, changed)
	if err := eventReceive(t, result); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	observed, access := r.workers[1].lastSync, r.workers[1].lastAccess
	r.mu.Unlock()
	closed, first := r.MailboxChanges(1)
	_, second := r.MailboxChanges(1)
	if first > r.fallbackFreshness || second > first {
		t.Fatal("subscribing extends freshness")
	}
	r.mu.Lock()
	if r.workers[1].lastSync != observed || r.workers[1].lastAccess != access {
		t.Error("subscribing changed worker activity")
	}
	r.mu.Unlock()
	r.Close()
	eventReceive(t, closed)
	if ch, _ := r.MailboxChanges(1); ch != nil {
		t.Fatal("closed receiver exposes an active subscription")
	}
}
