package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestSharedPickupAllowsBoundedBurstAndRejectedRefreshDoesNotDelayRefill(t *testing.T) {
	env := newAdminAPITestEnv(t)
	f := newPoolDemandLease(t, env)
	alias, err := env.store.GetAlias(context.Background(), f.aliasID)
	if err != nil {
		t.Fatal(err)
	}
	token, err := env.cipher.OTPToken(alias.ID, alias.APIKeyHash)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	now := base
	env.server.now = func() time.Time { return now }
	called := 0
	env.server.SetSharedMailboxReceiver(func(context.Context, int64) error { called++; return nil })
	router, _ := env.server.Router()
	poolPath := "/api/v1/pool/leases/" + f.id + "/code"
	headers := map[string]string{"Authorization": "Bearer " + f.key}
	first := serveV2Request(router, http.MethodGet, poolPath, "", headers)
	second := serveV2Request(router, http.MethodGet, "/api/v1/otp?token="+url.QueryEscape(token), "", nil)
	if first.Code != 200 || second.Code != 200 || called != 2 {
		t.Fatalf("normal read followed by browser refresh: %d/%d, calls=%d", first.Code, second.Code, called)
	}
	for _, elapsed := range []time.Duration{0, 300 * time.Millisecond, time.Second, 1900 * time.Millisecond, 1999 * time.Millisecond} {
		now = base.Add(elapsed)
		r := serveV2Request(router, http.MethodGet, poolPath, "", headers)
		wantRetry := strconv.Itoa(int((2*time.Second - elapsed + time.Second - 1) / time.Second))
		if r.Code != 429 || r.Header().Get("Retry-After") != wantRetry || r.Header().Get("X-RateLimit-Reason") != "mailbox_rate" {
			t.Fatalf("at %s: status=%d retry=%q reason=%q", elapsed, r.Code, r.Header().Get("Retry-After"), r.Header().Get("X-RateLimit-Reason"))
		}
	}
	now = base.Add(2 * time.Second)
	if r := serveV2Request(router, http.MethodGet, poolPath, "", headers); r.Code != 200 || called != 3 {
		t.Fatalf("refill boundary: status=%d calls=%d", r.Code, called)
	}
}

func TestSharedPickupSlowReadEarnsCreditAndReleaseIsIdempotent(t *testing.T) {
	env := newAdminAPITestEnv(t)
	base := time.Now()
	now := base
	env.server.now = func() time.Time { return now }
	env.server.SetSharedMailboxReceiver(func(context.Context, int64) error { return nil })
	begin := func(waiting bool) (func(), bool) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/otp", nil)
		return env.server.beginMailboxPickupMode(c, 1, 1, waiting)
	}
	read, ok := begin(false)
	if !ok {
		t.Fatal("first read blocked")
	}
	wait, ok := begin(true)
	if !ok {
		t.Fatal("waiter blocked by ordinary read")
	}
	if _, ok := begin(false); ok {
		t.Fatal("second ordinary in-flight read admitted")
	}
	if _, ok := begin(true); ok {
		t.Fatal("second waiter admitted")
	}
	now = base.Add(5 * time.Second)
	read()
	read()
	next, ok := begin(false)
	if !ok {
		t.Fatal("slow read incorrectly starts completion cooldown")
	}
	if env.server.accountPickupActive[1] != 2 {
		t.Fatalf("release cleared another admission: %v", env.server.accountPickupActive)
	}
	next()
	wait()
	wait()
	if env.server.accountPickupActive[1] != 0 {
		t.Fatal("admission leak or duplicate release")
	}
}

func TestSharedPickupAccountCapacityDoesNotConsumeOtherRootCredit(t *testing.T) {
	env := newAdminAPITestEnv(t)
	now := time.Now()
	env.server.now = func() time.Time { return now }
	env.server.SetSharedMailboxReceiver(func(context.Context, int64) error { return nil })
	releases := make([]func(), 0, 64)
	begin := func(alias int64) (func(), bool, *httptest.ResponseRecorder) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/otp", nil)
		release, ok := env.server.beginMailboxPickup(c, 1, alias)
		return release, ok, rec
	}
	for id := int64(1); id <= 64; id++ {
		release, ok, _ := begin(id)
		if !ok {
			t.Fatalf("account only admitted %d roots", id-1)
		}
		releases = append(releases, release)
		defer release()
	}
	for i := 0; i < 5; i++ {
		_, ok, rec := begin(65)
		if ok || rec.Header().Get("X-RateLimit-Reason") != "account_concurrency" {
			t.Fatal("account capacity not enforced")
		}
	}
	releases[0]()
	for i := 0; i < sharedPickupBurst; i++ {
		release, ok, _ := begin(65)
		if !ok {
			t.Fatal("account rejections spent unrelated root credit")
		}
		release()
	}
}

func TestPickupWindowRejectionsLeaveDeadlineAndCountFixed(t *testing.T) {
	limiter := newWindowLimiter(2, time.Second)
	base := time.Now()
	for i := 0; i < 2; i++ {
		if retry := limiter.allowAt("pickup", base); retry != 0 {
			t.Fatalf("early rate rejection: %s", retry)
		}
	}
	for _, elapsed := range []time.Duration{0, time.Millisecond, 900 * time.Millisecond, 999 * time.Millisecond} {
		if retry := limiter.allowAt("pickup", base.Add(elapsed)); retry != time.Second-elapsed {
			t.Fatalf("retry=%s at %s", retry, elapsed)
		}
		if state := limiter.items["pickup"]; state.count != 2 || !state.reset.Equal(base.Add(time.Second)) {
			t.Fatalf("rejection mutated window: %+v", state)
		}
	}
	if retry := limiter.allowAt("pickup", base.Add(time.Second)); retry != 0 {
		t.Fatalf("exact boundary blocked: %s", retry)
	}
}

func TestPickupGlobalRateReportsRemainingWindow(t *testing.T) {
	env := newAdminAPITestEnv(t)
	base := time.Now()
	now := base
	env.server.now = func() time.Time { return now }
	router, _ := env.server.Router()
	for i := 0; i < 100; i++ {
		if r := serveV2Request(router, http.MethodGet, "/api/v1/otp", "", nil); r.Code != 401 {
			t.Fatalf("unauthenticated admission %d: %d", i, r.Code)
		}
	}
	now = base.Add(900 * time.Millisecond)
	r := serveV2Request(router, http.MethodGet, "/api/v1/otp", "", nil)
	if r.Code != 429 || r.Header().Get("Retry-After") != "1" || r.Header().Get("X-Retry-After-Ms") != "100" || r.Header().Get("X-RateLimit-Reason") != "global_rate" {
		t.Fatalf("global reason/retry: %d %v", r.Code, r.Header())
	}
	now = base.Add(time.Second)
	if r := serveV2Request(router, http.MethodGet, "/api/v1/otp", "", nil); r.Code != 401 {
		t.Fatalf("global boundary still blocked: %d", r.Code)
	}
}

func TestSharedPickupWaitAllowsNormalReadAndDoesNotAddCompletionCooldown(t *testing.T) {
	for _, cancelRequest := range []bool{true, false} {
		t.Run(map[bool]string{true: "cancel", false: "timeout"}[cancelRequest], func(t *testing.T) {
			env := newAdminAPITestEnv(t)
			f := newPoolDemandLease(t, env)
			base := time.Now()
			var elapsed atomic.Int64
			env.server.now = func() time.Time { return base.Add(time.Duration(elapsed.Load())) }
			env.server.SetSharedMailboxReceiver(func(context.Context, int64) error { return nil })
			entered, changed := make(chan struct{}), make(chan struct{})
			var once sync.Once
			env.server.SetMailboxChanges(func(int64) (<-chan struct{}, time.Duration) {
				once.Do(func() { close(entered) })
				return changed, time.Hour
			})
			router, _ := env.server.Router()
			path := "/api/v1/pool/leases/" + f.id + "/code"
			headers := map[string]string{"Authorization": "Bearer " + f.key}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req := httptest.NewRequest(http.MethodGet, path+"?wait_seconds=1", nil).WithContext(ctx)
			req.Header.Set("Authorization", headers["Authorization"])
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); router.ServeHTTP(response, req) }()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("reader did not wait")
			}
			elapsed.Store(int64(3 * time.Second))
			duplicate := serveV2Request(router, http.MethodGet, path+"?wait_seconds=1", "", headers)
			if duplicate.Code != 429 || duplicate.Header().Get("X-RateLimit-Reason") != "mailbox_waiting" {
				t.Errorf("duplicate waiter: status=%d reason=%q", duplicate.Code, duplicate.Header().Get("X-RateLimit-Reason"))
			}
			read := serveV2Request(router, http.MethodGet, path, "", headers)
			if read.Code != 200 {
				t.Errorf("normal read blocked by waiter: %d", read.Code)
			}
			if cancelRequest {
				cancel()
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("waiter did not release")
			}
			read = serveV2Request(router, http.MethodGet, path, "", headers)
			if read.Code != 200 {
				t.Fatalf("completion added cooldown to refilled reader: %d", read.Code)
			}
		})
	}
}
