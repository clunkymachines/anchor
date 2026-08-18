package web

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"anchor/internal/db"
)

func TestNewServerUsesEmbeddedWebAssetsOutsideProjectDirectory(t *testing.T) {
	store, err := db.Open(context.Background(), db.Config{
		Dialect: db.DialectSQLite,
		DSN:     filepath.Join(t.TempDir(), "anchor.db"),
	})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	t.Chdir(t.TempDir())
	handler := NewServer(store)

	textAssets := []struct {
		path string
		want string
	}{
		{path: "/login", want: "Sign in - Anchor"},
		{path: "/static/app.css", want: `/static/fonts/poppins-400.woff2`},
		{path: "/static/app.js", want: `const telemetryRootID`},
		{path: "/static/htmx.min.js", want: `htmx`},
		{path: "/static/htmx.LICENSE", want: `Zero-Clause BSD`},
		{path: "/static/fonts/Poppins-OFL.txt", want: `SIL OPEN FONT LICENSE`},
		{path: "/static/fonts/SpaceGrotesk-OFL.txt", want: `SIL OPEN FONT LICENSE`},
	}
	for _, test := range textAssets {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), test.want) {
				t.Fatalf("expected embedded asset %q, got status %d body %q", test.path, response.Code, response.Body.String())
			}
		})
	}

	binaryAssets := []struct {
		path   string
		prefix []byte
	}{
		{path: "/logo.png", prefix: []byte("\x89PNG\r\n\x1a\n")},
		{path: "/static/fonts/poppins-400.woff2", prefix: []byte("wOF2")},
		{path: "/static/fonts/space-grotesk-500-700.woff2", prefix: []byte("wOF2")},
	}
	for _, test := range binaryAssets {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != http.StatusOK || !bytes.HasPrefix(response.Body.Bytes(), test.prefix) {
				t.Fatalf("expected embedded asset %q, got status %d", test.path, response.Code)
			}
		})
	}
}
