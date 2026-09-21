package hmesync

import (
	"context"
	"errors"
	"testing"
	"time"

	"icloud-api/internal/apple"
	"icloud-api/internal/domain"
	"icloud-api/internal/store"
)

type channelBudgetRepository struct {
	*budgetTestRepository
	channelClaim func(context.Context, int64, string, time.Time) error
	channelPause func(context.Context, int64, string, time.Time) error
}

func (r *channelBudgetRepository) ClaimAppleChannelCreationAttempt(ctx context.Context, id int64, channel string, at time.Time) error {
	return r.channelClaim(ctx, id, channel, at)
}
func (r *channelBudgetRepository) PauseAppleChannelCreation(ctx context.Context, id int64, channel string, at time.Time) error {
	return r.channelPause(ctx, id, channel, at)
}

func TestChannelCreationLocalQuotaFallsBackAndPreservesDiagnostic(t *testing.T) {
	now := time.Now()
	for _, bothFull := range []bool{false, true} {
		repo := &channelBudgetRepository{channelClaim: func(_ context.Context, id int64, channel string, at time.Time) error {
			if id != 1 || !at.Equal(now) {
				t.Fatal("wrong channel budget identity")
			}
			if channel == "apple_account" || bothFull {
				return &store.AppleCreationBudgetError{Now: now, Until: now.Add(time.Hour)}
			}
			return nil
		}}
		client := &accountTestClient{}
		calls := 0
		client.accountCreate = func(context.Context, apple.AccountSession) (apple.Alias, apple.AccountSession, error) {
			t.Fatal("exhausted account quota reached Apple")
			return apple.Alias{}, apple.AccountSession{}, nil
		}
		client.create = func(_ context.Context, s apple.Session, _, _ string) (apple.Alias, apple.Session, error) {
			calls++
			return apple.Alias{HME: "fallback@icloud.com"}, s, nil
		}
		s := &Service{repo: repo, client: client, now: func() time.Time { return now }}
		web := apple.Session{AppleID: "owner@icloud.com", Account: &apple.AccountSession{AppleID: "owner@icloud.com", APIKey: "fixture"}}
		a, _, err := s.createRemoteAliasWithChannel(context.Background(), 1, web, client, "auto")
		if bothFull {
			var budget *store.AppleCreationBudgetError
			var scope *creationChannelWaitError
			if !errors.As(err, &budget) || !errors.As(err, &scope) || calls != 0 {
				t.Fatalf("quota diagnostic=%v calls=%d", err, calls)
			}
		} else if err != nil || a.HME != "fallback@icloud.com" || calls != 1 {
			t.Fatalf("fallback=%v err=%v calls=%d", a, err, calls)
		}
	}
}

func TestChannelCreationRateLimitDoesNotPersistWholeAccountPause(t *testing.T) {
	now := testAutoCreateDiagnosticNow()
	base := newFakeRepository(domain.Account{ID: 1, Email: "owner@icloud.com", Enabled: true}, now)
	pauses := map[string]time.Time{}
	repo := &channelBudgetRepository{
		budgetTestRepository: &budgetTestRepository{fakeRepository: base,
			claim: func(context.Context, int64, time.Time) error { return nil },
			pause: func(context.Context, int64, time.Time) error {
				t.Fatal("channel throttle paused whole account")
				return nil
			},
		},
		channelClaim: func(context.Context, int64, string, time.Time) error { return nil },
		channelPause: func(_ context.Context, _ int64, channel string, until time.Time) error {
			pauses[channel] = until
			return nil
		},
	}
	client := &accountTestClient{}
	client.validate = func(_ context.Context, s apple.Session) (apple.Session, error) { return s, nil }
	client.list = func(_ context.Context, s apple.Session) (apple.ListResult, apple.Session, error) {
		return apple.ListResult{SelectedForwardTo: "owner@icloud.com"}, s, nil
	}
	client.accountCreate = func(_ context.Context, s apple.AccountSession) (apple.Alias, apple.AccountSession, error) {
		return apple.Alias{}, s, &apple.Error{Kind: apple.ErrService, StatusCode: 429}
	}
	client.create = func(_ context.Context, s apple.Session, _, _ string) (apple.Alias, apple.Session, error) {
		return apple.Alias{}, s, &apple.Error{Kind: apple.ErrService, StatusCode: 429}
	}
	s := newTestService(t, repo, client, &fakeLocker{}, func() time.Time { return now })
	storeSession(t, s, base, 1, apple.Session{AppleID: "owner@icloud.com", SessionToken: "fixture", Region: apple.RegionGlobal, Account: &apple.AccountSession{AppleID: "owner@icloud.com", APIKey: "fixture"}})
	_, err := s.CreateAutoAlias(context.Background(), 1)
	var scope *creationChannelWaitError
	if !errors.Is(err, ErrRateLimited) || !errors.As(err, &scope) {
		t.Fatalf("lost throttle scope: %v", err)
	}
	for _, channel := range []string{"apple_account", "icloud_web"} {
		if !pauses[channel].Equal(now.Add(time.Hour)) {
			t.Fatalf("%s pause=%v", channel, pauses[channel])
		}
	}
}

func TestScheduledWebSlotAndUncertainResultsNeverReplayed(t *testing.T) {
	for _, failure := range []string{"web_slot", "network", "candidate", "marked_uncertain"} {
		t.Run(failure, func(t *testing.T) {
			client := &accountTestClient{}
			webCalls, accountCalls := 0, 0
			client.accountCreate = func(_ context.Context, s apple.AccountSession) (apple.Alias, apple.AccountSession, error) {
				accountCalls++
				if failure == "marked_uncertain" {
					return apple.Alias{}, s, markRemoteSideEffectPossible(&apple.Error{Kind: apple.ErrService, StatusCode: 429})
				}
				if failure == "candidate" {
					return apple.Alias{HME: "pending@icloud.com"}, s, &apple.Error{Kind: apple.ErrService, StatusCode: 429}
				}
				return apple.Alias{}, s, errors.New("uncertain network result")
			}
			client.create = func(_ context.Context, s apple.Session, _, _ string) (apple.Alias, apple.Session, error) {
				webCalls++
				return apple.Alias{HME: "web@icloud.com"}, s, nil
			}
			s := &Service{client: client, now: time.Now}
			web := apple.Session{AppleID: "owner@icloud.com", Account: &apple.AccountSession{AppleID: "owner@icloud.com", APIKey: "fixture"}}
			ctx := context.Background()
			if failure == "web_slot" {
				ctx = domain.WithScheduledCreationSlot(ctx, 5)
			}
			_, _, err := s.createRemoteAliasWithChannel(ctx, 1, web, client, "auto")
			if failure == "web_slot" {
				if err != nil || webCalls != 1 || accountCalls != 0 {
					t.Fatalf("Web slot used wrong channel: %v %d %d", err, webCalls, accountCalls)
				}
			} else if err == nil || webCalls != 0 || accountCalls != 1 {
				t.Fatalf("uncertain result replayed: %v %d %d", err, webCalls, accountCalls)
			}
		})
	}
}

func TestChannelRateLimitPersistsAfterCancellationWithoutFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	now := time.Now()
	pauses := 0
	repo := &channelBudgetRepository{
		channelClaim: func(context.Context, int64, string, time.Time) error { return nil },
		channelPause: func(ctx context.Context, id int64, channel string, until time.Time) error {
			pauses++
			if ctx.Err() != nil || id != 1 || channel != "apple_account" || !until.Equal(now.Add(time.Hour)) {
				t.Fatal("lost throttle checkpoint on cancellation")
			}
			return nil
		},
	}
	client := &accountTestClient{}
	client.accountCreate = func(_ context.Context, session apple.AccountSession) (apple.Alias, apple.AccountSession, error) {
		cancel()
		return apple.Alias{}, session, &apple.Error{Kind: apple.ErrService, StatusCode: 429}
	}
	client.create = func(context.Context, apple.Session, string, string) (apple.Alias, apple.Session, error) {
		t.Fatal("cancelled creation fell back")
		return apple.Alias{}, apple.Session{}, nil
	}
	s := &Service{repo: repo, client: client, now: func() time.Time { return now }}
	web := apple.Session{AppleID: "owner@icloud.com", Account: &apple.AccountSession{AppleID: "owner@icloud.com", APIKey: "fixture"}}
	_, _, err := s.createRemoteAliasWithChannel(ctx, 1, web, client, "auto")
	var scope *creationChannelWaitError
	if !errors.As(err, &scope) || pauses != 1 {
		t.Fatalf("checkpoint=%d error=%v", pauses, err)
	}
}
