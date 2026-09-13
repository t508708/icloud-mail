package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"icloud-api/internal/domain"
	"icloud-api/internal/store"
)

func TestAliasMutationsPreserveAccountMailboxCursor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestStore(t)
	account := createAccount(t, ctx, db, "Cursor preservation", "cursor-preservation@icloud.com")
	seedMailboxCursor(t, ctx, db, account.ID, 77, 500)
	assertMailboxCursor(t, ctx, db, account.ID, 77, 500)

	if _, err := db.CreateAlias(ctx, domain.Alias{
		AccountID: account.ID, Address: "created@icloud.com", APIKeyHash: []byte("created-key"), Enabled: true,
	}); err != nil {
		t.Fatalf("create enabled alias: %v", err)
	}
	assertMailboxCursor(t, ctx, db, account.ID, 77, 500)

	autoAlias, _, err := db.CreateAliasWithPendingAPIKey(ctx,
		domain.AppleWebSession{AccountID: account.ID, Ciphertext: "session", AppleID: account.Email, Authenticated: true},
		domain.Alias{AccountID: account.ID, Address: "auto@icloud.com", APIKeyHash: []byte("auto-key"), APIKeyPrefix: "auto", Enabled: false},
		"pending-key",
	)
	if err != nil {
		t.Fatalf("create pending automatic alias: %v", err)
	}
	assertMailboxCursor(t, ctx, db, account.ID, 77, 500)
	if _, _, err := db.ConfirmPendingAutoAlias(ctx,
		domain.AppleWebSession{AccountID: account.ID, Ciphertext: "confirmed-session", AppleID: account.Email, Authenticated: true},
		autoAlias.ID,
	); err != nil {
		t.Fatalf("confirm automatic alias: %v", err)
	}
	assertMailboxCursor(t, ctx, db, account.ID, 77, 500)

	if _, err := db.ImportAliases(ctx, account.ID, []domain.AliasImportCandidate{{
		Address: "imported@icloud.com", APIKeyHash: []byte("imported-key"), APIKeyPrefix: "imported", Active: true,
	}}); err != nil {
		t.Fatalf("import enabled alias: %v", err)
	}
	assertMailboxCursor(t, ctx, db, account.ID, 77, 500)

	disabled, err := db.CreateAlias(ctx, domain.Alias{
		AccountID: account.ID, Address: "reenabled@icloud.com", APIKeyHash: []byte("reenabled-key"), Enabled: false,
	})
	if err != nil {
		t.Fatalf("create disabled alias: %v", err)
	}
	if err := db.SetAliasEnabled(ctx, disabled.ID, true); err != nil {
		t.Fatalf("re-enable alias: %v", err)
	}
	assertMailboxCursor(t, ctx, db, account.ID, 77, 500)
}

func TestCreatingFirstAliasDoesNotFabricateMailboxCursor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestStore(t)
	account := createAccount(t, ctx, db, "Initial cursor", "initial-cursor@icloud.com")
	if _, err := db.CreateAlias(ctx, domain.Alias{
		AccountID: account.ID, Address: "first@icloud.com", APIKeyHash: []byte("first-key"), Enabled: true,
	}); err != nil {
		t.Fatalf("create first alias: %v", err)
	}
	if _, err := db.GetIMAPSyncState(ctx, account.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cursor after first alias creation = %v, want ErrNotFound until first mailbox sync", err)
	}
}

func seedMailboxCursor(t *testing.T, ctx context.Context, db *store.Store, accountID int64, uidValidity, lastUID uint32) {
	t.Helper()
	_, err := db.DB().ExecContext(ctx, `
		INSERT INTO imap_sync_states(account_id, uid_validity, last_uid, updated_at)
		VALUES(?, ?, ?, ?)`, accountID, uidValidity, lastUID, time.Now().UTC().UnixNano())
	if err != nil {
		t.Fatalf("seed mailbox cursor: %v", err)
	}
}

func assertMailboxCursor(t *testing.T, ctx context.Context, db *store.Store, accountID int64, uidValidity, lastUID uint32) {
	t.Helper()
	state, err := db.GetIMAPSyncState(ctx, accountID)
	if err != nil {
		t.Fatalf("read mailbox cursor: %v", err)
	}
	if state.UIDValidity != uidValidity || state.LastUID != lastUID {
		t.Fatalf("mailbox cursor = UIDVALIDITY %d UID %d, want %d/%d", state.UIDValidity, state.LastUID, uidValidity, lastUID)
	}
}
