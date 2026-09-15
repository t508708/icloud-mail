package store

import (
	"context"
	"errors"
	"icloud-api/internal/domain"
	"testing"
	"time"
)

func TestAliasCreationJobsProgressAndInterrupt(t *testing.T) {
	s := openAliasDeletionJobTestStore(t, ":memory:")
	_ = createAliasDeletionJobTestAdmin(t, s, "creation-test")
	a, err := s.CreateAccount(context.Background(), domain.Account{Name: "creation", Email: "creation@icloud.com", Enabled: true, IMAPHost: "imap", IMAPPort: 993})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureAliasCreationJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	j := AliasCreationJob{ID: "job-1", AccountID: a.ID, Target: 30, Completed: 1, Channel: "auto", Status: "running", Entries: []AliasCreationJobEntry{{AliasID: 9, Address: "x@icloud.com", Channel: "auto", CreatedAt: now}}, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateAliasCreationJob(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if err := s.InterruptAliasCreationJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetLatestAliasCreationJob(context.Background(), a.ID)
	if err != nil || got.Status != "interrupted" || got.Completed != 1 || len(got.Entries) != 1 {
		t.Fatalf("job=%+v err=%v", got, err)
	}
	if _, err := s.GetActiveAliasCreationJob(context.Background(), a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("active err=%v", err)
	}
}

func TestAliasCreationJobsSingleActive(t *testing.T) {
	s := openAliasDeletionJobTestStore(t, ":memory:")
	_ = createAliasDeletionJobTestAdmin(t, s, "single")
	a, err := s.CreateAccount(context.Background(), domain.Account{Name: "single", Email: "single@icloud.com", Enabled: true, IMAPHost: "imap", IMAPPort: 993})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureAliasCreationJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	j := AliasCreationJob{ID: "one", AccountID: a.ID, Target: 1, Channel: "auto", Status: "running"}
	if err := s.CreateAliasCreationJob(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	j.ID = "two"
	if err := s.CreateAliasCreationJob(context.Background(), j); !errors.Is(err, ErrAliasCreationJobConflict) {
		t.Fatalf("err=%v", err)
	}
}
