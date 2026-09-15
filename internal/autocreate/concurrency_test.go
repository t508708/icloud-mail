package autocreate

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"icloud-api/internal/domain"
)

type duplicateDueRepository struct{ *fakeRepository }

func (r duplicateDueRepository) ListDueAliasCreationSchedules(ctx context.Context, now time.Time) ([]domain.AliasCreationSchedule, error) {
	rows, err := r.fakeRepository.ListDueAliasCreationSchedules(ctx, now)
	if len(rows) > 0 {
		rows = append(rows, rows[0])
	}
	return rows, err
}

func TestRunDueBoundedConcurrentAndDeduplicates(t *testing.T) {
	now := time.Now().UTC()
	repo := duplicateDueRepository{newFakeRepository()}
	for id := int64(1); id <= 4; id++ {
		plan := []time.Time{now.Add(-time.Second), now.Add(time.Hour)}
		repo.schedules[id] = domain.AliasCreationSchedule{AccountID: id, Enabled: true, NextRunAt: &plan[0], PlannedAt: plan}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release := make(chan int64, 8), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var mu sync.Mutex
	active, peak := 0, 0
	calls := make(map[int64]int)
	creator := func(ctx context.Context, id int64) (domain.Alias, error) {
		mu.Lock()
		active++
		peak = max(peak, active)
		calls[id]++
		mu.Unlock()
		entered <- id
		select {
		case <-release:
		case <-ctx.Done():
		}
		mu.Lock()
		active--
		mu.Unlock()
		return domain.Alias{Address: fmt.Sprintf("a%d@icloud.com", id)}, nil
	}
	m, err := New(repo, creator, nil, WithClock(func() time.Time { return now }), WithConcurrency(2))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); m.runDue(ctx) }()
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("two accounts did not run concurrently")
		}
	}
	select {
	case <-entered:
		t.Fatal("concurrency cap exceeded before release")
	case <-time.After(20 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("workers did not finish")
	}
	mu.Lock()
	defer mu.Unlock()
	if peak != 2 || len(calls) != 4 {
		t.Fatalf("peak=%d calls=%v", peak, calls)
	}
	for id, n := range calls {
		if n != 1 {
			t.Fatalf("account %d called %d times", id, n)
		}
	}
}

func TestRunDueCancellationWaitsForWorkers(t *testing.T) {
	now := time.Now().UTC()
	repo := newFakeRepository()
	for id := int64(1); id <= 3; id++ {
		next := now
		repo.schedules[id] = domain.AliasCreationSchedule{AccountID: id, Enabled: true, NextRunAt: &next, PlannedAt: []time.Time{next}}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{}, 3)
	m, err := New(repo, func(ctx context.Context, id int64) (domain.Alias, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return domain.Alias{}, ctx.Err()
	}, nil, WithConcurrency(2), WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); m.runDue(ctx) }()
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("workers not started")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("canceled workers did not drain")
	}
}
