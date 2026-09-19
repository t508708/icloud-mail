package mail

import (
	"context"
	"testing"
	"time"

	"icloud-api/internal/domain"
)

// These tests use the TLS IMAP fixture from archive_fetcher_transport_test.go;
// in particular they exercise the same LOGIN/SELECT/FETCH path as production.
func TestFetchActiveIncrementalInitialTailAndStaleCursor(t *testing.T) {
	fixture := startArchiveIMAPFixture(t, 130, 0)
	fetcher := NewFetcher()
	fetcher.ArchiveTempDir = t.TempDir()
	fetcher.ActiveRecentCandidates = 128
	fetcher.MaxIncrementalCandidates = 128
	fetcher.now = func() time.Time { return time.Date(2026, 9, 11, 1, 0, 0, 0, time.UTC) }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := fetcher.FetchActiveIncremental(ctx, fixture.account, "fixture-password", []domain.Alias{fixture.alias}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reset || len(result.ArchivedMessages) != 128 {
		t.Fatalf("active initial result reset=%v archived=%d, want reset and 128", result.Reset, len(result.ArchivedMessages))
	}
	// Replaying the committed active cursor must be a no-op, not another tail
	// download.
	state := result.State
	fixture.mu.Lock()
	fixture.bodyUIDs = nil
	fixture.mu.Unlock()
	result, err = fetcher.FetchActiveIncremental(ctx, fixture.account, "fixture-password", []domain.Alias{fixture.alias}, &state, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ArchivedMessages) != 0 || result.State.LastUID != state.LastUID {
		t.Fatalf("active same-cursor result archived=%d cursor=%d, want no-op at %d", len(result.ArchivedMessages), result.State.LastUID, state.LastUID)
	}
	fixture.mu.Lock()
	if len(fixture.bodyUIDs) != 0 {
		t.Fatalf("same active cursor fetched bodies=%d, want 0", len(fixture.bodyUIDs))
	}
	fixture.mu.Unlock()
	state = result.State
	state.LastUID = 1 // stale cursor: active fetch must still use the recent tail.
	state.UIDValidity++
	result, err = fetcher.FetchActiveIncremental(ctx, fixture.account, "fixture-password", []domain.Alias{fixture.alias}, &state, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reset || len(result.ArchivedMessages) != 128 {
		t.Fatalf("active stale/reset result reset=%v archived=%d, want reset and 128", result.Reset, len(result.ArchivedMessages))
	}
}

func TestFetchActiveIncrementalSkipsOldBodies(t *testing.T) {
	fixture := startArchiveIMAPFixture(t, 2, 0)
	fetcher := NewFetcher()
	fetcher.ArchiveTempDir = t.TempDir()
	fetcher.now = func() time.Time { return time.Date(2026, 9, 11, 1, 16, 0, 0, time.UTC) }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := fetcher.FetchActiveIncremental(ctx, fixture.account, "fixture-password", []domain.Alias{fixture.alias}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ArchivedMessages) != 0 {
		t.Fatalf("old active messages archived=%d, want 0", len(result.ArchivedMessages))
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.bodyUIDs) != 0 {
		t.Fatalf("old active messages fetched bodies=%d, want 0", len(fixture.bodyUIDs))
	}
}

func TestFetchActiveIncrementalLiveBacklogContinuesWithoutSkipping(t *testing.T) {
	f := startArchiveIMAPFixture(t, 140, 0)
	fetcher := NewFetcher()
	fetcher.ArchiveTempDir = t.TempDir()
	fetcher.MaxIncrementalCandidates = 32
	now := time.Date(2026, 9, 11, 1, 4, 0, 0, time.UTC)
	fetcher.now = func() time.Time { return now }
	state := f.initialState(t)
	state.UpdatedAt = now
	count := 0
	for i := 0; i < 5; i++ {
		result, err := fetcher.FetchActiveIncremental(context.Background(), f.account, "fixture-password", []domain.Alias{f.alias}, &state, nil)
		if err != nil {
			t.Fatal(err)
		}
		if result.State.LastUID <= state.LastUID {
			t.Fatal("live cursor did not advance")
		}
		if result.RecoveryBoundaryUID != 0 {
			t.Fatal("live backlog unexpectedly truncated")
		}
		count += len(result.ArchivedMessages)
		state = result.State
		if !result.HasMore {
			break
		}
	}
	if count != 140 || state.LastUID != 140 {
		t.Fatalf("live backlog messages=%d cursor=%d", count, state.LastUID)
	}
	// A stale cursor in the same UID generation uses a bounded recent tail.
	state.LastUID = 1
	state.UpdatedAt = now.Add(-time.Hour)
	fetcher.MaxIncrementalCandidates = 128
	result, err := fetcher.FetchActiveIncremental(context.Background(), f.account, "fixture-password", []domain.Alias{f.alias}, &state, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Reset || result.RecoveryBoundaryUID != 12 || len(result.ArchivedMessages) != 128 {
		t.Fatalf("stale recovery: reset=%v boundary=%d messages=%d", result.Reset, result.RecoveryBoundaryUID, len(result.ArchivedMessages))
	}
}
