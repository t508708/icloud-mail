package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"icloud-api/internal/apple"
	"icloud-api/internal/hmesync"
)

type codeDeliveryService struct {
	fakeHMESyncService
	send func(context.Context, int64, int64, string, string, int64) (hmesync.AuthResult, error)
}

func (f *codeDeliveryService) RequestAuthCode(ctx context.Context, owner, id int64, challenge, method string, phone int64) (hmesync.AuthResult, error) {
	return f.send(ctx, owner, id, challenge, method, phone)
}

func TestWebCodeRequiresSessionCSRFAndReturnsNoPrivateFields(t *testing.T) {
	env := newAdminAPITestEnv(t)
	account := adminAPITestCreateAccount(t, env, "sms-owner@icloud.com")
	cookie, csrf, admin := env.createSession(t, "sms-admin", "fixture-password")
	path := fmt.Sprintf("/admin/api/v1/accounts/%d/apple-auth/code", account.ID)
	body := []byte(`{"challenge_id":"fixture-challenge","method":"sms","phone_id":7}`)
	calls := 0
	throttle := false
	env.server.SetHMESyncService(&codeDeliveryService{send: func(_ context.Context, owner, id int64, challenge, method string, phone int64) (hmesync.AuthResult, error) {
		calls++
		if owner != admin.ID || id != account.ID || challenge != "fixture-challenge" || method != "sms" || phone != 7 {
			t.Fatal("wrong delivery args")
		}
		if throttle {
			return hmesync.AuthResult{}, errors.Join(hmesync.ErrRateLimited, &apple.Error{Kind: apple.ErrService, StatusCode: 429, RetryAfter: 90 * time.Second})
		}
		return hmesync.AuthResult{Status: hmesync.StatusVerificationRequired, ChallengeID: challenge, Verification: &hmesync.WebVerificationInfo{Method: "sms", Sent: true, PhoneID: 7, Phones: []hmesync.WebVerificationPhone{{ID: 7, Label: "受信任手机号 1（尾号 42）"}}}}, nil
	}})
	if got := env.request(t, "POST", path, body, "application/json", nil, ""); got.Code != 401 || calls != 0 {
		t.Fatal("missing session was accepted")
	}
	if got := env.request(t, "POST", path, body, "application/json", []*http.Cookie{cookie}, ""); got.Code != 403 || calls != 0 {
		t.Fatal("missing CSRF was accepted")
	}
	got := env.request(t, "POST", path, body, "application/json", []*http.Cookie{cookie}, csrf)
	if got.Code != 202 || calls != 1 || !strings.Contains(got.Body.String(), `"sent":true`) {
		t.Fatalf("status=%d body=%s", got.Code, got.Body.String())
	}
	for _, private := range []string{"fixture-password", "sms-owner@icloud.com", "session_token", "cookies", "numberWithDialCode"} {
		if strings.Contains(got.Body.String(), private) {
			t.Fatalf("private field leaked: %s", private)
		}
	}
	bad := env.request(t, "POST", path, []byte(`{"challenge_id":"c","method":"sms","phone_id":-1}`), "application/json", []*http.Cookie{cookie}, csrf)
	if bad.Code != 400 || calls != 1 {
		t.Fatal("bad phone reached service")
	}
	throttle = true
	got = env.request(t, "POST", path, body, "application/json", []*http.Cookie{cookie}, csrf)
	if got.Code != 429 || got.Header().Get("Retry-After") != "90" {
		t.Fatalf("cooldown status=%d header=%s", got.Code, got.Header().Get("Retry-After"))
	}
}
