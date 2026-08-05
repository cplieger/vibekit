package command

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// rpcErr builds an RPCError with an optional data payload, the way the bridge
// hands one to the command layer.
func rpcErr(t *testing.T, code int, msg string, data any) error {
	t.Helper()
	e := &api.RPCError{Code: code, Message: msg}
	if data != nil {
		raw, err := json.Marshal(data)
		if err != nil {
			t.Fatalf("marshal data: %v", err)
		}
		e.Data = raw
	}
	// The bridge wraps every RPC error before returning it, so the classifier
	// must reach through fmt.Errorf's %w. Testing the bare error would pass
	// while the real path failed.
	return fmt.Errorf("ACP error %d: %w", code, e)
}

// TestClassifyPromptFailure pins the four causes apart. Each row exists because
// the single-boolean predicate this replaced took the WRONG action on it.
func TestClassifyPromptFailure(t *testing.T) {
	cases := map[string]struct {
		err  error
		want promptFailureClass
	}{
		// The headline defect: a dead subprocess arrives wrapped in a
		// TransportError whose Retryable is TRUE, so the old predicate retried
		// it three times against a closed done channel, burning four seconds of
		// wall clock to arrive at the same error.
		"dead bridge is not retryable": {
			err:  &api.TransportError{Err: api.ErrBridgeExited, Retryable: true},
			want: classPipeDeath,
		},
		"dead bridge survives wrapping": {
			err:  fmt.Errorf("prompt: %w", &api.TransportError{Err: api.ErrBridgeExited, Retryable: true}),
			want: classPipeDeath,
		},
		"write failure is transient": {
			err:  &api.TransportError{Err: errors.New("write to ACP: broken pipe"), Retryable: true},
			want: classTransient,
		},
		"non-retryable transport is fatal": {
			err:  &api.TransportError{Err: errors.New("bad frame"), Retryable: false},
			want: classFatal,
		},
		"session busy by sentinel": {
			err:  fmt.Errorf("ACP error -32001: %w", api.ErrNotIdle),
			want: classBusy,
		},
		"session busy by code": {
			err:  rpcErr(t, api.RPCCodeNotIdle, "session is not idle", nil),
			want: classBusy,
		},
		"internal error is transient": {
			err:  rpcErr(t, api.RPCCodeInternal, "Internal error", map[string]string{"details": "transient upstream fault"}),
			want: classTransient,
		},
		// Retrying an expired token is pure latency, and KAS collapses auth
		// onto the same -32603 as a genuine fault.
		"auth failure is fatal, not transient": {
			err:  rpcErr(t, api.RPCCodeInternal, "Internal error", map[string]string{"details": "not logged in"}),
			want: classFatal,
		},
		"expired token is fatal": {
			err:  rpcErr(t, api.RPCCodeInternal, "ExpiredToken: refresh required", nil),
			want: classFatal,
		},
		// KAS reports a throttle on the same -32000 vibekit uses for its own
		// bridge-exited constant. The data payload is the distinguisher.
		"throttle is not retryable": {
			err: rpcErr(t, api.RPCCodeBridgeExited, "Internal error", throttleErrorData{
				ErrorType:      "ThrottlingException",
				RetryErrorType: "USER_REQUEST_RATE_EXCEEDED",
				RequestID:      "abc-123",
			}),
			want: classThrottled,
		},
		"mapped error with no retry classification is fatal": {
			err:  rpcErr(t, api.RPCCodeBridgeExited, "some other mapped failure", nil),
			want: classFatal,
		},
		"nil is fatal":         {err: nil, want: classFatal},
		"plain error is fatal": {err: errors.New("something else"), want: classFatal},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := classifyPromptFailure(tc.err)
			if got != tc.want {
				t.Errorf("classifyPromptFailure(%v) = %s, want %s", tc.err, got, tc.want)
			}
		})
	}
}

// TestIsRetryablePromptError_NeverRetriesADeadBridgeOrAThrottle states the two
// regressions this pass exists to prevent, in the predicate the retry loop
// actually calls. Both were true before and both were wrong: a corpse cannot
// answer, and KAS's own client already spent five adaptive attempts on the
// throttle before handing it over, so a sixth from here only deepens it.
func TestIsRetryablePromptError_NeverRetriesADeadBridgeOrAThrottle(t *testing.T) {
	dead := &api.TransportError{Err: api.ErrBridgeExited, Retryable: true}
	if IsRetryablePromptError(dead) {
		t.Error("a dead bridge is reported retryable; every attempt fails instantly against a closed done channel")
	}

	throttle := rpcErr(t, api.RPCCodeBridgeExited, "Internal error", throttleErrorData{
		ErrorType:      "ThrottlingException",
		RetryErrorType: "USER_REQUEST_RATE_EXCEEDED",
	})
	if IsRetryablePromptError(throttle) {
		t.Error("a throttle is reported retryable; kiro-cli already exhausted its own adaptive attempts")
	}

	// The two classes that SHOULD still retry, so the fix cannot be "return
	// false for everything".
	if !IsRetryablePromptError(fmt.Errorf("ACP error -32001: %w", api.ErrNotIdle)) {
		t.Error("a busy session must still be retried")
	}
	if !IsRetryablePromptError(&api.TransportError{Err: errors.New("write to ACP"), Retryable: true}) {
		t.Error("a transient write failure must still be retried")
	}
}

// TestPromptFailureReason_NamesAThrottle guards the user-facing half. Before
// this, a rate-limited turn surfaced as the literal string "Internal error",
// which describes neither the cause nor the remedy, on the one failure where
// both are known.
func TestPromptFailureReason_NamesAThrottle(t *testing.T) {
	err := rpcErr(t, api.RPCCodeBridgeExited, "Internal error", throttleErrorData{
		ErrorType:      "ThrottlingException",
		RetryErrorType: "USER_REQUEST_RATE_EXCEEDED",
		RequestID:      "req-9",
	})
	got := promptFailureReason(err)

	for _, want := range []string{"rate limiting", "ThrottlingException", "req-9"} {
		if !strings.Contains(got, want) {
			t.Errorf("reason %q does not mention %q", got, want)
		}
	}
	if strings.Contains(got, "Internal error") {
		t.Errorf("reason %q still leads with KAS's placeholder message", got)
	}

	// Anything else falls through verbatim: inventing prose for an error we do
	// not understand would hide the only text there is.
	plain := errors.New("some other failure")
	if got := promptFailureReason(plain); got != plain.Error() {
		t.Errorf("promptFailureReason(%v) = %q, want it passed through unchanged", plain, got)
	}
}
