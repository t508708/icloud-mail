package hmesync

import (
	"context"
	"errors"
	"testing"
	"time"

	"icloud-api/internal/apple"
	"icloud-api/internal/domain"
	"icloud-api/internal/secure"
)

func TestAccountProxyContextDecryptsCredentialsInMemory(t *testing.T) {
	repo := newFakeRepository(domain.Account{ID: 7, Proxy: &domain.AccountProxyConfig{
		Enabled: true, EndpointCiphertext: "enc-endpoint", UpstreamCiphertext: "enc-upstream",
	}}, time.Now())
	service := newTestService(t, repo, &fakeAppleClient{}, &fakeLocker{}, time.Now)
	cipher := service.cipher.(*secure.Cipher)
	endpoint, err := cipher.Encrypt("proxy-user:proxy-pass@127.0.0.1:1080")
	if err != nil {
		t.Fatal(err)
	}
	upstream, err := cipher.Encrypt("upstream-user:upstream-pass@127.0.0.1:1081")
	if err != nil {
		t.Fatal(err)
	}
	repo.account.Proxy.EndpointCiphertext = endpoint
	repo.account.Proxy.UpstreamCiphertext = upstream
	ctx, err := service.accountProxyForAccount(context.Background(), repo.account)
	if err != nil {
		t.Fatal(err)
	}
	config, ok := apple.ProxyConfigFromContext(ctx)
	if !ok || config.Endpoint != "proxy-user:proxy-pass@127.0.0.1:1080" || config.Upstream != "upstream-user:upstream-pass@127.0.0.1:1081" {
		t.Fatalf("proxy context config = %#v, present=%v", config, ok)
	}
}

func TestAccountProxyContextLeavesDisabledAccountUnchanged(t *testing.T) {
	repo := newFakeRepository(domain.Account{ID: 7}, time.Now())
	service := newTestService(t, repo, &fakeAppleClient{}, &fakeLocker{}, time.Now)
	original := context.Background()
	got, err := service.accountProxyForAccount(original, repo.account)
	if err != nil || got != original {
		t.Fatalf("context=%v err=%v, want unchanged context", got, err)
	}
}

func TestWebChallengeEndsAfterProxyRevisionChanges(t *testing.T) {
	now := time.Now().UTC()
	account := domain.Account{ID: 7, Email: "owner@icloud.com", Enabled: true,
		Proxy: &domain.AccountProxyConfig{Revision: 1}}
	repo := newFakeRepository(account, now)
	client := &fakeAppleClient{}
	client.signIn = func(_ context.Context, appleID, password string, region apple.Region, previous *apple.Session) (apple.Session, bool, error) {
		return apple.Session{AppleID: appleID, Region: region, SessionToken: "pending"}, true, nil
	}
	client.verify = func(context.Context, apple.Session, string) (apple.Session, error) {
		t.Fatal("old verification challenge reached Apple")
		return apple.Session{}, nil
	}
	service := newTestService(t, repo, client, &fakeLocker{}, func() time.Time { return now })
	started, err := service.StartAuth(context.Background(), 1, account.ID, account.Email, "password", apple.RegionGlobal)
	if err != nil {
		t.Fatal(err)
	}
	repo.mu.Lock()
	repo.account.Proxy = &domain.AccountProxyConfig{Revision: 2}
	repo.mu.Unlock()
	_, err = service.VerifyAuth(context.Background(), 1, account.ID, started.ChallengeID, "123456")
	if !errors.Is(err, ErrAccountChanged) || client.verifyCalls.Load() != 0 {
		t.Fatalf("old challenge err=%v verifyCalls=%d", err, client.verifyCalls.Load())
	}
}

func TestAccountProxyVersionComparison(t *testing.T) {
	first := &domain.AccountProxyConfig{Enabled: true, Revision: 4, EndpointCiphertext: "old"}
	cloned := cloneAccountProxy(first)
	first.EndpointCiphertext = "new"
	if sameAccountProxy(first, cloned) || !sameAccountProxy(nil, &domain.AccountProxyConfig{}) {
		t.Fatal("proxy identity did not preserve the original configuration")
	}
}
