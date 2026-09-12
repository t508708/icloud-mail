package autocreate

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"icloud-api/internal/domain"
)

func TestScheduleUsesFortySlotsAcrossOneHour(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	repo := newFakeRepository()
	m, err := New(repo, func(context.Context, int64) (domain.Alias, error) { return domain.Alias{}, nil }, slog.Default(), WithClock(func() time.Time { return now }), WithRandom(fixedRandom(0)))
	if err != nil {
		t.Fatal(err)
	}
	s, err := m.SetEnabled(context.Background(), 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.PlannedAt) != 40 {
		t.Fatalf("slots=%d", len(s.PlannedAt))
	}
	if span := s.PlannedAt[len(s.PlannedAt)-1].Sub(now); span != time.Hour {
		t.Fatalf("span=%v, want one hour from anchor", span)
	}
	previous := now
	for i, deadline := range s.PlannedAt {
		if gap := deadline.Sub(previous); gap < time.Minute {
			t.Fatalf("gap[%d]=%v", i, gap)
		}
		previous = deadline
	}
}

func TestMultipleAccountsEachHaveIndependentFortySlotPlans(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	repo := newFakeRepository()
	m, err := New(repo, func(context.Context, int64) (domain.Alias, error) { return domain.Alias{}, nil }, slog.Default(), WithClock(func() time.Time { return now }), WithRandom(fixedRandom(0)))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{10, 11, 12} {
		s, e := m.SetEnabled(context.Background(), id, true)
		if e != nil {
			t.Fatal(e)
		}
		if len(s.PlannedAt) != 40 {
			t.Fatalf("account %d slots=%d", id, len(s.PlannedAt))
		}
	}
}
