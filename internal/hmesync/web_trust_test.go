package hmesync

import (
	"context"
	"errors"
	"testing"

	"icloud-api/internal/apple"
	"icloud-api/internal/store"
)

func TestExpiredWebSessionRetainsOnlyTrustForSameAccountLogin(t *testing.T) {
	s, db, account := independentAccountFixture(t, true)
	ctx := context.Background()
	previous := apple.Session{AppleID: account.Email, Region: apple.RegionGlobal, ClientID: "stable-browser", TrustToken: "fixture-trust", SessionToken: "expired-service-token", DSID: "fixture-dsid"}
	if _, err := s.saveSession(ctx, account.ID, previous); err != nil {
		t.Fatal(err)
	}
	s.expireSession(ctx, account.ID)
	if info, err := s.GetSession(ctx, account.ID); err != nil || info.Status != StatusExpired {
		t.Fatalf("service still active: %#v %v", info, err)
	}
	if _, _, err := s.loadSession(ctx, account.ID); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("expired service remained usable: %v", err)
	}
	retained := s.previousSession(ctx, account.ID, account.Email, apple.RegionGlobal)
	if retained == nil || retained.TrustToken != "fixture-trust" || retained.ClientID != "stable-browser" || retained.SessionToken != "" || retained.DSID != "" || len(retained.Cookies) != 0 {
		t.Fatalf("unexpected retained checkpoint: present=%v", retained != nil)
	}
	if s.previousSession(ctx, account.ID, "other@icloud.com", apple.RegionGlobal) != nil {
		t.Fatal("cross-account trust reused")
	}
	if s.previousSession(ctx, account.ID, account.Email, apple.RegionChina) != nil {
		t.Fatal("cross-region trust reused")
	}
	if err := s.ClearAuth(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetAppleWebSession(ctx, account.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("explicit logout retained trust: %v", err)
	}
}
