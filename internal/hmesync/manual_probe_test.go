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

func TestManualProbeBypassesLocalBudgetAndDoesNotPersistBackgroundCooldown(t *testing.T) {
	for _, channel := range []string{"auto", "apple_account", "icloud_web"} {
		t.Run(channel, func(t *testing.T) {
			now := testAutoCreateDiagnosticNow()
			base := newFakeRepository(domain.Account{ID: 3, Email: "primary@icloud.com", Enabled: true}, now)
			claims, pauses, requests := 0, 0, 0
			repo := &budgetTestRepository{fakeRepository: base,
				claim: func(context.Context, int64, time.Time) error { claims++; return nil },
				pause: func(context.Context, int64, time.Time) error { pauses++; return nil },
			}
			client := &fakeAppleClient{validate: func(_ context.Context, session apple.Session) (apple.Session, error) {
				requests++
				return session, &apple.Error{Kind: apple.ErrService, StatusCode: http.StatusTooManyRequests, RetryAfter: 7 * time.Second}
			}}
			service := newTestService(t, repo, client, &fakeLocker{}, func() time.Time { return now })
			storeSession(t, service, base, 3, apple.Session{AppleID: "owner@example.com", Region: apple.RegionGlobal, SessionToken: "fixture"})
			for attempt := 0; attempt < 2; attempt++ {
				_, err := service.ProbeAliasWithChannel(context.Background(), 3, channel)
				if !errors.Is(err, ErrRateLimited) || apple.RetryDelay(err) != 7*time.Second {
					t.Fatalf("attempt=%d probe err=%v retry=%v", attempt, err, apple.RetryDelay(err))
				}
			}
			if claims != 0 || pauses != 0 || requests != 2 {
				t.Fatalf("background claims=%d pauses=%d requests=%d", claims, pauses, requests)
			}
		})
	}
}

func TestManualProbeBypassesChannelCooldownWithoutChangingIt(t *testing.T) {
	for _, channel := range []string{"auto", "apple_account", "icloud_web"} {
		for _, rateLimited := range []bool{false, true} {
			t.Run(channel+map[bool]string{false: "/success", true: "/rate"}[rateLimited], func(t *testing.T) {
				now := time.Now().UTC()
				until := now.Add(24 * time.Hour)
				calls := 0
				result := func() (apple.Alias, error) {
					calls++
					if rateLimited {
						return apple.Alias{}, &apple.Error{Kind: apple.ErrService, StatusCode: http.StatusTooManyRequests, RetryAfter: 7 * time.Second}
					}
					return apple.Alias{HME: "probe@icloud.com", IsActive: true}, nil
				}
				client := &accountTestClient{}
				client.accountCreate = func(_ context.Context, s apple.AccountSession) (apple.Alias, apple.AccountSession, error) {
					a, e := result()
					return a, s, e
				}
				client.create = func(_ context.Context, s apple.Session, _, _ string) (apple.Alias, apple.Session, error) {
					a, e := result()
					return a, s, e
				}
				service := &Service{client: client, now: func() time.Time { return now }, creationCooldowns: map[int64]map[string]time.Time{1: {"apple_account": until, "icloud_web": until}}}
				web := apple.Session{AppleID: "owner@icloud.com", Account: &apple.AccountSession{AppleID: "owner@icloud.com", APIKey: "fixture"}}
				alias, _, err := service.createRemoteAliasWithMode(context.Background(), 1, web, client, channel, true)
				if calls != 1 {
					t.Fatalf("probe requests=%d want 1", calls)
				}
				if rateLimited && (!apple.IsRateLimited(err) || apple.RetryDelay(err) != 7*time.Second) {
					t.Fatalf("probe throttle changed: %v", err)
				}
				if !rateLimited && (err != nil || alias.HME != "probe@icloud.com") {
					t.Fatalf("probe result=%v err=%v", alias, err)
				}
				for _, deadline := range service.creationCooldowns[1] {
					if !deadline.Equal(until) {
						t.Fatal("probe altered background cooldown")
					}
				}
				_, _, err = service.createRemoteAliasWithChannel(context.Background(), 1, web, client, channel)
				if !apple.IsRateLimited(err) || calls != 1 {
					t.Fatalf("background resumed unexpectedly: %v calls=%d", err, calls)
				}
			})
		}
	}
}

func TestManualProbePublishesConfirmedAliasDuringBackgroundCooldown(t *testing.T) {
	now := testAutoCreateDiagnosticNow()
	base := newFakeRepository(domain.Account{ID: 1, Email: "owner@icloud.com", Enabled: true}, now)
	claims := 0
	repo := &budgetTestRepository{fakeRepository: base,
		claim: func(context.Context, int64, time.Time) error { claims++; return nil },
		pause: func(context.Context, int64, time.Time) error { t.Fatal("probe paused background"); return nil },
	}
	client := &accountTestClient{}
	lists, creates := 0, 0
	client.validate = func(_ context.Context, s apple.Session) (apple.Session, error) { return s, nil }
	client.list = func(_ context.Context, s apple.Session) (apple.ListResult, apple.Session, error) {
		lists++
		result := apple.ListResult{SelectedForwardTo: "owner@icloud.com"}
		if lists > 1 {
			result.Aliases = []apple.Alias{{HME: "probe-created@icloud.com", ForwardToEmail: "owner@icloud.com", IsActive: true}}
		}
		return result, s, nil
	}
	client.accountCreate = func(_ context.Context, s apple.AccountSession) (apple.Alias, apple.AccountSession, error) {
		creates++
		return apple.Alias{HME: "probe-created@icloud.com", IsActive: true}, s, nil
	}
	service := newTestService(t, repo, client, &fakeLocker{}, func() time.Time { return now })
	until := now.Add(24 * time.Hour)
	service.creationCooldowns = map[int64]map[string]time.Time{1: {"apple_account": until, "icloud_web": until}}
	storeSession(t, service, base, 1, apple.Session{AppleID: "owner@icloud.com", Region: apple.RegionGlobal, SessionToken: "web-fixture", Account: &apple.AccountSession{AppleID: "owner@icloud.com", APIKey: "fixture"}})
	alias, err := service.ProbeAliasWithChannel(context.Background(), 1, "apple_account")
	if err != nil || !alias.Enabled || alias.ID < 1 || alias.Address != "probe-created@icloud.com" {
		t.Fatalf("probe publication=%v err=%v", alias, err)
	}
	if claims != 0 || creates != 1 || base.creates.Load() != 1 || base.confirms.Load() != 1 {
		t.Fatalf("claims=%d creates=%d staged=%d confirmed=%d", claims, creates, base.creates.Load(), base.confirms.Load())
	}
	if !service.creationCooldowns[1]["apple_account"].Equal(until) {
		t.Fatal("probe cleared background cooldown")
	}
}
