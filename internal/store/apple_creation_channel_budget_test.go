package store_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"icloud-api/internal/store"
)

func TestAppleCreationChannelsHaveIndependentLimitsAndRollingBoundary(t *testing.T) {
	db := openTestStore(t)
	ctx := context.Background()
	a := createAccount(t, ctx, db, "channel limits", "channel-limits@icloud.com")
	now := time.Date(2026, 9, 21, 1, 0, 0, 0, time.UTC)
	for i := 0; i < 19; i++ {
		if err := db.ClaimAppleChannelCreationAttempt(ctx, a.ID, "apple_account", now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("apple attempt %d: %v", i, err)
		}
	}
	if err := db.ClaimAppleChannelCreationAttempt(ctx, a.ID, "apple_account", now.Add(30*time.Minute)); !isAppleBudgetWait(err) {
		t.Fatalf("apple overflow = %v", err)
	}
	for i := 0; i < 4; i++ {
		if err := db.ClaimAppleChannelCreationAttempt(ctx, a.ID, "icloud_web", now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("web attempt %d: %v", i, err)
		}
	}
	if err := db.ClaimAppleChannelCreationAttempt(ctx, a.ID, "icloud_web", now.Add(30*time.Minute)); !isAppleBudgetWait(err) {
		t.Fatalf("web overflow = %v", err)
	}
	if err := db.ClaimAppleChannelCreationAttempt(ctx, a.ID, "apple_account", now.Add(time.Hour)); err != nil {
		t.Fatalf("rolling boundary: %v", err)
	}
}

func TestAppleCreationChannelPauseIsMaximumAndScoped(t *testing.T) {
	db := openTestStore(t)
	ctx := context.Background()
	a := createAccount(t, ctx, db, "channel pause", "channel-pause@icloud.com")
	now := time.Date(2026, 9, 21, 2, 0, 0, 0, time.UTC)
	long := now.Add(time.Hour)
	if err := db.PauseAppleChannelCreation(ctx, a.ID, "apple_account", long); err != nil {
		t.Fatal(err)
	}
	if err := db.PauseAppleChannelCreation(ctx, a.ID, "apple_account", now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := db.ClaimAppleChannelCreationAttempt(ctx, a.ID, "apple_account", now); !isAppleBudgetWait(err) || !budgetUntil(err).Equal(long) {
		t.Fatalf("pause = %v, until=%v", err, budgetUntil(err))
	}
	if err := db.ClaimAppleChannelCreationAttempt(ctx, a.ID, "icloud_web", now); err != nil {
		t.Fatalf("other channel: %v", err)
	}
}

func TestAppleCreationChannelRejectsDisabledAndInvalid(t *testing.T) {
	db := openTestStore(t)
	ctx := context.Background()
	a := createAccount(t, ctx, db, "channel disabled", "channel-disabled@icloud.com")
	now := time.Now().UTC()
	if _, err := db.DB().ExecContext(ctx, `UPDATE accounts SET enabled = FALSE WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.ClaimAppleChannelCreationAttempt(ctx, a.ID, "apple_account", now); !errors.Is(err, store.ErrAccountDisabled) {
		t.Fatalf("disabled claim = %v", err)
	}
	if err := db.ClaimAppleChannelCreationAttempt(ctx, a.ID, "unknown", now); err == nil {
		t.Fatal("invalid channel accepted")
	}
	if err := db.PauseAppleChannelCreation(ctx, a.ID, "unknown", now.Add(time.Hour)); err == nil {
		t.Fatal("invalid pause accepted")
	}
}

func TestAppleCreationChannelMigrationMarkerIsIdempotent(t *testing.T) {
	db := openTestStore(t)
	ctx := context.Background()
	a := createAccount(t, ctx, db, "channel migration", "channel-migration@icloud.com")
	now := time.Now().UTC()
	if _, err := db.DB().ExecContext(ctx, `DELETE FROM apple_creation_budget_migrations WHERE version = 2`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := db.DB().ExecContext(ctx, `INSERT INTO apple_creation_attempts(account_id,attempted_at) VALUES (?,?)`, a.ID, now.Add(-time.Duration(i+1)*time.Minute).UnixNano()); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, channel := range []string{"apple_account", "icloud_web"} {
		var count int
		if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM apple_creation_channel_attempts WHERE account_id=? AND channel=?`, a.ID, channel).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 5 {
			t.Fatalf("seed %s count=%d", channel, count)
		}
	}
	if err := db.ClaimAppleChannelCreationAttempt(ctx, a.ID, "icloud_web", now); !isAppleBudgetWait(err) {
		t.Fatalf("seed failed to protect Web channel: %v", err)
	}
	if _, err := db.DB().ExecContext(ctx, `INSERT INTO apple_creation_attempts(account_id,attempted_at) VALUES (?,?)`, a.ID, now.UnixNano()); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var after, attempts int
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM apple_creation_budget_migrations WHERE version = 2`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM apple_creation_channel_attempts`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if after != 1 {
		t.Fatalf("version 2 markers after rerun = %d", after)
	}
	if attempts != 10 {
		t.Fatalf("migration reseeded attempts: %d", attempts)
	}
}

func TestAppleCreationChannelPersistenceAndConcurrency(t *testing.T) {
	checkChannelPersistenceAndConcurrency(t, filepath.Join(t.TempDir(), "channel.db"))
}

func TestAppleCreationChannelPostgresPersistenceAndConcurrency(t *testing.T) {
	dsn := os.Getenv("ICLOUD_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("ICLOUD_TEST_POSTGRES_URL is not set")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") || !strings.Contains(u.Path, "test") {
		t.Fatal("expected disposable loopback test database")
	}
	checkChannelPersistenceAndConcurrency(t, dsn)
}

func checkChannelPersistenceAndConcurrency(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	now := time.Now().UTC()
	a := createAccount(t, ctx, db, "channel concurrency", fmt.Sprintf("channel-%d@icloud.com", now.UnixNano()))
	for _, channel := range []string{"apple_account", "icloud_web"} {
		results := make(chan error, 32)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range 32 {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				results <- db.ClaimAppleChannelCreationAttempt(ctx, a.ID, channel, now.Add(time.Duration(i)*time.Nanosecond))
			}(i)
		}
		close(start)
		wg.Wait()
		close(results)
		accepted := 0
		for err := range results {
			if err == nil {
				accepted++
			} else if !isAppleBudgetWait(err) {
				t.Fatal(err)
			}
		}
		want := 19
		if channel == "icloud_web" {
			want = 4
		}
		if accepted != want {
			t.Fatalf("%s accepted=%d want=%d", channel, accepted, want)
		}
	}
	until := now.Add(2 * time.Hour)
	if err := db.PauseAppleChannelCreation(ctx, a.ID, "apple_account", until); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.ClaimAppleChannelCreationAttempt(ctx, a.ID, "apple_account", now.Add(time.Hour+time.Second)); !isAppleBudgetWait(err) || !budgetUntil(err).Equal(until) {
		t.Fatalf("pause after restart: %v", err)
	}
	if err := db.ClaimAppleChannelCreationAttempt(ctx, a.ID, "icloud_web", now.Add(time.Hour+time.Second)); err != nil {
		t.Fatalf("other channel after restart: %v", err)
	}
}
