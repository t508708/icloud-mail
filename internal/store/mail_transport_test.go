package store

import (
	"context"
	"errors"
	"testing"

	"icloud-api/internal/domain"
)

func TestAccountMailTransportDefaultsAndEligibility(t *testing.T) {
	db, err := OpenContext(context.Background(), "file:"+t.TempDir()+"/mail-transport.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	account, err := db.CreateAccount(ctx, domain.Account{Name: "icloud", Email: "owner@icloud.com", IMAPHost: domain.DefaultIMAPHost, IMAPPort: 993, IMAPUsername: "owner@icloud.com", PasswordCiphertext: "cipher", Enabled: true, MailboxType: domain.MailboxTypeICloud})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := db.GetAccountMailTransport(ctx, account.ID); err != nil || got != MailTransportIMAP {
		t.Fatalf("default transport = %q, %v", got, err)
	}
	if err := db.SetAccountMailTransport(ctx, account.ID, MailTransportWebmail); err != nil {
		t.Fatal(err)
	}
	if got, err := db.GetAccountMailTransport(ctx, account.ID); err != nil || got != MailTransportWebmail {
		t.Fatalf("saved transport = %q, %v", got, err)
	}
	if err := db.SetAccountMailTransport(ctx, account.ID, "smtp"); !errors.Is(err, ErrInvalidMailTransport) {
		t.Fatalf("invalid transport error = %v", err)
	}
	custom, err := db.CreateAccount(ctx, domain.Account{Name: "custom", Email: "owner@example.com", EmailSuffix: "example.com", IMAPHost: "imap.example.com", IMAPPort: 993, IMAPUsername: "owner@example.com", PasswordCiphertext: "cipher", Enabled: true, MailboxType: domain.MailboxTypeCustom})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetAccountMailTransport(ctx, custom.ID, MailTransportWebmail); !errors.Is(err, ErrICloudMailboxRequired) {
		t.Fatalf("custom webmail error = %v", err)
	}
}
