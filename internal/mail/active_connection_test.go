package mail

import (
	"context"
	"errors"
	"testing"
	"time"

	imap "github.com/emersion/go-imap/v2"
	"icloud-api/internal/domain"
)

func TestActiveOwnerCloseInvalidatesContextAndNewOwnerRelogins(t *testing.T) {
	fixture := startArchiveIMAPFixture(t, 1, 0)
	fetcher := NewFetcher()
	fetcher.ArchiveTempDir = t.TempDir()
	ctx, closeOwner := fetcher.OpenActiveAccount(context.Background(), fixture.account.ID)
	state := fixture.initialState(t)
	if _, err := fetcher.FetchActiveIncremental(ctx, fixture.account, "fixture-password", []domain.Alias{fixture.alias}, &state, nil); err != nil {
		t.Fatal(err)
	}
	session := ctx.Value(activeConnectionKey{}).(*activeConnection)
	closeOwner()
	session.mu.Lock()
	if !session.closed || session.client != nil {
		t.Error("owner close retained a cached connection")
	}
	session.mu.Unlock()
	if _, err := fetcher.FetchActiveIncremental(ctx, fixture.account, "fixture-password", []domain.Alias{fixture.alias}, &state, nil); err == nil {
		t.Fatal("closed owner context unexpectedly remained usable")
	}
	ctx, closeOwner = fetcher.OpenActiveAccount(context.Background(), fixture.account.ID)
	defer closeOwner()
	if _, err := fetcher.FetchActiveIncremental(ctx, fixture.account, "fixture-password", []domain.Alias{fixture.alias}, &state, nil); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.loginCount != 2 {
		t.Fatalf("new owner login count=%d, want 2", fixture.loginCount)
	}
}

func TestActiveOwnerCancellationInterruptsFetchAndDropsSocket(t *testing.T) {
	fixture := startArchiveIMAPFixture(t, 1, 1)
	fetcher := NewFetcher()
	fetcher.ArchiveTempDir = t.TempDir()
	fetcher.now = func() time.Time { return time.Date(2026, 9, 11, 1, 4, 0, 0, time.UTC) }
	ctx, closeOwner := fetcher.OpenActiveAccount(context.Background(), fixture.account.ID)
	defer closeOwner()
	state := fixture.initialState(t)
	state.UpdatedAt = fetcher.now()
	done := make(chan error, 1)
	go func() {
		_, err := fetcher.FetchActiveIncremental(ctx, fixture.account, "fixture-password", []domain.Alias{fixture.alias}, &state, nil)
		done <- err
	}()
	select {
	case <-fixture.bodyStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("fetch did not start")
	}
	closeOwner()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("owner cancellation did not interrupt a blocked read")
	}
	session := ctx.Value(activeConnectionKey{}).(*activeConnection)
	if len(session.slot) != 0 {
		t.Fatal("cancelled read retained its slot")
	}
}

func TestActiveOwnerDoesNotReuseChangedCredentials(t *testing.T) {
	fixture := startArchiveIMAPFixture(t, 1, 0)
	fetcher := NewFetcher()
	fetcher.ArchiveTempDir = t.TempDir()
	ctx, closeOwner := fetcher.OpenActiveAccount(context.Background(), fixture.account.ID)
	defer closeOwner()
	state := fixture.initialState(t)
	if _, err := fetcher.FetchActiveIncremental(ctx, fixture.account, "fixture-password", []domain.Alias{fixture.alias}, &state, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := fetcher.FetchActiveIncremental(ctx, fixture.account, "wrong-password", []domain.Alias{fixture.alias}, &state, nil); err == nil {
		t.Fatal("wrong password reused the authenticated connection")
	}
	if _, err := fetcher.FetchActiveIncremental(ctx, fixture.account, "fixture-password", []domain.Alias{fixture.alias}, &state, nil); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.loginCount != 3 {
		t.Fatalf("credential rotation login count=%d, want 3", fixture.loginCount)
	}
}

func TestActiveOwnerRejectsDifferentAccount(t *testing.T) {
	fixture := startArchiveIMAPFixture(t, 1, 0)
	fetcher := NewFetcher()
	ctx, closeOwner := fetcher.OpenActiveAccount(context.Background(), fixture.account.ID)
	defer closeOwner()
	account := fixture.account
	account.ID++
	alias := fixture.alias
	alias.AccountID = account.ID
	_, err := fetcher.FetchActiveIncremental(ctx, account, "fixture-password", []domain.Alias{alias}, nil, nil)
	if err == nil || err.Error() != "active IMAP connection account mismatch" {
		t.Fatalf("cross-account error = %v", err)
	}
}

func TestActiveFetchFailureDropsConnectionAndPreservesCursor(t *testing.T) {
	fixture := startArchiveIMAPFixture(t, 2, 0)
	fail := true
	fixture.searchPolicy = func(*imap.SearchCriteria) error {
		if fail {
			fail = false
			return errors.New("injected search failure")
		}
		return nil
	}
	fetcher := NewFetcher()
	fetcher.ArchiveTempDir = t.TempDir()
	fetcher.now = func() time.Time { return time.Date(2026, 9, 11, 1, 4, 0, 0, time.UTC) }
	ctx, closeOwner := fetcher.OpenActiveAccount(context.Background(), fixture.account.ID)
	defer closeOwner()
	state := fixture.initialState(t)
	state.UpdatedAt = time.Date(2026, 9, 11, 1, 4, 0, 0, time.UTC)
	failed, err := fetcher.FetchActiveIncremental(ctx, fixture.account, "fixture-password", []domain.Alias{fixture.alias}, &state, nil)
	if err == nil {
		t.Fatal("injected search failure unexpectedly succeeded")
	}
	if failed.State != state {
		t.Fatal("failed fetch changed the committed cursor")
	}
	result, err := fetcher.FetchActiveIncremental(ctx, fixture.account, "fixture-password", []domain.Alias{fixture.alias}, &state, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.State.LastUID != 2 || len(result.ArchivedMessages) != 2 {
		t.Fatalf("retry cursor=%d archived=%d, want 2/2", result.State.LastUID, len(result.ArchivedMessages))
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.loginCount != 2 {
		t.Fatalf("retry login count=%d, want 2", fixture.loginCount)
	}
}

func TestFetchActiveIncrementalReusesTLSConnectionAcrossBatches(t *testing.T) {
	fixture := startArchiveIMAPFixture(t, 65, 0)
	fetcher := NewFetcher()
	fetcher.ArchiveTempDir = t.TempDir()
	fetcher.MaxIncrementalCandidates = 32
	now := time.Date(2026, 9, 11, 1, 4, 0, 0, time.UTC)
	fetcher.now = func() time.Time { return now }
	ctx, closeActive := fetcher.OpenActiveAccount(context.Background(), fixture.account.ID)
	defer closeActive()
	state := fixture.initialState(t)
	state.UpdatedAt = now
	count := 0
	for {
		result, err := fetcher.FetchActiveIncremental(ctx, fixture.account, "fixture-password", []domain.Alias{fixture.alias}, &state, nil)
		if err != nil {
			t.Fatal(err)
		}
		count += len(result.ArchivedMessages)
		state = result.State
		if !result.HasMore {
			break
		}
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.loginCount != 1 {
		t.Fatalf("active batches logged in %d times, want 1", fixture.loginCount)
	}
	if count != 65 || len(fixture.bodyUIDs) != 65 {
		t.Fatalf("active batches archived=%d bodies=%d, want 65", count, len(fixture.bodyUIDs))
	}
}
