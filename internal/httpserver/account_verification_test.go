package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"icloud-api/internal/hmesync"
)

type accountSMSAuthFake struct {
	accountAuthFake
	sends, verifies int
}

func (f *accountSMSAuthFake) RequestAccountAuthCode(_ context.Context, owner, id int64, challenge, method string, phone int64) (hmesync.AuthResult, error) {
	f.sends++
	return hmesync.AuthResult{Status: hmesync.StatusVerificationRequired, ChallengeID: challenge, Verification: &hmesync.WebVerificationInfo{Method: method, PhoneID: phone, Sent: true, Phones: []hmesync.WebVerificationPhone{{ID: phone, Label: "受信任手机号 1"}}}}, nil
}
func (f *accountSMSAuthFake) VerifyAccountAuthWithMethod(_ context.Context, owner, id int64, challenge, code, method string, phone int64) (hmesync.AuthResult, error) {
	if method != "sms" || phone != 7 || code != "123456" {
		return hmesync.AuthResult{}, hmesync.ErrVerificationInvalid
	}
	f.verifies++
	return hmesync.AuthResult{Status: hmesync.StatusAuthenticated}, nil
}

func TestAccountSMSHTTPRequiresSessionCSRFAndAcceptsExplicitMethod(t *testing.T) {
	env := newAdminAPITestEnv(t)
	account := adminAPITestCreateAccount(t, env, "account-sms@icloud.com")
	fake := &accountSMSAuthFake{}
	env.server.SetHMESyncService(fake)
	cookie, csrf, _ := env.createSession(t, "account-sms-admin", "fixture-password")
	base := fmt.Sprintf("/admin/api/v1/accounts/%d/apple-account-auth", account.ID)
	body := []byte(`{"challenge_id":"fixture-challenge","method":"sms","phone_id":7}`)
	if got := env.request(t, "POST", base+"/code", body, "application/json", nil, ""); got.Code != 401 || fake.sends != 0 {
		t.Fatal("anonymous request sent code")
	}
	if got := env.request(t, "POST", base+"/code", body, "application/json", []*http.Cookie{cookie}, ""); got.Code != 403 || fake.sends != 0 {
		t.Fatal("missing CSRF sent code")
	}
	got := env.request(t, "POST", base+"/code", body, "application/json", []*http.Cookie{cookie}, csrf)
	if got.Code != 202 || fake.sends != 1 || !strings.Contains(got.Body.String(), `"method":"sms"`) {
		t.Fatalf("send status=%d", got.Code)
	}
	got = env.request(t, "POST", base+"/verify", []byte(`{"challenge_id":"fixture-challenge","code":"123456","verification_method":"sms","phone_id":7}`), "application/json", []*http.Cookie{cookie}, csrf)
	if got.Code != 200 || fake.verifies != 1 || fake.sends != 1 {
		t.Fatalf("verify status=%d", got.Code)
	}
	got = env.request(t, "POST", base+"/verify", []byte(`{"challenge_id":"fixture-challenge","code":"123456","verification_method":"sms"}`), "application/json", []*http.Cookie{cookie}, csrf)
	if got.Code != 400 || fake.verifies != 1 {
		t.Fatal("missing phone accepted")
	}
}
