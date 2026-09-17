package hmesync

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"icloud-api/internal/apple"
	"icloud-api/internal/domain"
	"icloud-api/internal/store"
)

type budgetTestRepository struct {
	*fakeRepository
	claim func(context.Context, int64, time.Time) error
	pause func(context.Context, int64, time.Time) error
}

func (r *budgetTestRepository) ClaimAppleCreationAttempt(ctx context.Context, id int64, now time.Time) error {
	return r.claim(ctx, id, now)
}

func (r *budgetTestRepository) PauseAppleCreation(ctx context.Context, id int64, until time.Time) error {
	return r.pause(ctx, id, until)
}

func TestCreationBudgetStopsEveryChannelBeforeFreshAddressGeneration(t *testing.T) {
	for _, channel := range []string{"auto", "apple_account", "icloud_web"} {
		t.Run(channel, func(t *testing.T) {
			now := testAutoCreateDiagnosticNow()
			base := newFakeRepository(domain.Account{ID: 3, Email: "primary@icloud.com", Enabled: true}, now)
			claims := 0
			budgetErr := &store.AppleCreationBudgetError{Now: now, Until: now.Add(time.Hour)}
			repo := &budgetTestRepository{fakeRepository: base,
				claim: func(_ context.Context, id int64, at time.Time) error {
					claims++
					if id != 3 || !at.Equal(now) {
						t.Fatal("wrong budget identity or clock")
					}
					return budgetErr
				},
				pause: func(context.Context, int64, time.Time) error {
					t.Fatal("local budget triggered Apple cooldown")
					return nil
				},
			}
			validations, directoryReads := 0, 0
			client := &fakeAppleClient{
				validate: func(_ context.Context, session apple.Session) (apple.Session, error) {
					validations++
					return session, nil
				},
				list: func(_ context.Context, session apple.Session) (apple.ListResult, apple.Session, error) {
					directoryReads++
					return apple.ListResult{SelectedForwardTo: "primary@icloud.com"}, session, nil
				},
			}
			service := newTestService(t, repo, client, &fakeLocker{}, func() time.Time { return now })
			storeSession(t, service, base, 3, apple.Session{AppleID: "owner@example.com", Region: apple.RegionGlobal, SessionToken: "fixture"})
			if _, err := service.CreateAliasWithChannel(context.Background(), 3, channel); !errors.Is(err, budgetErr) {
				t.Fatalf("creation error = %v, want budget error", err)
			}
			if claims != 1 || validations != 0 || directoryReads != 0 {
				t.Fatalf("claims=%d validations=%d directory_reads=%d", claims, validations, directoryReads)
			}
		})
	}
}

func TestCreationRateLimitPersistsAccountCooldownEvenAfterCancellation(t *testing.T) {
	for _, persistFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "persisted", true: "persistence_error"}[persistFailure], func(t *testing.T) {
			now := testAutoCreateDiagnosticNow()
			base := newFakeRepository(domain.Account{ID: 3, Email: "primary@icloud.com", Enabled: true}, now)
			claims, pauses := 0, 0
			persistErr := errors.New("cooldown persistence fixture")
			repo := &budgetTestRepository{fakeRepository: base,
				claim: func(context.Context, int64, time.Time) error { claims++; return nil },
				pause: func(ctx context.Context, id int64, until time.Time) error {
					pauses++
					if ctx.Err() != nil || id != 3 || !until.Equal(now.Add(48*time.Hour)) {
						t.Fatal("cooldown lost context, identity or Retry-After")
					}
					if persistFailure {
						return persistErr
					}
					return nil
				},
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := &fakeAppleClient{validate: func(_ context.Context, session apple.Session) (apple.Session, error) {
				cancel()
				return session, &apple.Error{Kind: apple.ErrService, StatusCode: http.StatusTooManyRequests, RetryAfter: 48 * time.Hour}
			}}
			service := newTestService(t, repo, client, &fakeLocker{}, func() time.Time { return now })
			storeSession(t, service, base, 3, apple.Session{AppleID: "owner@example.com", Region: apple.RegionGlobal, SessionToken: "fixture"})
			_, err := service.CreateAutoAlias(ctx, 3)
			if !errors.Is(err, ErrRateLimited) || claims != 1 || pauses != 1 {
				t.Fatalf("err=%v claims=%d pauses=%d", err, claims, pauses)
			}
			if persistFailure && !errors.Is(err, persistErr) {
				t.Fatalf("persistence cause lost: %v", err)
			}
		})
	}
}

func TestCreationStopsForPausedMailboxBeforeValidation(t *testing.T) {
	now := testAutoCreateDiagnosticNow()
	repo := newFakeRepository(domain.Account{ID: 3, Email: "primary@icloud.com", Enabled: true,
		LastSyncError: "login IMAP account: imap: BAD [AUTHENTICATIONFAILED] Authentication Failed"}, now)
	client := &fakeAppleClient{validate: func(context.Context, apple.Session) (apple.Session, error) {
		t.Fatal("paused mailbox reached Apple validation")
		return apple.Session{}, nil
	}}
	service := newTestService(t, repo, client, &fakeLocker{}, func() time.Time { return now })
	_, err := service.CreateAutoAlias(context.Background(), 3)
	if Code(err) != CodeMailboxAuthenticationPaused || !errors.Is(err, domain.ErrIMAPAuthenticationPaused) {
		t.Fatalf("paused mailbox error=%v", err)
	}
}
