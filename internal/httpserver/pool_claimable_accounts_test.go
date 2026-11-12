package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"icloud-api/internal/domain"
	"icloud-api/internal/store"
)

func callPoolClaimableAccounts(t *testing.T, env *adminAPITestEnv, key string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "http://admin.example.test/admin/api/v1/pool/accounts/available", nil)
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	env.router.ServeHTTP(response, request)
	return response
}

func TestPoolClaimableAccountsRequiresDualAuthAndMatchesClaim(t *testing.T) {
	env := newAdminAPITestEnv(t)
	ctx := context.Background()
	account := adminAPITestCreateAccount(t, env, "claimable-owner@icloud.com")
	aliasIDs := make([]int64, 0, 6)
	for index := 0; index < 6; index++ {
		alias, _ := createV2AliasFixture(t, env, account.ID, "claimable-"+strconv.Itoa(index)+"@icloud.com")
		aliasIDs = append(aliasIDs, alias.ID)
	}
	if err := env.store.EnrollPoolAliases(ctx, aliasIDs); err != nil {
		t.Fatal(err)
	}

	// A legacy member left by an earlier version must not inflate the gateway.
	legacy, err := env.store.CreateAlias(ctx, domain.Alias{
		AccountID:  account.ID,
		Address:    "claimable-legacy@icloud.com",
		APIKeyHash: make([]byte, 32),
		Enabled:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.DB().ExecContext(ctx, `INSERT INTO pool_members(alias_id,state,updated_at) VALUES(?,'available',0)`, legacy.ID); err != nil {
		t.Fatal(err)
	}

	custom, err := env.store.CreateAccount(ctx, domain.Account{
		Name: "custom", Email: "claimable-custom@example.test", MailboxType: domain.MailboxTypeCustom, EmailSuffix: "example.test",
		IMAPHost: "imap.example.test", IMAPPort: 993, IMAPUsername: "claimable-custom@example.test", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	customAlias, _ := createV2AliasFixture(t, env, custom.ID, "claimable-custom-alias@example.test")
	if err := env.store.EnrollPoolAliases(ctx, []int64{customAlias.ID}); err != nil {
		t.Fatal(err)
	}

	disabled := adminAPITestCreateAccount(t, env, "claimable-disabled@icloud.com")
	disabledAlias, _ := createV2AliasFixture(t, env, disabled.ID, "claimable-disabled-alias@icloud.com")
	if err := env.store.EnrollPoolAliases(ctx, []int64{disabledAlias.ID}); err != nil {
		t.Fatal(err)
	}
	disabled.Enabled = false
	if _, err := env.store.UpdateAccount(ctx, disabled); err != nil {
		t.Fatal(err)
	}

	client, key, err := env.store.CreatePoolClient(ctx, "claimable-gateway-project")
	if err != nil {
		t.Fatal(err)
	}
	cookie, _, _ := env.createSession(t, "claimable-gateway-admin", "password")
	if response := callPoolClaimableAccounts(t, env, key, nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("missing administrator session = %d %s", response.Code, response.Body.String())
	}
	if response := callPoolClaimableAccounts(t, env, "invalid", cookie); response.Code != http.StatusUnauthorized {
		t.Fatalf("invalid Pool Key = %d %s", response.Code, response.Body.String())
	}
	response := callPoolClaimableAccounts(t, env, key, cookie)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("gateway = %d cache=%q %s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
	if containsAny(response.Body.String(), "claimable-owner@icloud.com", "claimable-custom@example.test", "api_key", "refresh_token", "imap_password") {
		t.Fatalf("gateway leaked sensitive inventory detail: %s", response.Body.String())
	}
	var payload struct {
		Data []map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data) != 1 || len(payload.Data[0]) != 2 || payload.Data[0]["account_id"] == nil || payload.Data[0]["available"] == nil {
		t.Fatalf("minimal gateway response = %s", response.Body.String())
	}
	var gatewayAccount store.PoolClaimableAccount
	encoded, err := json.Marshal(payload.Data[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &gatewayAccount); err != nil {
		t.Fatal(err)
	}
	if gatewayAccount.AccountID != account.ID || gatewayAccount.Available != 6 {
		t.Fatalf("gateway inventory = %#v", gatewayAccount)
	}

	leases, err := env.store.ClaimPool(ctx, client.ID, store.PoolClaim{
		RequestID: "claimable-gateway-five-0001", AccountID: gatewayAccount.AccountID, Count: 5, TTLSeconds: 600,
	})
	if err != nil || len(leases) != 5 {
		t.Fatalf("claim up to five = leases=%d err=%v", len(leases), err)
	}
	for _, lease := range leases {
		if lease.AliasID == legacy.ID || lease.AliasID == customAlias.ID || lease.AliasID == disabledAlias.ID {
			t.Fatalf("gateway inventory selected ineligible alias: %#v", lease)
		}
	}
}

func containsAny(value string, values ...string) bool {
	for _, candidate := range values {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}
