package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPoolLeaseCodeWaitSecondsValidation(t *testing.T) {
	for _, raw := range []string{"", "-1", "16", "1.5", "1&wait_seconds=2", "999999999999999999999"} {
		t.Run(raw, func(t *testing.T) {
			env := newAdminAPITestEnv(t)
			f := newPoolDemandLease(t, env)
			called := false
			env.server.SetSharedMailboxReceiver(func(context.Context, int64) error { called = true; return nil })
			router, _ := env.server.Router()
			path := "/api/v1/pool/leases/" + f.id + "/code?wait_seconds=" + raw
			r := serveV2Request(router, http.MethodGet, path, "", map[string]string{"Authorization": "Bearer " + f.key})
			if r.Code != http.StatusBadRequest || called {
				t.Fatalf("invalid wait %q status=%d callback=%v", raw, r.Code, called)
			}
		})
	}
}

func TestPoolLeaseCodeWaitRevalidatesWithoutGlobalLocks(t *testing.T) {
	for _, mutation := range []string{"key", "lease", "account"} {
		t.Run(mutation, func(t *testing.T) {
			env := newAdminAPITestEnv(t)
			f := newPoolDemandLease(t, env)
			entered, changed := make(chan struct{}), make(chan struct{})
			var once sync.Once
			env.server.SetSharedMailboxReceiver(func(context.Context, int64) error { return nil })
			env.server.SetMailboxChanges(func(int64) (<-chan struct{}, time.Duration) {
				once.Do(func() { close(entered) })
				return changed, time.Hour
			})
			router, _ := env.server.Router()
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				result <- serveV2Request(router, http.MethodGet, "/api/v1/pool/leases/"+f.id+"/code?wait_seconds=1", "", map[string]string{"Authorization": "Bearer " + f.key})
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("no waiting reader")
			}
			// Taking these locks must finish while the request remains pending.
			mutated := make(chan error, 1)
			go func() {
				env.server.credentialRotationMu.Lock()
				defer env.server.credentialRotationMu.Unlock()
				env.server.poolMu.Lock()
				defer env.server.poolMu.Unlock()
				var err error
				switch mutation {
				case "key":
					_, err = env.store.UpdatePoolClient(context.Background(), f.clientID, true, true)
				case "lease":
					_, err = env.store.ActPoolLease(context.Background(), f.id, f.clientID, "release", 0)
				case "account":
					account, loadErr := env.store.GetAccount(context.Background(), f.accountID)
					err = loadErr
					if err == nil {
						account.Enabled = false
						_, err = env.store.UpdateAccount(context.Background(), account)
					}
				}
				mutated <- err
			}()
			select {
			case err := <-mutated:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatal("waiting read held a global lock")
			}
			close(changed)
			select {
			case r := <-result:
				want := http.StatusConflict
				if mutation == "key" {
					want = http.StatusUnauthorized
				}
				if r.Code != want || strings.Contains(r.Body.String(), `"otp"`) {
					t.Fatalf("status=%d body=%s", r.Code, r.Body.String())
				}
			case <-time.After(2 * time.Second):
				t.Fatal("revoked waiting request did not finish")
			}
		})
	}
}

func TestPoolLeaseCodeWaitTimeoutAndCancellationReleaseAdmission(t *testing.T) {
	for _, cancelRequest := range []bool{false, true} {
		t.Run(map[bool]string{false: "deadline", true: "cancel"}[cancelRequest], func(t *testing.T) {
			env := newAdminAPITestEnv(t)
			f := newPoolDemandLease(t, env)
			entered := make(chan struct{})
			var once sync.Once
			env.server.SetSharedMailboxReceiver(func(context.Context, int64) error { return nil })
			changed := make(chan struct{})
			env.server.SetMailboxChanges(func(int64) (<-chan struct{}, time.Duration) {
				once.Do(func() { close(entered) })
				return changed, time.Hour
			})
			router, _ := env.server.Router()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/pool/leases/"+f.id+"/code?wait_seconds=1", nil).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer "+f.key)
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); router.ServeHTTP(response, req) }()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("wait not subscribed")
			}
			duplicate := serveV2Request(router, http.MethodGet, req.URL.String(), "", map[string]string{"Authorization": "Bearer " + f.key})
			if duplicate.Code != http.StatusTooManyRequests {
				t.Fatalf("duplicate status=%d", duplicate.Code)
			}
			if cancelRequest {
				cancel()
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("bounded reader did not finish")
			}
			if !cancelRequest && (response.Code != 200 || !strings.Contains(response.Body.String(), `"no_code"`)) {
				t.Fatalf("timeout=%d %s", response.Code, response.Body.String())
			}
			env.server.aliasDemandRateMu.Lock()
			state := env.server.aliasDemandRate[f.aliasID]
			active := env.server.accountPickupActive[f.accountID]
			env.server.aliasDemandRateMu.Unlock()
			if state.inFlight || active != 0 || state.nextAt.IsZero() {
				t.Fatal("request leaked its admission slot or cooldown")
			}
		})
	}
}

func TestPoolLeaseCodeWaitZeroDoesNotRequireMailboxChanges(t *testing.T) {
	env := newAdminAPITestEnv(t)
	f := newPoolDemandLease(t, env)
	env.server.SetSharedMailboxReceiver(func(context.Context, int64) error { return nil })
	called := false
	env.server.SetMailboxChanges(func(int64) (<-chan struct{}, time.Duration) { called = true; return nil, 0 })
	router, _ := env.server.Router()
	r := serveV2Request(router, http.MethodGet, "/api/v1/pool/leases/"+f.id+"/code?wait_seconds=0", "", map[string]string{"Authorization": "Bearer " + f.key})
	if r.Code != http.StatusOK || called {
		t.Fatalf("zero wait status=%d changes=%v", r.Code, called)
	}
}
