package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"icloud-api/internal/apple"
	"icloud-api/internal/hmesync"
)

type accountAuthFake struct{ fakeHMESyncService }

func (f *accountAuthFake) StartAccountAuth(context.Context, int64, int64, string, string, apple.Region) (hmesync.AuthResult, error) {
	return hmesync.AuthResult{Status: hmesync.StatusVerificationRequired, ChallengeID: "challenge"}, nil
}
func (f *accountAuthFake) VerifyAccountAuth(context.Context, int64, int64, string, string) (hmesync.AuthResult, error) {
	return hmesync.AuthResult{Status: hmesync.StatusAuthenticated}, nil
}
func (f *accountAuthFake) GetAccountSession(context.Context, int64) (hmesync.SessionInfo, error) {
	return hmesync.SessionInfo{Status: hmesync.StatusAuthenticated}, nil
}
func (f *accountAuthFake) ClearAccountAuth(context.Context, int64) error { return nil }

func TestAppleAccountAuthEndpoints(t *testing.T) {
	env := newAdminAPITestEnv(t)
	acct := adminAPITestCreateAccount(t, env, "auth-test@icloud.com")
	path := fmt.Sprintf("/admin/api/v1/accounts/%d/apple-account-auth", acct.ID)
	if got := env.request(t, http.MethodGet, path, nil, "", nil, "").Code; got != http.StatusUnauthorized {
		t.Fatalf("anonymous GET=%d", got)
	}
	cookie, csrf, _ := env.createSession(t, "auth-test-admin", "password")
	env.server.SetHMESyncService(&accountAuthFake{})
	if got := env.request(t, http.MethodPost, path, []byte(`{"apple_id":"a@icloud.com","password":"p","region":"global"}`), "application/json", []*http.Cookie{cookie}, "").Code; got != http.StatusForbidden {
		t.Fatalf("missing csrf=%d", got)
	}
	if got := env.request(t, http.MethodPost, path, []byte(`{"apple_id":"a@icloud.com","password":"p","region":"global"}`), "application/json", []*http.Cookie{cookie}, csrf).Code; got != http.StatusAccepted {
		t.Fatalf("start=%d", got)
	}
	verify := path + "/verify"
	if got := env.request(t, http.MethodPost, verify, []byte(`{"challenge_id":"challenge","code":"123456"}`), "application/json", []*http.Cookie{cookie}, csrf).Code; got != http.StatusOK {
		t.Fatalf("verify=%d", got)
	}
	if got := env.request(t, http.MethodDelete, path, []byte(`{}`), "application/json", []*http.Cookie{cookie}, csrf).Code; got != http.StatusNoContent {
		t.Fatalf("delete=%d", got)
	}
}
