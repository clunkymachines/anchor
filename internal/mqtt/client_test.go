package mqtt

import (
	"context"
	"log/slog"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"anchor/internal/db"
	"anchor/internal/domain"

	"github.com/eclipse/paho.golang/paho"
	"github.com/fxamacker/cbor/v2"
)

func TestHandlePublishRecordsEventAndTwin(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := db.Open(ctx, db.Config{
		Dialect: db.DialectSQLite,
		DSN:     filepath.Join(t.TempDir(), "anchor.db"),
	})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	organisationID, err := store.CreateOrganisation(ctx, domain.Organisation{Name: "Test Org"})
	if err != nil {
		t.Fatalf("create organisation: %v", err)
	}
	if err := store.SaveDeviceWithMQTTCredential(ctx, domain.DeviceWithMQTTCredential{
		Device: domain.Device{
			ID:               "device-001",
			OrganisationID:   organisationID,
			ModelName:        "Sensor",
			SoftwareVersions: domain.SoftwareVersions{},
		},
		Credential: domain.DeviceMQTTCredential{
			DeviceID:     "device-001",
			Username:     "device-001",
			PasswordHash: "hash",
			Enabled:      true,
		},
	}); err != nil {
		t.Fatalf("save device with mqtt credential: %v", err)
	}

	client := NewClient(store, Config{}, slog.Default())
	client.handlePublish(ctx, &paho.Publish{
		Topic:   "dev/" + int64String(organisationID) + "/device-001/data",
		Payload: []byte(`{"battery":87,"location":{"lat":43.6,"lon":1.44}}`),
		Properties: &paho.PublishProperties{
			ContentType: "application/json",
		},
	})

	properties, err := store.ListDeviceTwinProperties(ctx, "device-001", organisationID)
	if err != nil {
		t.Fatalf("list twin properties: %v", err)
	}
	got := map[string]string{}
	for _, property := range properties {
		got[property.Path] = property.ValueJSON
	}
	if got["battery"] != "87" || got["location.lat"] != "43.6" || got["location.lon"] != "1.44" {
		t.Fatalf("unexpected twin properties: %#v", properties)
	}

	events, err := store.ListRecentDeviceEvents(ctx, "device-001", organisationID, 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 || events[0].PayloadJSON == "" || events[0].ContentFormat != "application/json" {
		t.Fatalf("unexpected events: %#v", events)
	}
}

func TestHandlePublishUpdatesTaskStatus(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := db.Open(ctx, db.Config{
		Dialect: db.DialectSQLite,
		DSN:     filepath.Join(t.TempDir(), "anchor.db"),
	})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	organisationID, err := store.CreateOrganisation(ctx, domain.Organisation{Name: "Test Org"})
	if err != nil {
		t.Fatalf("create organisation: %v", err)
	}
	if err := store.SaveDeviceWithMQTTCredential(ctx, domain.DeviceWithMQTTCredential{
		Device: domain.Device{
			ID:               "device-001",
			OrganisationID:   organisationID,
			ModelName:        "Sensor",
			SoftwareVersions: domain.SoftwareVersions{},
		},
		Credential: domain.DeviceMQTTCredential{
			DeviceID:     "device-001",
			Username:     "device-001",
			PasswordHash: "hash",
			Enabled:      true,
		},
	}); err != nil {
		t.Fatalf("save device with mqtt credential: %v", err)
	}

	taskID, err := store.CreateDeviceTask(ctx, domain.DeviceTask{
		DeviceID:  "device-001",
		Type:      "fota",
		Parameter: "1",
		Status:    db.DeviceTaskStatusPending,
		CreatedAt: "2026-06-06T08:00:00Z",
	}, organisationID)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	client := NewClient(store, Config{}, slog.Default())
	client.handlePublish(ctx, &paho.Publish{
		Topic:   "dev/" + int64String(organisationID) + "/device-001/data",
		Payload: []byte(`{"task":` + int64String(taskID) + `,"status":"in_progress","msg":"downloading"}`),
		Properties: &paho.PublishProperties{
			ContentType: "application/json",
		},
	})

	tasks, err := store.ListOngoingDeviceTasks(ctx, "device-001", organisationID)
	if err != nil {
		t.Fatalf("list ongoing tasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].ID != taskID || tasks[0].Status != db.DeviceTaskStatusInProgress {
		t.Fatalf("expected task in progress, got %#v", tasks)
	}

	client.handlePublish(ctx, &paho.Publish{
		Topic:   "dev/" + int64String(organisationID) + "/device-001/data",
		Payload: []byte(`{"task":` + int64String(taskID) + `,"status":"success"}`),
		Properties: &paho.PublishProperties{
			ContentType: "application/json",
		},
	})

	tasks, err = store.ListOngoingDeviceTasks(ctx, "device-001", organisationID)
	if err != nil {
		t.Fatalf("list ongoing tasks after success: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected completed task to leave ongoing list, got %#v", tasks)
	}
}

func TestDeviceTaskPublishBuildsCBORDocument(t *testing.T) {
	t.Parallel()

	publish, err := deviceTaskPublish(42, domain.DeviceTask{
		ID:        7,
		DeviceID:  "device-001",
		Type:      "fota",
		Parameter: "/org/42/releases/9/binary",
		Status:    db.DeviceTaskStatusPending,
		CreatedAt: "2026-06-06T08:00:00Z",
	}, 1)
	if err != nil {
		t.Fatalf("build publish: %v", err)
	}
	if publish.Topic != "dev/42/device-001/task" || publish.QoS != 1 || publish.Properties.ContentType != "application/cbor" {
		t.Fatalf("unexpected publish metadata: %#v", publish)
	}

	var payload map[string]any
	if err := cbor.Unmarshal(publish.Payload, &payload); err != nil {
		t.Fatalf("decode publish payload: %v", err)
	}
	payload = normalizeDecodedValue(payload).(map[string]any)
	if payload["task"] != int64(7) || payload["type"] != "fota" || payload["parameter"] != "/org/42/releases/9/binary" || payload["status"] != "pending" {
		t.Fatalf("unexpected publish payload: %#v", payload)
	}
}

func TestMQTTReconnectBackoffDoesNotPanic(t *testing.T) {
	t.Parallel()

	backoff := mqttReconnectBackoff()
	if delay := backoff(1); delay < time.Second || delay > 30*time.Second {
		t.Fatalf("unexpected backoff delay: %s", delay)
	}
}

func int64String(value int64) string {
	return strconv.FormatInt(value, 10)
}
