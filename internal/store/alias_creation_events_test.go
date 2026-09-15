package store_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"icloud-api/internal/domain"
	"icloud-api/internal/secure"
)

func TestRecentAliasCreationEventsExcludeOrdinaryAliases(t *testing.T) {
	ctx := context.Background()
	db := openTestStore(t)
	a := createAccount(t, ctx, db, "Events", "events@icloud.com")
	createAlias(t, ctx, db, a.ID, "imported@icloud.com", []byte("ordinary-test-hash"))
	now := time.Now().UTC().Truncate(time.Second)
	count, err := db.CountRecentAliasCreations(ctx, a.ID, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("non-Apple alias count=%d, want 0", count)
	}
}

func TestAliasCreationEventLifecycle(t *testing.T) {
	ctx := context.Background()
	db := openTestStore(t)
	cipher, err := secure.NewCipher(bytes.Repeat([]byte{0x7a}, 32))
	if err != nil {
		t.Fatal(err)
	}
	db.ConfigureAliasCredentialFactory(func(id, v int64) (domain.AliasCredentialMaterial, error) {
		_, m, e := secure.NewAliasCredentialMaterial(cipher, id, v)
		return m, e
	})
	db.ConfigureAliasCredentialReuseFactory(func(id, v int64, p string) (domain.AliasCredentialMaterial, error) {
		k, e := cipher.DecryptPendingAliasAPIKey(p)
		if e != nil {
			return domain.AliasCredentialMaterial{}, e
		}
		_, m, e := secure.NewAliasCredentialMaterialWithAPIKey(cipher, id, v, k)
		return m, e
	})
	a := createAccount(t, ctx, db, "Lifecycle", "lifecycle@icloud.com")
	raw, hash, prefix, err := secure.NewAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	pending, err := cipher.EncryptPendingAliasAPIKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	al, _, err := db.CreateAliasWithPendingAPIKey(ctx, domain.AppleWebSession{AccountID: a.ID, Ciphertext: "session", AppleID: a.Email, Authenticated: true}, domain.Alias{AccountID: a.ID, Address: "life@icloud.com", APIKeyHash: hash, APIKeyPrefix: prefix}, pending)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	count := func() int {
		n, e := db.CountRecentAliasCreations(ctx, a.ID, now.Add(-time.Minute), now.Add(time.Minute))
		if e != nil {
			t.Fatal(e)
		}
		return n
	}
	if count() != 0 {
		t.Fatal("pending alias counted")
	}
	if _, _, err = db.ConfirmPendingAutoAlias(ctx, domain.AppleWebSession{AccountID: a.ID, Ciphertext: "confirmed", AppleID: a.Email, Authenticated: true}, al.ID); err != nil {
		t.Fatal(err)
	}
	if count() != 1 {
		t.Fatal("confirmed alias not counted")
	}
	// Simulate an old installation: recover only confirmed creation markers,
	// not directory imports or addresses awaiting Apple confirmation.
	unconfirmed := createAlias(t, ctx, db, a.ID, "pending@icloud.com", []byte("pending-test-hash"))
	if _, err := db.DB().ExecContext(ctx, `UPDATE aliases SET enabled = 0, last_sync_error = ? WHERE id = ?`, domain.AppleAliasConfirmationPending, unconfirmed.ID); err != nil { t.Fatal(err) }
	if _, err := db.DB().ExecContext(ctx, `INSERT INTO pending_alias_api_keys(alias_id, api_key_ciphertext, created_at) VALUES (?, ?, ?)`, unconfirmed.ID, pending, now.UnixNano()); err != nil { t.Fatal(err) }
	if _, err := db.DB().ExecContext(ctx, `DELETE FROM apple_alias_creation_events`); err != nil { t.Fatal(err) }
	for i := 0; i < 2; i++ {
		if err := db.Migrate(ctx); err != nil { t.Fatal(err) }
		if count() != 1 { t.Fatal("migration must backfill confirmed markers exactly once") }
	}
	if err = db.DeletePendingAliasAPIKeys(ctx, a.ID, []int64{al.ID}); err != nil {
		t.Fatal(err)
	}
	if count() != 1 {
		t.Fatal("ack changed event")
	}
	if err = db.DeleteAlias(ctx, al.ID); err != nil {
		t.Fatal(err)
	}
	if count() != 1 {
		t.Fatal("delete changed event")
	}
}

func TestRecentAliasCreationCountRejectsFutureWindow(t *testing.T) {
	db := openTestStore(t)
	if _, err := db.CountRecentAliasCreations(context.Background(), 1, time.Now().Add(time.Hour), time.Now()); err == nil {
		t.Fatal("future window should be rejected")
	}
}

func TestRecentAliasCreationEventsAreAccountAndWindowScoped(t *testing.T) {
	ctx := context.Background()
	db := openTestStore(t)
	a1 := createAccount(t, ctx, db, "Events one", "events-one@icloud.com")
	a2 := createAccount(t, ctx, db, "Events two", "events-two@icloud.com")
	now := time.Now().UTC().Truncate(time.Second)
	for _, event := range []struct { account int64; address string; at time.Time }{
		{a1.ID, "lower@icloud.com", now.Add(-time.Second)},
		{a1.ID, "upper@icloud.com", now},
		{a1.ID, "future@icloud.com", now.Add(time.Nanosecond)},
		{a2.ID, "other@icloud.com", now},
	} {
		if _, err := db.DB().ExecContext(ctx, `INSERT INTO apple_alias_creation_events(account_id, address, created_at) VALUES (?, ?, ?)`, event.account, event.address, event.at.UnixNano()); err != nil { t.Fatal(err) }
	}
	for _, tc := range []struct {
		name         string
		account      int64
		since, until time.Time
		want         int
	}{
		{"inclusive boundaries", a1.ID, now.Add(-time.Second), now, 2},
		{"exclusive outside bounds", a1.ID, now.Add(-time.Second).Add(time.Nanosecond), now.Add(-time.Nanosecond), 0},
		{"account two", a2.ID, now.Add(-time.Second), now, 1},
		{"before boundary", a1.ID, now.Add(time.Second), now.Add(2 * time.Second), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := db.CountRecentAliasCreations(ctx, tc.account, tc.since, tc.until)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("count=%d, want %d", got, tc.want)
			}
		})
	}
}
