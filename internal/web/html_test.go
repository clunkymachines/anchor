package web

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRenderBuffersTemplateBeforeWritingResponse(t *testing.T) {
	t.Parallel()

	templates := template.Must(template.New("broken").Parse(`partial output {{.MissingField}}`))
	server := &Server{templates: templates}
	response := httptest.NewRecorder()

	err := server.render(response, http.StatusTeapot, 42, "broken")
	if err == nil {
		t.Fatal("expected template execution error")
	}
	if response.Body.Len() != 0 {
		t.Fatalf("expected no response body after failed render, got %q", response.Body.String())
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "" {
		t.Fatalf("expected response headers to remain uncommitted, got Content-Type %q", contentType)
	}
}

func TestRenderWritesStatusHeadersAndBodyAfterSuccessfulExecution(t *testing.T) {
	t.Parallel()

	templates := template.Must(template.New("message").Parse(`Hello, {{.}}!`))
	server := &Server{templates: templates}
	response := httptest.NewRecorder()

	if err := server.render(response, http.StatusUnprocessableEntity, "Anchor", "message"); err != nil {
		t.Fatalf("render template: %v", err)
	}
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected status %d, got %d", http.StatusUnprocessableEntity, response.Code)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "text/html; charset=utf-8" {
		t.Fatalf("unexpected Content-Type %q", contentType)
	}
	if body := response.Body.String(); body != "Hello, Anchor!" {
		t.Fatalf("unexpected response body %q", body)
	}
}

func TestRedirectUsesHXRedirectForHTMXRequests(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodPost, "/source", nil)
	request.Header.Set("HX-Request", "true")
	response := httptest.NewRecorder()

	redirect(response, request, "/target", http.StatusSeeOther)

	if response.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, response.Code)
	}
	if target := response.Header().Get("HX-Redirect"); target != "/target" {
		t.Fatalf("expected HX-Redirect /target, got %q", target)
	}
	if location := response.Header().Get("Location"); location != "" {
		t.Fatalf("expected no Location header, got %q", location)
	}
}

func TestRedirectUsesHTTPRedirectForRegularRequests(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodPost, "/source", nil)
	response := httptest.NewRecorder()

	redirect(response, request, "/target", http.StatusSeeOther)

	if response.Code != http.StatusSeeOther {
		t.Fatalf("expected status %d, got %d", http.StatusSeeOther, response.Code)
	}
	if location := response.Header().Get("Location"); location != "/target" {
		t.Fatalf("expected Location /target, got %q", location)
	}
	if hxRedirect := response.Header().Get("HX-Redirect"); hxRedirect != "" {
		t.Fatalf("expected no HX-Redirect header, got %q", hxRedirect)
	}
}

func TestRequireAuthRedirectsHTMXRequestsAtBrowserLevel(t *testing.T) {
	t.Parallel()

	server := &Server{}
	nextCalled := false
	handler := server.requireAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		nextCalled = true
	}))
	request := httptest.NewRequest(http.MethodGet, "/devices/device-001/tasks", nil)
	request.Header.Set("HX-Request", "true")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if nextCalled {
		t.Fatal("expected unauthenticated request not to reach the next handler")
	}
	if response.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, response.Code)
	}
	if target := response.Header().Get("HX-Redirect"); target != "/login" {
		t.Fatalf("expected HX-Redirect /login, got %q", target)
	}
}
