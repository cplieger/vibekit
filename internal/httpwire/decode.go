package httpwire

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/cplieger/webhttp"
)

// DecodeJSON is the shared three-step JSON decode prelude: check
// Content-Type, cap body at MaxJSONBody via MaxBytesReader, and
// distinguish an oversize request (413) from a malformed body (400).
// Returns true when v was populated successfully; false when an error
// response has already been written to w.
func DecodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if ct := r.Header.Get("Content-Type"); ct != "" &&
		!strings.HasPrefix(ct, MIMETypeJSON) {
		slog.Debug("httpwire: decode bad content-type",
			"method", r.Method, "path", r.URL.Path, "content_type", ct)
		BadRequest(w, "expected "+MIMETypeJSON)
		return false
	}
	// Cap + decode + reject-trailing is webhttp.DecodeJSONInto (shared with the
	// fleet); vibekit keeps its Content-Type gate, the 413/400 split, and its
	// bare {"error":…} envelope on top — DecodeJSONInto writes nothing itself.
	if err := webhttp.DecodeJSONInto(w, r, v, MaxJSONBody); err != nil {
		if maxErr, ok := errors.AsType[*http.MaxBytesError](err); ok {
			slog.Warn("httpwire: decode body too large",
				"method", r.Method, "path", r.URL.Path,
				"limit", MaxJSONBody, "error", maxErr)
			WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
				map[string]string{JSONKeyError: "request body too large"})
			return false
		}
		slog.Debug("httpwire: decode invalid json",
			"method", r.Method, "path", r.URL.Path, "error", err)
		BadRequest(w, "invalid json")
		return false
	}
	return true
}
