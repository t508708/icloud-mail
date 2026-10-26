package store_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"icloud-api/internal/domain"
	"icloud-api/internal/secure"
	"icloud-api/internal/store"
)

func poolHistoryFixture(t *testing.T) (*store.Store, *secure.Cipher, domain.Account, []domain.Alias, store.PoolClient) {
	t.Helper()
	return poolHistoryFixtureDSN(t, filepath.Join(t.TempDir(), "history.db"))
}

func poolHistoryFixtureDSN(t *testing.T, dsn string) (*store.Store, *secure.Cipher, domain.Account, []domain.Alias, store.PoolClient) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cipher, err := secure.NewCipher(bytes.Repeat([]byte{0x45}, 32))
	if err != nil {
		t.Fatal(err)
	}
	db.ConfigureAliasCredentialFactory(func(id, version int64) (domain.AliasCredentialMaterial, error) {
		_, material, err := secure.NewAliasCredentialMaterial(cipher, id, version)
		return material, err
	})
	ctx := context.Background()
	account := createAccount(t, ctx, db, "history fixture", "owner@pool.invalid")
	alias, err := db.CreateAlias(ctx, domain.Alias{AccountID: account.ID, Address: "pool-0@pool.invalid", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.EnrollPoolAliases(ctx, []int64{alias.ID}); err != nil {
		t.Fatal(err)
	}
	client, _, err := db.CreatePoolClient(ctx, "history-fixture")
	if err != nil {
		t.Fatal(err)
	}
	return db, cipher, account, []domain.Alias{alias}, client
}

func TestPoolConsumedAddressSurvivesAliasReplacement(t *testing.T) {
	for _, state := range []string{"used", "retiring", "retired"} {
		t.Run(state, func(t *testing.T) {
			db, _, account, original, client := poolHistoryFixture(t)
			ctx := context.Background()
			leases, err := db.ClaimPool(ctx, client.ID, store.PoolClaim{RequestID: "original-history", Count: 1, TTLSeconds: 600})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.ActPoolLease(ctx, leases[0].ID, client.ID, "commit", 0); err != nil {
				t.Fatal(err)
			}
			// Simulate durable retirement history as well as direct used history.
			if _, err = db.DB().Exec(`UPDATE pool_leases SET state=?,address=? WHERE id=?`, state, "  "+strings.ToUpper(original[0].Address)+"  ", leases[0].ID); err != nil {
				t.Fatal(err)
			}
			if err = db.DeleteAlias(ctx, original[0].ID); err != nil {
				t.Fatal(err)
			}
			if err = db.SetPoolAccount(ctx, account.ID, true, 1); err != nil {
				t.Fatal(err)
			}
			replacement, err := db.CreateAlias(ctx, domain.Alias{AccountID: account.ID, Address: original[0].Address, Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			if replacement.ID == original[0].ID {
				t.Fatal("fixture did not replace alias identity")
			}
			if err = db.EnrollPoolAliases(ctx, []int64{replacement.ID}); !errors.Is(err, store.ErrPoolConflict) {
				t.Fatalf("explicit enrollment: %v", err)
			}
			if err = db.RefreshPool(ctx); err != nil {
				t.Fatal(err)
			}
			page, err := db.ListPoolMembers(ctx, "", "", 50, 0)
			if err != nil || page.Total != 0 {
				t.Fatalf("automatic enrollment: %d %v", page.Total, err)
			}
			// An older deployment may have left an available member. Claim and
			// stock accounting must still exclude it without rewriting history.
			if _, err = db.DB().Exec(`INSERT INTO pool_members(alias_id,state,updated_at) VALUES(?,'available',?)`, replacement.ID, time.Now().UnixNano()); err != nil {
				t.Fatal(err)
			}
			if _, err = db.ClaimPool(ctx, client.ID, store.PoolClaim{RequestID: "new-history-request", Count: 1, TTLSeconds: 600}); !errors.Is(err, store.ErrPoolEmpty) {
				t.Fatalf("replacement claimed: %v", err)
			}
			if err = db.SetPoolMember(ctx, replacement.ID, "available"); !errors.Is(err, store.ErrPoolConflict) {
				t.Fatalf("resume consumed member: %v", err)
			}
			accounts, err := db.ListPoolAccounts(ctx)
			if err != nil || len(accounts) != 1 || accounts[0].Available != 0 {
				t.Fatalf("stock includes history: %v %v", accounts, err)
			}
			allowed, err := db.PoolCreationAllowed(ctx, account.ID)
			if err != nil || !allowed {
				t.Fatalf("history blocks replenishment: %v %v", allowed, err)
			}
			old, err := db.GetPoolLease(ctx, leases[0].ID, client.ID)
			if err != nil || old.State != state || old.AliasID != 0 || old.ClientID != client.ID {
				t.Fatalf("history changed: %+v %v", old, err)
			}
			// No plus-tag stripping, dot folding or approximate domain match.
			for i, address := range []string{"pool-0+tag@pool.invalid", "po.ol-0@pool.invalid", "pool-0@other.invalid"} {
				a, err := db.CreateAlias(ctx, domain.Alias{AccountID: account.ID, Address: address, Enabled: true})
				if err != nil {
					t.Fatal(err)
				}
				if err = db.EnrollPoolAliases(ctx, []int64{a.ID}); err != nil {
					t.Fatal(err)
				}
				fresh, err := db.ClaimPool(ctx, client.ID, store.PoolClaim{RequestID: fmt.Sprintf("distinct-address-%d", i), Count: 1, TTLSeconds: 600})
				if err != nil || len(fresh) != 1 || fresh[0].Address != address {
					t.Fatalf("distinct address blocked: %v %v", fresh, err)
				}
			}
		})
	}
}

func TestPoolConsumedAddressReleaseAndExpiryStayPaused(t *testing.T) {
	for _, action := range []string{"release", "expire"} {
		t.Run(action, func(t *testing.T) {
			db, _, _, aliases, client := poolHistoryFixture(t)
			ctx := context.Background()
			leases, err := db.ClaimPool(ctx, client.ID, store.PoolClaim{RequestID: "release-with-history", Count: 1, TTLSeconds: 600})
			if err != nil {
				t.Fatal(err)
			}
			// Represents a lease issued before this fix, with detached used
			// history under another project. History is global, not per Key.
			other, _, err := db.CreatePoolClient(ctx, "historical-project")
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UnixNano()
			if _, err = db.DB().Exec(`INSERT INTO pool_requests(client_id,request_id,fingerprint,created_at) VALUES(?,?,?,?)`, other.ID, "old-history-intent", "{}", now); err != nil {
				t.Fatal(err)
			}
			if _, err = db.DB().Exec(`INSERT INTO pool_leases(id,alias_id,address,client_id,request_id,state,created_at,expires_at,updated_at) VALUES(?,NULL,?,?,?,'used',?,?,?)`, "detached-used-lease", " "+strings.ToUpper(aliases[0].Address)+" ", other.ID, "old-history-intent", now, now, now); err != nil {
				t.Fatal(err)
			}
			if action == "expire" {
				if _, err = db.DB().Exec(`UPDATE pool_leases SET expires_at=? WHERE id=?`, time.Now().Add(-time.Minute).UnixNano(), leases[0].ID); err != nil {
					t.Fatal(err)
				}
			}
			closed, err := db.ActPoolLease(ctx, leases[0].ID, client.ID, action, 0)
			if err != nil || action == "release" && closed.State != "released" || action == "expire" && closed.State != "expired" {
				t.Fatalf("close lease: %v %v", closed, err)
			}
			members, err := db.ListPoolMembers(ctx, "paused", "", 50, 0)
			if err != nil || members.Total != 1 {
				t.Fatalf("consumed root became available: %v %v", members, err)
			}
			if _, err = db.ClaimPool(ctx, client.ID, store.PoolClaim{RequestID: "claim-after-close", Count: 1, TTLSeconds: 600}); !errors.Is(err, store.ErrPoolEmpty) {
				t.Fatal(err)
			}
			var state, owner string
			if err = db.DB().QueryRow(`SELECT state,client_id FROM pool_leases WHERE id='detached-used-lease'`).Scan(&state, &owner); err != nil || state != "used" || owner != other.ID {
				t.Fatalf("old history rewritten: %s %s %v", state, owner, err)
			}
		})
	}
}

func TestPoolOldReleaseAndTerminalRequestDoNotChangeSuccessor(t *testing.T) {
	db, _, _, _, client := poolHistoryFixture(t)
	ctx := context.Background()
	original := store.PoolClaim{RequestID: "terminal-request-original", Count: 1, TTLSeconds: 600}
	first, err := db.ClaimPool(ctx, client.ID, original)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ActPoolLease(ctx, first[0].ID, client.ID, "release", 0); err != nil {
		t.Fatal(err)
	}
	successor, _, err := db.CreatePoolClient(ctx, "successor-project")
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.ClaimPool(ctx, successor.ID, store.PoolClaim{RequestID: "terminal-request-successor", Count: 1, TTLSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ActPoolLease(ctx, first[0].ID, client.ID, "release", 0); err != nil {
		t.Fatal(err)
	}
	replay, err := db.ClaimPool(ctx, client.ID, original)
	if err != nil || len(replay) != 1 || replay[0].ID != first[0].ID || replay[0].State != "released" {
		t.Fatalf("old request changed: %v %v", replay, err)
	}
	live, err := db.GetPoolLease(ctx, second[0].ID, successor.ID)
	if err != nil || live.State != "leased" {
		t.Fatalf("old release altered successor: %v %v", live, err)
	}
	if _, err = db.ActPoolLease(ctx, second[0].ID, successor.ID, "commit", 0); err != nil {
		t.Fatal(err)
	}
	if err = db.EnrollPoolAliases(ctx, []int64{second[0].AliasID}); err != nil {
		t.Fatalf("existing used enrollment lost idempotency: %v", err)
	}
	if _, err = db.ClaimPool(ctx, client.ID, store.PoolClaim{RequestID: "terminal-after-used", Count: 1, TTLSeconds: 600}); !errors.Is(err, store.ErrPoolEmpty) {
		t.Fatal(err)
	}
}

func TestPoolConsumedAddressIndexMigrationAndPlan(t *testing.T) {
	db, _, _, _, client := poolHistoryFixture(t)
	ctx := context.Background()
	leases, err := db.ClaimPool(ctx, client.ID, store.PoolClaim{RequestID: "sqlite-migration-used", Count: 1, TTLSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ActPoolLease(ctx, leases[0].ID, client.ID, "commit", 0); err != nil {
		t.Fatal(err)
	}
	rows, err := db.DB().Query(`EXPLAIN QUERY PLAN SELECT 1 FROM pool_leases WHERE lower(trim(address))=lower(trim(?)) AND state IN ('used','retiring','retired')`, "synthetic@example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.String(), "pool_lease_consumed_address") {
		t.Fatal(plan.String())
	}
	_ = rows.Close()
	var filename string
	if err = db.DB().QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&filename); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		reopened, err := store.Open(filename)
		if err != nil {
			t.Fatal(err)
		}
		var indexes int
		if err = reopened.DB().QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name='pool_lease_consumed_address'`).Scan(&indexes); err != nil || indexes != 1 {
			t.Fatalf("index migration: %d %v", indexes, err)
		}
		old, err := reopened.GetPoolLease(ctx, leases[0].ID, client.ID)
		if err != nil || old.State != "used" {
			t.Fatalf("migration changed used: %+v %v", old, err)
		}
		if err = reopened.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPoolConsumedAddressPostgres(t *testing.T) {
	dsn := os.Getenv("ICLOUD_POOL_HISTORY_TEST_DSN")
	if dsn == "" {
		t.Skip("set ICLOUD_POOL_HISTORY_TEST_DSN to an isolated empty pool_history_test database")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Path != "/pool_history_test" || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") {
		t.Fatal("expected dedicated loopback pool_history_test database")
	}
	db, _, account, aliases, client := poolHistoryFixtureDSN(t, dsn)
	ctx := context.Background()
	leases, err := db.ClaimPool(ctx, client.ID, store.PoolClaim{RequestID: "postgres-old-address", Count: 1, TTLSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ActPoolLease(ctx, leases[0].ID, client.ID, "commit", 0); err != nil {
		t.Fatal(err)
	}
	if err = db.DeleteAlias(ctx, aliases[0].ID); err != nil {
		t.Fatal(err)
	}
	if err = db.SetPoolAccount(ctx, account.ID, true, 1); err != nil {
		t.Fatal(err)
	}
	a, err := db.CreateAlias(ctx, domain.Alias{AccountID: account.ID, Address: aliases[0].Address, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.EnrollPoolAliases(ctx, []int64{a.ID}); !errors.Is(err, store.ErrPoolConflict) {
		t.Fatal(err)
	}
	if err = db.RefreshPool(ctx); err != nil {
		t.Fatal(err)
	}
	members, err := db.ListPoolMembers(ctx, "", "", 50, 0)
	if err != nil || members.Total != 0 {
		t.Fatalf("postgres history auto-enrolled: %+v %v", members, err)
	}
	if _, err = db.DB().Exec(`INSERT INTO pool_members(alias_id,state,updated_at) VALUES($1,'available',$2)`, a.ID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ClaimPool(ctx, client.ID, store.PoolClaim{RequestID: "postgres-new-address", Count: 1, TTLSeconds: 600}); !errors.Is(err, store.ErrPoolEmpty) {
		t.Fatal(err)
	}
	// A populated history should use the expression index without disabling
	// sequential scans or forcing a query planner setting.
	if _, err = db.DB().Exec(`INSERT INTO pool_leases(id,alias_id,address,client_id,request_id,state,created_at,expires_at,updated_at)
	 SELECT 'synthetic-load-'||n,NULL,'bulk-'||n||'@load.invalid',$1,$2,'used',1,1,1 FROM generate_series(1,5000) n`, client.ID, "postgres-old-address"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.DB().Exec(`ANALYZE pool_leases`); err != nil {
		t.Fatal(err)
	}
	rows, err := db.DB().Query(`EXPLAIN (COSTS OFF) SELECT 1 FROM pool_leases WHERE lower(trim(address))=lower(trim($1)) AND state IN ('used','retiring','retired')`, aliases[0].Address)
	if err != nil {
		t.Fatal(err)
	}
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err = rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	if !strings.Contains(plan.String(), "pool_lease_consumed_address") {
		t.Fatal(plan.String())
	}
	t.Log("PostgreSQL indexed history lookup:", plan.String())
	for i := 0; i < 2; i++ {
		reopened, err := store.Open(dsn)
		if err != nil {
			t.Fatal(err)
		}
		old, err := reopened.GetPoolLease(ctx, leases[0].ID, client.ID)
		if err != nil || old.State != "used" || old.AliasID != 0 {
			t.Fatalf("repeated migration rewrote history: %+v %v", old, err)
		}
		if err = reopened.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
