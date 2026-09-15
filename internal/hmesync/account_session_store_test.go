package hmesync

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"icloud-api/internal/apple"
	"icloud-api/internal/domain"
	"icloud-api/internal/store"
)

func independentAccountFixture(t *testing.T, enabled bool) (*Service, *store.Store, domain.Account) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	account, err := db.CreateAccount(context.Background(), domain.Account{Name: "fixture", Email: "owner@icloud.com", IMAPHost: "imap.mail.icloud.com", IMAPPort: 993, IMAPUsername: "owner@icloud.com", PasswordCiphertext: "fixture", Enabled: enabled})
	if err != nil {
		t.Fatal(err)
	}
	client := &accountTestClient{accountSignIn: func(s apple.AccountSession) (apple.AccountSession, bool, error) { return s, true, nil }}
	s := newTestService(t, db, client, &fakeLocker{}, time.Now)
	return s, db, account
}

func TestAccountLoginWithoutWebSessionWhilePrimaryDisabled(t *testing.T) {
	s, db, account := independentAccountFixture(t, false)
	ctx := context.Background()
	result, err := s.StartAccountAuth(ctx, 1, account.ID, "owner@icloud.com", "fixture-password", apple.RegionGlobal)
	if err != nil || result.Status != StatusVerificationRequired {
		t.Fatalf("start: %v %#v", err, result)
	}
	if _, err := s.VerifyAccountAuth(ctx, 2, account.ID, result.ChallengeID, "123456"); !errors.Is(err, ErrFlowExpired) {
		t.Fatalf("wrong owner: %v", err)
	}
	verified, err := s.VerifyAccountAuth(ctx, 1, account.ID, result.ChallengeID, "123456")
	if err != nil || verified.Status != StatusAuthenticated {
		t.Fatalf("verify: %v %#v", err, verified)
	}
	if _, err := db.GetAppleWebSession(ctx, account.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("login fabricated Web session: %v", err)
	}
	if info, err := s.GetAccountSession(ctx, account.ID); err != nil || info.Status != StatusAuthenticated {
		t.Fatalf("read management: %v %#v", err, info)
	}
	if fresh, err := db.GetAccount(ctx, account.ID); err != nil || fresh.Enabled {
		t.Fatal("login changed disabled primary")
	}
	if err := s.ClearAccountAuth(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	if info, err := s.GetAccountSession(ctx, account.ID); err != nil || info.Status != StatusLoginRequired {
		t.Fatalf("logout: %v %#v", err, info)
	}
}

func TestAccountWebExpiryPreservesLegacyManagementAndFreshLogin(t *testing.T) {
	s, db, account := independentAccountFixture(t, true)
	ctx := context.Background()
	managed := apple.AccountSession{AppleID: account.Email, Region: apple.RegionGlobal, APIKey: "old-key", AuthenticatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	web := apple.Session{AppleID: account.Email, Region: apple.RegionGlobal, SessionToken: "web-token", Account: &managed}
	if _, err := s.saveSession(ctx, account.ID, web); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearAuth(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetAppleWebSession(ctx, account.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("web logout: %v", err)
	}
	if current, err := s.readAccountManagementSession(ctx, account.ID); err != nil || current.APIKey != "old-key" {
		t.Fatalf("lost legacy management: %v", err)
	}
	// A later Web login has no management cookies; it must not erase ACC.
	web.Account = nil
	if _, err := s.saveSession(ctx, account.ID, web); err != nil {
		t.Fatal(err)
	}
	if _, current, err := s.loadSession(ctx, account.ID); err != nil || current.Account == nil || current.Account.APIKey != "old-key" {
		t.Fatalf("attach management: %v", err)
	}
	managed.APIKey = "new-key"
	if err := s.persistAccountManagementSession(ctx, account.ID, identityOf(account), managed); err != nil {
		t.Fatal(err)
	}
	if _, current, err := s.loadSession(ctx, account.ID); err != nil || current.Account == nil || current.Account.APIKey != "new-key" {
		t.Fatalf("fresh management: %v", err)
	}
	s.expireSession(ctx, account.ID)
	if current, err := s.readAccountManagementSession(ctx, account.ID); err != nil || current.APIKey != "new-key" {
		t.Fatalf("expiry lost management: %v", err)
	}
}

func TestAccountHMEServiceUnavailableKeepsAuthenticatedWebSession(t *testing.T) {
	s, db, account := independentAccountFixture(t, true)
	ctx := context.Background()
	s.client = &fakeAppleClient{
		validate: func(_ context.Context, s apple.Session) (apple.Session, error) { return s, nil },
		list: func(_ context.Context, s apple.Session) (apple.ListResult, apple.Session, error) {
			return apple.ListResult{}, s, &apple.Error{Op: "discover Hide My Email service", Kind: apple.ErrHMEUnavailable}
		},
	}
	if _, err := s.saveSession(ctx, account.ID, apple.Session{AppleID: account.Email, Region: apple.RegionGlobal, DSID: "fixture-dsid"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SyncAliases(ctx, account.ID); Code(err) != CodeHMEUnavailable {
		t.Fatalf("classification: %v", err)
	}
	if record, err := db.GetAppleWebSession(ctx, account.ID); err != nil || !record.Authenticated {
		t.Fatalf("directory failure erased login: %v", err)
	}
}

func TestAccountNewLoginSurvivesOlderWebVerificationChallenge(t *testing.T) {
	s, _, account := independentAccountFixture(t, true)
	ctx := context.Background()
	managed := apple.AccountSession{AppleID: account.Email, Region: apple.RegionGlobal, APIKey: "old-key", AuthenticatedAt: time.Now()}
	web := apple.Session{AppleID: account.Email, Region: apple.RegionGlobal, SessionToken: "web-token", Account: &managed}
	if _, err := s.saveSession(ctx, account.ID, web); err != nil {
		t.Fatal(err)
	}
	s.client = &fakeAppleClient{
		signIn: func(_ context.Context, _ string, _ string, _ apple.Region, previous *apple.Session) (apple.Session, bool, error) {
			return *previous, true, nil
		},
		verify: func(_ context.Context, session apple.Session, _ string) (apple.Session, error) { return session, nil },
	}
	flow, err := s.StartAuth(ctx, 1, account.ID, account.Email, "fixture", apple.RegionGlobal)
	if err != nil {
		t.Fatal(err)
	}
	managed.APIKey = "new-key"
	if err := s.persistAccountManagementSession(ctx, account.ID, identityOf(account), managed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyAuth(ctx, 1, account.ID, flow.ChallengeID, "123456"); err != nil {
		t.Fatal(err)
	}
	_, saved, err := s.loadSession(ctx, account.ID)
	if err != nil || saved.Account == nil || saved.Account.APIKey != "new-key" {
		t.Fatalf("old Web challenge overwrote new login: %v", err)
	}
}
