package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"icloud-api/internal/domain"
)

func TestPoolAPIIsolationCredentialsAndFreshCode(t *testing.T) {
	env := newAdminAPITestEnv(t)
	env.server.sync = nil
	now := time.Now().UTC()
	env.server.now = func() time.Time { return now }
	ctx := context.Background()
	if err := env.store.ConfigureMailArchive(t.TempDir(), 1<<20); err != nil {
		t.Fatal(err)
	}
	account := adminAPITestCreateAccount(t, env, "pool-api-owner@icloud.com")
	alias, _ := createV2AliasFixture(t, env, account.ID, "pool-api-alias@icloud.com")
	if err := env.store.EnrollPoolAliases(ctx, []int64{alias.ID}); err != nil {
		t.Fatal(err)
	}
	client, key, err := env.store.CreatePoolClient(ctx, "api-project")
	if err != nil {
		t.Fatal(err)
	}
	_, otherKey, err := env.store.CreatePoolClient(ctx, "other-project")
	if err != nil {
		t.Fatal(err)
	}
	router, err := env.server.Router()
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body, key string) *httptest.ResponseRecorder {
		return serveV2Request(router, method, path, body, map[string]string{"Authorization": "Bearer " + key, "Content-Type": "application/json"})
	}
	body := `{"request_id":"api-lifecycle-0001","count":1,"ttl_seconds":600}`
	bad := call("POST", "/api/v1/pool/claim", body, "bad")
	if bad.Code != 401 {
		t.Fatal(bad.Code)
	}
	response := call("POST", "/api/v1/pool/claim", body, key)
	if response.Code != 200 {
		t.Fatalf("claim %d: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("credential response cacheable")
	}
	var payload struct {
		Data struct {
			Leases []struct {
				Lease struct {
					ID        string    `json:"id"`
					CreatedAt time.Time `json:"created_at"`
				}
				Mailbox struct {
					APIKey       string `json:"api_key"`
					IMAPPassword string `json:"imap_password"`
					ClientID     string `json:"client_id"`
					RefreshToken string `json:"refresh_token"`
				}
			}
		}
	}
	if err = json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data.Leases) != 1 {
		t.Fatal("missing lease")
	}
	lease := payload.Data.Leases[0]
	if lease.Mailbox.APIKey == "" || lease.Mailbox.IMAPPassword == "" || lease.Mailbox.ClientID == "" || lease.Mailbox.RefreshToken == "" {
		t.Fatal("missing credential bundle")
	}
	path := "/api/v1/pool/leases/" + lease.Lease.ID
	for _, suffix := range []string{"", "/code"} {
		if r := call("GET", path+suffix, "", otherKey); r.Code != 404 {
			t.Fatal("cross-project read", r.Code)
		}
	}
	if r := call("POST", path+"/release", "", otherKey); r.Code != 404 {
		t.Fatal("cross-project release", r.Code)
	}
	if r := call("GET", path+"/code", "", key); !strings.Contains(r.Body.String(), `"no_code"`) {
		t.Fatal(r.Body.String())
	}
	if r := call("POST", "/api/v1/pool/claim", body, key); r.Body.String() != response.Body.String() {
		t.Fatal("retry did not return same credentials")
	}
	before := lease.Lease.CreatedAt.Add(-time.Hour)
	after := lease.Lease.CreatedAt.Add(time.Second)
	account, err = env.store.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	alias, err = env.store.GetAlias(ctx, alias.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = env.store.ApplyMailboxSync(ctx, account.ID, account.UpdatedAt, []domain.Alias{alias}, domain.MailboxSyncResult{
		ArchivedMessages: []domain.ArchivedMessage{
			{AccountID: account.ID, UIDValidity: 99, UID: 1, InternalDate: before, Subject: "old", RawMIME: []byte("Subject: old\r\n\r\n111111"), OTP: "111111", AliasIDs: []int64{alias.ID}},
			{AccountID: account.ID, UIDValidity: 99, UID: 2, InternalDate: after, Subject: "new", RawMIME: []byte("Subject: new\r\n\r\n222222"), OTP: "222222", AliasIDs: []int64{alias.ID}},
		}, State: domain.IMAPSyncState{AccountID: account.ID, UIDValidity: 99, LastUID: 2, UpdatedAt: after}, Reset: true,
	}, after); err != nil {
		t.Fatal(err)
	}
	now = now.Add(3 * time.Second)
	r := call("GET", path+"/code", "", key)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"otp":"222222"`) {
		t.Fatalf("fresh code: %s", r.Body.String())
	}
	now = now.Add(3 * time.Second)
	r = call("GET", path+"/code?after="+url.QueryEscape(after.Add(time.Second).Format(time.RFC3339Nano)), "", key)
	if !strings.Contains(r.Body.String(), `"no_code"`) {
		t.Fatal("after ignored")
	}
	if r = call("GET", path+"/code?after=invalid", "", key); r.Code != 400 {
		t.Fatal("invalid timestamp accepted")
	}
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {lease.Mailbox.ClientID}, "refresh_token": {lease.Mailbox.RefreshToken}}.Encode()
	token := serveV2Request(router, "POST", "/oauth2/v2.0/token", form, map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if token.Code != 200 {
		t.Fatalf("OAuth issue: %d %s", token.Code, token.Body.String())
	}
	if r = call("POST", path+"/release", "", key); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if r = call("GET", "/api/v1/otp", "", lease.Mailbox.APIKey); r.Code != 401 {
		t.Fatal("old alias key usable")
	}
	token = serveV2Request(router, "POST", "/oauth2/v2.0/token", form, map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if token.Code != 401 {
		t.Fatal("old refresh token usable")
	}
	if r = call("GET", path, "", key); r.Code != 200 || strings.Contains(r.Body.String(), `"mailbox"`) {
		t.Fatal("released lease exposes credentials")
	}
	if r = call("POST", "/api/v1/pool/claim", body, key); strings.Contains(r.Body.String(), `"api_key"`) {
		t.Fatal("closed idempotency retry exposes replacement credentials")
	}
	newKey, err := env.store.UpdatePoolClient(ctx, client.ID, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if r = call("GET", "/api/v1/pool/leases", "", key); r.Code != 401 {
		t.Fatal("old project key usable")
	}
	if r = call("GET", "/api/v1/pool/leases", "", newKey); r.Code != 200 {
		t.Fatal("rotated project key failed")
	}
}

func TestPoolAdminRequiresSessionAndCSRF(t *testing.T) {
	env := newAdminAPITestEnv(t)
	request := func(cookieValue, csrf string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://admin.example.test/admin/api/v1/pool/clients", strings.NewReader(`{"project":"admin-created"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://admin.example.test")
		if cookieValue != "" {
			r.Header.Set("Cookie", fmt.Sprintf("%s=%s", sessionCookie, cookieValue))
		}
		r.Header.Set("X-CSRF-Token", csrf)
		w := httptest.NewRecorder()
		env.router.ServeHTTP(w, r)
		return w
	}
	if r := request("", ""); r.Code != 401 {
		t.Fatal("unauthenticated admin accepted", r.Code)
	}
	cookie, csrf, _ := env.createSession(t, "pool-admin", "fixture-password")
	if r := request(cookie.Value, ""); r.Code != 403 {
		t.Fatal("missing CSRF accepted", r.Code)
	}
	if r := request(cookie.Value, csrf); r.Code != 201 {
		t.Fatalf("admin creation %d %s", r.Code, r.Body.String())
	}
}
