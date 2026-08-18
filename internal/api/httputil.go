package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/cplieger/webhttp"
)

// JSONKeyError is the standard JSON error response key.
const JSONKeyError = "error"

// ErrorJSON returns the canonical error response map for JSON encoding.
// Use with WriteJSONStatus to write error responses consistently.
func ErrorJSON(msg string) map[string]string {
	return map[string]string{JSONKeyError: msg}
}

// ErrorJSONWithCode returns an error response map with an additional
// machine-readable "code" field. Used by forge handlers that surface
// a stable error code alongside the human-readable message.
func ErrorJSONWithCode(msg, code string) map[string]string {
	return map[string]string{JSONKeyError: msg, "code": code}
}

// JSONKeyOutput is the standard JSON response key for successful
// command output. Used by git/ and server/ packages.
const JSONKeyOutput = "output"

// JSONKeyName is the standard JSON field name for "name" in ACP wire
// format. Used in content blocks, command descriptors, and MCP entries.
const JSONKeyName = "name"

// MIMETypeJSON is the standard MIME type for JSON content.
const MIMETypeJSON = "application/json"

// MaxJSONBody is the maximum size for JSON request bodies (1 MiB).
const MaxJSONBody = 1024 * 1024

// MsgInternalError is the standard client-facing message for 500 responses.
const MsgInternalError = "internal error"

// LimitBody wraps r.Body with MaxBytesReader to prevent oversized requests.
// It delegates to webhttp.LimitBody so the body-cap mechanism is shared fleet-wide.
func LimitBody(w http.ResponseWriter, r *http.Request, maxBytes int64) {
	webhttp.LimitBody(w, r, maxBytes)
}

// --- JSON response writers ---
//
// The mechanism (headers, status, encode) is webhttp's; vibekit's error
// taxonomy (the bare {"error":…} named helpers below) is layered on top.

// JSONHeaders sets the standard JSON response headers (Content-Type
// and X-Content-Type-Options). Exported for the handler packages that write
// a JSON body without going through the named helpers below.
func JSONHeaders(w http.ResponseWriter) {
	webhttp.JSONHeaders(w)
}

// WriteJSON encodes v as JSON with status 200.
func WriteJSON(w http.ResponseWriter, v any) {
	webhttp.WriteJSON(w, v)
}

// WriteJSONStatus sets the JSON headers, writes the status code, and encodes v.
// An encode failure after the status is committed is logged at Warn (by
// webhttp) rather than returned, since the response line is already on the wire.
func WriteJSONStatus(w http.ResponseWriter, code int, v any) {
	webhttp.WriteJSONStatus(w, code, v)
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
// with the correct status code + Content-Type. Each helper layers
// vibekit's bare error taxonomy on webhttp.ErrorResponse; the Code and
// RequestID envelope fields are left empty (omitempty), so the wire
// shape stays the bare {"error": "msg"} vibekit clients expect.

// BadRequest writes a 400 with {"error": msg}.
func BadRequest(w http.ResponseWriter, msg string) {
	WriteJSONStatus(w, http.StatusBadRequest, webhttp.ErrorResponse{Error: msg})
}

// Forbidden writes a 403 with {"error": msg}.
func Forbidden(w http.ResponseWriter, msg string) {
	WriteJSONStatus(w, http.StatusForbidden, webhttp.ErrorResponse{Error: msg})
}

// NotFound writes a 404 with {"error": msg}.
func NotFound(w http.ResponseWriter, msg string) {
	WriteJSONStatus(w, http.StatusNotFound, webhttp.ErrorResponse{Error: msg})
}

// Conflict writes a 409 with {"error": msg}.
func Conflict(w http.ResponseWriter, msg string) {
	WriteJSONStatus(w, http.StatusConflict, webhttp.ErrorResponse{Error: msg})
}

// headerAllow is the response-header name RFC 9110 §10.2.1 reserves for the
// set of methods a resource supports.
const headerAllow = "Allow"

// setAllow sets the Allow header to the comma-separated list of methods the
// addressed resource supports (RFC 9110 §10.2.1). Method tokens are
// case-sensitive (RFC 9110 §9.1), so they are emitted verbatim rather than
// normalised; empty arguments are dropped because §5.6.1.1 forbids empty
// list elements. The separator is ", " — the same rendering net/http's own
// ServeMux uses for the method-pattern routes, so every Allow vibekit emits
// looks alike. Must be called before the status is committed.
func setAllow(w http.ResponseWriter, method string, more ...string) {
	methods := make([]string, 0, 1+len(more))
	if method != "" {
		methods = append(methods, method)
	}
	for _, m := range more {
		if m != "" {
			methods = append(methods, m)
		}
	}
	if len(methods) == 0 {
		return
	}
	w.Header().Set(headerAllow, strings.Join(methods, ", "))
}

// MethodNotAllowed writes a 405 with a standard message plus the Allow
// header RFC 9110 §15.5.6 requires an origin server to send on every 405.
//
// The method list is PER-RESOURCE: pass exactly the methods the addressed
// resource dispatches, read off its own routing and switch — never a
// blanket constant. An Allow that over-promises is worse than no Allow at
// all, because a client that trusts it retries a method that 405s again.
// At least one method is mandatory at the call site by construction: a
// resource that permits nothing is a 404, not a 405.
//
// HEAD is deliberately NOT implied by GET. vibekit's JSON handlers compare
// r.Method for equality and its API routes are registered as plain paths,
// so net/http's ServeMux does no method matching and a HEAD request reaches
// the handler and lands here — listing HEAD would advertise a method that
// also 405s.
func MethodNotAllowed(w http.ResponseWriter, method string, more ...string) {
	setAllow(w, method, more...)
	WriteJSONStatus(w, http.StatusMethodNotAllowed, webhttp.ErrorResponse{Error: "method not allowed"})
}

// InternalError writes a 500 with {"error": "internal error"} and logs
// the actual error at slog.Error for correlation. Never exposes internal
// error details to HTTP clients.
func InternalError(w http.ResponseWriter, err error) {
	if err != nil {
		slog.Error("api: internal error", "error", err)
	}
	WriteJSONStatus(w, http.StatusInternalServerError, webhttp.ErrorResponse{Error: MsgInternalError})
}

// ServerError writes a 500 with a caller-specified client-visible message
// and logs the actual error at slog.Error. Use when the handler wants to
// surface a safe, specific message (e.g. "save failed") while still
// logging the raw error for debugging.
func ServerError(w http.ResponseWriter, clientMsg string, err error) {
	if err != nil {
		slog.Error("api: server error", "client_msg", clientMsg, "error", err)
	}
	WriteJSONStatus(w, http.StatusInternalServerError, webhttp.ErrorResponse{Error: clientMsg})
}

// Ok writes a 200 with {"ok": true} — the standard "action succeeded"
// response used by the handful of endpoints that don't return data.
func Ok(w http.ResponseWriter) {
	webhttp.Ok(w)
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
// writes a 405 response — with Allow set to that single method, which is
// by construction the resource's whole permitted set — and returns false.
func RequireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method != method {
		MethodNotAllowed(w, method)
		return false
	}
	return true
}

// DecodeBody caps + decodes exactly one JSON value into v via
// webhttp.DecodeJSONInto (rejecting trailing data), returning true on success.
// On any decode failure it writes vibekit's bare {"error":errMsg} 400 and
// returns false.
func DecodeBody(w http.ResponseWriter, r *http.Request, v any, errMsg string) bool {
	if err := webhttp.DecodeJSONInto(w, r, v, MaxJSONBody); err != nil {
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
