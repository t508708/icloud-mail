package httpserver

import (
	"net/http"
	"strings"
	"testing"
)

func TestPoolPickupDoesNotSpendMutationIPBudget(t *testing.T) {
	env := newAdminAPITestEnv(t)
	f := newPoolDemandLease(t, env)
	env.server.sync = nil
	// Exhaust every IP's mutation allowance without issuing network requests.
	env.server.externalAPILimiter.limit = 0
	router, err := env.server.Router()
	if err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{"Authorization": "Bearer " + f.key}
	response := serveV2Request(router, http.MethodGet, "/api/v1/pool/leases/"+f.id+"/code", "", headers)
	if response.Code != 200 {
		t.Fatalf("code used mutation budget: %d %s", response.Code, response.Body.String())
	}
	response = serveV2Request(router, http.MethodGet, "/api/v1/pool/leases/"+f.id+"/code", "", headers)
	if response.Code != 429 || response.Header().Get("Retry-After") != "2" {
		t.Fatal("code lost alias cooldown")
	}
	response = serveV2Request(router, http.MethodGet, "/api/v1/pool/leases/"+f.id+"/code", "", nil)
	if response.Code != 401 || !strings.Contains(response.Body.String(), "INVALID_POOL_KEY") {
		t.Fatal("code bypassed Pool authentication")
	}
	response = serveV2Request(router, http.MethodGet, "/api/v1/pool/leases", "", headers)
	if response.Code != 429 || response.Header().Get("Retry-After") != "60" {
		t.Fatal("management IP budget changed")
	}
}
