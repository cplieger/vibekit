// Package httpreply writes what vibekit answers an HTTP request with: the
// named status responses, the error envelope they carry, and the request
// guards whose failure IS one of those responses.
//
// Every symbol here either writes a reply or refuses a request by writing one,
// which is what the name claims and the whole of what the package does.
//
// It exists because this is BEHAVIOUR, and it used to sit in internal/vibekit
// beside the wire and domain TYPES. That put the 405 helper every handler
// imports inside the one package the code generator walks for the
// cross-language type contract, so neither half could be read or changed
// without the other in view.
//
// The mechanism is the fleet's: webhttp owns the headers, the status and the
// encode, and handlers call it directly for a plain body. What is vibekit's,
// and the only reason this package exists at all, is the error TAXONOMY: every
// helper here writes the bare {"error": "msg"} envelope vibekit's clients
// decode, leaving webhttp.ErrorResponse's Code and RequestID fields empty. A
// handler that hand-rolls that envelope instead is the drift these helpers
// exist to stop, and webhttp's own contract puts the named helpers here — it
// ships WriteError as the mechanism and leaves each app's taxonomy per app.
//
// NOT httpwire, which this package was called for one commit. subflux has a
// package of that name for the OPPOSITE direction — bridging httpx errors
// while reading an UPSTREAM response — so one spelling would have named two
// disjoint concerns and taught a fleet reader that the name carries no
// information. Fleet alignment aligns concepts, not spellings: the shared
// concept is "the app's own HTTP boundary vocabulary", and the direction is
// what the name has to say out loud. NOT apireply either: internal/vibekit is the
// package being renamed away from api because api names nothing, so borrowing
// the word for its neighbour would repeat the mistake and leave a reader
// guessing whether the prefix means the package, the URL space, or the idea.
package httpreply

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/cplieger/webhttp"
)

// JSONKeyError is the standard JSON error response key.
const JSONKeyError = "error"

// ErrorJSON returns the canonical error response map for JSON encoding.
// Use with webhttp.WriteJSONStatus to write error responses consistently.
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

// msgInternalError is the standard client-facing message for 500 responses.
const msgInternalError = "internal error"

// --- JSON response writers ---
//
// The mechanism (headers, status, encode) is webhttp's; vibekit's error
// taxonomy (the bare {"error":…} named helpers below) is layered on top.

// WriteRawJSON writes pre-marshalled JSON bytes with the standard
// Content-Type + X-Content-Type-Options headers. Use for pass-through
// of cached command replies and upstream JSON bodies (e.g. kiro-cli
// slash-command results) where json.Marshal would be a redundant
// round-trip. Write errors are best-effort (client may have hung up).
func WriteRawJSON(w http.ResponseWriter, data []byte) {
	webhttp.JSONHeaders(w)
	if _, err := w.Write(data); err != nil {
		slog.Debug("httpreply: raw json write failed", "error", err)
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
	webhttp.WriteJSONStatus(w, http.StatusBadRequest, webhttp.ErrorResponse{Error: msg})
}

// Forbidden writes a 403 with {"error": msg}.
func Forbidden(w http.ResponseWriter, msg string) {
	webhttp.WriteJSONStatus(w, http.StatusForbidden, webhttp.ErrorResponse{Error: msg})
}

// NotFound writes a 404 with {"error": msg}.
func NotFound(w http.ResponseWriter, msg string) {
	webhttp.WriteJSONStatus(w, http.StatusNotFound, webhttp.ErrorResponse{Error: msg})
}

// Conflict writes a 409 with {"error": msg}.
func Conflict(w http.ResponseWriter, msg string) {
	webhttp.WriteJSONStatus(w, http.StatusConflict, webhttp.ErrorResponse{Error: msg})
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
	webhttp.WriteJSONStatus(w, http.StatusMethodNotAllowed, webhttp.ErrorResponse{Error: "method not allowed"})
}

// InternalError writes a 500 with {"error": "internal error"} and logs
// the actual error at slog.Error for correlation. Never exposes internal
// error details to HTTP clients.
func InternalError(w http.ResponseWriter, err error) {
	if err != nil {
		slog.Error("httpreply: internal error", "error", err)
	}
	webhttp.WriteJSONStatus(w, http.StatusInternalServerError, webhttp.ErrorResponse{Error: msgInternalError})
}

// ServerError writes a 500 with a caller-specified client-visible message
// and logs the actual error at slog.Error. Use when the handler wants to
// surface a safe, specific message (e.g. "save failed") while still
// logging the raw error for debugging.
func ServerError(w http.ResponseWriter, clientMsg string, err error) {
	if err != nil {
		slog.Error("httpreply: server error", "client_msg", clientMsg, "error", err)
	}
	webhttp.WriteJSONStatus(w, http.StatusInternalServerError, webhttp.ErrorResponse{Error: clientMsg})
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
	if err := webhttp.DecodeJSONInto(w, r, v, webhttp.MaxJSONBody); err != nil {
		BadRequest(w, errMsg)
		return false
	}
	return true
}

// DecodeBodyOptional caps the body and decodes JSON into v.
// Unlike DecodeBody, a decode failure is silently ignored (v keeps
// its zero value) because the caller accepts an empty request body.
func DecodeBodyOptional(w http.ResponseWriter, r *http.Request, v any) {
	webhttp.LimitBody(w, r, webhttp.MaxJSONBody)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		_ = err
	}
}
