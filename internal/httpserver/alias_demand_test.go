package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"icloud-api/internal/domain"
)

func TestAliasDemandSyncOTPInvokesTargetAndRejectsInvalidBeforeCallback(t *testing.T) {
	env := newAdminAPITestEnv(t)
	env.server.sync = nil
	account := adminAPITestCreateAccount(t, env, "demand-otp@example.test")
	alias, credentials := createV2AliasFixture(t, env, account.ID, "demand-alias@example.test")
	router, err := env.server.Router()
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var calls []int64
	now := time.Now().UTC()
	env.server.now = func() time.Time { return now }
	env.server.SetAliasDemandSync(func(_ context.Context, id int64) error { mu.Lock(); calls = append(calls, id); mu.Unlock(); return nil })
	request := func(target, auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	if got := request("/api/v1/otp", "Bearer "+credentials.APIKey); got.Code != http.StatusOK {
		t.Fatalf("bearer status=%d", got.Code)
	}
	now = now.Add(3 * time.Second)
	token, err := env.cipher.OTPToken(alias.ID, alias.APIKeyHash)
	if err != nil {
		t.Fatal(err)
	}
	if got := request("/api/v1/otp?token="+token, ""); got.Code != http.StatusOK {
		t.Fatalf("token status=%d", got.Code)
	}
	if got := request("/api/v1/otp", "Bearer invalid-token"); got.Code == http.StatusOK {
		t.Fatal("invalid credential accepted")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 || calls[0] != alias.ID || calls[1] != alias.ID {
		t.Fatalf("callback calls=%v, want [%d %d]", calls, alias.ID, alias.ID)
	}
}

func TestAliasDemandSyncOTPErrorReturns503(t *testing.T) {
	env := newAdminAPITestEnv(t)
	account := adminAPITestCreateAccount(t, env, "demand-error@example.test")
	_, credentials := createV2AliasFixture(t, env, account.ID, "demand-error-alias@example.test")
	var syncRequestID string
	env.server.SetAliasDemandSync(func(ctx context.Context, _ int64) error {
		syncRequestID = domain.MailboxRequestID(ctx)
		return context.Canceled
	})
	router, err := env.server.Router()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/otp", nil)
	req.Header.Set("Authorization", "Bearer "+credentials.APIKey)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "SYNC_UNAVAILABLE") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if syncRequestID == "" || rec.Header().Get("X-Request-ID") != syncRequestID || !strings.Contains(rec.Body.String(), syncRequestID) {
		t.Fatal("request ID did not reach the demand sync and error response")
	}
}

func TestAliasDemandSyncLegacyUsesAliasID(t *testing.T) {
	env := newAdminAPITestEnv(t)
	account := adminAPITestCreateAccount(t, env, "demand-legacy@example.test")
	alias, credentials := createV2AliasFixture(t, env, account.ID, "demand-legacy-alias@example.test")
	env.server.SetAliasDemandSync(func(_ context.Context, id int64) error {
		if id != alias.ID {
			t.Errorf("callback id=%d, want %d", id, alias.ID)
		}
		now := time.Now().UTC()
		if err := env.store.UpdateAliasSyncStatus(context.Background(), alias.ID, domain.SyncStatusOK, "", &now); err != nil {
			return err
		}
		_, err := env.store.UpsertLatestMessage(context.Background(), domain.LatestMessage{AliasID: alias.ID, UIDValidity: 77, UID: 1, InternalDate: now, Subject: "fetched-on-request", SyncedAt: now})
		return err
	})
	router, err := env.server.Router()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/latest", nil)
	req.Header.Set("Authorization", "Bearer "+credentials.APIKey)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "fetched-on-request") {
		t.Fatalf("legacy latest status=%d body=%s", rec.Code, rec.Body.String())
	}
}
