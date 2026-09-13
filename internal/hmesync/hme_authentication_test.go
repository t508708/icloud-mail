package hmesync

import (
	"context"
	"errors"
	"testing"
	"time"

	"icloud-api/internal/apple"
	"icloud-api/internal/domain"
)

func TestDirectoryAuthFailureKeepsValidatedCheckpointAndAccountSession(t *testing.T) {
	for _, operation := range []string{"directory", "create"} {
		for _, locked := range []bool{false, true} {
			t.Run(operation+map[bool]string{false: "/simple-lock", true: "/held-lock"}[locked], func(t *testing.T) {
				now := time.Now().UTC()
				repo := newFakeRepository(domain.Account{ID: 3, Email: "primary@icloud.com", Enabled: true}, now)
				var locker AccountLocker = &fakeLocker{}
				if locked {
					locker = newFakeAcquiringLocker()
				}
				creates, lists := 0, 0
				client := &fakeAppleClient{
					validate: func(_ context.Context, s apple.Session) (apple.Session, error) {
						s.SessionToken = "validated-checkpoint"
						return s, nil
					},
					list: func(_ context.Context, s apple.Session) (apple.ListResult, apple.Session, error) {
						lists++
						s.SessionToken = "unaccepted-directory-header"
						return apple.ListResult{}, s, &apple.Error{Op: "list Hide My Email aliases", Kind: apple.ErrHMEAuthentication, StatusCode: 401}
					},
					create: func(_ context.Context, s apple.Session, _, _ string) (apple.Alias, apple.Session, error) {
						creates++
						return apple.Alias{}, s, nil
					},
				}
				service := newTestService(t, repo, client, locker, func() time.Time { return now })
				managed := &apple.AccountSession{AppleID: "owner@example.test", APIKey: "fixture-account-key", AuthenticatedAt: now}
				storeSession(t, service, repo, 3, apple.Session{AppleID: managed.AppleID, Region: apple.RegionGlobal, DSID: "42", SessionToken: "original-checkpoint", Account: managed})
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				var err error
				if operation == "directory" {
					_, err = service.SyncAliases(ctx, 3)
				} else {
					_, err = service.CreateAutoAlias(ctx, 3)
				}
				if Code(err) != CodeHMEAuthentication || !errors.Is(err, ErrAccountActionRequired) || errors.Is(err, ErrSessionExpired) {
					t.Fatalf("classification=%s error=%v", Code(err), err)
				}
				if creates != 0 || lists != 1 || repo.imports.Load() != 0 {
					t.Fatal("directory failure retried or changed aliases")
				}
				stored, err := service.decryptSession(repo.mustSession(t, 3))
				if err != nil || stored.SessionToken != "validated-checkpoint" || stored.Account == nil || stored.Account.APIKey != managed.APIKey {
					t.Fatal("trusted checkpoint or independent Account session was lost")
				}
			})
		}
	}
}
