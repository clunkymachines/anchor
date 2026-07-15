package taskworker

import (
	"context"
	"errors"
	"testing"
	"time"

	"anchor/internal/db"
	"anchor/internal/domain"
)

type fakeStore struct {
	calls    []string
	promoted []db.PromotedTask
	finalErr error
}

func (s *fakeStore) ExpireOverdueDeviceTasks(context.Context, time.Time) (int64, error) {
	s.calls = append(s.calls, "expire")
	return 0, nil
}

func (s *fakeStore) PromoteQueuedDeviceTasks(context.Context, time.Time) ([]db.PromotedTask, error) {
	s.calls = append(s.calls, "promote")
	return s.promoted, nil
}

func (s *fakeStore) FinalizeFinishedCampaigns(context.Context, time.Time) (int64, error) {
	s.calls = append(s.calls, "finalize")
	return 0, s.finalErr
}

func (s *fakeStore) SubscribeTaskScheduler(context.Context) (<-chan struct{}, func()) {
	return make(chan struct{}), func() {}
}

type fakePublisher struct {
	tasks  []domain.DeviceTask
	failed map[int64]bool
}

func (p *fakePublisher) PublishDeviceTask(_ context.Context, task domain.DeviceTask, _ int64) error {
	p.tasks = append(p.tasks, task)
	if p.failed[task.ID] {
		return errors.New("publish failed")
	}
	return nil
}

func TestProcessOnceOrdersQueueWorkAndAttemptsEveryPublish(t *testing.T) {
	store := &fakeStore{promoted: []db.PromotedTask{
		{Task: domain.DeviceTask{ID: 1, DeviceID: "a"}},
		{Task: domain.DeviceTask{ID: 2, DeviceID: "b"}},
	}}
	publisher := &fakePublisher{failed: map[int64]bool{1: true}}
	worker := New(Config{
		Store:     store,
		Publisher: publisher,
		Clock:     func() time.Time { return time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC) },
	})

	if err := worker.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("process once: %v", err)
	}
	if got, want := store.calls, []string{"expire", "promote", "finalize"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("unexpected scheduler order: got %#v want %#v", got, want)
	}
	if len(publisher.tasks) != 2 {
		t.Fatalf("expected both promoted tasks to be attempted, got %#v", publisher.tasks)
	}
}
