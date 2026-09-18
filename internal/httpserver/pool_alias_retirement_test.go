package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"icloud-api/internal/hmesync"
	"icloud-api/internal/store"
)

func startTestPoolAliasRetirementJobs(t *testing.T, env *adminAPITestEnv) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	if err := env.server.StartPoolAliasRetirementJobs(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		env.server.RunPoolAliasRetirementJobs()
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("pool alias retirement workers did not stop")
		}
	})
}

func callPoolAliasRetirement(t *testing.T, env *adminAPITestEnv, method, path, key string, body any, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	var encoded []byte
	if body != nil {
		encoded = adminAPITestJSON(t, body)
	}
	request := httptest.NewRequest(method, "http://admin.example.test"+path, nil)
	if body != nil {
		request = httptest.NewRequest(method, "http://admin.example.test"+path, bytes.NewReader(encoded))
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Origin", "http://admin.example.test")
	if cookie != nil {
		request.AddCookie(cookie)
	}
	if csrf != "" {
		request.Header.Set(adminAPICSRFHeader, csrf)
	}
	response := httptest.NewRecorder()
	env.router.ServeHTTP(response, request)
	return response
}

func poolAliasRetirementUsedLease(t *testing.T, env *adminAPITestEnv) (store.PoolClient, string, store.PoolLease) {
	t.Helper()
	ctx := context.Background()
	account := adminAPITestCreateAccount(t, env, "pool-retirement-owner@example.test")
	alias, _ := createV2AliasFixture(t, env, account.ID, "pool-retirement-alias@example.test")
	if err := env.store.EnrollPoolAliases(ctx, []int64{alias.ID}); err != nil {
		t.Fatal(err)
	}
	client, key, err := env.store.CreatePoolClient(ctx, "pool-retirement-project")
	if err != nil {
		t.Fatal(err)
	}
	leases, err := env.store.ClaimPool(ctx, client.ID, store.PoolClaim{RequestID: "http-retirement-used-0001", Count: 1, TTLSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := env.store.ActPoolLease(ctx, leases[0].ID, client.ID, "commit", 0)
	if err != nil {
		t.Fatal(err)
	}
	return client, key, lease
}

func waitPoolAliasRetirementJob(t *testing.T, env *adminAPITestEnv, operationID, clientID string) store.PoolAliasRetirementJob {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		job, err := env.store.GetPoolAliasRetirementJob(context.Background(), operationID, clientID)
		if err == nil && job.Status != store.PoolAliasRetirementJobQueued && job.Status != store.PoolAliasRetirementJobRunning {
			return job
		}
		select {
		case <-deadline.C:
			t.Fatal("pool retirement job did not finish")
		case <-ticker.C:
		}
	}
}

func TestPoolAliasRetirementRequiresDualAuthAndIsProjectIsolated(t *testing.T) {
	env := newAdminAPITestEnv(t)
	startTestPoolAliasRetirementJobs(t, env)
	client, key, lease := poolAliasRetirementUsedLease(t, env)
	cookie, csrf, _ := env.createSession(t, "pool-retirement-admin", "password")
	env.server.SetHMESyncService(&fakeHMESyncService{
		getSession: func(context.Context, int64) (hmesync.SessionInfo, error) {
			return hmesync.SessionInfo{Status: hmesync.StatusAuthenticated}, nil
		},
	})
	path := "/admin/api/v1/pool/aliases/batch"
	body := map[string]any{"operation_id": "pool-retirement-http-auth-0001", "items": []map[string]any{{"alias_id": lease.AliasID, "lease_id": lease.ID}}}
	if response := callPoolAliasRetirement(t, env, http.MethodPost, path, key, body, nil, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("missing admin auth = %d", response.Code)
	}
	if response := callPoolAliasRetirement(t, env, http.MethodPost, path, "invalid", body, cookie, csrf); response.Code != http.StatusUnauthorized {
		t.Fatalf("missing pool key = %d", response.Code)
	}
	if response := callPoolAliasRetirement(t, env, http.MethodPost, path, key, body, cookie, ""); response.Code != http.StatusForbidden || adminAPITestErrorCode(t, response) != "CSRF_INVALID" {
		t.Fatalf("missing csrf = %d %s", response.Code, response.Body.String())
	}
	other, otherKey, err := env.store.CreatePoolClient(context.Background(), "pool-retirement-other")
	if err != nil {
		t.Fatal(err)
	}
	_ = other
	if response := callPoolAliasRetirement(t, env, http.MethodPost, path, otherKey, body, cookie, csrf); response.Code != http.StatusConflict || adminAPITestErrorCode(t, response) != "LEASE_PROJECT_MISMATCH" {
		t.Fatalf("cross project = %d %s", response.Code, response.Body.String())
	}
	if _, accepted, err := env.store.StartPoolAliasRetirement(context.Background(), client.ID, "pool-retirement-http-status-0001", "fixture", []store.PoolAliasRetirementItemInput{{AliasID: lease.AliasID, LeaseID: lease.ID}}); err != nil || !accepted {
		t.Fatalf("create status fixture accepted=%v err=%v", accepted, err)
	}
	statusPath := "/admin/api/v1/pool/aliases/batch/jobs/pool-retirement-http-status-0001"
	if response := callPoolAliasRetirement(t, env, http.MethodGet, statusPath, otherKey, nil, cookie, ""); response.Code != http.StatusNotFound {
		t.Fatalf("cross project status = %d %s", response.Code, response.Body.String())
	}
	if response := callPoolAliasRetirement(t, env, http.MethodGet, statusPath, key, nil, cookie, ""); response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("own status = %d cache=%q %s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
	if client.ID == "" {
		t.Fatal("fixture client missing")
	}
}

func TestPoolAliasRetirementRejectsExpiredAppleSessionWithoutMutatingLease(t *testing.T) {
	env := newAdminAPITestEnv(t)
	startTestPoolAliasRetirementJobs(t, env)
	client, key, lease := poolAliasRetirementUsedLease(t, env)
	cookie, csrf, _ := env.createSession(t, "pool-retirement-expired-admin", "password")
	env.server.SetHMESyncService(&fakeHMESyncService{
		getSession: func(context.Context, int64) (hmesync.SessionInfo, error) {
			return hmesync.SessionInfo{Status: hmesync.StatusExpired}, nil
		},
	})
	body := map[string]any{"operation_id": "pool-retirement-expired-session-0001", "items": []map[string]any{{"alias_id": lease.AliasID, "lease_id": lease.ID}}}
	response := callPoolAliasRetirement(t, env, http.MethodPost, "/admin/api/v1/pool/aliases/batch", key, body, cookie, csrf)
	if response.Code != http.StatusConflict || adminAPITestErrorCode(t, response) != hmesync.CodeSessionExpired {
		t.Fatalf("expired session = %d %s", response.Code, response.Body.String())
	}
	current, err := env.store.GetPoolLease(context.Background(), lease.ID, client.ID)
	if err != nil || current.State != "used" {
		t.Fatalf("expired preflight mutated lease: %#v err=%v", current, err)
	}
}

func TestPoolAliasRetirementJobSuccessFailureAndIdempotency(t *testing.T) {
	env := newAdminAPITestEnv(t)
	startTestPoolAliasRetirementJobs(t, env)
	client, key, lease := poolAliasRetirementUsedLease(t, env)
	cookie, csrf, _ := env.createSession(t, "pool-retirement-job-admin", "password")
	var deleteCalls int
	env.server.SetHMESyncService(&fakeHMESyncService{
		getSession: func(context.Context, int64) (hmesync.SessionInfo, error) {
			return hmesync.SessionInfo{Status: hmesync.StatusAuthenticated}, nil
		},
		deleteAliases: func(_ context.Context, ids []int64) ([]hmesync.AliasDeletionOutcome, error) {
			deleteCalls++
			return []hmesync.AliasDeletionOutcome{{AliasID: ids[0]}}, nil
		},
	})
	operationID := "pool-retirement-http-success-0001"
	body := map[string]any{"operation_id": operationID, "items": []map[string]any{{"alias_id": lease.AliasID, "lease_id": lease.ID}}}
	response := callPoolAliasRetirement(t, env, http.MethodPost, "/admin/api/v1/pool/aliases/batch", key, body, cookie, csrf)
	if response.Code != http.StatusAccepted {
		t.Fatalf("accept = %d %s", response.Code, response.Body.String())
	}
	job := waitPoolAliasRetirementJob(t, env, operationID, client.ID)
	if job.Status != store.PoolAliasRetirementJobCompleted || job.Items[0].State != "retired" {
		t.Fatalf("success job = %#v", job)
	}
	if deleteCalls != 1 {
		t.Fatalf("delete calls = %d", deleteCalls)
	}
	replay := callPoolAliasRetirement(t, env, http.MethodPost, "/admin/api/v1/pool/aliases/batch", key, body, cookie, csrf)
	if replay.Code != http.StatusAccepted || deleteCalls != 1 {
		t.Fatalf("replay = %d calls=%d %s", replay.Code, deleteCalls, replay.Body.String())
	}
	conflict := callPoolAliasRetirement(t, env, http.MethodPost, "/admin/api/v1/pool/aliases/batch", key, map[string]any{"operation_id": operationID, "items": []map[string]any{}}, cookie, csrf)
	if conflict.Code != http.StatusConflict || adminAPITestErrorCode(t, conflict) != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("idempotency conflict = %d %s", conflict.Code, conflict.Body.String())
	}
	get := callPoolAliasRetirement(t, env, http.MethodGet, "/admin/api/v1/pool/aliases/batch/jobs/"+operationID, key, nil, cookie, "")
	if get.Code != http.StatusOK || strings.Contains(get.Body.String(), "api_key") || strings.Contains(get.Body.String(), "refresh_token") {
		t.Fatalf("status response = %d %s", get.Code, get.Body.String())
	}
	var statusBody map[string]any
	if err := json.Unmarshal(get.Body.Bytes(), &statusBody); err != nil {
		t.Fatal(err)
	}
}

func TestPoolAliasRetirementExplicitFailureRestoresAndUnknownStaysReview(t *testing.T) {
	for name, deleteErr := range map[string]error{
		"explicit":     errors.New("fixture Apple explicit failure"),
		"unknown":      hmesync.ErrUpstream,
		"rate_limited": hmesync.ErrRateLimited,
	} {
		t.Run(name, func(t *testing.T) {
			env := newAdminAPITestEnv(t)
			startTestPoolAliasRetirementJobs(t, env)
			client, key, lease := poolAliasRetirementUsedLease(t, env)
			cookie, csrf, _ := env.createSession(t, "pool-retirement-"+name, "password")
			env.server.SetHMESyncService(&fakeHMESyncService{
				getSession: func(context.Context, int64) (hmesync.SessionInfo, error) {
					return hmesync.SessionInfo{Status: hmesync.StatusAuthenticated}, nil
				},
				deleteAliases: func(_ context.Context, ids []int64) ([]hmesync.AliasDeletionOutcome, error) {
					return []hmesync.AliasDeletionOutcome{{AliasID: ids[0], Err: deleteErr}}, nil
				},
			})
			operationID := "pool-retirement-http-" + name + "-0001"
			body := map[string]any{"operation_id": operationID, "items": []map[string]any{{"alias_id": lease.AliasID, "lease_id": lease.ID}}}
			response := callPoolAliasRetirement(t, env, http.MethodPost, "/admin/api/v1/pool/aliases/batch", key, body, cookie, csrf)
			if response.Code != http.StatusAccepted {
				t.Fatalf("accept = %d %s", response.Code, response.Body.String())
			}
			job := waitPoolAliasRetirementJob(t, env, operationID, client.ID)
			current, err := env.store.GetPoolLease(context.Background(), lease.ID, client.ID)
			if err != nil {
				t.Fatal(err)
			}
			if name == "explicit" {
				if job.Status != store.PoolAliasRetirementJobCompleted || job.Items[0].State != "used" || current.State != "used" {
					t.Fatalf("explicit failure = %#v lease=%#v", job, current)
				}
			} else if job.Status != store.PoolAliasRetirementJobReview || job.Items[0].State != "review" || current.State != "retiring" {
				t.Fatalf("unknown failure = %#v lease=%#v", job, current)
			}
		})
	}
}
