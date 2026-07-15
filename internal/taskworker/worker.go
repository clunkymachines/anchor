// Package taskworker owns the durable task queue's small in-process scheduler.
package taskworker

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"anchor/internal/db"
	"anchor/internal/domain"
)

// Store is the persistence surface needed by the scheduler. Keeping this
// interface small makes ProcessOnce deterministic and straightforward to test.
type Store interface {
	ExpireOverdueDeviceTasks(context.Context, time.Time) (int64, error)
	PromoteQueuedDeviceTasks(context.Context, time.Time) ([]db.PromotedTask, error)
	FinalizeFinishedCampaigns(context.Context, time.Time) (int64, error)
	SubscribeTaskScheduler(context.Context) (<-chan struct{}, func())
}

// Publisher sends a server-selected pending task to its device.
type Publisher interface {
	PublishDeviceTask(context.Context, domain.DeviceTask, int64) error
}

// Config configures a Worker. Clock and Interval are injectable for tests.
type Config struct {
	Store     Store
	Publisher Publisher
	Logger    *slog.Logger
	Clock     func() time.Time
	Interval  time.Duration
}

// Worker expires tasks, promotes device queues, finalizes campaigns, and
// dispatches promoted tasks after their database transaction commits.
type Worker struct {
	store     Store
	publisher Publisher
	logger    *slog.Logger
	clock     func() time.Time
	interval  time.Duration
	notify    chan struct{}
	startOnce sync.Once
}

// New constructs a task worker with a one-minute default interval and UTC wall
// clock.
func New(config Config) *Worker {
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	clock := config.Clock
	if clock == nil {
		clock = time.Now
	}
	interval := config.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	return &Worker{
		store:     config.Store,
		publisher: config.Publisher,
		logger:    logger,
		clock:     clock,
		interval:  interval,
		notify:    make(chan struct{}, 1),
	}
}

// Notify requests a run without blocking or creating another goroutine.
func (w *Worker) Notify() {
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

// ProcessOnce runs one complete scheduler pass in the required order. A
// publish failure is logged and deliberately does not change durable state or
// prevent other promoted tasks from being attempted.
func (w *Worker) ProcessOnce(ctx context.Context) error {
	if w.store == nil {
		return nil
	}
	now := w.clock().UTC()
	if _, err := w.store.ExpireOverdueDeviceTasks(ctx, now); err != nil {
		w.logger.Error("task expiry failed", "err", err)
		return err
	}
	promoted, err := w.store.PromoteQueuedDeviceTasks(ctx, now)
	if err != nil {
		w.logger.Error("queued task promotion failed", "err", err)
		return err
	}
	for _, promotedTask := range promoted {
		if w.publisher == nil {
			continue
		}
		if err := w.publisher.PublishDeviceTask(ctx, promotedTask.Task, promotedTask.OrganisationID); err != nil {
			w.logger.Error("promoted task publish failed",
				"task_id", promotedTask.Task.ID,
				"device_id", promotedTask.Task.DeviceID,
				"organisation_id", promotedTask.OrganisationID,
				"err", err,
			)
		}
	}
	if _, err := w.store.FinalizeFinishedCampaigns(ctx, now); err != nil {
		w.logger.Error("campaign finalization failed", "err", err)
		return err
	}
	return nil
}

// Start runs an immediate pass and then responds to durable task notifications
// and periodic ticks. Notifications are coalesced by both the store and the
// worker channel.
func (w *Worker) Start(ctx context.Context) {
	w.startOnce.Do(func() {
		if w.store == nil {
			return
		}
		signals, unsubscribe := w.store.SubscribeTaskScheduler(ctx)
		go w.run(ctx, signals, unsubscribe)
	})
}

func (w *Worker) run(ctx context.Context, signals <-chan struct{}, unsubscribe func()) {
	defer unsubscribe()
	_ = w.ProcessOnce(ctx)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-signals:
			_ = w.ProcessOnce(ctx)
		case <-w.notify:
			_ = w.ProcessOnce(ctx)
		case <-ticker.C:
			_ = w.ProcessOnce(ctx)
		}
	}
}
