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
	TaskTypeRead  = "read"
	TaskTypeWrite = "write"
	TaskTypeFOTA  = "fota"

	TaskPathMaxEntries = 32
	TaskPathMaxLength  = 128
)

type ReadTaskParameters struct {
	Paths []string `json:"paths"`
}

type WriteTaskParameters struct {
	Values []WriteTaskValue `json:"values"`
}

type WriteTaskValue struct {
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

type FOTATaskParameters struct {
	ReleaseID int64 `json:"release_id"`
}

func BuildReadTaskParameters(paths []string) (string, error) {
	normalized, err := normalizeTaskPaths(paths)
	if err != nil {
		return "", err
	}
	return marshalTaskParameters(ReadTaskParameters{Paths: normalized})
}

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

func BuildFOTATaskParameters(releaseID int64) (string, error) {
	if releaseID <= 0 {
		return "", errors.New("release_id is required")
	}
	return marshalTaskParameters(FOTATaskParameters{ReleaseID: releaseID})
}

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
