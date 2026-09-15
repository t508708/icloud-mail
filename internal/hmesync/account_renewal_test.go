package hmesync

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"icloud-api/internal/apple"
)

func TestAccountRenewalUsesTTLAndPersistsBackoff(t *testing.T) {
	s, db, account := independentAccountFixture(t, true)
	ctx := context.Background()
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	managed := apple.AccountSession{AppleID: account.Email, Region: apple.RegionGlobal, SCNT: "old", APIKey: "old", AuthenticatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(15 * time.Minute)}
	if err := s.saveAccountManagementSession(ctx, account.ID, managed); err != nil {
		t.Fatal(err)
	}
	calls, fail := 0, false
	client := &accountTestClient{accountRefresh: func(current apple.AccountSession) (apple.AccountSession, error) {
		calls++
		if fail {
			return current, &apple.Error{Kind: apple.ErrService, StatusCode: http.StatusServiceUnavailable}
		}
		current.APIKey = "renewed"
		current.ExpiresAt = now.Add(15 * time.Minute)
		return current, nil
	}}
	s.client = client
	now = now.Add(3 * time.Minute)
	if err := s.keepAliveAccountSession(ctx, account.ID, true, nil); err != nil || calls != 0 {
		t.Fatalf("early renewal: calls=%d err=%v", calls, err)
	}
	now = now.Add(time.Minute)
	if err := s.keepAliveAccountSession(ctx, account.ID, true, nil); err != nil || calls != 1 {
		t.Fatalf("due renewal: calls=%d err=%v", calls, err)
	}
	fail = true
	now = now.Add(4 * time.Minute)
	if err := s.keepAliveAccountSession(ctx, account.ID, true, nil); err == nil {
		t.Fatal("expected service outage")
	}
	fresh := newTestService(t, db, client, &fakeLocker{}, func() time.Time { return now })
	stored, err := fresh.readAccountManagementSession(ctx, account.ID)
	if err != nil || stored.APIKey != "renewed" || stored.RefreshFailures != 1 || stored.RefreshRejected {
		t.Fatalf("outage damaged session or lost retry state: %v", err)
	}
	if err := fresh.keepAliveAccountSession(ctx, account.ID, true, nil); err != nil || calls != 2 {
		t.Fatal("new service ignored persisted retry deadline")
	}
	now = now.Add(2 * time.Minute)
	fail = false
	if err := fresh.keepAliveAccountSession(ctx, account.ID, true, nil); err != nil || calls != 3 {
		t.Fatalf("retry did not recover: %v", err)
	}
}

func TestAccountRenewalKeepsAcceptedCheckpointWhenLaterRefreshStepFails(t *testing.T) {
	s, _, account := independentAccountFixture(t, true)
	ctx := context.Background()
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	old := apple.AccountSession{AppleID: account.Email, Region: apple.RegionGlobal, SCNT: "old", APIKey: "old", AuthenticatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if err := s.saveAccountManagementSession(ctx, account.ID, old); err != nil {
		t.Fatal(err)
	}
	rotated := old
	rotated.SCNT, rotated.UpdatedAt = "rotated", now.Add(time.Second)
	s.client = &accountTestClient{accountRefresh: func(apple.AccountSession) (apple.AccountSession, error) {
		return rotated, &apple.Error{Kind: apple.ErrService, StatusCode: http.StatusBadGateway}
	}}
	now = now.Add(50 * time.Second)
	if err := s.keepAliveAccountSession(ctx, account.ID, true, nil); err == nil {
		t.Fatal("expected refresh failure")
	}
	stored, err := s.readAccountManagementSession(ctx, account.ID)
	if err != nil || stored.SCNT != "rotated" || stored.APIKey != "old" || stored.RefreshFailures != 1 {
		t.Fatalf("accepted checkpoint was discarded: %+v err=%v", stored, err)
	}
}

func TestAccountRenewalSurvivesLaterStaleWebCheckpoint(t *testing.T) {
	s, _, account := independentAccountFixture(t, true)
	ctx := context.Background()
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	old := apple.AccountSession{AppleID: account.Email, Region: apple.RegionGlobal, APIKey: "old", AuthenticatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(15 * time.Minute)}
	web := apple.Session{AppleID: account.Email, Region: apple.RegionGlobal, SessionToken: "web-token", Account: &old}
	if _, err := s.saveSession(ctx, account.ID, web); err != nil {
		t.Fatal(err)
	}
	now = now.Add(12 * time.Minute)
	s.client = &accountTestClient{accountRefresh: func(current apple.AccountSession) (apple.AccountSession, error) {
		current.APIKey = "fresh"
		current.ExpiresAt = now.Add(15 * time.Minute)
		return current, nil
	}}
	if err := s.keepAliveAccountSession(ctx, account.ID, true, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.saveSession(ctx, account.ID, web); err != nil {
		t.Fatal(err)
	}
	_, current, err := s.loadSession(ctx, account.ID)
	if err != nil || current.Account.APIKey != "fresh" || current.SessionToken != "web-token" {
		t.Fatalf("stale Web checkpoint overwrote renewed ACC: %v", err)
	}
}

func TestAccountRenewalStopsRejectedSessionAndDisabledAccounts(t *testing.T) {
	s, db, account := independentAccountFixture(t, true)
	ctx := context.Background()
	now := time.Now().UTC()
	old := apple.AccountSession{AppleID: account.Email, Region: apple.RegionGlobal, APIKey: "old", AuthenticatedAt: now.Add(-time.Hour)}
	if err := s.saveAccountManagementSession(ctx, account.ID, old); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.client = &accountTestClient{accountRefresh: func(current apple.AccountSession) (apple.AccountSession, error) {
		calls++
		return current, apple.ErrInvalidSession
	}}
	if err := s.keepAliveAccountSession(ctx, account.ID, true, nil); !errors.Is(err, apple.ErrInvalidSession) {
		t.Fatal(err)
	}
	if err := s.keepAliveAccountSession(ctx, account.ID, true, nil); err != nil || calls != 1 {
		t.Fatal("repeated rejected credentials")
	}
	if info, err := s.GetAccountSession(ctx, account.ID); err != nil || info.Status != StatusExpired {
		t.Fatal("server rejection not reflected in session status")
	}
	if err := s.saveAccountManagementSession(ctx, account.ID, old); err != nil {
		t.Fatal(err)
	}
	account.Enabled = false
	if _, err := db.UpdateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	if err := s.keepAliveAccountSession(ctx, account.ID, true, nil); err != nil || calls != 1 {
		t.Fatal("renewed a disabled primary")
	}
}

func TestAccountRenewalWorkerRunsAndStopsWithContext(t *testing.T) {
	s, _, account := independentAccountFixture(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.saveAccountManagementSession(ctx, account.ID, apple.AccountSession{AppleID: account.Email, Region: apple.RegionGlobal, APIKey: "key", AuthenticatedAt: time.Now().Add(-5 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	called := false
	s.client = &accountTestClient{accountRefresh: func(current apple.AccountSession) (apple.AccountSession, error) {
		called = true
		cancel()
		return current, context.Canceled
	}}
	s.RunAccountSessionRenewal(ctx, nil)
	if !called {
		t.Fatal("worker never renewed the saved session")
	}
}
