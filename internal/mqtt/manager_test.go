package mqtt

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"anchor/internal/domain"
)

func TestManagerTracksConnectionStatusByGeneration(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, "", slog.Default())
	initial := manager.MQTTIntegrationStatus()
	if initial.State != domain.MQTTConnectionDisabled || initial.Reason == "" || initial.UpdatedAt == "" {
		t.Fatalf("unexpected initial status: %#v", initial)
	}

	manager.mu.Lock()
	manager.generation = 2
	manager.mu.Unlock()
	manager.setStatus(1, domain.MQTTConnectionConnected, "stale connection")
	if got := manager.MQTTIntegrationStatus(); got.State != domain.MQTTConnectionDisabled {
		t.Fatalf("expected stale status callback to be ignored, got %#v", got)
	}

	manager.setStatus(2, domain.MQTTConnectionFailed, "connection refused")
	got := manager.MQTTIntegrationStatus()
	if got.State != domain.MQTTConnectionFailed || got.Reason != "connection refused" || got.UpdatedAt == "" {
		t.Fatalf("unexpected failed status: %#v", got)
	}
}

func TestManagerReportsImmediateClientStartFailure(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, "", slog.Default())
	err := manager.ApplyMQTTIntegration(context.Background(), domain.MQTTIntegrationConfig{
		Enabled: true, BrokerURL: "://invalid", ClientID: "anchor", Username: "anchor", Password: "secret",
	})
	if err == nil {
		t.Fatal("expected invalid broker URL to fail")
	}
	status := manager.MQTTIntegrationStatus()
	if status.State != domain.MQTTConnectionFailed || status.Reason == "" {
		t.Fatalf("expected failed connection status, got %#v", status)
	}
	manager.Close()
	if errors.Is(err, context.Canceled) {
		t.Fatalf("expected broker URL error, got %v", err)
	}
}
