package httpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"icloud-api/internal/store"
)

func TestPoolBatchCommitAPI(t *testing.T) {
	env := newAdminAPITestEnv(t)
	ctx := context.Background()
	account := adminAPITestCreateAccount(t, env, "batch-owner@icloud.test")
	alias, _ := createV2AliasFixture(t, env, account.ID, "batch-alias@icloud.test")
	if err := env.store.EnrollPoolAliases(ctx, []int64{alias.ID}); err != nil {
		t.Fatal(err)
	}
	client, key, err := env.store.CreatePoolClient(ctx, "batch-fixture")
	if err != nil {
		t.Fatal(err)
	}
	leases, err := env.store.ClaimPool(ctx, client.ID, store.PoolClaim{RequestID: "batch-api-fixture", Count: 1, TTLSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	router, err := env.server.Router()
	if err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{"Authorization": "Bearer " + key, "Content-Type": "application/json"}
	capabilities := serveV2Request(router, "GET", "/api/v1/pool/capabilities", "", headers)
	if capabilities.Code != 200 || !strings.Contains(capabilities.Body.String(), `"batch_commit_max":50`) {
		t.Fatal(capabilities.Code, capabilities.Body.String())
	}
	if r := serveV2Request(router, "POST", "/api/v1/pool/leases/commit", `{"lease_ids":[]}`, headers); r.Code != 400 {
		t.Fatal(r.Code)
	}
	body, _ := json.Marshal(map[string]any{"lease_ids": []string{leases[0].ID}})
	for attempt := 0; attempt < 2; attempt++ {
		r := serveV2Request(router, "POST", "/api/v1/pool/leases/commit", string(body), headers)
		if r.Code != 200 || !strings.Contains(r.Body.String(), `"state":"used"`) || r.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(r.Code, r.Body.String())
		}
		if strings.Contains(r.Body.String(), "imap_password") || strings.Contains(r.Body.String(), "api_key") {
			t.Fatal("batch commit leaked mailbox credentials")
		}
	}
	if r := serveV2Request(router, "POST", "/api/v1/pool/leases/commit", string(body), nil); r.Code != 401 {
		t.Fatal(r.Code)
	}
}
