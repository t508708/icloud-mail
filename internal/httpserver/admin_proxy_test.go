package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestAdminAPIAccountProxyCreateEditAndSecretProtection(t *testing.T) {
	env := newAdminAPITestEnv(t)
	cookie, csrf, _ := env.createSession(t, "proxy-account-admin", "unused-password")
	email := "owner-proxy@icloud.com"
	create := env.request(t, http.MethodPost, "/admin/api/v1/accounts", adminAPITestJSON(t, map[string]any{
		"name": "Proxy account", "email": email, "imap_username": email,
		"imap_password": "app-pass", "proxy": map[string]any{
			"enabled": true, "endpoint": "socks5://proxy-user:proxy-secret@localhost:1080",
			"upstream": "http://front-user:front-secret@localhost:8080",
		},
	}), "application/json", []*http.Cookie{cookie}, csrf)
	if create.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", create.Code, create.Body.String())
	}
	var result struct {
		Data adminAPIAccountDTO `json:"data"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Data.Proxy.Enabled || !result.Data.Proxy.Configured || !result.Data.Proxy.UpstreamConfigured {
		t.Fatalf("created proxy status = %+v", result.Data.Proxy)
	}
	accountID := result.Data.ID
	account, err := env.store.GetAccount(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	if account.Proxy == nil || account.Proxy.EndpointCiphertext == "" || strings.Contains(account.Proxy.EndpointCiphertext, "proxy-secret") {
		t.Fatal("proxy endpoint was not stored encrypted")
	}
	if strings.Contains(create.Body.String(), "proxy-secret") || strings.Contains(create.Body.String(), "front-secret") {
		t.Fatal("create response exposed proxy secrets")
	}
	accountPath := "/admin/api/v1/accounts/" + strconv.FormatInt(accountID, 10)
	get := env.request(t, http.MethodGet, accountPath, nil, "", []*http.Cookie{cookie}, csrf)
	if get.Code != http.StatusOK || strings.Contains(get.Body.String(), "proxy-secret") || strings.Contains(get.Body.String(), "front-secret") {
		t.Fatalf("get: %d %s", get.Code, get.Body.String())
	}

	update := func(proxy map[string]any) *httptest.ResponseRecorder {
		return env.request(t, http.MethodPut, accountPath, adminAPITestJSON(t, map[string]any{
			"name": "Proxy account", "email": email, "imap_username": email,
			"imap_password": "", "enabled": true, "proxy": proxy,
		}), "application/json", []*http.Cookie{cookie}, csrf)
	}
	if response := update(map[string]any{"enabled": false}); response.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", response.Code, response.Body.String())
	}
	account, _ = env.store.GetAccount(context.Background(), accountID)
	if account.Proxy.Enabled || account.Proxy.Revision < 2 {
		t.Fatalf("disabled state = %+v", account.Proxy)
	}
	firstCiphertext, firstUpstream := account.Proxy.EndpointCiphertext, account.Proxy.UpstreamCiphertext
	if response := update(map[string]any{"enabled": true}); response.Code != http.StatusOK {
		t.Fatalf("re-enable with preserved credentials: %d %s", response.Code, response.Body.String())
	}
	account, _ = env.store.GetAccount(context.Background(), accountID)
	if !account.Proxy.Enabled || account.Proxy.EndpointCiphertext != firstCiphertext || account.Proxy.UpstreamCiphertext != firstUpstream {
		t.Fatalf("saved credentials were not preserved: %+v", account.Proxy)
	}
	if response := update(map[string]any{"enabled": true, "clear_upstream": true}); response.Code != http.StatusOK {
		t.Fatalf("clear upstream: %d %s", response.Code, response.Body.String())
	}
	account, _ = env.store.GetAccount(context.Background(), accountID)
	if account.Proxy.UpstreamCiphertext != "" || !account.Proxy.Enabled || account.Proxy.EndpointCiphertext != firstCiphertext {
		t.Fatalf("unexpected upstream after clear: %+v", account.Proxy)
	}
	if response := update(map[string]any{"enabled": true, "endpoint": "ftp://example.org:21"}); response.Code != http.StatusBadRequest {
		t.Fatalf("unsupported protocol status: %d %s", response.Code, response.Body.String())
	}

	// The edit API also preserves proxy settings for older clients with no proxy field.
	if response := update(nil); response.Code != http.StatusOK {
		t.Fatalf("update without proxy config: %d %s", response.Code, response.Body.String())
	}
	account, _ = env.store.GetAccount(context.Background(), accountID)
	if account.Proxy.EndpointCiphertext != firstCiphertext || !account.Proxy.Enabled {
		t.Fatal("proxy config changed when omitted")
	}
}

func TestAdminAPIProxyTestRequiresCSRFAndReportsFailedHopWithoutSecrets(t *testing.T) {
	env := newAdminAPITestEnv(t)
	cookie, csrf, _ := env.createSession(t, "proxy-test-admin", "unused-password")
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			t.Errorf("proxy request method: %s", r.Method)
		}
		user, pass, ok := r.BasicAuth()
		if ok || user != "" || pass != "" {
			t.Fatal("proxy authentication leaked as origin Basic auth")
		}
		w.WriteHeader(http.StatusProxyAuthRequired)
	}))
	defer proxy.Close()
	endpoint := "http://proxy-user:proxy-secret@" + strings.TrimPrefix(proxy.URL, "http://")
	body := adminAPITestJSON(t, map[string]any{"proxy": map[string]any{"enabled": true, "endpoint": endpoint}})
	noCSRF := env.request(t, http.MethodPost, "/admin/api/v1/accounts/proxy-test", body, "application/json", []*http.Cookie{cookie}, "")
	if noCSRF.Code == http.StatusOK {
		t.Fatal("proxy test accepted request without CSRF token")
	}
	response := env.request(t, http.MethodPost, "/admin/api/v1/accounts/proxy-test", body, "application/json", []*http.Cookie{cookie}, csrf)
	if response.Code != http.StatusBadGateway || adminAPITestErrorCode(t, response) != "PROXY_CONNECT_FAILED" {
		t.Fatalf("failed proxy response: %d %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "落地代理认证失败") {
		t.Fatalf("missing actionable proxy failure: %s", response.Body.String())
	}
	if strings.Contains(response.Body.String(), "proxy-secret") {
		t.Fatal("proxy response exposed password")
	}
}
