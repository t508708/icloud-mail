package autocreate

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"icloud-api/internal/domain"
)

type poolGateRepository struct {
	*fakeRepository
	allowed bool
}

func (r *poolGateRepository) PoolCreationAllowed(context.Context, int64) (bool, error) {
	return r.allowed, nil
}

func TestPoolTargetSkipsRemoteCreationAndResumesOnNextSlot(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Now())
	repo := &poolGateRepository{fakeRepository: newFakeRepository()}
	calls := 0
	m, err := New(repo, func(context.Context, int64) (domain.Alias, error) {
		calls++
		return domain.Alias{Address: "created@icloud.com"}, nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), WithClock(clock.Now), WithRandom(fixedRandom(0)))
	if err != nil {
		t.Fatal(err)
	}
	schedule := enableForTest(t, m, 1)
	clock.Set(*schedule.NextRunAt)
	m.processDue(ctx, schedule)
	if calls != 0 {
		t.Fatal("created above stock target")
	}
	next, err := m.GetSchedule(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if next.NextRunAt == nil || !next.NextRunAt.After(*schedule.NextRunAt) {
		t.Fatal("stocked schedule did not advance")
	}
	repo.allowed = true
	clock.Set(*next.NextRunAt)
	m.processDue(ctx, next)
	if calls != 1 {
		t.Fatal("creation did not resume after stock dropped")
	}
}
