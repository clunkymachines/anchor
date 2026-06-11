package mqtt

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/fxamacker/cbor/v2"
)

type decodedPayload struct {
	value       any
	payloadJSON string
	format      string
}

type propertyUpdate struct {
	path      string
	valueJSON string
	valueType string
}

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
	var updates []propertyUpdate
	flattenValue("", normalizeDecodedValue(value), &updates)
	sort.Slice(updates, func(i, j int) bool {
		return updates[i].path < updates[j].path
	})
	return updates, nil
}

// flattenValue appends leaf values from a decoded payload using dot-separated paths.
func flattenValue(path string, value any, updates *[]propertyUpdate) {
	if object, ok := value.(map[string]any); ok {
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			flattenValue(childPath, object[key], updates)
		}
		return
	}

	if path == "" {
		path = "value"
	}

	valueJSON, err := json.Marshal(value)
	if err != nil {
		valueJSON = []byte("null")
	}
	*updates = append(*updates, propertyUpdate{
		path:      path,
		valueJSON: string(valueJSON),
		valueType: valueType(value),
	})
}

// normalizeDecodedValue converts decoder-specific numeric and map types into stable Go values.
func normalizeDecodedValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, value := range typed {
			out[key] = normalizeDecodedValue(value)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(typed))
		for key, value := range typed {
			out[fmt.Sprint(key)] = normalizeDecodedValue(value)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, value := range typed {
			out[i] = normalizeDecodedValue(value)
		}
		return out
	case []byte:
		return typed
	case uint64:
		if typed <= uint64(^uint(0)>>1) {
			return int64(typed)
		}
		return strconv.FormatUint(typed, 10)
	case uint:
		return int64(typed)
	case uint8:
		return int64(typed)
	case uint16:
		return int64(typed)
	case uint32:
		return int64(typed)
	case int:
		return int64(typed)
	case int8:
		return int64(typed)
	case int16:
		return int64(typed)
	case int32:
		return int64(typed)
	default:
		return value
	}
}

// valueType maps a decoded value to the storage type used by device twin properties.
func valueType(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return "number"
	case string:
		return "string"
	case []byte:
		return "bytes"
	case []any:
		return "array"
	case map[string]any, map[any]any:
		return "object"
	default:
		return "object"
	}
}
