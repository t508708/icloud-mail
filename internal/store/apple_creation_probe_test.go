package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"icloud-api/internal/store"
)

func TestAppleCreationProbeBudgetIndependentAndPersistent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "probe.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	a := createAccount(t, ctx, db, "Probe budget", "probe-budget@icloud.com")
	b := createAccount(t, ctx, db, "Probe isolated", "probe-isolated@icloud.com")
	now := time.Now().UTC().Truncate(time.Second)
	if err := db.ClaimAppleCreationAttempt(ctx, a.ID, now); err != nil {
		t.Fatal(err)
	}
	until := now.Add(48 * time.Hour)
	if err := db.PauseAppleCreation(ctx, a.ID, until); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ {
		if err := db.ClaimAppleCreationProbe(ctx, a.ID, now.Add(time.Duration(i)*2*time.Minute)); err != nil {
			t.Fatalf("probe %d blocked by background budget/cooldown: %v", i+1, err)
		}
	}
	if err := db.ClaimAppleCreationProbe(ctx, a.ID, now.Add(50*time.Minute)); !isAppleBudgetWait(err) || !budgetUntil(err).Equal(now.Add(time.Hour)) {
		t.Fatalf("probe overflow: %v until %v", err, budgetUntil(err))
	}
	if err := db.ClaimAppleCreationProbe(ctx, b.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := db.ClaimAppleCreationAttempt(ctx, b.ID, now); err != nil {
		t.Fatalf("probe consumed background budget: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ClaimAppleCreationProbe(ctx, a.ID, now.Add(50*time.Minute)); !isAppleBudgetWait(err) {
		t.Fatalf("probe ledger lost on restart: %v", err)
	}
	if err := db.ClaimAppleCreationProbe(ctx, a.ID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	err = db.ClaimAppleCreationAttempt(ctx, a.ID, now.Add(time.Hour))
	if !isAppleBudgetWait(err) || !budgetUntil(err).Equal(until) {
		t.Fatalf("probe changed background cooldown: %v until %v", err, budgetUntil(err))
	}
	var backgroundCount int
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM apple_creation_attempts WHERE account_id=?`, a.ID).Scan(&backgroundCount); err != nil {
		t.Fatal(err)
	}
	if backgroundCount != 1 {
		t.Fatalf("probes changed background history: %d", backgroundCount)
	}
	if _, err := db.DB().ExecContext(ctx, `UPDATE accounts SET enabled=FALSE WHERE id=?`, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.ClaimAppleCreationProbe(ctx, b.ID, now.Add(time.Hour)); !errors.Is(err, store.ErrAccountDisabled) {
		t.Fatalf("disabled probe = %v", err)
	}
}

func TestAppleCreationProbesSerializeConcurrentClicks(t *testing.T) {
	ctx := context.Background()
	db := openTestStore(t)
	a := createAccount(t, ctx, db, "Concurrent probe", "concurrent-probe@icloud.com")
	now := time.Now().UTC()
	var successes, waits atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := db.ClaimAppleCreationProbe(ctx, a.ID, now)
			if err == nil {
				successes.Add(1)
			} else if isAppleBudgetWait(err) {
				waits.Add(1)
			} else {
				t.Errorf("probe claim: %v", err)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 || waits.Load() != 7 {
		t.Fatalf("claims %d waits %d", successes.Load(), waits.Load())
	}
}
