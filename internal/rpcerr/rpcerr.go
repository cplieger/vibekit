// Package rpcerr turns a JSON-RPC error from kiro-cli into text a person can
// read.
//
// It exists as its own package because THREE packages surface an ACP failure and
// none of them owns the rule: internal/command (the prompt path, the retry, the
// bridge-start failure and the dispatcher's shared 502), internal/agent (the model
// switch and every workflow read endpoint) and internal/workflow (Classify's
// feature detection). It used to live in internal/workflow as Details, reachable
// only from the run handlers, so the 127-of-137 error frames that carry their
// text in `error.data` rendered to chat users as the literal
// "ACP error -32603: Internal error". Then it lived in internal/vibekit beside the
// wire and domain TYPES, which is what made that package's own claim to declare
// no interfaces false — the detailer below is the interface it was declaring
// three files away.
//
// Nothing here imports another vibekit package. The one shape it needs is
// reached through detailer rather than through *vibekit.RPCError, so an error is
// found by errors.AsType at any wrapping depth and this package stays a leaf.
package rpcerr

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/cplieger/runesafe"
)

// maxTextBytes bounds one error string on its way to a user surface.
//
// A bound is required rather than tidy: Details' last fallback returns the
// raw `error.data` blob, which on a Zod failure over a large params object is
// unbounded, and the same value reaches an SSE payload, a chat banner and a log
// line. 2 KiB is far more than any real cause needs and far less than a
// transcript-sized blob.
const maxTextBytes = 2048

// detailer is satisfied by an error carrying KAS's `error.data`. An interface
// rather than *RPCError so a wrapped error is found at any depth.
//
// It EMBEDS error, which is what lets Details read it with errors.AsType.
// errors.AsType's type parameter is constrained to error, so a capability-only
// interface does not compile against it — measured on go1.27.0: `bare does not
// satisfy error (missing method Error)`. The declaration was strictly wider than
// the set of values that can inhabit it: this is read ONLY by walking an error
// tree, every node of such a tree is an error by construction, and no non-error
// implementation was ever reachable. net.Error is the stdlib's answer to the same
// question and embeds error for the same reason.
//
// Consumer cost is zero: the sole implementation is *vibekit.RPCError, which had
// to have an Error method to be in a chain at all.
type detailer interface {
	error
	ErrorData() json.RawMessage
}

// Stated as an assertion rather than left to Details' call of errors.AsType so a
// future widening of the interface fails here, next to the reason.
var _ error = detailer(nil)

// Details extracts the text KAS put in `error.data`, or "" when there is
// none.
//
// Returning "" for an error whose text is in `error.message` is why this is not
// the function callers should reach for: use Text, which composes both.
// It stays exported because workflow.Classify's feature detection wants the data
// half specifically, and matching its marker against the message too would widen
// it to any error that merely quotes KAS.
func Details(err error) string {
	// errors.AsType rather than errors.As: one expression, no var declaration, and
	// no addressable target to get wrong. Note go fix could not have found this
	// site — its errorsastype modernizer only fires on a bare POSITIVE
	// `if errors.As(...)`, so the negated guard this replaces was invisible to
	// both the fixer and golangci-lint's copy of the same analyzer.
	d, ok := errors.AsType[detailer](err)
	if !ok {
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
	//
	// Only Message is decoded. The struct used to carry a `Path []any` beside it
	// that nothing ever read, so every issue in every failure allocated a slice
	// and an interface box per path element for a field with no consumer — on the
	// path whose whole reason for existing is a Zod failure over a large params
	// object.
	var issues []struct {
		Message string `json:"message"`
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

// Text is the one function a user-facing surface should call on an ACP
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
// maxTextBytes gives.
func Text(err error) string {
	if err == nil {
		return ""
	}
	text := Details(err)
	if text == "" {
		text = err.Error()
	}
	// SanitizeSingleLineCapped, not SanitizeSingleLineBounded: Bounded puts its
	// elision marker OUTSIDE the cap, so a truncated value runs to n+3 bytes and
	// every caller with a real budget subtracts the marker width by hand. Capped
	// bounds the TOTAL, which is what a cap is for.
	capped, _ := runesafe.SanitizeSingleLineCapped(text, maxTextBytes, "...")
	return capped
}
