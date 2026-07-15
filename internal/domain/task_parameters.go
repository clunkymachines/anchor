package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

const (
	// TaskTypeRead requests current values for one or more device paths.
	TaskTypeRead = "read"
	// TaskTypeWrite updates one or more writable device paths.
	TaskTypeWrite = "write"
	// TaskTypeFOTA requests installation of a firmware release.
	TaskTypeFOTA = "fota"

	// TaskPathMaxEntries limits paths or values in one task.
	TaskPathMaxEntries = 32
	// TaskPathMaxLength limits an individual task path in bytes.
	TaskPathMaxLength = 128
)

// ReadTaskParameters is the JSON payload for a read task.
type ReadTaskParameters struct {
	Paths []string `json:"paths"`
}

// WriteTaskParameters is the JSON payload for a write task.
type WriteTaskParameters struct {
	Values []WriteTaskValue `json:"values"`
}

// WriteTaskValue associates a normalized device path with an arbitrary JSON value.
type WriteTaskValue struct {
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

// FOTATaskParameters identifies the firmware release installed by a FOTA task.
type FOTATaskParameters struct {
	ReleaseID int64 `json:"release_id"`
}

// BuildReadTaskParameters validates and normalizes paths, then returns the task
// parameters as JSON.
func BuildReadTaskParameters(paths []string) (string, error) {
	normalized, err := normalizeTaskPaths(paths)
	if err != nil {
		return "", err
	}
	return marshalTaskParameters(ReadTaskParameters{Paths: normalized})
}

// BuildWriteTaskParameters validates a JSON write-task payload, normalizes its
// paths and values, and returns canonical compact JSON.
func BuildWriteTaskParameters(input string) (string, error) {
	var params WriteTaskParameters
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("write values must be valid JSON")
	}
	if len(params.Values) == 0 {
		return "", errors.New("write task needs at least one value")
	}
	if len(params.Values) > TaskPathMaxEntries {
		return "", fmt.Errorf("write task can include at most %d values", TaskPathMaxEntries)
	}

	seen := make(map[string]struct{}, len(params.Values))
	for i := range params.Values {
		path, err := NormalizeTaskPath(params.Values[i].Path)
		if err != nil {
			return "", err
		}
		if _, ok := seen[path]; ok {
			return "", fmt.Errorf("duplicate path %q", path)
		}
		seen[path] = struct{}{}
		params.Values[i].Path = path

		if len(params.Values[i].Value) == 0 {
			return "", fmt.Errorf("write value for %q is required", path)
		}
		if !json.Valid(params.Values[i].Value) {
			return "", fmt.Errorf("write value for %q must be valid JSON", path)
		}
		params.Values[i].Value = json.RawMessage(bytes.TrimSpace(params.Values[i].Value))
	}

	return marshalTaskParameters(params)
}

// BuildFOTATaskParameters validates releaseID and returns FOTA parameters as JSON.
func BuildFOTATaskParameters(releaseID int64) (string, error) {
	if releaseID <= 0 {
		return "", errors.New("release_id is required")
	}
	return marshalTaskParameters(FOTATaskParameters{ReleaseID: releaseID})
}

// ParseFOTATaskParameters parses and validates FOTA parameters from JSON.
func ParseFOTATaskParameters(parametersJSON string) (FOTATaskParameters, error) {
	var params FOTATaskParameters
	if err := json.Unmarshal([]byte(parametersJSON), &params); err != nil {
		return FOTATaskParameters{}, err
	}
	if params.ReleaseID <= 0 {
		return FOTATaskParameters{}, errors.New("release_id is required")
	}
	return params, nil
}

// NormalizeTaskPath trims path and rejects empty, oversized, whitespace, and
// control-character-containing paths.
func NormalizeTaskPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("path is required")
	}
	if len(path) > TaskPathMaxLength {
		return "", fmt.Errorf("path %q is longer than %d characters", path, TaskPathMaxLength)
	}
	for _, r := range path {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", fmt.Errorf("path %q contains unsupported whitespace or control characters", path)
		}
	}
	return path, nil
}

func normalizeTaskPaths(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, errors.New("task needs at least one path")
	}
	if len(paths) > TaskPathMaxEntries {
		return nil, fmt.Errorf("task can include at most %d paths", TaskPathMaxEntries)
	}

	normalized := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, rawPath := range paths {
		path, err := NormalizeTaskPath(rawPath)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[path]; ok {
			return nil, fmt.Errorf("duplicate path %q", path)
		}
		seen[path] = struct{}{}
		normalized = append(normalized, path)
	}
	return normalized, nil
}

func marshalTaskParameters(value any) (string, error) {
	bytes, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}
