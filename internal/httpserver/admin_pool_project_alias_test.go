package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"icloud-api/internal/store"
)

func TestAdminPoolProjectMailboxLinksAndExport(t *testing.T) {
	env := newAdminAPITestEnv(t)
	ctx := context.Background()
	account := adminAPITestCreateAccount(t, env, "shared-pool-account@icloud.com")
	alias, _ := createV2AliasFixture(t, env, account.ID, "shared-pool-alias@icloud.com")
	if err := env.store.EnrollPoolAliases(ctx, []int64{alias.ID}); err != nil {
		t.Fatal(err)
	}
	original, _, err := env.store.CreatePoolClient(ctx, "chatgpt")
	if err != nil {
		t.Fatal(err)
	}
	leases, err := env.store.ClaimPool(ctx, original.ID, store.PoolClaim{RequestID: "original-shared-claim", Count: 1, AccountID: account.ID, TTLSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.ActPoolLease(ctx, leases[0].ID, original.ID, "commit", 0); err != nil {
		t.Fatal(err)
	}
	alias, err = env.store.GetAlias(ctx, alias.ID)
	if err != nil {
		t.Fatal(err)
	}
	deepseek, _, err := env.store.CreatePoolClient(ctx, "DeepSeek")
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf, _ := env.createSession(t, "shared-pool-admin", "fixture-password")
	base := fmt.Sprintf("/admin/api/v1/pool/projects/%s/mailboxes", deepseek.ID)
	request := func(method, path, body string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://admin.example.test"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://admin.example.test")
		if authenticated {
			r.AddCookie(cookie)
			r.Header.Set("X-CSRF-Token", csrf)
		}
		w := httptest.NewRecorder()
		env.router.ServeHTTP(w, r)
		return w
	}
	if r := request(http.MethodGet, base+"?account_id=1", "", false); r.Code != 401 {
		t.Fatalf("unauthenticated project link read: %d", r.Code)
	}
	if r := request(http.MethodPost, base, fmt.Sprintf(`{"account_id":%d}`, account.ID), false); r.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated project link write: %d", r.Code)
	}
	withoutCSRF := httptest.NewRequest(http.MethodPost, "http://admin.example.test"+base, strings.NewReader(fmt.Sprintf(`{"account_id":%d}`, account.ID)))
	withoutCSRF.Header.Set("Content-Type", "application/json")
	withoutCSRF.Header.Set("Origin", "http://admin.example.test")
	withoutCSRF.AddCookie(cookie)
	csrfRejected := httptest.NewRecorder()
	env.router.ServeHTTP(csrfRejected, withoutCSRF)
	if csrfRejected.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF accepted: %d", csrfRejected.Code)
	}
	r := request(http.MethodPost, base, fmt.Sprintf(`{"account_id":%d}`, account.ID), true)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"added":1`) {
		t.Fatalf("link: %d %s", r.Code, r.Body.String())
	}
	r = request(http.MethodGet, base+fmt.Sprintf("?account_id=%d", account.ID), "", true)
	if r.Code != 200 {
		t.Fatalf("read: %d %s", r.Code, r.Body.String())
	}
	var payload struct {
		Data struct {
			Items []struct {
				Address    string `json:"address"`
				Source     string `json:"source"`
				OTPURLPath string `json:"otp_url_path"`
			} `json:"items"`
			Total int `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Total != 1 || len(payload.Data.Items) != 1 ||
		payload.Data.Items[0].Address != alias.Address || payload.Data.Items[0].Source != "shared" ||
		!strings.HasPrefix(payload.Data.Items[0].OTPURLPath, "/api/v1/otp?token=") {
		t.Fatalf("bad shared mailbox response: %+v", payload.Data)
	}
	if strings.Contains(r.Body.String(), `"api_key"`) || strings.Contains(r.Body.String(), `"imap_password"`) {
		t.Fatal("shared mailbox list exposed additional credentials")
	}
	parsed, err := url.Parse(payload.Data.Items[0].OTPURLPath)
	if err != nil || !env.cipher.VerifyOTPToken(parsed.Query().Get("token"), alias.ID, alias.APIKeyHash) {
		t.Fatal("shared mailbox did not return a usable OTP token", err)
	}
	originalBase := fmt.Sprintf("/admin/api/v1/pool/projects/%s/mailboxes", original.ID)
	r = request(http.MethodGet, originalBase+fmt.Sprintf("?account_id=%d", account.ID), "", true)
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"source":"claimed"`) {
		t.Fatalf("original project claim missing: %d %s", r.Code, r.Body.String())
	}
	r = request(http.MethodDelete, fmt.Sprintf("%s/%d", originalBase, alias.ID), "", true)
	if r.Code != http.StatusNotFound {
		t.Fatalf("claimed lease could be unlinked: %d", r.Code)
	}
	r = request(http.MethodDelete, fmt.Sprintf("%s/%d", base, alias.ID), "", true)
	if r.Code != 200 {
		t.Fatalf("unlink: %d %s", r.Code, r.Body.String())
	}
	r = request(http.MethodGet, base, "", true)
	if !strings.Contains(r.Body.String(), `"total":0`) {
		t.Fatalf("unlink left shared membership: %s", r.Body.String())
	}
	lease, err := env.store.GetPoolLease(ctx, leases[0].ID, original.ID)
	if err != nil || lease.State != "used" {
		t.Fatal("original lease affected by sharing", err)
	}
}
