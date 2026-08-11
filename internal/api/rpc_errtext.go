package api

// Turning a JSON-RPC error from kiro-cli into text a person can read.
//
// This lives in api rather than in a domain package because EVERY caller that
// surfaces an ACP failure needs it: the prompt path, the model switch, the
// command dispatcher's shared 502 and the workflow read endpoints. It used to
// live in internal/workflow as Details, reachable only from the run handlers, so
// the 127-of-137 error frames that carry their text in `error.data` rendered to
// chat users as the literal "ACP error -32603: Internal error".

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/cplieger/runesafe"
)

// MaxRPCErrorTextBytes bounds one error string on its way to a user surface.
//
// A bound is required rather than tidy: RPCDetails' last fallback returns the
// raw `error.data` blob, which on a Zod failure over a large params object is
// unbounded, and the same value reaches an SSE payload, a chat banner and a log
// line. 2 KiB is far more than any real cause needs and far less than a
// transcript-sized blob.
const MaxRPCErrorTextBytes = 2048

// rpcDetailer is satisfied by an error carrying KAS's `error.data`. An interface
// rather than *RPCError so a wrapped error is found by errors.As at any depth.
type rpcDetailer interface {
	ErrorData() json.RawMessage
}

// RPCDetails extracts the text KAS put in `error.data`, or "" when there is
// none.
//
// Returning "" for an error whose text is in `error.message` is why this is not
// the function callers should reach for: use RPCErrorText, which composes both.
// It stays exported because IsUnknownMethod-style feature detection wants the
// data half specifically, and matching the marker against the message too would
// widen it to any error that merely quotes KAS.
func RPCDetails(err error) string {
	var d rpcDetailer
	if !errors.As(err, &d) {
		return ""
	}
	raw := d.ErrorData()
	if len(raw) == 0 {
		return ""
	}
	// The common shape: {"details": "…"}.
	var obj struct {
		Details string `json:"details"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Details != "" {
		return obj.Details
	}
	// The other shape: a Zod issue array. Its messages are what a caller wants.
	var issues []struct {
		Message string `json:"message"`
		Path    []any  `json:"path"`
	}
	if json.Unmarshal(raw, &issues) == nil && len(issues) > 0 {
		msgs := make([]string, 0, len(issues))
		for _, is := range issues {
			if is.Message != "" {
				msgs = append(msgs, is.Message)
			}
		}
		if len(msgs) > 0 {
			return strings.Join(msgs, "; ")
		}
	}
	// Neither shape parsed: the raw JSON still beats an empty string, because it
	// is what KAS actually said. This is the branch the byte cap exists for.
	return string(raw)
}

// RPCErrorText is the one function a user-facing surface should call on an ACP
// error. It answers the most specific text the error carries, sanitized and
// bounded.
//
// BOTH fields have to be read, and that is measured rather than defensive.
// Counted over every engine-emitted frame in the wire logs: 127 `-32603` errors
// put the text in `error.data` (as `{"details": …}` or a Zod issue array) and
// set `error.message` to the literal "Internal error", while 6 `-32602` and 4
// `-32000` errors put it in `error.message`. So reading only `data` renders an
// empty string for every parameter-validation failure, and reading only
// `message` renders "Internal error" for the overwhelming majority. Preferring
// data and falling back to the error string covers both without a code switch.
//
// The value is sanitized because it is upstream text bound for a log line, an
// SSE payload and a banner: runesafe's single-line preset turns C0/C1 controls,
// DEL, Bidi overrides and the paragraph separators into spaces, so a hostile or
// merely mangled provider message cannot forge a log record or reorder a
// sentence in a viewer. It is capped on a rune boundary for the reason
// MaxRPCErrorTextBytes gives.
func RPCErrorText(err error) string {
	if err == nil {
		return ""
	}
	text := RPCDetails(err)
	if text == "" {
		text = err.Error()
	}
	// SanitizeSingleLineCapped, not SanitizeSingleLineBounded: Bounded puts its
	// elision marker OUTSIDE the cap, so a truncated value runs to n+3 bytes and
	// every caller with a real budget subtracts the marker width by hand. Capped
	// bounds the TOTAL, which is what a cap is for.
	capped, _ := runesafe.SanitizeSingleLineCapped(text, MaxRPCErrorTextBytes, "...")
	return capped
}
