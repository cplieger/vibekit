package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
)

// JSONKeyError is the standard JSON error response key.
const JSONKeyError = "error"

// JSONKeyOutput is the standard JSON response key for successful
// command output. Used by git/ and server/ packages.
const JSONKeyOutput = "output"

// MaxJSONBody is the maximum size for JSON request bodies (1 MiB).
const MaxJSONBody = 1024 * 1024

// LimitBody wraps r.Body with MaxBytesReader to prevent oversized requests.
func LimitBody(w http.ResponseWriter, r *http.Request, maxBytes int64) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
}

// --- JSON response writers ---

// JSONHeaders sets the standard JSON response headers (Content-Type
// and X-Content-Type-Options). Exported for use by internal/fileutil.
func JSONHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

// WriteJSON encodes v as JSON with status 200.
func WriteJSON(w http.ResponseWriter, v any) {
	WriteJSONStatus(w, http.StatusOK, v)
}

// WriteJSONStatus encodes v with the given status code. Encode failures
// after the status has already been committed almost always indicate a
// non-marshalable value in v (a programmer bug), so they're logged at
// Warn so Loki picks them up.
func WriteJSONStatus(w http.ResponseWriter, code int, v any) {
	JSONHeaders(w)
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("api: json encode failed after status committed",
			"code", code, "error", err)
	}
}

// WriteRawJSON writes pre-marshalled JSON bytes with the standard
// Content-Type + X-Content-Type-Options headers. Use for pass-through
// of cached command replies and upstream JSON bodies (e.g. kiro-cli
// slash-command results) where json.Marshal would be a redundant
// round-trip. Write errors are best-effort (client may have hung up).
func WriteRawJSON(w http.ResponseWriter, data []byte) {
	JSONHeaders(w)
	if _, err := w.Write(data); err != nil {
		slog.Debug("api: raw json write failed", "error", err)
	}
}

// --- Named error responses ---
//
// Use these in handlers instead of hand-crafting JSON strings with
// http.Error. Consistent shape across every package: {"error": "msg"}
// with the correct status code + Content-Type.

// BadRequest writes a 400 with {"error": msg}.
func BadRequest(w http.ResponseWriter, msg string) {
	WriteJSONStatus(w, http.StatusBadRequest, map[string]string{JSONKeyError: msg})
}

// Forbidden writes a 403 with {"error": msg}.
func Forbidden(w http.ResponseWriter, msg string) {
	WriteJSONStatus(w, http.StatusForbidden, map[string]string{JSONKeyError: msg})
}

// NotFound writes a 404 with {"error": msg}.
func NotFound(w http.ResponseWriter, msg string) {
	WriteJSONStatus(w, http.StatusNotFound, map[string]string{JSONKeyError: msg})
}

// Conflict writes a 409 with {"error": msg}.
func Conflict(w http.ResponseWriter, msg string) {
	WriteJSONStatus(w, http.StatusConflict, map[string]string{JSONKeyError: msg})
}

// MethodNotAllowed writes a 405 with a standard message.
func MethodNotAllowed(w http.ResponseWriter) {
	WriteJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{JSONKeyError: "method not allowed"})
}

// InternalError writes a 500 with {"error": "internal error"} and logs
// the actual error at slog.Error for correlation. Never exposes internal
// error details to HTTP clients.
func InternalError(w http.ResponseWriter, err error) {
	if err != nil {
		slog.Error("api: internal error", "error", err)
	}
	WriteJSONStatus(w, http.StatusInternalServerError, map[string]string{JSONKeyError: "internal error"})
}

// ServerError writes a 500 with a caller-specified client-visible message
// and logs the actual error at slog.Error. Use when the handler wants to
// surface a safe, specific message (e.g. "save failed") while still
// logging the raw error for debugging.
func ServerError(w http.ResponseWriter, clientMsg string, err error) {
	if err != nil {
		slog.Error("api: server error", "client_msg", clientMsg, "error", err)
	}
	WriteJSONStatus(w, http.StatusInternalServerError, map[string]string{JSONKeyError: clientMsg})
}

// Ok writes a 200 with {"ok": true} — the standard "action succeeded"
// response used by the handful of endpoints that don't return data.
func Ok(w http.ResponseWriter) {
	WriteJSON(w, map[string]bool{"ok": true})
}

// --- Capped I/O writers ---

// LimitedWriter writes at most N bytes to W and silently drops the rest.
// Used for bounded stderr/stdout capture from subprocess handlers so a
// misbehaving or hostile subprocess can't OOM the container.
type LimitedWriter struct {
	W io.Writer
	N int64
}

// Write implements io.Writer, enforcing the byte cap.
func (lw *LimitedWriter) Write(p []byte) (int, error) {
	if lw.N <= 0 {
		return len(p), nil // pretend we wrote, drop the bytes
	}
	if int64(len(p)) > lw.N {
		p = p[:lw.N]
	}
	n, err := lw.W.Write(p)
	lw.N -= int64(n)
	return n, err
}

// --- Handler-prelude helpers ---
//
// Canonical request-prelude helpers used by git, server, and other
// packages. Consolidates the duplicated requirePOST/requireMethod and
// decodeBody/decodePostBody patterns into a single import.

// RequireMethod returns true if r.Method matches method; otherwise it
// writes a 405 response and returns false.
func RequireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method != method {
		MethodNotAllowed(w)
		return false
	}
	return true
}

// DecodeBody applies LimitBody, decodes JSON into v, and returns true
// on success. On failure it writes a 400 response with errMsg and
// returns false.
func DecodeBody(w http.ResponseWriter, r *http.Request, v any, errMsg string) bool {
	LimitBody(w, r, MaxJSONBody)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		BadRequest(w, errMsg)
		return false
	}
	return true
}

// DecodeBodyOptional applies LimitBody and decodes JSON into v.
// Unlike DecodeBody, a decode failure is silently ignored (v keeps
// its zero value) because the caller accepts an empty request body.
func DecodeBodyOptional(w http.ResponseWriter, r *http.Request, v any) {
	LimitBody(w, r, MaxJSONBody)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		_ = err
	}
}
