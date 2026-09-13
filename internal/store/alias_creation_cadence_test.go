package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"icloud-api/internal/domain"
)

func TestUpgradeAliasCreationCadenceMigratesEnabledOnly(t *testing.T) {
	s := openAliasDeletionJobTestStore(t, ":memory:")
	a1, err := s.CreateAccount(context.Background(), domain.Account{Name: "cad1", Email: "cad1@icloud.com", IMAPHost: "imap", IMAPPort: 993, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	a2, err := s.CreateAccount(context.Background(), domain.Account{Name: "cad2", Email: "cad2@icloud.com", IMAPHost: "imap", IMAPPort: 993, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	a3, err := s.CreateAccount(context.Background(), domain.Account{Name: "cad3", Email: "cad3@icloud.com", IMAPHost: "imap", IMAPPort: 993, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	for _, id := range []int64{a1.ID, a2.ID, a3.ID} {
		if err := s.EnableAliasCreation(context.Background(), id, []time.Time{now.Add(time.Hour)}, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DisableAliasCreation(context.Background(), a3.ID, now); err != nil {
		t.Fatal(err)
	}
	oldAttempt := now.Add(-time.Hour)
	oldCreated := now.Add(-2 * time.Hour)
	if _, err := s.db.Exec(`UPDATE alias_creation_schedules SET last_attempted_at=?,last_created_at=?,last_alias_address=?,last_error=?,next_run_at=? WHERE account_id=?`, timestamp(oldAttempt), timestamp(oldCreated), "old@icloud.com", "cooldown", timestamp(now.Add(2*time.Hour)), a1.ID); err != nil {
		t.Fatal(err)
	}
	var calls int
	planner := func(anchor time.Time) ([]time.Time, error) {
		calls++
		out := make([]time.Time, 40)
		for i := range out {
			out[i] = anchor.Add(time.Duration(i+1) * 90 * time.Second)
		}
		return out, nil
	}
	if err := s.UpgradeAliasCreationCadence(context.Background(), "v1", now, planner); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAliasCreationSchedule(context.Background(), a1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || len(got.PlannedAt) != 40 || got.LastAliasAddress != "old@icloud.com" || got.LastError != "cooldown" || got.LastAttemptedAt == nil || got.LastCreatedAt == nil || got.NextRunAt == nil || !got.NextRunAt.After(now.Add(2*time.Hour)) {
		t.Fatalf("schedule=%+v", got)
	}
	if !got.LastAttemptedAt.Equal(oldAttempt) || !got.LastCreatedAt.Equal(oldCreated) {
		t.Fatal("history changed")
	}
	second, err := s.GetAliasCreationSchedule(context.Background(), a2.ID)
	if err != nil || !second.Enabled || len(second.PlannedAt) != 40 {
		t.Fatalf("second schedule=%+v error=%v", second, err)
	}
	disabled, err := s.GetAliasCreationSchedule(context.Background(), a3.ID)
	if err != nil || disabled.Enabled || len(disabled.PlannedAt) != 0 || disabled.NextRunAt != nil {
		t.Fatalf("disabled changed: %+v error=%v", disabled, err)
	}
	if calls != 2 {
		t.Fatalf("planner calls=%d", calls)
	}
	if err := s.UpgradeAliasCreationCadence(context.Background(), "v1", now, func(time.Time) ([]time.Time, error) { panic("planner called twice") }); err != nil {
		t.Fatal(err)
	}
}

func TestUpgradeAliasCreationCadencePlannerRollback(t *testing.T) {
	s := openAliasDeletionJobTestStore(t, ":memory:")
	a, err := s.CreateAccount(context.Background(), domain.Account{Name: "rollback", Email: "rollback@icloud.com", IMAPHost: "imap", IMAPPort: 993, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := s.EnableAliasCreation(context.Background(), a.ID, []time.Time{now.Add(time.Hour)}, now); err != nil {
		t.Fatal(err)
	}
	want := errors.New("planner failed")
	if err := s.UpgradeAliasCreationCadence(context.Background(), "rollback-v1", now, func(time.Time) ([]time.Time, error) { return nil, want }); !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
	called := false
	if err := s.UpgradeAliasCreationCadence(context.Background(), "rollback-v1", now, func(anchor time.Time) ([]time.Time, error) {
		called = true
		return []time.Time{anchor.Add(time.Minute)}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("planner was not retried")
	}
}

func TestUpgradeAliasCreationCadenceRechecksOnlyLocalBudgetWait(t *testing.T) {
	for _, message := range []string{"APPLE_CREATION_BUDGET_WAIT", "本地主号创建预算已用尽，冷却后将自动继续", "APPLE_RATE_LIMITED"} {
		t.Run(message, func(t *testing.T) {
			s := openAliasDeletionJobTestStore(t, ":memory:")
			ctx := context.Background()
			a, err := s.CreateAccount(ctx, domain.Account{Name: "cadence", Email: "cadence@icloud.com", IMAPHost: "imap", IMAPPort: 993, Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			until := now.Add(24 * time.Hour)
			if err := s.EnableAliasCreation(ctx, a.ID, []time.Time{until}, now); err != nil {
				t.Fatal(err)
			}
			if err := s.RecordAliasCreationFailure(ctx, a.ID, now, message); err != nil {
				t.Fatal(err)
			}
			if err := s.UpgradeAliasCreationCadence(ctx, "25-test", now, func(anchor time.Time) ([]time.Time, error) {
				want := now
				if message == "APPLE_RATE_LIMITED" {
					want = until
				}
				if !anchor.Equal(want) {
					t.Fatalf("anchor=%v want=%v", anchor, want)
				}
				return []time.Time{anchor.Add(2 * time.Minute)}, nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
