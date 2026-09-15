package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"
)

func TestPickupRateIsSharedByAliasTokensAndHonorsThreeSecondBoundary(t *testing.T) {
	env := newAdminAPITestEnv(t)
	env.server.sync = nil
	now := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	env.server.now = func() time.Time { return now }
	account := adminAPITestCreateAccount(t, env, "pickup-rate@example.test")
	alias, credentials := createV2AliasFixture(t, env, account.ID, "pickup-rate-alias@example.test")
	router, err := env.server.Router()
	if err != nil {
		t.Fatal(err)
	}
	called := 0
	env.server.SetAliasDemandSync(func(context.Context, int64) error { called++; return nil })
	otp, _ := env.cipher.OTPToken(alias.ID, alias.APIKeyHash)
	recent, _ := env.cipher.RecentMailToken(alias.ID, alias.APIKeyHash)
	first := serveV2Request(router, http.MethodGet, "/api/v1/otp", "", map[string]string{"Authorization": "Bearer " + credentials.APIKey})
	if first.Code != http.StatusOK || called != 1 {
		t.Fatalf("first pickup = %d, calls=%d", first.Code, called)
	}
	for _, target := range []string{
		"/api/v1/otp?token=" + url.QueryEscape(otp),
		"/api/v1/mail/latest",
		"/api/v1/mail/recent?api_key=" + url.QueryEscape(recent),
		"/api/v1/mail/recent/?api_key=" + url.QueryEscape(recent),
	} {
		headers := map[string]string(nil)
		if target == "/api/v1/mail/latest" {
			headers = map[string]string{"Authorization": "Bearer " + credentials.APIKey}
		}
		got := serveV2Request(router, http.MethodGet, target, "", headers)
		if got.Code != http.StatusTooManyRequests || got.Header().Get("Retry-After") != "3" {
			t.Fatalf("shared pickup %s = %d Retry-After=%q", target, got.Code, got.Header().Get("Retry-After"))
		}
	}
	now = now.Add(2999 * time.Millisecond)
	if got := serveV2Request(router, http.MethodGet, "/api/v1/otp", "", map[string]string{"Authorization": "Bearer " + credentials.APIKey}); got.Code != http.StatusTooManyRequests {
		t.Fatalf("before boundary = %d", got.Code)
	}
	now = now.Add(time.Millisecond)
	if got := serveV2Request(router, http.MethodGet, "/api/v1/otp", "", map[string]string{"Authorization": "Bearer " + credentials.APIKey}); got.Code != http.StatusOK {
		t.Fatalf("at boundary = %d body=%s", got.Code, got.Body.String())
	}
}

func TestPickupRateFailedFetchStillStartsCooldown(t *testing.T) {
	env := newAdminAPITestEnv(t)
	env.server.sync = nil
	now := time.Date(2026, 9, 14, 11, 0, 0, 0, time.UTC)
	env.server.now = func() time.Time { return now }
	account := adminAPITestCreateAccount(t, env, "pickup-failed@example.test")
	_, credentials := createV2AliasFixture(t, env, account.ID, "pickup-failed-alias@example.test")
	env.server.SetAliasDemandSync(func(context.Context, int64) error { return errors.New("upstream failed") })
	router, _ := env.server.Router()
	first := serveV2Request(router, http.MethodGet, "/api/v1/otp", "", map[string]string{"Authorization": "Bearer " + credentials.APIKey})
	if first.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed pickup = %d", first.Code)
	}
	second := serveV2Request(router, http.MethodGet, "/api/v1/otp", "", map[string]string{"Authorization": "Bearer " + credentials.APIKey})
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("failed pickup retry = %d", second.Code)
	}
}

func TestPickupRatePoolCodeSharesOTPAndWorksWithoutDemandCallback(t *testing.T) {
	env := newAdminAPITestEnv(t)
	env.server.sync = nil
	f := newPoolDemandLease(t, env)
	alias, err := env.store.GetAlias(context.Background(), f.aliasID)
	if err != nil {
		t.Fatal(err)
	}
	token, err := env.cipher.OTPToken(alias.ID, alias.APIKeyHash)
	if err != nil {
		t.Fatal(err)
	}
	router, _ := env.server.Router()
	pool := serveV2Request(router, http.MethodGet, "/api/v1/pool/leases/"+f.id+"/code", "", map[string]string{"Authorization": "Bearer " + f.key})
	if pool.Code != http.StatusOK {
		t.Fatalf("pool code = %d body=%s", pool.Code, pool.Body.String())
	}
	otp := serveV2Request(router, http.MethodGet, "/api/v1/otp?token="+url.QueryEscape(token), "", nil)
	if otp.Code != http.StatusTooManyRequests {
		t.Fatalf("OTP after pool pickup = %d", otp.Code)
	}
}

func TestPickupRateInFlightBlocksSameAliasButAllowsOtherAliasAndHealth(t *testing.T) {
	env := newAdminAPITestEnv(t)
	env.server.sync = nil
	account := adminAPITestCreateAccount(t, env, "pickup-flight@example.test")
	first, firstCreds := createV2AliasFixture(t, env, account.ID, "pickup-flight-a@example.test")
	_, secondCreds := createV2AliasFixture(t, env, account.ID, "pickup-flight-b@example.test")
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	env.server.SetAliasDemandSync(func(ctx context.Context, id int64) error {
		if id == first.ID {
			close(entered)
			<-release
		}
		return nil
	})
	router, _ := env.server.Router()
	result := make(chan int, 1)
	go func() {
		result <- serveV2Request(router, http.MethodGet, "/api/v1/otp", "", map[string]string{"Authorization": "Bearer " + firstCreds.APIKey}).Code
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("callback did not start")
	}
	dup := serveV2Request(router, http.MethodGet, "/api/v1/otp", "", map[string]string{"Authorization": "Bearer " + firstCreds.APIKey})
	if dup.Code != http.StatusTooManyRequests {
		t.Fatalf("same alias in-flight = %d", dup.Code)
	}
	other := serveV2Request(router, http.MethodGet, "/api/v1/otp", "", map[string]string{"Authorization": "Bearer " + secondCreds.APIKey})
	if other.Code == http.StatusTooManyRequests {
		t.Fatalf("other alias blocked: %d", other.Code)
	}
	health := serveV2Request(router, http.MethodGet, "/healthz", "", nil)
	if health.Code != http.StatusOK {
		t.Fatalf("health while pickup full = %d", health.Code)
	}
	releaseOnce.Do(func() { close(release) })
	<-result
}

func TestPickupRateGlobalSixteenSlotsRejectSeventeenthAndLeavesHealth(t *testing.T) {
	env := newAdminAPITestEnv(t)
	env.server.sync = nil
	entered := make(chan struct{}, 16)
	release := make(chan struct{})
	var releaseOnce sync.Once
	var workers sync.WaitGroup
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }); workers.Wait() })
	env.server.beforeCredentialRotationReadLock = func() { entered <- struct{}{}; <-release }
	router, _ := env.server.Router()
	results := make(chan int, 17)
	for i := 0; i < 16; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			results <- serveV2Request(router, http.MethodGet, "/api/v1/otp", "", nil).Code
		}()
	}
	for i := 0; i < 16; i++ {
		select {
		case <-time.After(time.Second):
			t.Fatalf("only %d requests reached authentication", i)
		case <-entered:
		}
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		results <- serveV2Request(router, http.MethodGet, "/api/v1/otp", "", nil).Code
	}()
	select {
	case code := <-results:
		if code != http.StatusTooManyRequests {
			t.Fatalf("full ingress status=%d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("full ingress queued a request before authentication")
	}
	if got := serveV2Request(router, http.MethodGet, "/healthz", "", nil); got.Code != http.StatusOK {
		t.Fatalf("health while full = %d", got.Code)
	}
	releaseOnce.Do(func() { close(release) })
	workers.Wait()
	for i := 0; i < 16; i++ {
		if code := <-results; code != http.StatusUnauthorized {
			t.Fatalf("released unauthenticated request status=%d", code)
		}
	}
}

func TestPickupRateLimitsPendingReadsPerAccount(t *testing.T) {
	env := newAdminAPITestEnv(t)
	account := adminAPITestCreateAccount(t, env, "pickup-account@example.test")
	other := adminAPITestCreateAccount(t, env, "pickup-other@example.test")
	first, a := createV2AliasFixture(t, env, account.ID, "pickup-a@example.test")
	second, b := createV2AliasFixture(t, env, account.ID, "pickup-b@example.test")
	_, third := createV2AliasFixture(t, env, account.ID, "pickup-c@example.test")
	_, independent := createV2AliasFixture(t, env, other.ID, "pickup-d@example.test")
	entered, release := make(chan struct{}, 2), make(chan struct{})
	var once sync.Once
	var workers sync.WaitGroup
	t.Cleanup(func() { once.Do(func() { close(release) }); workers.Wait() })
	env.server.SetAliasDemandSync(func(_ context.Context, id int64) error {
		if id == first.ID || id == second.ID {
			entered <- struct{}{}
			<-release
		}
		return nil
	})
	router, _ := env.server.Router()
	for _, key := range []string{a.APIKey, b.APIKey} {
		workers.Add(1)
		go func(key string) {
			defer workers.Done()
			serveV2Request(router, http.MethodGet, "/api/v1/otp", "", map[string]string{"Authorization": "Bearer " + key})
		}(key)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("account pickup did not start")
		}
	}
	if got := serveV2Request(router, http.MethodGet, "/api/v1/otp", "", map[string]string{"Authorization": "Bearer " + third.APIKey}); got.Code != http.StatusTooManyRequests {
		t.Fatalf("third account pickup status=%d", got.Code)
	}
	if got := serveV2Request(router, http.MethodGet, "/api/v1/otp", "", map[string]string{"Authorization": "Bearer " + independent.APIKey}); got.Code != http.StatusOK {
		t.Fatalf("independent account pickup status=%d", got.Code)
	}
	once.Do(func() { close(release) })
	workers.Wait()
	if got := serveV2Request(router, http.MethodGet, "/api/v1/otp", "", map[string]string{"Authorization": "Bearer " + third.APIKey}); got.Code != http.StatusOK {
		t.Fatalf("released account pickup status=%d", got.Code)
	}
}
