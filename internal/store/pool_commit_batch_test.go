package store_test

import (
	"context"
	"errors"
	"testing"

	"icloud-api/internal/store"
)

func TestPoolCommitBatchAtomicIsolationAndReplay(t *testing.T) {
	db, _, _, _, client := poolFixture(t, 3, "")
	ctx := context.Background()
	leases, err := db.ClaimPool(ctx, client.ID, store.PoolClaim{RequestID: "batch-commit-fixture", Count: 3, TTLSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{leases[0].ID, leases[1].ID, leases[2].ID}
	if _, err = db.CommitPoolLeases(ctx, client.ID, []string{ids[0], "missing-fixture-lease"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	first, err := db.GetPoolLease(ctx, ids[0], client.ID)
	if err != nil || first.State != "leased" {
		t.Fatalf("failed batch left partial commit: %s %v", first.State, err)
	}
	other, _, err := db.CreatePoolClient(ctx, "batch-other-project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.CommitPoolLeases(ctx, other.ID, ids); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	for _, invalid := range [][]string{nil, {ids[0], ids[0]}, {""}, make([]string, 51)} {
		if _, err = db.CommitPoolLeases(ctx, client.ID, invalid); !errors.Is(err, store.ErrPoolInput) {
			t.Fatal(err)
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		committed, err := db.CommitPoolLeases(ctx, client.ID, ids)
		if err != nil || len(committed) != 3 {
			t.Fatalf("commit/replay: %v", err)
		}
		for index, lease := range committed {
			if lease.ID != ids[index] || lease.State != "used" {
				t.Fatal("batch returned unrelated lease")
			}
		}
	}
}

func TestPoolCommitBatchClosedLeaseRollsBackPrefix(t *testing.T) {
	db, _, _, _, client := poolFixture(t, 2, "")
	ctx := context.Background()
	leases, err := db.ClaimPool(ctx, client.ID, store.PoolClaim{RequestID: "batch-closed-fixture", Count: 2, TTLSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ActPoolLease(ctx, leases[1].ID, client.ID, "release", 0); err != nil {
		t.Fatal(err)
	}
	if _, err = db.CommitPoolLeases(ctx, client.ID, []string{leases[0].ID, leases[1].ID}); !errors.Is(err, store.ErrPoolClosed) {
		t.Fatal(err)
	}
	first, err := db.GetPoolLease(ctx, leases[0].ID, client.ID)
	if err != nil || first.State != "leased" {
		t.Fatalf("closed batch left partial commit: %s %v", first.State, err)
	}
}
