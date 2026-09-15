package store_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"icloud-api/internal/domain"
	"icloud-api/internal/secure"
	"icloud-api/internal/store"
)

func TestPoolAccountSuspensionPostgres(t *testing.T) {
	dsn := os.Getenv("ICLOUD_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("ICLOUD_TEST_POSTGRES_URL is not set")
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if (parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1") || !strings.Contains(strings.ToLower(strings.TrimPrefix(parsed.Path, "/")), "test") {
		t.Fatalf("refusing pool test outside a loopback test database: %s", parsed.Host)
	}
	ctx := context.Background()
	db, err := store.OpenContext(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cipher, err := secure.NewCipher(bytes.Repeat([]byte{0x52}, 32))
	if err != nil {
		t.Fatal(err)
	}
	db.ConfigureAliasCredentialFactory(func(id, version int64) (domain.AliasCredentialMaterial, error) {
		_, material, err := secure.NewAliasCredentialMaterial(cipher, id, version)
		return material, err
	})
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	account := createAccount(t, ctx, db, "PG pool suspension", "pg-pool-suspend-"+suffix+"@icloud.com")
	alias, err := db.CreateAlias(ctx, domain.Alias{AccountID: account.ID, Address: "pg-pool-member-" + suffix + "@icloud.com", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.EnrollPoolAliases(ctx, []int64{alias.ID}); err != nil {
		t.Fatal(err)
	}
	account.Enabled = false
	if _, err := db.UpdateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	assertAccountPoolMembers(t, ctx, db, account.ID, 0)
	account, err = db.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	account.Enabled = true
	if _, err := db.UpdateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	assertAccountPoolMembers(t, ctx, db, account.ID, 1, alias.ID)

	racingAccount := createAccount(t, ctx, db, "PG pool lock race", "pg-pool-race-"+suffix+"@icloud.com")
	racingAlias, err := db.CreateAlias(ctx, domain.Alias{AccountID: racingAccount.ID, Address: "pg-pool-race-alias-" + suffix + "@icloud.com", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.EnrollPoolAliases(ctx, []int64{racingAlias.ID}); err != nil {
		t.Fatal(err)
	}
	client, _, err := db.CreatePoolClient(ctx, "pg-pool-race-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	claimResult := make(chan error, 1)
	disableResult := make(chan error, 1)
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, err := db.ClaimPool(ctx, client.ID, store.PoolClaim{RequestID: "disable-race-claim", Count: 1, TTLSeconds: 600, AccountID: racingAccount.ID})
		claimResult <- err
	}()
	go func() {
		defer wg.Done()
		<-start
		current, err := db.GetAccount(ctx, racingAccount.ID)
		if err == nil {
			current.Enabled = false
			_, err = db.UpdateAccount(ctx, current)
		}
		disableResult <- err
	}()
	close(start)
	wg.Wait()
	claimErr, disableErr := <-claimResult, <-disableResult
	if disableErr != nil {
		t.Fatalf("disable race: %v", disableErr)
	}
	if claimErr != nil && !errors.Is(claimErr, store.ErrPoolEmpty) {
		t.Fatalf("concurrent claim error: %v", claimErr)
	}
	if _, err := db.ClaimPool(ctx, client.ID, store.PoolClaim{RequestID: "disabled-after-race", Count: 1, TTLSeconds: 600, AccountID: racingAccount.ID}); !errors.Is(err, store.ErrPoolEmpty) {
		t.Fatalf("claim after disable committed: %v", err)
	}
	var visible int
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pool_members m JOIN aliases al ON al.id=m.alias_id WHERE al.account_id=$1`, racingAccount.ID).Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("concurrent disable left visible membership count=%d err=%v", visible, err)
	}
	racingAccount, err = db.GetAccount(ctx, racingAccount.ID)
	if err != nil {
		t.Fatal(err)
	}
	racingAccount.Enabled = true
	if _, err := db.UpdateAccount(ctx, racingAccount); err != nil {
		t.Fatal(err)
	}
	state := "available"
	if claimErr == nil {
		state = "leased"
	}
	if err := db.DB().QueryRowContext(ctx, `SELECT state FROM pool_members WHERE alias_id=$1`, racingAlias.ID).Scan(&state); err != nil || (claimErr == nil && state != "leased") || (errors.Is(claimErr, store.ErrPoolEmpty) && state != "available") {
		t.Fatalf("post-race restore state=%q claimErr=%v err=%v", state, claimErr, err)
	}
}

func TestDisablingAccountWithdrawsAndReenableRestoresPoolSnapshot(t *testing.T) {
	db, _, account, aliases, client := poolFixture(t, 6, "")
	ctx := context.Background()
	other, err := db.CreateAccount(ctx, domain.Account{
		Name: "independent pool account", Email: "independent-pool@icloud.com", IMAPHost: "imap.mail.me.com",
		IMAPPort: 993, IMAPUsername: "independent-pool@icloud.com", PasswordCiphertext: "encrypted", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	otherAlias, err := db.CreateAlias(ctx, domain.Alias{AccountID: other.ID, Address: "independent-pool-alias@icloud.com", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.EnrollPoolAliases(ctx, []int64{otherAlias.ID}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetPoolMember(ctx, aliases[0].ID, "paused"); err != nil {
		t.Fatal(err)
	}
	leases, err := db.ClaimPool(ctx, client.ID, store.PoolClaim{RequestID: "disable-snapshot-001", Count: 5, TTLSeconds: 600, AccountID: account.ID})
	if err != nil || len(leases) != 5 {
		t.Fatalf("lease initial pool members: leases=%d err=%v", len(leases), err)
	}
	leaseByAlias := make(map[int64]store.PoolLease, len(leases))
	for _, lease := range leases {
		leaseByAlias[lease.AliasID] = lease
	}
	if _, err := db.ActPoolLease(ctx, leaseByAlias[aliases[3].ID].ID, client.ID, "commit", 0); err != nil {
		t.Fatal(err)
	}

	updated, err := db.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	updated.Enabled = false
	if _, err := db.UpdateAccount(ctx, updated); err != nil {
		t.Fatal(err)
	}
	assertVisiblePoolMembers(t, ctx, db, 1, otherAlias.ID)
	if _, err := db.ClaimPool(ctx, client.ID, store.PoolClaim{RequestID: "disabled-no-first-account", Count: 1, TTLSeconds: 600, AccountID: account.ID}); !errors.Is(err, store.ErrPoolEmpty) {
		t.Fatalf("disabled account remained allocatable: %v", err)
	}
	otherLease, err := db.ClaimPool(ctx, client.ID, store.PoolClaim{RequestID: "other-account-still-works", Count: 1, TTLSeconds: 600, AccountID: other.ID})
	if err != nil || len(otherLease) != 1 || otherLease[0].AliasID != otherAlias.ID {
		t.Fatalf("other account allocation: %+v %v", otherLease, err)
	}

	if _, err := db.ActPoolLease(ctx, leaseByAlias[aliases[2].ID].ID, client.ID, "release", 0); err != nil {
		t.Fatalf("release during suspension: %v", err)
	}
	if _, err := db.DB().Exec(`UPDATE pool_leases SET expires_at=? WHERE id=?`, time.Now().Add(-time.Second).UnixNano(), leaseByAlias[aliases[4].ID].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ActPoolLease(ctx, leaseByAlias[aliases[4].ID].ID, client.ID, "expire", 0); err != nil {
		t.Fatalf("expire during suspension: %v", err)
	}
	if _, err := db.ActPoolLease(ctx, leaseByAlias[aliases[5].ID].ID, client.ID, "commit", 0); err != nil {
		t.Fatalf("commit during suspension: %v", err)
	}
	assertVisiblePoolMembers(t, ctx, db, 1, otherAlias.ID)
	for _, lease := range leases {
		if _, err := db.GetPoolLease(ctx, lease.ID, client.ID); err != nil {
			t.Fatalf("lease history %s was removed: %v", lease.ID, err)
		}
	}
	for _, alias := range aliases {
		if _, err := db.GetAlias(ctx, alias.ID); err != nil {
			t.Fatalf("alias %d was removed: %v", alias.ID, err)
		}
	}

	updated, err = db.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	updated.Enabled = true
	if _, err := db.UpdateAccount(ctx, updated); err != nil {
		t.Fatal(err)
	}
	assertVisiblePoolMembers(t, ctx, db, 7, aliases[0].ID, aliases[1].ID, aliases[2].ID, aliases[3].ID, aliases[4].ID, aliases[5].ID, otherAlias.ID)
	for id, want := range map[int64]string{
		aliases[0].ID: "paused", aliases[1].ID: "leased", aliases[2].ID: "available",
		aliases[3].ID: "used", aliases[4].ID: "available", aliases[5].ID: "used",
	} {
		var state string
		if err := db.DB().QueryRow(`SELECT state FROM pool_members WHERE alias_id=?`, id).Scan(&state); err != nil || state != want {
			t.Fatalf("restored alias %d state=%q err=%v want=%q", id, state, err, want)
		}
	}
	resumed, err := db.ClaimPool(ctx, client.ID, store.PoolClaim{RequestID: "reenabled-available-only", Count: 2, TTLSeconds: 600, AccountID: account.ID})
	resumedIDs := map[int64]bool{}
	for _, lease := range resumed {
		resumedIDs[lease.AliasID] = true
	}
	if err != nil || len(resumed) != 2 || !resumedIDs[aliases[2].ID] || !resumedIDs[aliases[4].ID] {
		t.Fatalf("reenabled availability did not preserve lease states: %+v %v", resumed, err)
	}
}

func TestPoolMembershipDisableAndRestoreAreAtomic(t *testing.T) {
	db, _, account, aliases, _ := poolFixture(t, 1, "")
	ctx := context.Background()
	if _, err := db.DB().Exec(`CREATE TRIGGER reject_pool_member_delete BEFORE DELETE ON pool_members BEGIN SELECT RAISE(ABORT, 'fixture rollback'); END`); err != nil {
		t.Fatal(err)
	}
	account.Enabled = false
	if _, err := db.UpdateAccount(ctx, account); err == nil {
		t.Fatal("disable unexpectedly committed through injected failure")
	}
	got, err := db.GetAccount(ctx, account.ID)
	if err != nil || !got.Enabled {
		t.Fatalf("account update was not rolled back: enabled=%v err=%v", got.Enabled, err)
	}
	assertVisiblePoolMembers(t, ctx, db, 1, aliases[0].ID)
	var snapshots int
	if err := db.DB().QueryRow(`SELECT COUNT(*) FROM pool_suspended_members WHERE alias_id=?`, aliases[0].ID).Scan(&snapshots); err != nil || snapshots != 0 {
		t.Fatalf("snapshot transaction leaked: rows=%d err=%v", snapshots, err)
	}
	if _, err := db.DB().Exec(`DROP TRIGGER reject_pool_member_delete`); err != nil {
		t.Fatal(err)
	}
	rollbackDisable, err := db.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	rollbackDisable.Enabled = false
	if _, err := db.UpdateAccount(ctx, rollbackDisable); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().Exec(`CREATE TRIGGER reject_pool_snapshot_delete BEFORE DELETE ON pool_suspended_members BEGIN SELECT RAISE(ABORT, 'fixture rollback'); END`); err != nil {
		t.Fatal(err)
	}
	disabled, err := db.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	disabled.Enabled = true
	if _, err := db.UpdateAccount(ctx, disabled); err == nil {
		t.Fatal("reenable unexpectedly committed through injected failure")
	}
	got, err = db.GetAccount(ctx, account.ID)
	if err != nil || got.Enabled {
		t.Fatalf("reenable transaction was not rolled back: enabled=%v err=%v", got.Enabled, err)
	}
	assertVisiblePoolMembers(t, ctx, db, 0)
	if err := db.DB().QueryRow(`SELECT COUNT(*) FROM pool_suspended_members WHERE alias_id=?`, aliases[0].ID).Scan(&snapshots); err != nil || snapshots != 1 {
		t.Fatalf("snapshot was cleared despite failed reenable: rows=%d err=%v", snapshots, err)
	}
}

func TestPoolMigrationSnapshotsDisabledMembersIdempotently(t *testing.T) {
	db, _, account, aliases, _ := poolFixture(t, 1, "")
	ctx := context.Background()
	if _, err := db.DB().Exec(`UPDATE accounts SET enabled=FALSE WHERE id=?`, account.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	assertVisiblePoolMembers(t, ctx, db, 0)
	var count int
	var state string
	if err := db.DB().QueryRow(`SELECT COUNT(*),MIN(state) FROM pool_suspended_members WHERE alias_id=?`, aliases[0].ID).Scan(&count, &state); err != nil || count != 1 || state != "available" {
		t.Fatalf("migration snapshot count=%d state=%q err=%v", count, state, err)
	}
}

func assertVisiblePoolMembers(t *testing.T, ctx context.Context, db *store.Store, count int, ids ...int64) {
	t.Helper()
	page, err := db.ListPoolMembers(ctx, "", "", 100, 0)
	if err != nil || page.Total != count {
		t.Fatalf("pool members=%d err=%v; want %d", page.Total, err, count)
	}
	want := make(map[int64]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	for _, member := range page.Items {
		if len(want) > 0 && !want[member.AliasID] {
			t.Fatalf("unexpected visible pool member %d", member.AliasID)
		}
		delete(want, member.AliasID)
	}
	if len(want) > 0 {
		t.Fatalf("missing visible pool members: %v", want)
	}
}

func assertAccountPoolMembers(t *testing.T, ctx context.Context, db *store.Store, accountID int64, count int, ids ...int64) {
	t.Helper()
	var got int
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pool_members m JOIN aliases al ON al.id=m.alias_id WHERE al.account_id=$1`, accountID).Scan(&got); err != nil || got != count {
		t.Fatalf("account %d pool members=%d err=%v; want %d", accountID, got, err, count)
	}
	want := make(map[int64]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	rows, err := db.DB().QueryContext(ctx, `SELECT m.alias_id FROM pool_members m JOIN aliases al ON al.id=m.alias_id WHERE al.account_id=$1`, accountID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		if len(want) > 0 && !want[id] {
			t.Fatalf("unexpected account %d pool member %d", accountID, id)
		}
		delete(want, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(want) > 0 {
		t.Fatalf("account %d missing pool members: %v", accountID, want)
	}
}

func TestPoolMemberSnapshotQueryIsAccountScoped(t *testing.T) {
	db, _, first, firstAliases, _ := poolFixture(t, 1, "")
	ctx := context.Background()
	second, err := db.CreateAccount(ctx, domain.Account{Name: "second", Email: "pool-second@icloud.com", IMAPHost: "imap.mail.me.com", IMAPPort: 993, IMAPUsername: "pool-second@icloud.com", PasswordCiphertext: "encrypted", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	secondAlias, err := db.CreateAlias(ctx, domain.Alias{AccountID: second.ID, Address: "pool-second-alias@icloud.com", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.EnrollPoolAliases(ctx, []int64{secondAlias.ID}); err != nil {
		t.Fatal(err)
	}
	first.Enabled = false
	if _, err := db.UpdateAccount(ctx, first); err != nil {
		t.Fatal(err)
	}
	assertVisiblePoolMembers(t, ctx, db, 1, secondAlias.ID)
	var snapshots int
	if err := db.DB().QueryRow(`SELECT COUNT(*) FROM pool_suspended_members WHERE alias_id=?`, firstAliases[0].ID).Scan(&snapshots); err != nil || snapshots != 1 {
		t.Fatalf("first-account snapshot=%d err=%v", snapshots, err)
	}
	if err := db.DB().QueryRow(`SELECT COUNT(*) FROM pool_suspended_members WHERE alias_id=?`, secondAlias.ID).Scan(&snapshots); err != nil || snapshots != 0 {
		t.Fatalf("second-account was snapshotted=%d err=%v", snapshots, err)
	}
}
