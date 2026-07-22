package telemetry

import (
	"errors"
	"reflect"
	"testing"
)

func TestFlattenDeterministicTypedLeaves(t *testing.T) {
	updates, err := Flatten(map[string]any{"z": []any{1.0, true}, "a": map[string]any{"null": nil, "text": "ok"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []PropertyUpdate{
		{Path: "a.null", ValueJSON: "null", ValueType: "null"},
		{Path: "a.text", ValueJSON: `"ok"`, ValueType: "string"},
		{Path: "z", ValueJSON: `[1,true]`, ValueType: "array"},
	}
	if !reflect.DeepEqual(updates, want) {
		t.Fatalf("updates = %#v, want %#v", updates, want)
	}
}

func TestFlattenRejectsCollidingPaths(t *testing.T) {
	_, err := Flatten(map[string]any{"a": map[string]any{"b": 1}, "a.b": 2})
	if !errors.Is(err, ErrDuplicatePath) {
		t.Fatalf("expected duplicate path, got %v", err)
	}
}
