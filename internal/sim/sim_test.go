package sim

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
)

func TestGenerateFleetIsDeterministic(t *testing.T) {
	t.Parallel()

	cfg := Config{
		DeviceModelID:      42,
		FleetSize:          3,
		DevicePrefix:       "dev-",
		StartIndex:         10,
		MQTTUsernamePrefix: "user-",
		Secret:             "secret",
		FirmwareVersion:    "1.2.3",
	}
	first := GenerateFleet(cfg)
	second := GenerateFleet(cfg)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("expected deterministic fleet, got %#v and %#v", first, second)
	}
	if first[0].ID != "dev-10" || first[0].MQTTUsername != "user-10" || first[0].Firmware != "1.2.3" {
		t.Fatalf("unexpected first device: %#v", first[0])
	}
	if first[0].MQTTPassword != DeterministicPassword("secret", "dev-10") {
		t.Fatal("expected deterministic password derived from secret and device id")
	}
}

func TestTelemetryCBORUsesNestedObjects(t *testing.T) {
	t.Parallel()

	payload, err := TelemetryCBOR(DeviceDefinition{Firmware: "9.9.9"}, 7, map[string]any{
		"config.mode": "normal",
	})
	if err != nil {
		t.Fatalf("marshal telemetry: %v", err)
	}
	var decoded map[string]any
	if err := cbor.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode telemetry: %v", err)
	}
	if decoded["firmware"] != "9.9.9" {
		t.Fatalf("expected firmware at top level, got %#v", decoded)
	}
	config, ok := decoded["config"].(map[interface{}]interface{})
	if !ok || config["mode"] != "normal" {
		t.Fatalf("expected nested config mode, got %#v", decoded["config"])
	}
	simObject, ok := decoded["sim"].(map[interface{}]interface{})
	if !ok || simObject["sequence"] != uint64(7) {
		t.Fatalf("expected nested sim sequence, got %#v", decoded["sim"])
	}
}

func TestWriteTaskUpdatesSupportedState(t *testing.T) {
	t.Parallel()

	device := &deviceRuntime{
		def:     DeviceDefinition{ID: "sim-1", Firmware: "1.0.0"},
		cfg:     NormalizeConfig(Config{Secret: "secret", DeviceModelID: 1, AnchorBaseURL: "http://anchor", APIToken: "token", MQTTBrokerURL: "mqtt://broker"}),
		state:   defaultWritableState(),
		started: time.Now(),
		metrics: &Metrics{},
	}
	task := taskEnvelope{
		Task: 1,
		Type: "write",
		Parameters: map[string]any{
			"values": []any{
				map[string]any{"path": "config.mode", "value": "maintenance"},
			},
		},
	}
	if err := device.handleWriteTask(context.Background(), task); err != nil {
		t.Fatalf("handle write: %v", err)
	}
	if got, ok := device.valueForPath("config.mode"); !ok || got != "maintenance" {
		t.Fatalf("expected updated config.mode, got %v ok=%v", got, ok)
	}
}

func TestCBORWriteTaskEnvelopeUpdatesSupportedState(t *testing.T) {
	t.Parallel()

	payload, err := cbor.Marshal(map[string]any{
		"task": int64(1),
		"type": "write",
		"parameters": map[string]any{
			"values": []any{
				map[string]any{"path": "config.mode", "value": "maintenance"},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal task: %v", err)
	}
	var task taskEnvelope
	if err := cbor.Unmarshal(payload, &task); err != nil {
		t.Fatalf("unmarshal task: %v", err)
	}

	device := &deviceRuntime{
		def:     DeviceDefinition{ID: "sim-1", Firmware: "1.0.0"},
		cfg:     NormalizeConfig(Config{Secret: "secret", DeviceModelID: 1, AnchorBaseURL: "http://anchor", APIToken: "token", MQTTBrokerURL: "mqtt://broker"}),
		state:   defaultWritableState(),
		started: time.Now(),
		metrics: &Metrics{},
	}
	if err := device.handleWriteTask(context.Background(), task); err != nil {
		t.Fatalf("handle cbor write: %v", err)
	}
}

func TestFOTATaskValidatesHTTPStatus(t *testing.T) {
	t.Parallel()

	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	}))
	defer server.Close()

	device := &deviceRuntime{
		cfg:     NormalizeConfig(Config{Secret: "secret", DeviceModelID: 1, AnchorBaseURL: "http://anchor", APIToken: "token", MQTTBrokerURL: "mqtt://broker"}),
		metrics: &Metrics{},
	}
	task := taskEnvelope{Task: 1, Type: "fota", Parameters: map[string]any{"url": server.URL}}
	if err := device.handleFOTATask(context.Background(), task); err != nil {
		t.Fatalf("expected successful fota validation: %v", err)
	}
	status = http.StatusNotFound
	if err := device.handleFOTATask(context.Background(), task); err == nil {
		t.Fatal("expected failing HTTP status to fail FOTA validation")
	}
}
