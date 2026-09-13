package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type poolDemandLeaseFixture struct {
	id        string
	key       string
	clientID  string
	accountID int64
	aliasID   int64
}

func newPoolDemandLease(t *testing.T, env *adminAPITestEnv) poolDemandLeaseFixture {
	t.Helper()
	account := adminAPITestCreateAccount(t, env, "pool-demand-owner@example.test")
	alias, _ := createV2AliasFixture(t, env, account.ID, "pool-demand-alias@example.test")
	if err := env.store.EnrollPoolAliases(context.Background(), []int64{alias.ID}); err != nil {
		t.Fatal(err)
	}
	client, key, err := env.store.CreatePoolClient(context.Background(), "pool-demand-project")
	if err != nil {
		t.Fatal(err)
	}
	router, err := env.server.Router()
	if err != nil {
		t.Fatal(err)
	}
	req := serveV2Request(router, http.MethodPost, "/api/v1/pool/claim", `{"request_id":"demand-lease-1","count":1}`, map[string]string{"Authorization": "Bearer " + key, "Content-Type": "application/json"})
	if req.Code != http.StatusOK {
		t.Fatalf("claim status=%d body=%s", req.Code, req.Body.String())
	}
	var body struct {
		Data struct {
			Leases []struct {
				Lease struct {
					ID string `json:"id"`
				}
			} `json:"leases"`
		} `json:"data"`
	}
	if err := json.Unmarshal(req.Body.Bytes(), &body); err != nil || len(body.Data.Leases) != 1 {
		t.Fatalf("claim response: %v %s", err, req.Body.String())
	}
	return poolDemandLeaseFixture{id: body.Data.Leases[0].Lease.ID, key: key, clientID: client.ID, accountID: account.ID, aliasID: alias.ID}
}

func TestPoolLeaseCodeDemandCallbackAndRejectsClosedOrDisabled(t *testing.T) {
	for _, tc := range []struct {
		name  string
		close func(*adminAPITestEnv, poolDemandLeaseFixture)
	}{
		{"valid", nil},
		{"released", func(env *adminAPITestEnv, f poolDemandLeaseFixture) {
			if _, err := env.store.ActPoolLease(context.Background(), f.id, f.clientID, "release", 0); err != nil {
				t.Fatal(err)
			}
		}},
		{"disabled", func(env *adminAPITestEnv, f poolDemandLeaseFixture) {
			account, err := env.store.GetAccount(context.Background(), f.accountID)
			if err != nil {
				t.Fatal(err)
			}
			account.Enabled = false
			if _, err = env.store.UpdateAccount(context.Background(), account); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newAdminAPITestEnv(t)
			f := newPoolDemandLease(t, env)
			if tc.close != nil {
				tc.close(env, f)
			}
			called := make(chan struct{}, 1)
			env.server.SetAliasDemandSync(func(_ context.Context, id int64) error {
				if id != f.aliasID {
					t.Errorf("callback id=%d, want %d", id, f.aliasID)
				}
				called <- struct{}{}
				return nil
			})
			router, _ := env.server.Router()
			r := serveV2Request(router, http.MethodGet, "/api/v1/pool/leases/"+f.id+"/code", "", map[string]string{"Authorization": "Bearer " + f.key})
			if tc.name == "valid" {
				if r.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", r.Code, r.Body.String())
				}
				select {
				case <-called:
				case <-time.After(time.Second):
					t.Fatal("callback not invoked")
				}
				bad := serveV2Request(router, http.MethodGet, "/api/v1/pool/leases/"+f.id+"/code", "", map[string]string{"Authorization": "Bearer pool-invalid"})
				if bad.Code == http.StatusOK {
					t.Fatal("invalid pool key accepted")
				}
				select {
				case <-called:
					t.Fatal("invalid key invoked callback")
				default:
				}
			} else {
				if r.Code != http.StatusConflict {
					t.Fatalf("closed lease status=%d body=%s", r.Code, r.Body.String())
				}
				select {
				case <-called:
					t.Fatal("closed lease invoked callback")
				default:
				}
			}
		})
	}
}

func TestPoolLeaseCodeDemandDoesNotHoldPoolLockDuringCallback(t *testing.T) {
	env := newAdminAPITestEnv(t)
	f := newPoolDemandLease(t, env)
	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	env.server.SetAliasDemandSync(func(context.Context, int64) error { close(entered); <-release; return nil })
	router, _ := env.server.Router()
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		result <- serveV2Request(router, http.MethodGet, "/api/v1/pool/leases/"+f.id+"/code", "", map[string]string{"Authorization": "Bearer " + f.key})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("callback did not start")
	}
	other := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		other <- serveV2Request(router, http.MethodGet, "/api/v1/pool/leases", "", map[string]string{"Authorization": "Bearer " + f.key})
	}()
	select {
	case r := <-other:
		if r.Code != http.StatusOK {
			t.Fatalf("leases status=%d", r.Code)
		}
	case <-time.After(time.Second):
		t.Fatal("pool lock held during callback")
	}
	close(release)
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("code request did not finish")
	}
}

func TestPoolLeaseCodeDemandRevalidatesRotationAndRelease(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*adminAPITestEnv, poolDemandLeaseFixture)
	}{
		{"key_rotation", func(env *adminAPITestEnv, f poolDemandLeaseFixture) {
			if _, err := env.store.UpdatePoolClient(context.Background(), f.clientID, true, true); err != nil {
				t.Fatal(err)
			}
		}},
		{"lease_release", func(env *adminAPITestEnv, f poolDemandLeaseFixture) {
			if _, err := env.store.ActPoolLease(context.Background(), f.id, f.clientID, "release", 0); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newAdminAPITestEnv(t)
			f := newPoolDemandLease(t, env)
			entered, release := make(chan struct{}), make(chan struct{})
			t.Cleanup(func() {
				select {
				case <-release:
				default:
					close(release)
				}
			})
			env.server.SetAliasDemandSync(func(_ context.Context, id int64) error {
				if id != f.aliasID {
					t.Errorf("callback id=%d, want %d", id, f.aliasID)
				}
				close(entered)
				<-release
				return nil
			})
			router, _ := env.server.Router()
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				result <- serveV2Request(router, http.MethodGet, "/api/v1/pool/leases/"+f.id+"/code", "", map[string]string{"Authorization": "Bearer " + f.key})
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("callback did not start")
			}
			tc.mutate(env, f)
			close(release)
			select {
			case response := <-result:
				want := http.StatusConflict
				if tc.name == "key_rotation" {
					want = http.StatusUnauthorized
				}
				if response.Code != want {
					t.Fatalf("stale %s status=%d want=%d", tc.name, response.Code, want)
				}
			case <-time.After(time.Second):
				t.Fatal("code request did not finish")
			}
		})
	}
}
