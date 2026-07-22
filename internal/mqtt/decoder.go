package mqtt

import (
	"encoding/json"
	"strings"

	"anchor/internal/telemetry"

	"github.com/fxamacker/cbor/v2"
)

type decodedPayload struct {
	value       any
	payloadJSON string
	format      string
}

type propertyUpdate struct{ path, valueJSON, valueType string }

// decodePayload decodes a telemetry payload using content type metadata when present.
func decodePayload(payload []byte, contentType string) (decodedPayload, error) {
	contentType = strings.ToLower(contentType)

	if strings.Contains(contentType, "json") {
		return decodeJSON(payload)
	}
	if strings.Contains(contentType, "cbor") {
		return decodeCBOR(payload)
	}

	if decoded, err := decodeCBOR(payload); err == nil {
		return decoded, nil
	}
	return decodeJSON(payload)
}

// decodeJSON decodes JSON telemetry and stores its canonical JSON representation.
func decodeJSON(payload []byte) (decodedPayload, error) {
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return decodedPayload{}, err
	}

	payloadJSON, err := json.Marshal(value)
	if err != nil {
		return decodedPayload{}, err
	}

	return decodedPayload{
		value:       normalizeDecodedValue(value),
		payloadJSON: string(payloadJSON),
		format:      "application/json",
	}, nil
}

// decodeCBOR decodes CBOR telemetry and stores an equivalent JSON representation.
func decodeCBOR(payload []byte) (decodedPayload, error) {
	var value any
	if err := cbor.Unmarshal(payload, &value); err != nil {
		return decodedPayload{}, err
	}

	value = normalizeDecodedValue(value)
	payloadJSON, err := json.Marshal(value)
	if err != nil {
		return decodedPayload{}, err
	}

	return decodedPayload{
		value:       value,
		payloadJSON: string(payloadJSON),
		format:      "application/cbor",
	}, nil
}

// flattenPayload turns a decoded payload into sorted device twin property updates.
func flattenPayload(value any) ([]propertyUpdate, error) {
	flat, err := telemetry.Flatten(value)
	if err != nil {
		return nil, err
	}
	updates := make([]propertyUpdate, len(flat))
	for i, update := range flat {
		updates[i] = propertyUpdate{update.Path, update.ValueJSON, update.ValueType}
	}
	return updates, nil
}

// normalizeDecodedValue converts decoder-specific numeric and map types into stable Go values.
func normalizeDecodedValue(value any) any {
	return telemetry.Normalize(value)
}

// valueType maps a decoded value to the storage type used by device twin properties.
func valueType(value any) string {
	return telemetry.ValueType(value)
}
