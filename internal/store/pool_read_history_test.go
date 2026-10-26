package store_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"icloud-api/internal/domain"
	"icloud-api/internal/store"
)

func TestPoolConsumedAddressReadSideSQLite(t *testing.T) {
	runPoolConsumedAddressReadSide(t, filepath.Join(t.TempDir(), "read-history.db"))
}

func TestPoolConsumedAddressReadSidePostgres(t *testing.T) {
	dsn := os.Getenv("ICLOUD_POOL_HISTORY_TEST_DSN")
	if dsn == "" {
		t.Skip("set ICLOUD_POOL_HISTORY_TEST_DSN to an isolated empty pool_history_test database")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Path != "/pool_history_test" || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") {
		t.Fatal("expected dedicated loopback pool_history_test database")
	}
	runPoolConsumedAddressReadSide(t, dsn)
}

func runPoolConsumedAddressReadSide(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	db, _, account, originals, client := poolHistoryFixtureDSN(t, dsn)
	createAlias := func(address string) domain.Alias {
		t.Helper()
		a, err := db.CreateAlias(ctx, domain.Alias{AccountID: account.ID, Address: address, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	enroll := func(a domain.Alias) {
		t.Helper()
		if err := db.EnrollPoolAliases(ctx, []int64{a.ID}); err != nil {
			t.Fatal(err)
		}
	}
	claim := func(request string) store.PoolLease {
		t.Helper()
		leases, err := db.ClaimPool(ctx, client.ID, store.PoolClaim{RequestID: request, Count: 1, TTLSeconds: 3600})
		if err != nil || len(leases) != 1 {
			t.Fatalf("claim %s: %+v %v", request, leases, err)
		}
		return leases[0]
	}
	blocked := []int64{}
	historyIDs := []string{}
	for i, state := range []string{"used", "retiring", "retired"} {
		old := originals[0]
		if i > 0 {
			old = createAlias(fmt.Sprintf("consumed-%s@read.invalid", state))
			enroll(old)
		}
		lease := claim("read-history-" + state)
		if _, err := db.ActPoolLease(ctx, lease.ID, client.ID, "commit", 0); err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB().Exec(`UPDATE pool_leases SET state=$1 WHERE id=$2`, state, lease.ID); err != nil {
			t.Fatal(err)
		}
		if err := db.DeleteAlias(ctx, old.ID); err != nil {
			t.Fatal(err)
		}
		replacement := createAlias(old.Address)
		if _, err := db.DB().Exec(`INSERT INTO pool_members(alias_id,state,updated_at) VALUES($1,'available',$2)`, replacement.ID, time.Now().UnixNano()); err != nil {
			t.Fatal(err)
		}
		blocked = append(blocked, replacement.ID)
		historyIDs = append(historyIDs, lease.ID)
	}
	leasedAlias := createAlias("live-leased@read.invalid")
	enroll(leasedAlias)
	leased := claim("read-current-leased")
	usedAlias := createAlias("live-used@read.invalid")
	enroll(usedAlias)
	used := claim("read-current-used")
	if _, err := db.ActPoolLease(ctx, used.ID, client.ID, "commit", 0); err != nil {
		t.Fatal(err)
	}
	pausedAlias := createAlias("paused@read.invalid")
	enroll(pausedAlias)
	if err := db.SetPoolMember(ctx, pausedAlias.ID, "paused"); err != nil {
		t.Fatal(err)
	}
	normal := []int64{}
	for i := 0; i < 3; i++ {
		a := createAlias(fmt.Sprintf("normal-%d@read.invalid", i))
		enroll(a)
		normal = append(normal, a.ID)
	}

	assertPage := func(state, search string, limit, offset, total int, expected []int64) {
		t.Helper()
		page, err := db.ListPoolMembers(ctx, state, search, limit, offset)
		if err != nil || page.Total != total || len(page.Items) != len(expected) {
			t.Fatalf("page %s/%s: %+v %v", state, search, page, err)
		}
		for i, id := range expected {
			if page.Items[i].AliasID != id || state != "" && page.Items[i].State != state {
				t.Fatalf("page state/order: %+v", page)
			}
		}
	}
	assertStock := func() {
		t.Helper()
		accounts, err := db.ListPoolAccounts(ctx)
		available, err2 := db.ListPoolMembers(ctx, "available", "", 50, 0)
		if err != nil || err2 != nil || len(accounts) != 1 || accounts[0].Available != 3 || available.Total != accounts[0].Available {
			t.Fatalf("stock differs: %+v %+v %v %v", accounts, available, err, err2)
		}
	}
	assertStock()
	assertPage("available", "", 1, 1, 3, []int64{normal[1]})
	assertPage("available", "", 1, 3, 3, nil)
	assertPage("available", "normal-1", 50, 0, 1, []int64{normal[1]})
	assertPage("", "consumed", 50, 0, 0, nil)
	assertPage("", "pool-0", 50, 0, 0, nil)
	assertPage("leased", "", 50, 0, 1, []int64{leasedAlias.ID})
	assertPage("used", "", 50, 0, 1, []int64{usedAlias.ID})
	assertPage("paused", "", 50, 0, 1, []int64{pausedAlias.ID})
	assertPage("", "", 2, 2, 6, []int64{normal[0], pausedAlias.ID})
	// Reads must not rewrite the stale rows or their timestamps.
	for _, id := range blocked {
		var state string
		if err := db.DB().QueryRow(`SELECT state FROM pool_members WHERE alias_id=$1`, id).Scan(&state); err != nil || state != "available" {
			t.Fatalf("read rewrote member: %s %v", state, err)
		}
	}

	account.Enabled = false
	if _, err := db.UpdateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	for _, id := range blocked {
		var state string
		if err := db.DB().QueryRow(`SELECT state FROM pool_suspended_members WHERE alias_id=$1`, id).Scan(&state); err != nil || state != "paused" {
			t.Fatalf("disable kept consumed available: %s %v", state, err)
		}
		// Also prove restoration of a snapshot from an older deployment.
		if _, err := db.DB().Exec(`UPDATE pool_suspended_members SET state='available' WHERE alias_id=$1`, id); err != nil {
			t.Fatal(err)
		}
	}
	account, err := db.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	account.Enabled = true
	if _, err := db.UpdateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	assertStock()
	assertPage("available", "", 50, 0, 3, []int64{normal[2], normal[1], normal[0]})
	assertPage("leased", "", 50, 0, 1, []int64{leasedAlias.ID})
	assertPage("used", "", 50, 0, 1, []int64{usedAlias.ID})
	assertPage("paused", "", 50, 0, 4, []int64{pausedAlias.ID, blocked[2], blocked[1], blocked[0]})
	for i, id := range historyIDs {
		old, err := db.GetPoolLease(ctx, id, client.ID)
		if err != nil || old.State != []string{"used", "retiring", "retired"}[i] || old.AliasID != 0 || old.ClientID != client.ID {
			t.Fatalf("history changed: %+v %v", old, err)
		}
	}
	current, err := db.GetPoolLease(ctx, leased.ID, client.ID)
	if err != nil || current.State != "leased" {
		t.Fatalf("live lease changed: %+v %v", current, err)
	}
	// A disabled-account row left by an older deployment is snapshotted on
	// startup too. Repeating startup must not restore it as available.
	if _, err := db.DB().Exec(`UPDATE pool_members SET state='available' WHERE alias_id=$1`, blocked[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().Exec(`UPDATE accounts SET enabled=FALSE WHERE id=$1`, account.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := db.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		var state string
		if err := db.DB().QueryRow(`SELECT state FROM pool_suspended_members WHERE alias_id=$1`, blocked[0]).Scan(&state); err != nil || state != "paused" {
			t.Fatalf("startup snapshot kept consumed available: %s %v", state, err)
		}
	}
	assertPage("", "", 50, 0, 0, nil)
	account, err = db.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	account.Enabled = true
	if _, err := db.UpdateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	assertStock()
}
