package httpserver

import (
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestMailboxWatchHealthySuppressesAPIPollsAndOutageCoalesces(t *testing.T) {
	calls := make(chan int64, 20)
	var healthy atomic.Bool
	healthy.Store(true)
	s := &Server{sync: func(id int64) error { calls <- id; return nil }}
	s.SetMailboxWatchHealth(func(id int64) bool { return id == 1 && healthy.Load() })
	now := time.Now()
	for i := 0; i < 100; i++ {
		s.requestMailboxSync(1, now.Add(time.Duration(i)*time.Second))
	}
	select {
	case <-calls:
		t.Fatal("healthy IDLE caused upstream polling")
	case <-time.After(20 * time.Millisecond):
	}
	healthy.Store(false)
	for i := 0; i < 10; i++ {
		s.requestMailboxSync(1, now.Add(time.Duration(i)*time.Second))
	}
	select {
	case id := <-calls:
		if id != 1 {
			t.Fatal("wrong account")
		}
	case <-time.After(time.Second):
		t.Fatal("outage did not trigger sync")
	}
	select {
	case <-calls:
		t.Fatal("outage API calls were not coalesced")
	case <-time.After(20 * time.Millisecond):
	}
	s.requestMailboxSync(1, now.Add(10*time.Second))
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("outage cooldown never expired")
	}
	healthy.Store(true)
	s.requestMailboxSync(2, now)
	select {
	case id := <-calls:
		if id != 2 {
			t.Fatal("wrong account")
		}
	case <-time.After(time.Second):
		t.Fatal("another account inherited IDLE health")
	}
}

func TestLegacySnapshotStaysFreshOnlyWhileWatchIsHealthy(t *testing.T) {
	now := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	f := newLegacyMailBoundaryFixture(t, now, now.Add(-10*time.Minute), now.Add(-time.Minute))
	f.env.server.cfg.PollInterval = 10 * time.Second
	f.env.server.cfg.SyncTimeout = 70 * time.Second
	f.env.server.SetMailboxWatchHealth(func(id int64) bool { return id == f.account.ID })
	if got := f.latest(t, f.rawKey); got.Code != http.StatusOK {
		t.Fatalf("healthy watch snapshot status=%d", got.Code)
	}
	f.env.server.SetMailboxWatchHealth(func(int64) bool { return false })
	if got := f.latest(t, f.rawKey); got.Code != http.StatusServiceUnavailable {
		t.Fatalf("disconnected watch stale snapshot status=%d", got.Code)
	}
}
