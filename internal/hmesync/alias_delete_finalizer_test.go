package hmesync

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"icloud-api/internal/apple"
	"icloud-api/internal/domain"
)

func TestDeleteAliasesCustomFinalizerReplacesDefaultLocalDeletion(t *testing.T) {
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	repo := newFakeRepository(domain.Account{ID: 3, Email: "primary@icloud.com", Enabled: true}, now)
	repo.addAlias(domain.Alias{ID: 41, AccountID: 3, Address: "alias@icloud.com", Enabled: true})
	client := &fakeAppleClient{
		validate: func(_ context.Context, session apple.Session) (apple.Session, error) { return session, nil },
		list: func(_ context.Context, session apple.Session) (apple.ListResult, apple.Session, error) {
			result := aliasDeletionDirectory()
			result.Aliases = []apple.Alias{{
				AnonymousID: "remote-id", HME: "alias@icloud.com",
				ForwardToEmail: "primary@icloud.com", IsActive: false,
			}}
			return result, session, nil
		},
		deleteRemote: func(_ context.Context, session apple.Session, anonymousID string) (apple.Session, error) {
			if anonymousID != "remote-id" {
				t.Fatalf("remote alias ID = %q", anonymousID)
			}
			return session, nil
		},
	}
	service := newTestService(t, repo, client, &fakeLocker{}, func() time.Time { return now })
	storeSession(t, service, repo, 3, apple.Session{
		AppleID: "owner@example.com", Region: apple.RegionGlobal, SessionToken: "initial-session",
	})

	var finalized atomic.Int32
	var finalizedID atomic.Int64
	ctx := WithAliasDeletionFinalizer(context.Background(), func(_ context.Context, aliasID int64) error {
		finalized.Add(1)
		finalizedID.Store(aliasID)
		return nil
	})
	outcomes, err := service.DeleteAliases(ctx, []int64{41})
	if err != nil || len(outcomes) != 1 || outcomes[0].Err != nil {
		t.Fatalf("delete outcomes = %#v, err = %v", outcomes, err)
	}
	if finalized.Load() != 1 || finalizedID.Load() != 41 {
		t.Fatalf("finalizer calls = %d, id = %d", finalized.Load(), finalizedID.Load())
	}
	if !repo.hasAlias(41) || repo.aliasDeletes.Load() != 0 {
		t.Fatalf("default local deletion was not suppressed: exists=%v deletes=%d", repo.hasAlias(41), repo.aliasDeletes.Load())
	}
}
