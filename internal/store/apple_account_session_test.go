package store_test

import (
	"context"
	"errors"
	"icloud-api/internal/domain"
	"icloud-api/internal/store"
	"testing"
	"time"
)

func TestAppleAccountSessionIsIndependentFromWebSession(t *testing.T) {
	ctx := context.Background()
	db := openTestStore(t)
	a := createAccount(t, ctx, db, "Primary", "account-session@icloud.com")
	web := domain.AppleWebSession{AccountID: a.ID, Ciphertext: "web", AppleID: a.Email}
	account := domain.AppleWebSession{AccountID: a.ID, Ciphertext: "account", AppleID: a.Email}
	if _, err := db.UpsertAppleWebSession(ctx, web); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpsertAppleAccountSession(ctx, account); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.GetAppleWebSession(ctx, a.ID); got.Ciphertext != "web" {
		t.Fatalf("web=%q", got.Ciphertext)
	}
	if got, _ := db.GetAppleAccountSession(ctx, a.ID); got.Ciphertext != "account" {
		t.Fatalf("account=%q", got.Ciphertext)
	}
	if err := db.DeleteAppleWebSession(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetAppleAccountSession(ctx, a.ID); err != nil {
		t.Fatalf("account removed with web: %v", err)
	}
	if _, err := db.UpsertAppleWebSession(ctx, web); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteAppleAccountSession(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := db.GetAppleWebSession(ctx, a.ID); err != nil || got.Ciphertext != "web" {
		t.Fatalf("web changed after management logout: %v", err)
	}
	if _, err := db.GetAppleAccountSession(ctx, a.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("account lookup=%v", err)
	}
}

func TestAppleAccountSessionAllowsDisabledAndPreservesCreatedAt(t *testing.T) {
	ctx := context.Background()
	db := openTestStore(t)
	a := createAccount(t, ctx, db, "Disabled", "disabled-session@icloud.com")
	a.Enabled = false
	if _, err := db.UpdateAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	first, err := db.UpsertAppleAccountSession(ctx, domain.AppleWebSession{AccountID: a.ID, Ciphertext: "one", AppleID: a.Email})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	second, err := db.UpsertAppleAccountSession(ctx, domain.AppleWebSession{AccountID: a.ID, Ciphertext: "two", AppleID: a.Email})
	if err != nil {
		t.Fatal(err)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("created_at changed: %v -> %v", first.CreatedAt, second.CreatedAt)
	}
}

func TestAppleAccountSessionRequiresICloudMailbox(t *testing.T) {
	ctx := context.Background()
	db := openTestStore(t)
	a := createAccount(t, ctx, db, "Custom", "custom-session@example.test")
	a.MailboxType = domain.MailboxTypeCustom
	a.EmailSuffix = "example.test"
	if _, err := db.UpdateAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpsertAppleAccountSession(ctx, domain.AppleWebSession{AccountID: a.ID, Ciphertext: "x", AppleID: a.Email}); !errors.Is(err, store.ErrICloudMailboxRequired) {
		t.Fatalf("error=%v", err)
	}
}

func TestDeleteAccountCascadesAppleAccountSession(t *testing.T) {
	ctx := context.Background()
	db := openTestStore(t)
	a := createAccount(t, ctx, db, "Cascade", "cascade-session@icloud.com")
	if _, err := db.UpsertAppleAccountSession(ctx, domain.AppleWebSession{AccountID: a.ID, Ciphertext: "x", AppleID: a.Email}); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetAppleAccountSession(ctx, a.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("lookup=%v", err)
	}
}
