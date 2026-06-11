package web

import (
	"context"

	"anchor/internal/domain"
)

type recordedTaskPublish struct {
	task           domain.DeviceTask
	organisationID int64
}

type recordedPendingPublish struct {
	deviceID       string
	organisationID int64
}

type recordingTaskPublisher struct {
	tasks   chan recordedTaskPublish
	pending chan recordedPendingPublish
}

func newRecordingTaskPublisher() *recordingTaskPublisher {
	return &recordingTaskPublisher{
		tasks:   make(chan recordedTaskPublish, 8),
		pending: make(chan recordedPendingPublish, 8),
	}
}

func (p *recordingTaskPublisher) PublishDeviceTask(_ context.Context, task domain.DeviceTask, organisationID int64) error {
	p.tasks <- recordedTaskPublish{task: task, organisationID: organisationID}
	return nil
}

func (p *recordingTaskPublisher) PublishPendingDeviceTasks(_ context.Context, deviceID string, organisationID int64) error {
	p.pending <- recordedPendingPublish{deviceID: deviceID, organisationID: organisationID}
	return nil
}
