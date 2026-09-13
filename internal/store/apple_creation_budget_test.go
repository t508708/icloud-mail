package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"icloud-api/internal/store"
)

func TestAppleCreationBudgetConcurrentClaimsAreSerialized(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestStore(t)
	account := createAccount(t, ctx, db, "Budget concurrency", "budget-concurrency@icloud.com")
	now := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	const workers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes, waits := 0, 0
	otherErrors := make([]error, 0)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := db.ClaimAppleCreationAttempt(ctx, account.ID, now)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successes++
				return
			}
			var budgetErr *store.AppleCreationBudgetError
			if errors.As(err, &budgetErr) {
				waits++
				return
			}
			otherErrors = append(otherErrors, err)
		}()
	}
	close(start)
	wg.Wait()
	if successes != 1 || waits != workers-1 || len(otherErrors) != 0 {
		t.Fatalf("claim results success=%d waits=%d other=%v; want one success and %d waits", successes, waits, otherErrors, workers-1)
	}
}

func TestAppleCreationBudgetRollingHourlyDailyAndMinInterval(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestStore(t)
	account := createAccount(t, ctx, db, "Budget windows", "budget-windows@icloud.com")
	start := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	if err := db.ClaimAppleCreationAttempt(ctx, account.ID, start); err != nil {
		t.Fatal(err)
	}
	if err := db.ClaimAppleCreationAttempt(ctx, account.ID, start.Add(5*time.Minute)); !isAppleBudgetWait(err) {
		t.Fatalf("claim inside minimum interval = %v, want budget wait", err)
	}
	for attempt := 1; attempt < store.AppleCreationHourlyLimit; attempt++ {
		if err := db.ClaimAppleCreationAttempt(ctx, account.ID, start.Add(time.Duration(attempt)*10*time.Minute)); err != nil {
			t.Fatalf("hourly attempt %d: %v", attempt+1, err)
		}
	}
	hourlyWaitAt := start.Add(50 * time.Minute)
	err := db.ClaimAppleCreationAttempt(ctx, account.ID, hourlyWaitAt)
	if !isAppleBudgetWait(err) || !budgetUntil(err).Equal(start.Add(time.Hour)) {
		t.Fatalf("sixth rolling-hour claim = %v, until=%v; want wait until %v", err, budgetUntil(err), start.Add(time.Hour))
	}
	var hourlyBudgetErr *store.AppleCreationBudgetError
	if !errors.As(err, &hourlyBudgetErr) {
		t.Fatalf("hourly wait error type = %T, want AppleCreationBudgetError", err)
	}
	if delay := hourlyBudgetErr.RetryDelay(); delay != 10*time.Minute {
		t.Fatalf("hourly RetryDelay = %v, want 10m", delay)
	}
	if err := db.ClaimAppleCreationAttempt(ctx, account.ID, start.Add(time.Hour)); err != nil {
		t.Fatalf("claim after oldest hourly attempt expired: %v", err)
	}

	dailyAccount := createAccount(t, ctx, db, "Daily budget", "daily-budget@icloud.com")
	for attempt := 0; attempt < store.AppleCreationDailyLimit; attempt++ {
		at := start.Add(time.Duration(attempt) * time.Hour)
		if err := db.ClaimAppleCreationAttempt(ctx, dailyAccount.ID, at); err != nil {
			t.Fatalf("daily attempt %d: %v", attempt+1, err)
		}
	}
	dailyWaitUntil := start.Add(24 * time.Hour)
	err = db.ClaimAppleCreationAttempt(ctx, dailyAccount.ID, start.Add(20*time.Hour))
	if !isAppleBudgetWait(err) || !budgetUntil(err).Equal(dailyWaitUntil) {
		t.Fatalf("21st rolling-day claim = %v, until=%v; want wait until %v", err, budgetUntil(err), dailyWaitUntil)
	}
	if err := db.ClaimAppleCreationAttempt(ctx, dailyAccount.ID, dailyWaitUntil); err != nil {
		t.Fatalf("claim after oldest daily attempt expired: %v", err)
	}
}

func TestAppleCreationBudgetIsAccountScopedAndDisabledAccountConsumesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestStore(t)
	first := createAccount(t, ctx, db, "Budget one", "budget-one@icloud.com")
	second := createAccount(t, ctx, db, "Budget two", "budget-two@icloud.com")
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	for i := 0; i < store.AppleCreationHourlyLimit; i++ {
		if err := db.ClaimAppleCreationAttempt(ctx, first.ID, now.Add(time.Duration(i)*10*time.Minute)); err != nil {
			t.Fatalf("claim first-account attempt %d: %v", i+1, err)
		}
	}
	if err := db.ClaimAppleCreationAttempt(ctx, second.ID, now); err != nil {
		t.Fatalf("second account shared first account budget: %v", err)
	}
	if _, err := db.DB().ExecContext(ctx, `UPDATE accounts SET enabled = FALSE WHERE id = ?`, second.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.ClaimAppleCreationAttempt(ctx, second.ID, now.Add(time.Minute)); !errors.Is(err, store.ErrAccountDisabled) {
		t.Fatalf("disabled account claim = %v, want ErrAccountDisabled", err)
	}
	var attempts int
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM apple_creation_attempts WHERE account_id = ?`, second.ID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("disabled account attempts = %d, want only its earlier enabled claim", attempts)
	}
}

func TestAppleCreationPauseNeverShortensAndExpires(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestStore(t)
	account := createAccount(t, ctx, db, "Budget pause", "budget-pause@icloud.com")
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	longPause := now.Add(2 * time.Hour)
	if err := db.PauseAppleCreation(ctx, account.ID, longPause); err != nil {
		t.Fatal(err)
	}
	if err := db.PauseAppleCreation(ctx, account.ID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	err := db.ClaimAppleCreationAttempt(ctx, account.ID, now)
	if !isAppleBudgetWait(err) || !budgetUntil(err).Equal(longPause) {
		t.Fatalf("claim during pause = %v until=%v; want original max deadline %v", err, budgetUntil(err), longPause)
	}
	if err := db.ClaimAppleCreationAttempt(ctx, account.ID, longPause); err != nil {
		t.Fatalf("claim after pause expired: %v", err)
	}
}

func TestAppleCreationBudgetOverfullHistoryWaitsForEnoughExpirations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestStore(t)
	account := createAccount(t, ctx, db, "Overfull history", "overfull-history@icloud.com")
	now := time.Now().UTC()
	for i := 0; i < 30; i++ {
		at := now.Add(time.Duration(i-30) * time.Minute)
		if _, err := db.DB().ExecContext(ctx, `INSERT INTO apple_creation_attempts(account_id, attempted_at) VALUES (?, ?)`, account.ID, at.UnixNano()); err != nil {
			t.Fatal(err)
		}
	}
	err := db.ClaimAppleCreationAttempt(ctx, account.ID, now)
	want := now.Add(24*time.Hour - 20*time.Minute)
	if !isAppleBudgetWait(err) || !budgetUntil(err).Equal(want) {
		t.Fatalf("overfull history deadline=%v, want %v", budgetUntil(err), want)
	}
}

func TestAppleCreationBudgetClockRollbackDoesNotGrantNewAttempt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestStore(t)
	account := createAccount(t, ctx, db, "Clock rollback", "clock-rollback@icloud.com")
	now := time.Now().UTC()
	if err := db.ClaimAppleCreationAttempt(ctx, account.ID, now); err != nil {
		t.Fatal(err)
	}
	err := db.ClaimAppleCreationAttempt(ctx, account.ID, now.Add(-time.Hour))
	if !isAppleBudgetWait(err) || !budgetUntil(err).Equal(now.Add(store.AppleCreationMinInterval)) {
		t.Fatalf("clock rollback deadline=%v, want preserved minimum interval", budgetUntil(err))
	}
}

func TestAppleCreationBudgetMigrationSeedsEventsAndSurvivesReopen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "apple-budget.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	account := createAccount(t, ctx, db, "Budget migration", "budget-migration@icloud.com")
	attemptedAt := time.Now().UTC().Add(-time.Hour)
	for _, address := range []string{"seed-one@icloud.com", "seed-two@icloud.com"} {
		if _, err := db.DB().ExecContext(ctx, `INSERT INTO apple_alias_creation_events(account_id, address, created_at) VALUES (?, ?, ?)`, account.ID, address, attemptedAt.UnixNano()); err != nil {
			t.Fatal(err)
		}
	}
	// Simulate a pre-budget deployment with only creation history.
	if _, err := db.DB().ExecContext(ctx, `DELETE FROM apple_creation_budget_migrations`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate budget seed: %v", err)
	}
	assertSeededBudgetAttempt(t, ctx, db, account.ID, attemptedAt)
	// New successful events must not be reseeded on subsequent starts.
	if _, err := db.DB().ExecContext(ctx, `INSERT INTO apple_alias_creation_events(account_id, address, created_at) VALUES (?, ?, ?)`, account.ID, "post-upgrade@icloud.com", attemptedAt.Add(time.Minute).UnixNano()); err != nil {
		t.Fatal(err)
	}
	pauseUntil := time.Now().UTC().Add(24 * time.Hour)
	if err := db.PauseAppleCreation(ctx, account.ID, pauseUntil); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if err := reopened.Migrate(ctx); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	assertSeededBudgetAttempt(t, ctx, reopened, account.ID, attemptedAt)
	if err := reopened.ClaimAppleCreationAttempt(ctx, account.ID, time.Now().UTC()); !isAppleBudgetWait(err) || !budgetUntil(err).Equal(pauseUntil) {
		t.Fatalf("cooldown after reopen = %v until=%v; want %v", err, budgetUntil(err), pauseUntil)
	}
}

func isAppleBudgetWait(err error) bool {
	var budgetErr *store.AppleCreationBudgetError
	return errors.As(err, &budgetErr) && budgetErr.DiagnosticCode() == "APPLE_CREATION_BUDGET_WAIT"
}

func budgetUntil(err error) time.Time {
	var budgetErr *store.AppleCreationBudgetError
	if errors.As(err, &budgetErr) {
		return budgetErr.Until
	}
	return time.Time{}
}

func assertSeededBudgetAttempt(t *testing.T, ctx context.Context, db *store.Store, accountID int64, attemptedAt time.Time) {
	t.Helper()
	var count int
	var stored int64
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*), MIN(attempted_at) FROM apple_creation_attempts WHERE account_id = ?`, accountID).Scan(&count, &stored); err != nil {
		t.Fatal(err)
	}
	if count != 1 || stored != attemptedAt.UnixNano() {
		t.Fatalf("seeded budget rows count=%d timestamp=%d; want one distinct attempt at %d", count, stored, attemptedAt.UnixNano())
	}
}
