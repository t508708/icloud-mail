package syncer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"icloud-api/internal/domain"
	"icloud-api/internal/store"
)

func TestAliasDemandFailureLogsRequestAndRedactsCredentials(t *testing.T) {
	account := domain.Account{ID: 1, Email: "owner@example.test", IMAPUsername: "login@example.test", PasswordCiphertext: "encrypted-fixture", Enabled: true}
	base := newFakeRepo(account)
	base.aliases[1] = []domain.Alias{{ID: 10, AccountID: 1, Address: "target@example.test", Enabled: true}}
	var logs bytes.Buffer
	fetcher := demandFetcherFake{fn: func(context.Context, domain.Alias, *domain.IMAPSyncState, map[int64]domain.MailboxSnapshotPosition) (domain.MailboxSyncResult, error) {
		return domain.MailboxSyncResult{}, errors.New("discover new mailbox UIDs: imap: NO [UNAVAILABLE] Unexpected exception fixture encrypted-fixture owner@example.test login@example.test target@example.test")
	}}
	m := New(&demandRepoFake{fakeRepo: base}, demandCipherFake{}, fetcher, slog.New(slog.NewJSONHandler(&logs, nil)), time.Second, 1)
	ctx := domain.WithMailboxRequestID(context.Background(), "demand-request")
	if err := m.SyncAliasOnDemand(ctx, 10); err == nil {
		t.Fatal("search failure lost")
	}
	output := logs.String()
	for _, want := range []string{"单邮箱按需获取失败", `"request_id":"demand-request"`, `"account_id":1`, `"alias_id":10`, `"failed_operation":"fetch"`, "discover new mailbox UIDs", "[UNAVAILABLE]"} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing diagnostic %q", want)
		}
	}
	for _, secret := range []string{"fixture", account.Email, account.IMAPUsername, account.PasswordCiphertext, "target@example.test"} {
		if strings.Contains(output, secret) {
			t.Fatal("credential or mailbox appeared in diagnostic")
		}
	}
	if strings.Count(output, "单邮箱按需获取") != 1 {
		t.Fatal("duplicate failure diagnostics")
	}
}

type demandRepoFake struct {
	*fakeRepo
	aliasStates map[int64]domain.IMAPSyncState
	targets     []int64
}

func (r *demandRepoFake) GetAlias(_ context.Context, id int64) (domain.Alias, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, aliases := range r.aliases {
		for _, alias := range aliases {
			if alias.ID == id {
				return alias, nil
			}
		}
	}
	return domain.Alias{}, store.ErrNotFound
}
func (r *demandRepoFake) GetAliasIMAPSyncState(_ context.Context, id int64) (domain.IMAPSyncState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if state, ok := r.aliasStates[id]; ok {
		return state, nil
	}
	return domain.IMAPSyncState{}, store.ErrNotFound
}
func (r *demandRepoFake) ApplyAliasMailboxSync(_ context.Context, _ time.Time, a domain.Alias, result domain.MailboxSyncResult, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.aliasStates == nil {
		r.aliasStates = make(map[int64]domain.IMAPSyncState)
	}
	r.aliasStates[a.ID] = result.State
	r.targets = append(r.targets, a.ID)
	return nil
}

type demandFetcherFake struct {
	fn func(context.Context, domain.Alias, *domain.IMAPSyncState, map[int64]domain.MailboxSnapshotPosition) (domain.MailboxSyncResult, error)
}

func (f demandFetcherFake) FetchIncremental(context.Context, domain.Account, string, []domain.Alias, *domain.IMAPSyncState, map[int64]domain.MailboxSnapshotPosition) (domain.MailboxSyncResult, error) {
	return domain.MailboxSyncResult{}, errors.New("unexpected account-wide fetch")
}
func (f demandFetcherFake) FetchAliasIncremental(ctx context.Context, _ domain.Account, _ string, a domain.Alias, _ []domain.Alias, s *domain.IMAPSyncState, p map[int64]domain.MailboxSnapshotPosition) (domain.MailboxSyncResult, error) {
	return f.fn(ctx, a, s, p)
}

type demandCipherFake struct{}

func (demandCipherFake) Decrypt(string) (string, error) { return "fixture", nil }

func TestAliasDemandCoalescesAndKeepsOtherAliasCursor(t *testing.T) {
	account := domain.Account{ID: 1, Enabled: true, Email: "owner@example.test", UpdatedAt: time.Now()}
	base := newFakeRepo(account)
	base.aliases[1] = []domain.Alias{{ID: 10, AccountID: 1, Address: "a@example.test", Enabled: true}, {ID: 20, AccountID: 1, Address: "b@example.test", Enabled: true}}
	base.positions[1] = map[int64]domain.MailboxSnapshotPosition{10: {AliasID: 10, UIDValidity: 1, UID: 5}, 20: {AliasID: 20, UIDValidity: 1, UID: 6}}
	repo := &demandRepoFake{fakeRepo: base}
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	fetcher := demandFetcherFake{fn: func(ctx context.Context, a domain.Alias, state *domain.IMAPSyncState, p map[int64]domain.MailboxSnapshotPosition) (domain.MailboxSyncResult, error) {
		calls.Add(1)
		if state != nil {
			t.Error("other alias cursor reused")
		}
		if len(p) != 1 || p[a.ID].AliasID != a.ID {
			t.Errorf("other alias positions leaked: %v", p)
		}
		if a.ID == 10 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return domain.MailboxSyncResult{}, ctx.Err()
			}
		}
		return domain.MailboxSyncResult{State: domain.IMAPSyncState{AccountID: 1, UIDValidity: 1, LastUID: 50}, Reset: true, HasMore: true}, nil
	}}
	m := New(repo, demandCipherFake{}, fetcher, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second, 2)
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := m.SyncAliasOnDemand(context.Background(), 10); err != nil {
				t.Error(err)
			}
		}()
	}
	<-started
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("duplicate fetches=%d", calls.Load())
	}
	if err := m.SyncAliasOnDemand(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("immediate refresh ignored debounce")
	}
	if err := m.SyncAliasOnDemand(context.Background(), 20); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || len(repo.targets) != 2 || len(base.applies) != 0 {
		t.Fatalf("calls=%d target=%v wholeaccount=%d", calls.Load(), repo.targets, len(base.applies))
	}
}

func TestAliasDemandModeMakesNoPeriodicRequests(t *testing.T) {
	base := newFakeRepo(domain.Account{ID: 1, Enabled: true})
	base.listFn = func(context.Context) ([]domain.Account, error) {
		t.Error("periodic scan in demand mode")
		return nil, nil
	}
	m := New(base, demandCipherFake{}, demandFetcherFake{}, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Millisecond, 1)
	m.SetOnDemandOnly(true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	m.BeginShutdown()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("demand loop did not stop")
	}
}

func TestAliasDemandAuthenticationFailureStopsFurtherAliases(t *testing.T) {
	base := newFakeRepo(domain.Account{ID: 1, Enabled: true, Email: "owner@example.test", UpdatedAt: time.Now()})
	base.aliases[1] = []domain.Alias{{ID: 10, AccountID: 1, Enabled: true}, {ID: 20, AccountID: 1, Enabled: true}}
	repo := &demandRepoFake{fakeRepo: base}
	calls := 0
	base.failureFn = func(_ context.Context, id int64, _ time.Time, message string, _ time.Time) error {
		base.mu.Lock()
		defer base.mu.Unlock()
		base.accounts[0].LastSyncError = message
		return nil
	}
	fetcher := demandFetcherFake{fn: func(context.Context, domain.Alias, *domain.IMAPSyncState, map[int64]domain.MailboxSnapshotPosition) (domain.MailboxSyncResult, error) {
		calls++
		return domain.MailboxSyncResult{}, errors.New("login IMAP: AUTHENTICATIONFAILED")
	}}
	m := New(repo, demandCipherFake{}, fetcher, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second, 1)
	if err := m.SyncAliasOnDemand(context.Background(), 10); err == nil {
		t.Fatal("authentication error lost")
	}
	if err := m.SyncAliasOnDemand(context.Background(), 20); !errors.Is(err, domain.ErrIMAPAuthenticationPaused) {
		t.Fatalf("breaker=%v", err)
	}
	if calls != 1 {
		t.Fatalf("auth retry requests=%d", calls)
	}
}
