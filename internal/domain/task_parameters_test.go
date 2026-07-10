package domain

import (
	"strings"
	"testing"
)

func TestBuildReadTaskParametersValidatesPaths(t *testing.T) {
	t.Parallel()

	tooManyPaths := make([]string, TaskPathMaxEntries+1)
	for i := range tooManyPaths {
		tooManyPaths[i] = "path" + string(rune('a'+i))
	}

	tests := []struct {
		name  string
		paths []string
	}{
		{name: "empty", paths: nil},
		{name: "duplicate", paths: []string{"battery.percent", "battery.percent"}},
		{name: "whitespace", paths: []string{"battery percent"}},
		{name: "control", paths: []string{"battery\npercent"}},
		{name: "too many", paths: tooManyPaths},
		{name: "too long", paths: []string{strings.Repeat("a", TaskPathMaxLength+1)}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := BuildReadTaskParameters(tt.paths); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}

	got, err := BuildReadTaskParameters([]string{" battery.percent ", "lwm2m./3/0/9"})
	if err != nil {
		t.Fatalf("build read parameters: %v", err)
	}
	if want := `{"paths":["battery.percent","lwm2m./3/0/9"]}`; got != want {
		t.Fatalf("unexpected read parameters: got %s want %s", got, want)
	}
}

func TestBuildWriteTaskParametersValidatesJSONValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{name: "malformed json", input: `[`},
		{name: "missing values", input: `{}`},
		{name: "missing value", input: `{"values":[{"path":"config.sample_interval"}]}`},
		{name: "duplicate path", input: `{"values":[{"path":"led","value":true},{"path":"led","value":false}]}`},
		{name: "whitespace path", input: `{"values":[{"path":"bad path","value":1}]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := BuildWriteTaskParameters(tt.input); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}

	got, err := BuildWriteTaskParameters(`{"values":[{"path":"config.sample_interval","value":60},{"path":"enabled","value":true},{"path":"label","value":"main"},{"path":"metadata","value":{"mode":"eco"}},{"path":"unset","value":null}]}`)
	if err != nil {
		t.Fatalf("build write parameters: %v", err)
	}
	want := `{"values":[{"path":"config.sample_interval","value":60},{"path":"enabled","value":true},{"path":"label","value":"main"},{"path":"metadata","value":{"mode":"eco"}},{"path":"unset","value":null}]}`
	if got != want {
		t.Fatalf("unexpected write parameters: got %s want %s", got, want)
	}
}

func TestBuildFOTATaskParametersRequiresReleaseID(t *testing.T) {
	t.Parallel()

	if _, err := BuildFOTATaskParameters(0); err == nil {
		t.Fatal("expected release_id validation error")
	}
	got, err := BuildFOTATaskParameters(9)
	if err != nil {
		t.Fatalf("build FOTA parameters: %v", err)
	}
	if want := `{"release_id":9}`; got != want {
		t.Fatalf("unexpected FOTA parameters: got %s want %s", got, want)
	}
}
