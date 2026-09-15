package store_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"icloud-api/internal/store"
)

// Run against a dedicated, disposable PostgreSQL database via
// ICLOUD_TEST_POSTGRES_URL. Never point this at the application's database.
func TestAppleCreationBudgetPostgresPersistenceConcurrencyAndMigration(t *testing.T) {
	dsn := os.Getenv("ICLOUD_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("ICLOUD_TEST_POSTGRES_URL is not set")
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	database := strings.TrimPrefix(parsed.Path, "/")
	host := parsed.Hostname()
	if host != "localhost" && host != "127.0.0.1" && host != "::1" || !strings.Contains(strings.ToLower(database), "test") {
		t.Fatalf("refusing PostgreSQL budget test outside a loopback test database: host=%q database=%q", host, database)
	}
	ctx := context.Background()
	db, err := store.OpenContext(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	now := started.Truncate(time.Second)
	suffix := fmt.Sprintf("%d", started.UnixNano())
	account := createAccount(t, ctx, db, "PG budget migration", "pg-budget-migration-"+suffix+"@icloud.com")
	seedAt := now.Add(-time.Hour)
	for _, address := range []string{"pg-seed-one@icloud.com", "pg-seed-two@icloud.com"} {
		if _, err := db.DB().ExecContext(ctx, `INSERT INTO apple_alias_creation_events(account_id, address, created_at) VALUES ($1, $2, $3)`, account.ID, address, seedAt.UnixNano()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.DB().ExecContext(ctx, `DELETE FROM apple_creation_budget_migrations`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("apply one-time budget seed: %v", err)
	}
	assertPGAttempts(t, ctx, db, account.ID, 1, seedAt)
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("repeat budget migration: %v", err)
	}
	assertPGAttempts(t, ctx, db, account.ID, 1, seedAt)

	concurrent := createAccount(t, ctx, db, "PG budget concurrency", "pg-budget-concurrency-"+suffix+"@icloud.com")
	const workers = 12
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes, waits := 0, 0
	other := []error{}
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := db.ClaimAppleCreationAttempt(ctx, concurrent.ID, now)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successes++
			} else {
				var budget *store.AppleCreationBudgetError
				if errors.As(err, &budget) {
					waits++
				} else {
					other = append(other, err)
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	if successes != 1 || waits != workers-1 || len(other) > 0 {
		t.Fatalf("concurrent PostgreSQL claims: success=%d waits=%d other=%v", successes, waits, other)
	}
	pauseUntil := now.Add(30 * time.Hour)
	if err := db.PauseAppleCreation(ctx, concurrent.ID, pauseUntil); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.OpenContext(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if err := reopened.Migrate(ctx); err != nil {
		t.Fatalf("migrate reopened PostgreSQL store: %v", err)
	}
	assertPGAttempts(t, ctx, reopened, concurrent.ID, 1, now)
	err = reopened.ClaimAppleCreationAttempt(ctx, concurrent.ID, now.Add(time.Hour))
	var budget *store.AppleCreationBudgetError
	if !errors.As(err, &budget) || !budget.Until.Equal(pauseUntil) {
		t.Fatalf("PostgreSQL cooldown after reopen=%v, want wait until %v", err, pauseUntil)
	}
}

func assertPGAttempts(t *testing.T, ctx context.Context, db *store.Store, accountID int64, wantCount int, wantAt time.Time) {
	t.Helper()
	var count int
	var at int64
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*), MIN(attempted_at) FROM apple_creation_attempts WHERE account_id = $1`, accountID).Scan(&count, &at); err != nil {
		t.Fatal(err)
	}
	if count != wantCount || at != wantAt.UnixNano() {
		t.Fatalf("PostgreSQL attempts count=%d at=%d; want %d at %d", count, at, wantCount, wantAt.UnixNano())
	}
}
