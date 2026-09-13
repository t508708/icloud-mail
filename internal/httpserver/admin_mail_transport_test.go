package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"icloud-api/internal/hmesync"
	"icloud-api/internal/store"
)

func TestAdminAPIMailTransportContract(t *testing.T) {
	env := newAdminAPITestEnv(t)
	env.server.cfg.MailOnDemandOnly = true
	cookie, csrf, _ := env.createSession(t, "mail-transport-admin", "password")
	account := adminAPITestCreateAccount(t, env, "transport-owner@icloud.com")
	path := fmt.Sprintf("/admin/api/v1/accounts/%d/mail-transport", account.ID)
	initial, err := env.store.GetAccount(context.Background(), account.ID)
	if err != nil {
		t.Fatal(err)
	}
	unauthenticated := env.request(t, http.MethodPut, path, adminAPITestJSON(t, map[string]string{"transport": store.MailTransportWebmail}), "application/json", nil, csrf)
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("without admin auth: %d %s", unauthenticated.Code, unauthenticated.Body.String())
	}
	if got, err := env.store.GetAccountMailTransport(context.Background(), account.ID); err != nil || got != store.MailTransportIMAP {
		t.Fatalf("default transport=%q err=%v", got, err)
	}

	withoutCSRF := env.request(t, http.MethodPut, path, adminAPITestJSON(t, map[string]string{"transport": "webmail"}), "application/json", []*http.Cookie{cookie}, "")
	if withoutCSRF.Code != http.StatusForbidden || adminAPITestErrorCode(t, withoutCSRF) != "CSRF_INVALID" {
		t.Fatalf("without csrf: %d %s", withoutCSRF.Code, withoutCSRF.Body.String())
	}

	webSession := hmesync.SessionInfo{Status: hmesync.StatusAuthenticated, AppleID: account.Email}
	var sessionChecks int
	env.server.SetHMESyncService(&fakeHMESyncService{getSession: func(_ context.Context, id int64) (hmesync.SessionInfo, error) {
		sessionChecks++
		if id != account.ID {
			t.Fatalf("GetSession account=%d", id)
		}
		return webSession, nil
	}})
	success := env.request(t, http.MethodPut, path, adminAPITestJSON(t, map[string]string{"transport": store.MailTransportWebmail}), "application/json", []*http.Cookie{cookie}, csrf)
	if success.Code != http.StatusOK {
		t.Fatalf("webmail switch: %d %s", success.Code, success.Body.String())
	}
	if sessionChecks != 1 {
		t.Fatalf("web session checks after switch=%d, want 1", sessionChecks)
	}
	var switched struct {
		Data struct {
			Transport string `json:"transport"`
		} `json:"data"`
	}
	if err := json.Unmarshal(success.Body.Bytes(), &switched); err != nil || switched.Data.Transport != store.MailTransportWebmail {
		t.Fatalf("switch response=%s", success.Body.String())
	}

	detail := env.request(t, http.MethodGet, fmt.Sprintf("/admin/api/v1/accounts/%d", account.ID), nil, "", []*http.Cookie{cookie}, "")
	var envelope struct {
		Data struct {
			MailTransport string `json:"mail_transport"`
		} `json:"data"`
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &envelope); err != nil || detail.Code != http.StatusOK || envelope.Data.MailTransport != store.MailTransportWebmail {
		t.Fatalf("detail=%s", detail.Body.String())
	}
	got, err := env.store.GetAccount(context.Background(), account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || got.IMAPUsername != initial.IMAPUsername || got.IMAPHost != initial.IMAPHost || got.IMAPPort != initial.IMAPPort || got.PasswordCiphertext != initial.PasswordCiphertext {
		t.Fatalf("account credentials/status changed")
	}
	beforeIMAPSwitch := sessionChecks
	webSession.Status = hmesync.StatusLoginRequired
	back := env.request(t, http.MethodPut, path, adminAPITestJSON(t, map[string]string{"transport": store.MailTransportIMAP}), "application/json", []*http.Cookie{cookie}, csrf)
	if back.Code != http.StatusOK || sessionChecks != beforeIMAPSwitch {
		t.Fatalf("IMAP switch unexpectedly requires Web login: status=%d", back.Code)
	}
	webSession.Status = hmesync.StatusAuthenticated

	if _, err := env.store.DB().ExecContext(context.Background(), `UPDATE accounts SET enabled = FALSE WHERE id = ?`, account.ID); err != nil {
		t.Fatal(err)
	}
	disabled := env.request(t, http.MethodPut, path, adminAPITestJSON(t, map[string]string{"transport": store.MailTransportWebmail}), "application/json", []*http.Cookie{cookie}, csrf)
	if disabled.Code != http.StatusConflict || adminAPITestErrorCode(t, disabled) != "ACCOUNT_DISABLED" {
		t.Fatalf("disabled switch: %d %s", disabled.Code, disabled.Body.String())
	}

	if got, err := env.store.GetAccountMailTransport(context.Background(), account.ID); err != nil || got != store.MailTransportIMAP {
		t.Fatalf("failed switch changed transport=%q err=%v", got, err)
	}
}

func TestAdminAPIMailTransportRejectsWebmailWhenOnDemandDisabledOrSessionMissing(t *testing.T) {
	env := newAdminAPITestEnv(t)
	env.server.cfg.MailOnDemandOnly = false
	cookie, csrf, _ := env.createSession(t, "mail-transport-config-admin", "password")
	account := adminAPITestCreateAccount(t, env, "transport-config@icloud.com")
	path := fmt.Sprintf("/admin/api/v1/accounts/%d/mail-transport", account.ID)
	missing := env.request(t, http.MethodPut, path, adminAPITestJSON(t, map[string]string{"transport": store.MailTransportWebmail}), "application/json", []*http.Cookie{cookie}, csrf)
	if missing.Code != http.StatusConflict || adminAPITestErrorCode(t, missing) != "MAIL_ON_DEMAND_REQUIRED" {
		t.Fatalf("on-demand disabled: %d %s", missing.Code, missing.Body.String())
	}
	env.server.cfg.MailOnDemandOnly = true
	noSession := env.request(t, http.MethodPut, path, adminAPITestJSON(t, map[string]string{"transport": store.MailTransportWebmail}), "application/json", []*http.Cookie{cookie}, csrf)
	if noSession.Code != http.StatusConflict || adminAPITestErrorCode(t, noSession) != "WEB_MAIL_LOGIN_REQUIRED" {
		t.Fatalf("missing web session: %d %s", noSession.Code, noSession.Body.String())
	}
}
