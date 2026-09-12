package httpserver

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestRequestSamplerAllowAndSuppression(t *testing.T) {
	s := newRequestSampler()
	now := time.Unix(100, 0)
	if ok, n := s.allow("GET /x", now); !ok || n != 0 {
		t.Fatalf("first = %v,%d", ok, n)
	}
	for i := 0; i < 3; i++ {
		if ok, _ := s.allow("GET /x", now.Add(time.Second)); ok {
			t.Fatal("duplicate allowed")
		}
	}
	if ok, n := s.allow("GET /x", now.Add(61*time.Second)); !ok || n != 3 {
		t.Fatalf("next window = %v,%d", ok, n)
	}
}

func TestRequestSamplerBoundedAndConcurrent(t *testing.T) {
	s := newRequestSampler()
	now := time.Unix(200, 0)
	for i := 0; i < 3000; i++ {
		s.allow(fmt.Sprintf("k-%d", i), now)
	}
	if len(s.items) > 2048 {
		t.Fatalf("sampler size %d", len(s.items))
	}
	s = newRequestSampler()
	var wg sync.WaitGroup
	allowed := 0
	var mu sync.Mutex
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, _ := s.allow("same", now)
			if ok {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != 1 {
		t.Fatalf("concurrent allowed = %d", allowed)
	}
}

func TestHttpOperationRouteMap(t *testing.T) {
	cases := []struct{ method, path, want string }{
		{"DELETE", "/admin/api/v1/accounts/:id", "删除主号"},
		{"GET", "/admin/api/v1/accounts", "读取主号列表"},
		{"POST", "/admin/api/v1/pool/members", "添加邮箱池成员"},
		{"POST", "/api/v1/pool/claim", "领取邮箱"},
		{"GET", "/admin/api/v1/unknown", "其他请求"},
	}
	for _, tc := range cases {
		if got := httpOperation(tc.path, tc.method); got != tc.want {
			t.Errorf("%s %s = %q, want %q", tc.method, tc.path, got, tc.want)
		}
	}
}
