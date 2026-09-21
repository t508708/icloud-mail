package autocreate

import (
	"context"
	"testing"
	"time"

	"icloud-api/internal/apple"
	"icloud-api/internal/domain"
)

type channelWaitTestError struct{ error }

func (channelWaitTestError) CreationChannelScoped() bool { return true }
func (e channelWaitTestError) Unwrap() error             { return e.error }

func TestCreationRateLimitDefaultAndLegacyStatus(t *testing.T) {
	if appleRateLimitCooldown != time.Hour {
		t.Fatalf("default pause=%s", appleRateLimitCooldown)
	}
	for _, status := range []string{"APPLE_RATE_LIMITED", failureMessage(&apple.Error{Kind: apple.ErrService, StatusCode: 429}), "Apple 请求被限流，当前周期剩余计划槽已跳过，冷却后会继续执行"} {
		if !IsRateLimitStatus(status) {
			t.Fatalf("unrecognized stored throttle: %s", status)
		}
	}
}

func TestChannelScheduleDistributesNineteenAndFourAcrossRestarts(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC))
	repo := newFakeRepository()
	counts := map[string]int{}
	var webSlots []int
	attempts := 0
	creator := func(ctx context.Context, _ int64) (domain.Alias, error) {
		channel := domain.ScheduledCreationChannel(ctx)
		counts[channel]++
		if channel == "icloud_web" {
			webSlots = append(webSlots, attempts)
		}
		attempts++
		return domain.Alias{ID: 1, Address: "fixture@icloud.com"}, nil
	}
	newManager := func() *Manager {
		m, err := New(repo, creator, nil, WithClock(clock.Now), WithRandom(&testRandom{state: 3}))
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	m := newManager()
	if _, err := m.SetEnabled(ctx, 1, true); err != nil {
		t.Fatal(err)
	}
	for range 23 {
		// A fresh manager must derive channel order from the durable plan.
		m = newManager()
		s, err := m.GetSchedule(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		clock.Set(*s.NextRunAt)
		m.runDue(ctx)
	}
	if counts["apple_account"] != 19 || counts["icloud_web"] != 4 {
		t.Fatalf("counts=%v", counts)
	}
	for i, want := range []int{5, 11, 17, 22} {
		if webSlots[i] != want {
			t.Fatalf("Web slot=%v", webSlots)
		}
	}
}

func TestChannelWaitPreservesNextSlotInsteadOfPausingAccount(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC))
	repo := newFakeRepository()
	m, err := New(repo, func(context.Context, int64) (domain.Alias, error) {
		return domain.Alias{}, channelWaitTestError{&apple.Error{Kind: apple.ErrService, StatusCode: 429, RetryAfter: time.Hour}}
	}, nil, WithClock(clock.Now), WithRandom(&testRandom{state: 4}))
	if err != nil {
		t.Fatal(err)
	}
	s, err := m.SetEnabled(ctx, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	want := s.PlannedAt[1]
	clock.Set(*s.NextRunAt)
	m.runDue(ctx)
	s, err = m.GetSchedule(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Enabled || s.NextRunAt == nil || !s.NextRunAt.Equal(want) {
		t.Fatalf("channel pause shifted whole account: %+v", s)
	}
}
