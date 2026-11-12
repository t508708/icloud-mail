package autocreate

import (
	"context"
	"net/http"
	"testing"
	"time"

	"icloud-api/internal/apple"
	"icloud-api/internal/domain"
)

func TestCreationFailureScheduleAndDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		kind                  error
		code, category, event string
		enabled               bool
	}{
		{apple.ErrAliasCapacity, "APPLE_ALIAS_CAPACITY_REACHED", "capacity", "run_failed", false},
		{apple.ErrCreationNotAllowed, "APPLE_CREATION_NOT_ALLOWED", "creation_not_allowed", "run_failed", true},
		{apple.ErrRateLimited, "APPLE_RATE_LIMITED", "rate_limited", "run_rate_limited", true},
	} {
		t.Run(tc.code, func(t *testing.T) {
			clock := newTestClock(time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC))
			repo := newFakeRepository()
			calls := 0
			upstream := &apple.Error{Kind: tc.kind, Op: "account POST /account/manage/email/private/add", StatusCode: http.StatusPreconditionFailed,
				AccountFailure: &apple.AccountFailureDiagnostics{Category: tc.category, Evidence: "unclassified_precondition"}}
			manager, logs := newFlowLogManager(t, repo, clock, func(ctx context.Context, _ int64) (domain.Alias, error) {
				calls++
				domain.ReportAliasCreationProgress(ctx, domain.AliasCreationPhaseReserving, 65, 0)
				return domain.Alias{}, upstream
			})
			schedule := enableForTest(t, manager, 8)
			clock.Set(*schedule.NextRunAt)
			manager.runDue(context.Background())
			failed := requireAutoCreateEvent(t, logs, tc.event)
			if failed.Fields["error_code"] != tc.code || failed.Fields["account_failure_diagnostics.category"] != tc.category {
				t.Fatalf("fields=%+v", failed.Fields)
			}
			current, err := manager.GetSchedule(context.Background(), 8)
			if err != nil || current.Enabled != tc.enabled || calls != 1 {
				t.Fatalf("schedule=%+v calls=%d err=%v", current, calls, err)
			}
			if tc.enabled && (current.NextRunAt == nil || !current.NextRunAt.After(clock.Now())) {
				t.Fatal("failure must not cause an immediate retry")
			}
		})
	}
}
