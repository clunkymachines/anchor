package mqtt

import (
	"testing"

	"github.com/fxamacker/cbor/v2"
)

func TestDecodeJSONPayloadFlattensProperties(t *testing.T) {
	t.Parallel()

	decoded, err := decodePayload([]byte(`{"battery":87,"location":{"lat":43.6,"lon":1.44},"online":true}`), "application/json")
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}

	updates, err := flattenPayload(decoded.value)
	if err != nil {
		t.Fatalf("flatten payload: %v", err)
	}

	got := map[string]string{}
	for _, update := range updates {
		got[update.path] = update.valueJSON
	}

	if got["battery"] != "87" {
		t.Fatalf("unexpected battery value: %q", got["battery"])
	}
	if got["location.lat"] != "43.6" {
		t.Fatalf("unexpected lat value: %q", got["location.lat"])
	}
	if got["location.lon"] != "1.44" {
		t.Fatalf("unexpected lon value: %q", got["location.lon"])
	}
	if got["online"] != "true" {
		t.Fatalf("unexpected online value: %q", got["online"])
	}
}

func TestDecodeCBORPayloadWithoutContentType(t *testing.T) {
	t.Parallel()

	payload, err := cbor.Marshal(map[string]any{"temperature": 21.4})
	if err != nil {
		t.Fatalf("marshal cbor: %v", err)
	}

	decoded, err := decodePayload(payload, "")
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if decoded.format != "application/cbor" {
		t.Fatalf("expected cbor fallback, got %q", decoded.format)
	}

	updates, err := flattenPayload(decoded.value)
	if err != nil {
		t.Fatalf("flatten payload: %v", err)
	}
	if len(updates) != 1 || updates[0].path != "temperature" || updates[0].valueJSON != "21.4" {
		t.Fatalf("unexpected updates: %#v", updates)
	}
}

func TestTaskStatusFromCBORPayload(t *testing.T) {
	t.Parallel()

	payload, err := cbor.Marshal(map[string]any{
		"task":   uint64(12),
		"status": "failure",
		"msg":    "checksum mismatch",
	})
	if err != nil {
		t.Fatalf("marshal cbor: %v", err)
	}

	decoded, err := decodePayload(payload, "application/cbor")
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	update, ok := taskStatusFromPayload(decoded.value)
	if !ok {
		t.Fatal("expected task status update")
	}
	if update.taskID != 12 || update.status != "failure" || update.message != "checksum mismatch" {
		t.Fatalf("unexpected task status update: %#v", update)
	}
}

func TestParseDataTopic(t *testing.T) {
	t.Parallel()

	organisationID, deviceID, ok := parseDataTopic("dev/42/device-001/data")
	if !ok || organisationID != 42 || deviceID != "device-001" {
		t.Fatalf("unexpected topic parse: org=%d device=%q ok=%v", organisationID, deviceID, ok)
	}
	if _, _, ok := parseDataTopic("dev/42/device-001/data/temp"); ok {
		t.Fatal("expected nested data topic to be rejected")
	}
}
