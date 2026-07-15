package db

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"anchor/internal/domain"
)

func TestSaveAndLoadMQTTIntegration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := Open(ctx, Config{Dialect: DialectSQLite, DSN: filepath.Join(t.TempDir(), "anchor.db")})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	if _, err := store.MQTTIntegration(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected unconfigured integration, got %v", err)
	}

	want := domain.MQTTIntegrationConfig{
		Enabled:   true,
		BrokerURL: "mqtt://broker.example.com:1883",
		ClientID:  "anchor-ingest",
		Username:  "anchor-service",
		Password:  "secret",
		QoS:       1,
	}
	if err := store.SaveMQTTIntegration(ctx, want); err != nil {
		t.Fatalf("save mqtt integration: %v", err)
	}

	got, err := store.MQTTIntegration(ctx)
	if err != nil {
		t.Fatalf("load mqtt integration: %v", err)
	}
	if !got.Configured || got.Enabled != want.Enabled || got.BrokerURL != want.BrokerURL || got.ClientID != want.ClientID || got.Username != want.Username || got.Password != want.Password || got.QoS != want.QoS || got.UpdatedAt == "" {
		t.Fatalf("unexpected mqtt integration: %#v", got)
	}

	want.Enabled = false
	want.Password = "rotated"
	want.QoS = 2
	if err := store.SaveMQTTIntegration(ctx, want); err != nil {
		t.Fatalf("update mqtt integration: %v", err)
	}
	got, err = store.MQTTIntegration(ctx)
	if err != nil {
		t.Fatalf("reload mqtt integration: %v", err)
	}
	if got.Enabled || got.Password != "rotated" || got.QoS != 2 {
		t.Fatalf("unexpected updated mqtt integration: %#v", got)
	}
}
