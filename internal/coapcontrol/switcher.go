package coapcontrol

import (
	"context"
	"errors"
	"sync"

	"anchor/internal/coapapi"
	"anchor/internal/domain"
)

// Switcher applies persisted CoAP integration settings without replacing the
// backend task dispatcher. It contains no task queue; tasks stay durable in
// Anchor until the active frontend accepts a dispatch.
type Switcher struct {
	mu                  sync.RWMutex
	manager             *Manager
	fotaDownloadBaseURL string
}

func NewSwitcher(fotaDownloadBaseURL string) *Switcher {
	return &Switcher{fotaDownloadBaseURL: fotaDownloadBaseURL}
}

func (s *Switcher) ApplyCoAPIntegration(_ context.Context, config domain.CoAPIntegrationConfig) error {
	var manager *Manager
	var err error
	if config.Enabled {
		manager, err = New(Config{BaseURL: config.FrontendURL, BearerToken: config.BearerToken, FOTADownloadBaseURL: s.fotaDownloadBaseURL})
		if err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.manager = manager
	s.mu.Unlock()
	return nil
}
func (s *Switcher) current() *Manager { s.mu.RLock(); defer s.mu.RUnlock(); return s.manager }
func (s *Switcher) PublishDeviceTask(ctx context.Context, task domain.DeviceTask, organisationID int64) error {
	m := s.current()
	if m == nil {
		return errors.New("CoAP integration is disabled")
	}
	return m.PublishDeviceTask(ctx, task, organisationID)
}
func (s *Switcher) PublishPendingDeviceTasks(context.Context, string, int64) error { return nil }
func (s *Switcher) Invalidate(ctx context.Context, deviceID string, revision int64, force bool) error {
	m := s.current()
	if m == nil {
		return nil
	}
	return m.Invalidate(ctx, deviceID, revision, force)
}
func (s *Switcher) Association(ctx context.Context, deviceID string) (coapapi.AssociationStatus, error) {
	m := s.current()
	if m == nil {
		return coapapi.AssociationStatus{DeviceID: deviceID}, errors.New("CoAP integration is disabled")
	}
	return m.Association(ctx, deviceID)
}
func (s *Switcher) IntegrationStatus(ctx context.Context) domain.CoAPIntegrationStatus {
	m := s.current()
	if m == nil {
		return domain.CoAPIntegrationStatus{State: domain.CoAPIntegrationDisabled, Reason: "The integration is inactive."}
	}
	return m.IntegrationStatus(ctx)
}
