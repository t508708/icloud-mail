package hmesync

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"icloud-api/internal/apple"
	"icloud-api/internal/domain"
)

type accountTestClient struct {
	fakeAppleClient
	accountCreate  func(context.Context, apple.AccountSession) (apple.Alias, apple.AccountSession, error)
	accountSignIn  func(apple.AccountSession) (apple.AccountSession, bool, error)
	accountRefresh func(apple.AccountSession) (apple.AccountSession, error)
}

func (c *accountTestClient) RefreshAccountSession(ctx context.Context, s apple.AccountSession) (apple.AccountSession, error) {
	if c.accountRefresh != nil {
		return c.accountRefresh(s)
	}
	return s, nil
}

func TestAccountKeepAlivePreservesWebAndIgnoresFailedState(t *testing.T) {
	now := time.Now().UTC()
	repo := newFakeRepository(domain.Account{ID: 1, Email: "owner@icloud.com", Enabled: true}, now)
	fail := false
	client := &accountTestClient{accountRefresh: func(s apple.AccountSession) (apple.AccountSession, error) {
		s.APIKey = "fresh-key"
		s.ExpiresAt = now.Add(30 * time.Minute)
		if fail {
			s.APIKey = "failed-key"
			return s, apple.ErrInvalidSession
		}
		return s, nil
	}}
	service := newTestService(t, repo, client, &fakeLocker{}, func() time.Time { return now })
	storeSession(t, service, repo, 1, apple.Session{AppleID: "owner@icloud.com", Region: apple.RegionChina, SessionToken: "web-token", Account: &apple.AccountSession{AppleID: "owner@icloud.com", APIKey: "old-key", AuthenticatedAt: now}})
	if err := service.KeepAliveAccountSession(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	fail = true
	if err := service.KeepAliveAccountSession(context.Background(), 1); !errors.Is(err, apple.ErrInvalidSession) {
		t.Fatal(err)
	}
	_, web, err := service.loadSession(context.Background(), 1)
	if err != nil || web.SessionToken != "web-token" || web.Account.APIKey != "fresh-key" {
		t.Fatal("refresh damaged saved session")
	}
}

func (c *accountTestClient) SignInAccount(ctx context.Context, id, password string, r apple.Region, previous *apple.AccountSession) (apple.AccountSession, bool, error) {
	return c.accountSignIn(apple.AccountSession{AppleID: id, Region: r})
}
func (c *accountTestClient) VerifyAccountCode(ctx context.Context, s apple.AccountSession, code string) (apple.AccountSession, error) {
	s.APIKey = "managed-key"
	s.AuthenticatedAt = time.Now()
	return s, nil
}
func (c *accountTestClient) CreateAccountAlias(ctx context.Context, s apple.AccountSession, label, note string) (apple.Alias, apple.AccountSession, error) {
	return c.accountCreate(ctx, s)
}

func TestAccountChannelsFallbackOnlyOnDefiniteRateLimit(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(map[bool]string{false: "rate", true: "uncertain"}[uncertain], func(t *testing.T) {
			now := time.Now()
			managedCalls, oldCalls := 0, 0
			client := &accountTestClient{}
			client.accountCreate = func(ctx context.Context, s apple.AccountSession) (apple.Alias, apple.AccountSession, error) {
				managedCalls++
				s.SCNT = "rotated"
				if uncertain {
					return apple.Alias{HME: "pending@icloud.com"}, s, errors.New("network interrupted")
				}
				return apple.Alias{}, s, &apple.Error{Kind: apple.ErrService, StatusCode: http.StatusTooManyRequests}
			}
			client.create = func(ctx context.Context, s apple.Session, _, _ string) (apple.Alias, apple.Session, error) {
				oldCalls++
				return apple.Alias{HME: "old@icloud.com", IsActive: true}, s, nil
			}
			service := &Service{client: client, now: func() time.Time { return now }}
			web := apple.Session{AppleID: "owner@icloud.com", Account: &apple.AccountSession{AppleID: "owner@icloud.com", APIKey: "key"}}
			alias, returned, err := service.createRemoteAliasWithChannel(context.Background(), 1, web, client, "auto")
			if uncertain {
				if alias.HME != "pending@icloud.com" || err == nil || oldCalls != 0 {
					t.Fatalf("uncertain result replayed: %v %v", alias, err)
				}
			} else {
				if err != nil || alias.HME != "old@icloud.com" || oldCalls != 1 {
					t.Fatalf("rate limit did not fall back: %v %v", alias, err)
				}
				_, _, _ = service.createRemoteAliasWithChannel(context.Background(), 1, returned, client, "auto")
				if managedCalls != 1 || oldCalls != 2 {
					t.Fatalf("channel cooldown ignored: %d %d", managedCalls, oldCalls)
				}
				if !service.creationCooldowns[1]["apple_account"].Equal(now.Add(time.Hour)) || !service.creationCooldowns[1]["icloud_web"].IsZero() {
					t.Fatal("default cooldown is not one hour on the affected channel")
				}
			}
			if returned.Account.SCNT != "rotated" {
				t.Fatal("management checkpoint lost")
			}
		})
	}
}

func TestExplicitChannelsRespectIndependentCooldown(t *testing.T) {
	now := time.Now()
	client := &accountTestClient{}
	managedCalls, webCalls := 0, 0
	client.accountCreate = func(context.Context, apple.AccountSession) (apple.Alias, apple.AccountSession, error) {
		managedCalls++
		return apple.Alias{}, apple.AccountSession{}, &apple.Error{Kind: apple.ErrService, StatusCode: http.StatusTooManyRequests, RetryAfter: 48 * time.Hour}
	}
	client.create = func(context.Context, apple.Session, string, string) (apple.Alias, apple.Session, error) {
		webCalls++
		return apple.Alias{}, apple.Session{}, nil
	}
	service := &Service{client: client, now: func() time.Time { return now }}
	web := apple.Session{AppleID: "owner@icloud.com", Account: &apple.AccountSession{AppleID: "owner@icloud.com", APIKey: "key"}}
	if _, _, err := service.createRemoteAliasWithChannel(context.Background(), 1, web, client, "apple_account"); err == nil {
		t.Fatal("expected Apple Account rate limit")
	}
	if _, _, err := service.createRemoteAliasWithChannel(context.Background(), 1, web, client, "icloud_web"); err != nil {
		t.Fatalf("Web channel shared Account cooldown: %v", err)
	}
	if managedCalls != 1 || webCalls != 1 {
		t.Fatalf("cooldown allowed request: account=%d web=%d", managedCalls, webCalls)
	}
	if service.creationCooldowns[1]["apple_account"].Before(now.Add(48*time.Hour)) || !service.creationCooldowns[1]["icloud_web"].IsZero() {
		t.Fatal("channel cooldown did not honor longer Retry-After")
	}
}

func TestAccountChannelUsesCommonPublicationAndPreservesWeb(t *testing.T) {
	now := time.Now().UTC()
	repo := newFakeRepository(domain.Account{ID: 1, Email: "owner@icloud.com", Enabled: true}, now)
	client := &accountTestClient{}
	lists := 0
	client.validate = func(ctx context.Context, s apple.Session) (apple.Session, error) { return s, nil }
	client.list = func(ctx context.Context, s apple.Session) (apple.ListResult, apple.Session, error) {
		lists++
		result := apple.ListResult{SelectedForwardTo: "owner@icloud.com"}
		if lists > 1 {
			result.Aliases = []apple.Alias{{HME: "managed@icloud.com", ForwardToEmail: "owner@icloud.com", IsActive: true}}
		}
		return result, s, nil
	}
	client.accountCreate = func(ctx context.Context, s apple.AccountSession) (apple.Alias, apple.AccountSession, error) {
		s.SCNT = "new-scnt"
		return apple.Alias{HME: "managed@icloud.com", IsActive: true}, s, nil
	}
	service := newTestService(t, repo, client, &fakeLocker{}, func() time.Time { return now })
	storeSession(t, service, repo, 1, apple.Session{AppleID: "owner@icloud.com", Region: apple.RegionChina, SessionToken: "web-token", Account: &apple.AccountSession{AppleID: "owner@icloud.com", APIKey: "key"}})
	alias, err := service.CreateAliasWithChannel(context.Background(), 1, "apple_account")
	if err != nil || alias.ID < 1 || !alias.Enabled || alias.Address != "managed@icloud.com" {
		t.Fatalf("publication: %v %v", alias, err)
	}
	if repo.creates.Load() != 1 || repo.confirms.Load() != 1 || client.createCalls.Load() != 0 {
		t.Fatal("unexpected create path")
	}
	_, web, err := service.loadSession(context.Background(), 1)
	if err != nil || web.Account.SCNT != "new-scnt" || web.SessionToken != "web-token" {
		t.Fatal("web or account session lost")
	}
}

func TestAccountAuthenticationIsIndependentAndBoundToWebIdentity(t *testing.T) {
	now := time.Now().UTC()
	repo := newFakeRepository(domain.Account{ID: 1, Email: "owner@icloud.com", Enabled: true}, now)
	client := &accountTestClient{accountSignIn: func(s apple.AccountSession) (apple.AccountSession, bool, error) { return s, true, nil }}
	service := newTestService(t, repo, client, &fakeLocker{}, func() time.Time { return now })
	storeSession(t, service, repo, 1, apple.Session{AppleID: "owner@icloud.com", Region: apple.RegionChina, SessionToken: "web-token"})
	if _, err := service.StartAccountAuth(context.Background(), 2, 1, "different@icloud.com", "password", apple.RegionChina); !errors.Is(err, ErrAccountMismatch) {
		t.Fatal("different account accepted")
	}
	result, err := service.StartAccountAuth(context.Background(), 2, 1, "owner@icloud.com", "password", apple.RegionChina)
	if err != nil || result.Status != StatusVerificationRequired {
		t.Fatalf("start: %v", err)
	}
	if _, web, err := service.loadSession(context.Background(), 1); err != nil || web.SessionToken != "web-token" {
		t.Fatal("starting account login removed Web")
	}
	if _, err := service.VerifyAccountAuth(context.Background(), 3, 1, result.ChallengeID, "123456"); !errors.Is(err, ErrFlowExpired) {
		t.Fatal("other admin accepted")
	}
	verified, err := service.VerifyAccountAuth(context.Background(), 2, 1, result.ChallengeID, "123456")
	if err != nil || verified.Status != StatusAuthenticated {
		t.Fatalf("verify: %v", err)
	}
	if err := service.ClearAccountAuth(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	_, web, err := service.loadSession(context.Background(), 1)
	if err != nil || web.Account != nil || web.SessionToken != "web-token" {
		t.Fatal("clear management removed Web")
	}
}
