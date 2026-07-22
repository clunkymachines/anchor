// Package telemetry contains protocol-neutral telemetry normalization and flattening.
package telemetry

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
)

var ErrDuplicatePath = errors.New("duplicate flattened telemetry path")

type PropertyUpdate struct {
	Path      string
	ValueJSON string
	ValueType string
}

// Flatten turns a decoded value into deterministic dot-separated leaf updates.
func Flatten(value any) ([]PropertyUpdate, error) {
	var updates []PropertyUpdate
	seen := make(map[string]struct{})
	if err := flatten("", Normalize(value), &updates, seen); err != nil {
		return nil, err
	}
	sort.Slice(updates, func(i, j int) bool { return updates[i].Path < updates[j].Path })
	return updates, nil
}

func flatten(path string, value any, updates *[]PropertyUpdate, seen map[string]struct{}) error {
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
			if err := flatten(childPath, object[key], updates, seen); err != nil {
				return err
			}
		}
		return nil
	}
	if path == "" {
		path = "value"
	}
	if _, exists := seen[path]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicatePath, path)
	}
	seen[path] = struct{}{}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	*updates = append(*updates, PropertyUpdate{Path: path, ValueJSON: string(encoded), ValueType: ValueType(value)})
	return nil
}

// Normalize converts decoder-specific maps and integer types to stable Go values.
func Normalize(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			out[key] = Normalize(child)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			out[fmt.Sprint(key)] = Normalize(child)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = Normalize(child)
		}
		return out
	case uint64:
		if typed <= uint64(^uint(0)>>1) {
			return int64(typed)
		}
		return strconv.FormatUint(typed, 10)
	case uint, uint8, uint16, uint32:
		return reflectUnsigned(typed)
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

func reflectUnsigned(value any) int64 {
	switch v := value.(type) {
	case uint:
		return int64(v)
	case uint8:
		return int64(v)
	case uint16:
		return int64(v)
	case uint32:
		return int64(v)
	default:
		return 0
	}
}

func ValueType(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
		return "number"
	case string:
		return "string"
	case []byte:
		return "bytes"
	case []any:
		return "array"
	default:
		return "object"
	}
}
