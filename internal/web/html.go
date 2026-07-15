package web

import (
	"bytes"
	"net/http"
)

// render executes templateName into a buffer before committing the response.
// This ensures that template execution errors can be returned without sending a
// partial response or an incorrect success status to the client.
func (s *Server) render(w http.ResponseWriter, status int, data any, templateName string) error {
	var buf bytes.Buffer
	if err := s.templates.ExecuteTemplate(&buf, templateName, data); err != nil {
		return err
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, err := buf.WriteTo(w)
	return err
}

// isHTMXRequest reports whether HTMX initiated the request.
func isHTMXRequest(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// redirect navigates HTMX requests at the browser level so that a redirected
// page is not swapped into the current target. Regular requests receive a
// standard HTTP redirect using status; HTMX requests receive a 204 response
// with HX-Redirect instead.
func redirect(w http.ResponseWriter, r *http.Request, target string, status int) {
	if isHTMXRequest(r) {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	http.Redirect(w, r, target, status)
}
