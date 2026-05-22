package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
)

// DecodeJSON is the shared three-step JSON decode prelude: check
// Content-Type, cap body at MaxJSONBody via MaxBytesReader, and
// distinguish an oversize request (413) from a malformed body (400).
// Returns true when v was populated successfully; false when an error
// response has already been written to w.
func DecodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if ct := r.Header.Get("Content-Type"); ct != "" &&
		!strings.HasPrefix(ct, "application/json") {
		slog.Debug("api: decode bad content-type",
			"method", r.Method, "path", r.URL.Path, "content_type", ct)
		BadRequest(w, "expected application/json")
		return false
	}
	LimitBody(w, r, MaxJSONBody)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		if maxErr, ok := errors.AsType[*http.MaxBytesError](err); ok {
			slog.Warn("api: decode body too large",
				"method", r.Method, "path", r.URL.Path,
				"limit", MaxJSONBody, "error", maxErr)
			WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
				map[string]string{"error": "request body too large"})
			return false
		}
		slog.Debug("api: decode invalid json",
			"method", r.Method, "path", r.URL.Path, "error", err)
		BadRequest(w, "invalid json")
		return false
	}
	return true
}
