package store_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"icloud-api/internal/domain"
	"icloud-api/internal/secure"
	"icloud-api/internal/store"
)

func poolFixture(t *testing.T, count int, dsn string) (*store.Store, *secure.Cipher, domain.Account, []domain.Alias, store.PoolClient) {
	t.Helper()
	if dsn == "" {
		dsn = filepath.Join(t.TempDir(), "pool.db")
	}
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cipher, err := secure.NewCipher(bytes.Repeat([]byte{0x36}, 32))
	if err != nil {
		t.Fatal(err)
	}
	db.ConfigureAliasCredentialFactory(func(id, version int64) (domain.AliasCredentialMaterial, error) {
		_, v, err := secure.NewAliasCredentialMaterial(cipher, id, version)
		return v, err
	})
	ctx := context.Background()
	account := createAccount(t, ctx, db, "pool fixture", "pool-owner@icloud.com")
	aliases := []domain.Alias{}
	ids := []int64{}
	for i := 0; i < count; i++ {
		alias, err := db.CreateAlias(ctx, domain.Alias{AccountID: account.ID, Address: fmt.Sprintf("pool-%d@icloud.com", i), Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		aliases = append(aliases, alias)
		ids = append(ids, alias.ID)
	}
	if len(ids) > 0 {
		if err := db.EnrollPoolAliases(ctx, ids); err != nil {
			t.Fatal(err)
		}
	}
	client, _, err := db.CreatePoolClient(ctx, "fixture-project")
	if err != nil {
		t.Fatal(err)
	}
	return db, cipher, account, aliases, client
}

func TestPoolConcurrentClaimsAndAtomicShortage(t *testing.T) { runPoolConcurrency(t, "") }

// A dedicated empty PostgreSQL database allows the same concurrency contract
// to run against the deployment engine without touching application data.
func TestPoolPostgresConcurrency(t *testing.T) {
	dsn := os.Getenv("ICLOUD_POOL_TEST_DSN")
	if dsn == "" {
		t.Skip("set ICLOUD_POOL_TEST_DSN to an empty test database")
	}
	runPoolConcurrency(t, dsn)
}

func runPoolConcurrency(t *testing.T, dsn string) {
	db, _, _, aliases, client := poolFixture(t, 12, dsn)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan []store.PoolLease, 20)
	failures := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			leases, err := db.ClaimPool(ctx, client.ID, store.PoolClaim{RequestID: fmt.Sprintf("concurrent-%03d", index), Count: 1, TTLSeconds: 600})
			if err != nil {
				failures <- err
			} else {
				results <- leases
			}
		}(i)
	}
	wg.Wait()
	close(results)
	close(failures)
	seen := map[int64]bool{}
	for result := range results {
		if len(result) != 1 || seen[result[0].AliasID] {
			t.Fatal("duplicate allocation")
		}
		seen[result[0].AliasID] = true
	}
	if len(seen) != len(aliases) {
		t.Fatalf("allocated %d of %d", len(seen), len(aliases))
	}
	for err := range failures {
		if !errors.Is(err, store.ErrPoolEmpty) {
			t.Fatal(err)
		}
	}
	page, err := db.ListPoolLeases(ctx, "", "", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ActPoolLease(ctx, page.Items[0].ID, client.ID, "release", 0); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ClaimPool(ctx, client.ID, store.PoolClaim{RequestID: "atomic-shortage", Count: 2, TTLSeconds: 600}); !errors.Is(err, store.ErrPoolEmpty) {
		t.Fatal(err)
	}
	pageMembers, err := db.ListPoolMembers(ctx, "available", "", 50, 0)
	if err != nil || pageMembers.Total != 1 {
		t.Fatalf("partial allocation on shortage: %d %v", pageMembers.Total, err)
	}
}

func TestPoolLeaseLifecycleAndCredentialRevocation(t *testing.T) {
	db, cipher, _, aliases, client := poolFixture(t, 1, "")
	ctx := context.Background()
	input := store.PoolClaim{RequestID: "lifecycle-001", Count: 1, TTLSeconds: 600}
	leases, err := db.ClaimPool(ctx, client.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	first, err := db.GetAlias(ctx, aliases[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := cipher.DecryptAliasCredentials(first.ID, first.CredentialCiphertext)
	if err != nil {
		t.Fatal(err)
	}
	if first.CredentialVersion != aliases[0].CredentialVersion+1 {
		t.Fatal("claim did not rotate credentials")
	}
	again, err := db.ClaimPool(ctx, client.ID, input)
	if err != nil || again[0].ID != leases[0].ID {
		t.Fatalf("idempotency: %v", err)
	}
	same, _ := db.GetAlias(ctx, first.ID)
	if same.CredentialVersion != first.CredentialVersion {
		t.Fatal("retry rotated credentials")
	}
	changed := input
	changed.Count = 2
	if _, err = db.ClaimPool(ctx, client.ID, changed); !errors.Is(err, store.ErrPoolRequestConflict) {
		t.Fatal(err)
	}
	other, _, err := db.CreatePoolClient(ctx, "other-project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ActPoolLease(ctx, leases[0].ID, other.ID, "release", 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("cross-project release", err)
	}
	renewed, err := db.ActPoolLease(ctx, leases[0].ID, client.ID, "renew", 1800)
	if err != nil || !renewed.ExpiresAt.After(leases[0].ExpiresAt) {
		t.Fatal("renew", err)
	}
	if _, err = db.ActPoolLease(ctx, leases[0].ID, client.ID, "release", 0); err != nil {
		t.Fatal(err)
	}
	if _, err = db.GetMailboxBindingByAPIKeyHash(ctx, secure.HashToken(creds.APIKey)); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("released API key remains valid", err)
	}
	rotated, _ := db.GetAlias(ctx, first.ID)
	if bytes.Equal(rotated.IMAPPasswordHash, first.IMAPPasswordHash) || bytes.Equal(rotated.RefreshTokenHash, first.RefreshTokenHash) || rotated.OAuthClientID == first.OAuthClientID {
		t.Fatal("release did not revoke all credentials")
	}
	if _, err = db.ActPoolLease(ctx, leases[0].ID, client.ID, "release", 0); err != nil {
		t.Fatal("idempotent release", err)
	}
	closed, err := db.ClaimPool(ctx, client.ID, input)
	if err != nil || closed[0].State != "released" {
		t.Fatal("old request allocated new mail", err)
	}
	input.RequestID = "lifecycle-002"
	next, err := db.ClaimPool(ctx, other.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if next[0].AliasID != first.ID {
		t.Fatal("released mail not reusable")
	}
	if _, err = db.ActPoolLease(ctx, next[0].ID, other.ID, "commit", 0); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ActPoolLease(ctx, next[0].ID, other.ID, "release", 0); !errors.Is(err, store.ErrPoolClosed) {
		t.Fatal("used mailbox recycled", err)
	}
}

func TestPoolExpiryAndInventoryGate(t *testing.T) {
	db, _, account, aliases, client := poolFixture(t, 1, "")
	ctx := context.Background()
	if err := db.SetPoolAccount(ctx, account.ID, true, 1); err != nil {
		t.Fatal(err)
	}
	allowed, err := db.PoolCreationAllowed(ctx, account.ID)
	if err != nil || allowed {
		t.Fatal("stock target ignored", err)
	}
	leases, err := db.ClaimPool(ctx, client.ID, store.PoolClaim{RequestID: "expiry-00001", Count: 1, TTLSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	allowed, err = db.PoolCreationAllowed(ctx, account.ID)
	if err != nil || !allowed {
		t.Fatal("creation stayed paused", err)
	}
	if _, err = db.DB().Exec(`UPDATE pool_leases SET expires_at=? WHERE id=?`, time.Now().Add(-time.Minute).UnixNano(), leases[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ActPoolLease(ctx, leases[0].ID, client.ID, "commit", 0); !errors.Is(err, store.ErrPoolClosed) {
		t.Fatal("expired lease committed", err)
	}
	if err = db.RefreshPool(ctx); err != nil {
		t.Fatal(err)
	}
	v, err := db.GetPoolLease(ctx, leases[0].ID, client.ID)
	if err != nil || v.State != "expired" {
		t.Fatal("expiry not persisted", err)
	}
	al, _ := db.GetAlias(ctx, aliases[0].ID)
	if al.CredentialVersion != aliases[0].CredentialVersion+2 {
		t.Fatal("expiry did not rotate")
	}
	if err = db.RefreshPool(ctx); err != nil {
		t.Fatal(err)
	}
	same, _ := db.GetAlias(ctx, al.ID)
	if same.CredentialVersion != al.CredentialVersion {
		t.Fatal("expiry repeated rotation")
	}
}

func TestPoolNewOnlyEnrollmentAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persistent.db")
	db, _, account, _, client := poolFixture(t, 0, path)
	ctx := context.Background()
	old, err := db.CreateAlias(ctx, domain.Alias{AccountID: account.ID, Address: "existing@icloud.com", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.SetPoolAccount(ctx, account.ID, true, 1); err != nil {
		t.Fatal(err)
	}
	newAlias, err := db.CreateAlias(ctx, domain.Alias{AccountID: account.ID, Address: "new@icloud.com", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.RefreshPool(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := db.ListPoolMembers(ctx, "", "", 50, 0)
	if err != nil || page.Total != 1 || page.Items[0].AliasID != newAlias.ID {
		t.Fatal("existing mail auto-enrolled", err)
	}
	leases, err := db.ClaimPool(ctx, client.ID, store.PoolClaim{RequestID: "persist-0001", Count: 1, TTLSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	v, err := reopened.GetPoolLease(ctx, leases[0].ID, client.ID)
	if err != nil || v.State != "leased" {
		t.Fatal("lease lost on restart", err)
	}
	page, err = reopened.ListPoolMembers(ctx, "", "", 50, 0)
	if err != nil || page.Total != 1 || page.Items[0].AliasID == old.ID {
		t.Fatal("pool lost on restart", err)
	}
}

func TestPoolExpiryClosesDeletedAliasLease(t *testing.T) {
	db, _, _, aliases, client := poolFixture(t, 1, "")
	ctx := context.Background()
	leases, err := db.ClaimPool(ctx, client.ID, store.PoolClaim{RequestID: "deleted-lease-0001", Count: 1, TTLSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteAlias(ctx, aliases[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().Exec(`UPDATE pool_leases SET expires_at=? WHERE id=?`, time.Now().Add(-time.Minute).UnixNano(), leases[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := db.RefreshPool(ctx); err != nil {
		t.Fatal(err)
	}
	v, err := db.GetPoolLease(ctx, leases[0].ID, client.ID)
	if err != nil || v.State != "expired" || v.AliasID != 0 {
		t.Fatalf("deleted alias left in expiry queue: %#v %v", v, err)
	}
}
