package store_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"icloud-api/internal/store"
)

func usedPoolLease(t *testing.T) (*store.Store, store.PoolClient, store.PoolLease) {
	t.Helper()
	db, _, _, _, client := poolFixture(t, 1, "")
	ctx := context.Background()
	leases, err := db.ClaimPool(ctx, client.ID, store.PoolClaim{RequestID: "retirement-used-0001", Count: 1, TTLSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := db.ActPoolLease(ctx, leases[0].ID, client.ID, "commit", 0)
	if err != nil {
		t.Fatal(err)
	}
	return db, client, lease
}

func TestPoolRetirementSQLiteUpgradesExistingLeaseStatesAndPreservesHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pool-retirement-upgrade.db")
	db, _, _, _, client := poolFixture(t, 1, path)
	ctx := context.Background()
	leases, err := db.ClaimPool(ctx, client.ID, store.PoolClaim{RequestID: "retirement-upgrade-0001", Count: 1, TTLSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := db.ActPoolLease(ctx, leases[0].ID, client.ID, "commit", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`PRAGMA foreign_keys = OFF`,
		`DROP TABLE pool_alias_retirement_job_items`,
		`DROP TABLE pool_alias_retirement_jobs`,
		`DROP INDEX pool_one_live_lease`,
		`DROP INDEX pool_lease_expiry`,
		`DROP INDEX pool_lease_request`,
		`CREATE TABLE pool_leases_old (id TEXT PRIMARY KEY, alias_id BIGINT REFERENCES aliases(id) ON DELETE SET NULL,
			address TEXT NOT NULL, client_id TEXT NOT NULL REFERENCES pool_clients(id), request_id TEXT NOT NULL,
			state TEXT NOT NULL CHECK(state IN ('leased','used','released','expired')),
			created_at BIGINT NOT NULL, expires_at BIGINT NOT NULL, updated_at BIGINT NOT NULL,
			FOREIGN KEY(client_id,request_id) REFERENCES pool_requests(client_id,request_id))`,
		`INSERT INTO pool_leases_old SELECT id,alias_id,address,client_id,request_id,state,created_at,expires_at,updated_at FROM pool_leases`,
		`DROP TABLE pool_leases`,
		`ALTER TABLE pool_leases_old RENAME TO pool_leases`,
		`CREATE UNIQUE INDEX pool_one_live_lease ON pool_leases(alias_id) WHERE state IN ('leased','used')`,
		`CREATE INDEX pool_lease_expiry ON pool_leases(state, expires_at)`,
		`CREATE INDEX pool_lease_request ON pool_leases(client_id, request_id)`,
		`PRAGMA foreign_keys = ON`,
	} {
		if _, err := raw.ExecContext(ctx, statement); err != nil {
			_ = raw.Close()
			t.Fatalf("prepare previous pool schema: %v", err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	upgraded, err := migrated.GetPoolLease(ctx, lease.ID, client.ID)
	if err != nil || upgraded.State != "used" || upgraded.AliasID != lease.AliasID {
		t.Fatalf("upgraded history = %#v err=%v", upgraded, err)
	}
	var definition string
	if err := migrated.DB().QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='pool_leases'`).Scan(&definition); err != nil || !strings.Contains(definition, "'retiring'") {
		t.Fatalf("upgraded state constraint = %q err=%v", definition, err)
	}
	if err := migrated.Migrate(ctx); err != nil {
		t.Fatalf("reentrant migration: %v", err)
	}
}

func TestPoolAliasRetirementLifecycleAndIdempotency(t *testing.T) {
	db, client, lease := usedPoolLease(t)
	ctx := context.Background()
	operationID := "pool-retirement-lifecycle-0001"
	input := []store.PoolAliasRetirementItemInput{{AliasID: lease.AliasID, LeaseID: lease.ID}}
	job, accepted, err := db.StartPoolAliasRetirement(ctx, client.ID, operationID, "request-a", input)
	if err != nil || !accepted || job.Status != store.PoolAliasRetirementJobQueued || job.Items[0].State != "retiring" {
		t.Fatalf("start = %#v accepted=%v err=%v", job, accepted, err)
	}
	retiring, err := db.GetPoolLease(ctx, lease.ID, client.ID)
	if err != nil || retiring.State != "retiring" {
		t.Fatalf("lease after acceptance = %#v err=%v", retiring, err)
	}
	for _, action := range []string{"commit", "release", "renew"} {
		if _, err := db.ActPoolLease(ctx, lease.ID, client.ID, action, 600); !errors.Is(err, store.ErrPoolClosed) {
			t.Fatalf("retiring %s = %v, want closed", action, err)
		}
	}
	if _, err := db.ClaimPool(ctx, client.ID, store.PoolClaim{RequestID: "retirement-reclaim-0001", Count: 1, TTLSeconds: 600}); !errors.Is(err, store.ErrPoolEmpty) {
		t.Fatalf("retiring alias claim = %v, want empty", err)
	}
	replay, accepted, err := db.StartPoolAliasRetirement(ctx, client.ID, operationID, "request-b", input)
	if err != nil || accepted || replay.OperationID != job.OperationID || replay.Items[0].LeaseID != lease.ID {
		t.Fatalf("idempotent replay = %#v accepted=%v err=%v", replay, accepted, err)
	}
	other, _, err := db.CreatePoolClient(ctx, "retirement-operation-collision")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.StartPoolAliasRetirement(ctx, other.ID, operationID, "request-other", input); !errors.Is(err, store.ErrPoolRequestConflict) {
		t.Fatalf("cross-project operation collision = %v", err)
	}
	if _, _, err := db.StartPoolAliasRetirement(ctx, client.ID, operationID, "request-c", []store.PoolAliasRetirementItemInput{{AliasID: lease.AliasID + 1, LeaseID: lease.ID}}); !errors.Is(err, store.ErrPoolRequestConflict) {
		t.Fatalf("changed operation replay = %v", err)
	}
	if err := db.FinalizePoolAliasRetirement(ctx, operationID, 0, lease.AliasID); err != nil {
		t.Fatal(err)
	}
	retired, err := db.GetPoolLease(ctx, lease.ID, client.ID)
	if err != nil || retired.State != "retired" || retired.AliasID != 0 {
		t.Fatalf("retired lease = %#v err=%v", retired, err)
	}
	if _, err := db.GetAlias(ctx, lease.AliasID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("retired alias remains: %v", err)
	}
	completed, err := db.GetPoolAliasRetirementJob(ctx, operationID, client.ID)
	if err != nil || completed.Items[0].State != "retired" {
		t.Fatalf("retirement job after finalization = %#v err=%v", completed, err)
	}
}

func TestPoolAliasRetirementPreflightRejectsWholeBatchAtomically(t *testing.T) {
	db, _, _, _, client := poolFixture(t, 2, "")
	ctx := context.Background()
	leases, err := db.ClaimPool(ctx, client.ID, store.PoolClaim{RequestID: "retirement-atomic-claim-0001", Count: 2, TTLSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	for index := range leases {
		leases[index], err = db.ActPoolLease(ctx, leases[index].ID, client.ID, "commit", 0)
		if err != nil {
			t.Fatal(err)
		}
	}
	items := []store.PoolAliasRetirementItemInput{
		{AliasID: leases[0].AliasID, LeaseID: leases[0].ID},
		{AliasID: leases[1].AliasID + 1000, LeaseID: leases[1].ID},
	}
	if _, _, err := db.StartPoolAliasRetirement(ctx, client.ID, "pool-retirement-atomic-0001", "request", items); !errors.Is(err, store.ErrPoolRetirementAlias) {
		t.Fatalf("invalid batch = %v", err)
	}
	for _, lease := range leases {
		current, err := db.GetPoolLease(ctx, lease.ID, client.ID)
		if err != nil || current.State != "used" {
			t.Fatalf("atomic preflight mutated lease %#v err=%v", current, err)
		}
	}
}

func TestPoolAliasRetirementExplicitFailureRestoresUsedAndRejectsProjectMismatch(t *testing.T) {
	db, client, lease := usedPoolLease(t)
	ctx := context.Background()
	other, _, err := db.CreatePoolClient(ctx, "retirement-other-project")
	if err != nil {
		t.Fatal(err)
	}
	input := []store.PoolAliasRetirementItemInput{{AliasID: lease.AliasID, LeaseID: lease.ID}}
	if _, _, err := db.StartPoolAliasRetirement(ctx, other.ID, "pool-retirement-cross-project-0001", "request", input); !errors.Is(err, store.ErrPoolRetirementProject) {
		t.Fatalf("cross project start = %v", err)
	}
	operationID := "pool-retirement-restore-0001"
	if _, accepted, err := db.StartPoolAliasRetirement(ctx, client.ID, operationID, "request", input); err != nil || !accepted {
		t.Fatalf("start restore test accepted=%v err=%v", accepted, err)
	}
	if err := db.RestorePoolAliasRetirement(ctx, operationID, 0, "APPLE_DELETE_FAILED", "fixture"); err != nil {
		t.Fatal(err)
	}
	restored, err := db.GetPoolLease(ctx, lease.ID, client.ID)
	if err != nil || restored.State != "used" || restored.AliasID != lease.AliasID {
		t.Fatalf("restored lease = %#v err=%v", restored, err)
	}
	if _, err := db.GetAlias(ctx, lease.AliasID); err != nil {
		t.Fatalf("restored alias missing: %v", err)
	}
	job, err := db.GetPoolAliasRetirementJob(ctx, operationID, client.ID)
	if err != nil || job.Items[0].State != "used" || job.Items[0].ResultCode != "APPLE_DELETE_FAILED" {
		t.Fatalf("restored job = %#v err=%v", job, err)
	}
	if _, accepted, err := db.StartPoolAliasRetirement(ctx, client.ID, "pool-retirement-retry-0001", "request-retry", input); err != nil || !accepted {
		t.Fatalf("retry after explicit failure accepted=%v err=%v", accepted, err)
	}
}

func TestPoolAliasRetirementStartupInterruptKeepsLeaseClosed(t *testing.T) {
	db, client, lease := usedPoolLease(t)
	ctx := context.Background()
	operationID := "pool-retirement-interrupted-0001"
	if _, accepted, err := db.StartPoolAliasRetirement(ctx, client.ID, operationID, "request", []store.PoolAliasRetirementItemInput{{AliasID: lease.AliasID, LeaseID: lease.ID}}); err != nil || !accepted {
		t.Fatalf("start accepted=%v err=%v", accepted, err)
	}
	if err := db.SetPoolAliasRetirementJobStatus(ctx, operationID, client.ID, store.PoolAliasRetirementJobRunning); err != nil {
		t.Fatal(err)
	}
	if err := db.InterruptPoolAliasRetirementJobs(ctx); err != nil {
		t.Fatal(err)
	}
	job, err := db.GetPoolAliasRetirementJob(ctx, operationID, client.ID)
	if err != nil || job.Status != store.PoolAliasRetirementJobInterrupted || job.Items[0].State != "review" || job.Items[0].ResultCode != "JOB_INTERRUPTED" {
		t.Fatalf("interrupted job = %#v err=%v", job, err)
	}
	if _, err := db.ActPoolLease(ctx, lease.ID, client.ID, "release", 0); !errors.Is(err, store.ErrPoolClosed) {
		t.Fatalf("interrupted lease reopened: %v", err)
	}
}
