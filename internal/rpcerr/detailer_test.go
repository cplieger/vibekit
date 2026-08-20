package rpcerr

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

// dataErr is a minimal detailer, standing in for *vibekit.RPCError (which this
// package deliberately does not import — see the package doc).
type dataErr struct {
	msg  string
	data json.RawMessage
}

func (e *dataErr) Error() string              { return e.msg }
func (e *dataErr) ErrorData() json.RawMessage { return e.data }

// TestDetails_FoundAtAnyWrappingDepth is the property the interface exists for,
// and the one the errors.As -> errors.AsType swap had to preserve: KAS's cause
// reaches a user surface through several layers of fmt.Errorf on the way up, and
// a type-assertion on the top-level error would find none of them.
func TestDetails_FoundAtAnyWrappingDepth(t *testing.T) {
	leaf := &dataErr{msg: "Internal error", data: json.RawMessage(`{"details":"session not idle"}`)}

	for _, depth := range []int{0, 1, 5} {
		err := error(leaf)
		for range depth {
			err = fmt.Errorf("layer: %w", err)
		}
		if got := Details(err); got != "session not idle" {
			t.Errorf("Details at wrapping depth %d = %q, want %q", depth, got, "session not idle")
		}
		// Text composes Details with the error string, so it must agree.
		if got := Text(err); got != "session not idle" {
			t.Errorf("Text at wrapping depth %d = %q, want %q", depth, got, "session not idle")
		}
	}
}

// TestDetails_FoundThroughAJoin pins the other tree shape errors.AsType walks and
// a type assertion does not: errors.Join's multi-Unwrap.
func TestDetails_FoundThroughAJoin(t *testing.T) {
	leaf := &dataErr{msg: "Internal error", data: json.RawMessage(`{"details":"policy parse failed"}`)}
	err := errors.Join(errors.New("first"), fmt.Errorf("second: %w", leaf))
	if got := Details(err); got != "policy parse failed" {
		t.Errorf("Details through errors.Join = %q, want %q", got, "policy parse failed")
	}
}

// TestDetails_IgnoresAnErrorWithNoData covers the arm the negated guard used to
// be: an ordinary error carries no `error.data`, so Details is empty and Text
// falls back to the error string.
func TestDetails_IgnoresAnErrorWithNoData(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", errors.New("params invalid"))
	if got := Details(err); got != "" {
		t.Errorf("Details on a plain error = %q, want %q", got, "")
	}
	if got := Text(err); got != "wrapped: params invalid" {
		t.Errorf("Text on a plain error = %q, want the error string", got)
	}
}
