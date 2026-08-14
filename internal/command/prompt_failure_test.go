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
			err: rpcErr(t, api.RPCCodeBridgeExited, "Too many requests, please wait.", mappedErrorData{
				ErrorType:      "ClientThrottleError",
				RetryErrorType: "THROTTLING",
				RequestID:      "abc-123",
			}),
			want: classThrottled,
		},
		// The regression this table exists to prevent. 16 of KAS's 24 mapped
		// error classes report CLIENT_ERROR and 2 report SERVER_ERROR; an earlier
		// revision classified a mapped error by the mere PRESENCE of the data
		// block, so all 21 non-throttle classes were reported to the user as a
		// rate limit. A validation failure telling the user to wait and retry is
		// worse than the bare "Internal error" it replaced.
		"validation error is not a throttle": {
			err: rpcErr(t, api.RPCCodeBridgeExited, "The request was invalid.", mappedErrorData{
				ErrorType:      "GenericValidationError",
				RetryErrorType: "CLIENT_ERROR",
			}),
			want: classFatal,
		},
		"access denied is not a throttle": {
			err: rpcErr(t, api.RPCCodeBridgeExited, "Access denied.", mappedErrorData{
				ErrorType:      "AccessDeniedError",
				RetryErrorType: "CLIENT_ERROR",
			}),
			want: classFatal,
		},
		"server error is not a throttle": {
			err: rpcErr(t, api.RPCCodeBridgeExited, "The service failed.", mappedErrorData{
				ErrorType:      "InternalServerError",
				RetryErrorType: "SERVER_ERROR",
			}),
			want: classFatal,
		},
		"mapped error with no retry classification is fatal": {
			err:  rpcErr(t, api.RPCCodeBridgeExited, "some other mapped failure", nil),
			want: classFatal,
		},
		"nil is fatal":         {err: nil, want: classFatal},
		"plain error is fatal": {err: errors.New("something else"), want: classFatal},
		// Untyped errors fail CLOSED even when their text looks retryable. The
		// bridge layer is what types a retryable condition; matching on
		// substrings here would make the classifier guess from prose.
		"untyped not-idle text is fatal": {err: errors.New("agent is not idle right now"), want: classFatal},
		"untyped internal text is fatal": {err: errors.New("Internal error"), want: classFatal},
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

// retriesFor mirrors callPromptWithRetry's decision so the policy is asserted
// through the same expression the retry loop evaluates. A helper rather than a
// production predicate: the previous pass kept an IsRetryablePromptError wrapper
// for readability, and punused correctly called it out as used in tests only.
func retriesFor(err error) bool {
	class := classifyPromptFailure(err)
	return class == classBusy || class == classTransient
}

// TestRetryPolicy_NeverRetriesADeadBridgeOrAThrottle states the two regressions
// this pass exists to prevent. Both were true before and both were wrong: a
// corpse cannot answer, and KAS's own client already spent five adaptive
// attempts on the throttle before handing it over, so a sixth only deepens it.
func TestRetryPolicy_NeverRetriesADeadBridgeOrAThrottle(t *testing.T) {
	dead := &api.TransportError{Err: api.ErrBridgeExited, Retryable: true}
	if retriesFor(dead) {
		t.Error("a dead bridge is reported retryable; every attempt fails instantly against a closed done channel")
	}

	throttle := rpcErr(t, api.RPCCodeBridgeExited, "Too many requests.", mappedErrorData{
		ErrorType:      "ClientThrottleError",
		RetryErrorType: "THROTTLING",
	})
	if retriesFor(throttle) {
		t.Error("a throttle is reported retryable; kiro-cli already exhausted its own adaptive attempts")
	}

	// The two classes that SHOULD still retry, so the fix cannot be "return
	// false for everything".
	if !retriesFor(fmt.Errorf("ACP error -32001: %w", api.ErrNotIdle)) {
		t.Error("a busy session must still be retried")
	}
	if !retriesFor(&api.TransportError{Err: errors.New("write to ACP"), Retryable: true}) {
		t.Error("a transient write failure must still be retried")
	}
}

// TestPromptFailureReason_NamesAThrottle guards the user-facing half. Before
// this, a rate-limited turn surfaced as the literal string "Internal error",
// which describes neither the cause nor the remedy, on the one failure where
// both are known.
func TestPromptFailureReason_NamesAThrottle(t *testing.T) {
	const kasMsg = "Too many requests, please wait before trying again."
	err := rpcErr(t, api.RPCCodeBridgeExited, kasMsg, mappedErrorData{
		ErrorType:      "ClientThrottleError",
		RetryErrorType: "THROTTLING",
		RequestID:      "req-9",
	})
	got := promptFailureReason(err)

	// KAS's own message is already written for a user, so it must SURVIVE rather
	// than be replaced. What is added is the one thing it cannot know (retrying
	// now will not help, because KAS already spent its attempts) plus the request
	// id, which lives in `data` where nothing else surfaces it.
	if !strings.Contains(got, kasMsg) {
		t.Errorf("reason %q dropped KAS's own user-facing message", got)
	}
	for _, want := range []string{"already retried", "req-9"} {
		if !strings.Contains(got, want) {
			t.Errorf("reason %q does not mention %q", got, want)
		}
	}

	// A non-throttle mapped error must NOT gain the wait-and-retry advice.
	other := rpcErr(t, api.RPCCodeBridgeExited, "The request was invalid.", mappedErrorData{
		ErrorType:      "GenericValidationError",
		RetryErrorType: "CLIENT_ERROR",
	})
	if reason := promptFailureReason(other); strings.Contains(reason, "already retried") {
		t.Errorf("a validation error was given throttle advice: %q", reason)
	}

	// A mapped error with an EMPTY message must not fall back to the raw triplet.
	// `data` is machine fields here, so RPCDetails parses it as neither of its two
	// shapes and would return the JSON verbatim — the rendering this function's
	// own comment names as the regression. The cases above cannot reach that
	// branch, because KAS normally fills `message`.
	blank := rpcErr(t, api.RPCCodeBridgeExited, "", mappedErrorData{
		ErrorType:      "ClientThrottleError",
		RetryErrorType: "THROTTLING",
		RequestID:      "req-11",
	})
	got = promptFailureReason(blank)
	for _, leak := range []string{"{", "retryErrorType", "\"requestId\""} {
		if strings.Contains(got, leak) {
			t.Errorf("empty-message mapped error leaked the raw triplet (%q) at the user: %q", leak, got)
		}
	}
	if !strings.Contains(got, "ClientThrottleError") {
		t.Errorf("reason %q does not name the errorType, the one readable token in the triplet", got)
	}
	if !strings.Contains(got, "req-11") {
		t.Errorf("reason %q dropped the request id", got)
	}

	// Anything else falls through verbatim: inventing prose for an error we do
	// not understand would hide the only text there is.
	plain := errors.New("some other failure")
	if got := promptFailureReason(plain); got != plain.Error() {
		t.Errorf("promptFailureReason(%v) = %q, want it passed through unchanged", plain, got)
	}
}
