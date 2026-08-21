package command

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// rpcErr builds an RPCError with an optional data payload, the way the bridge
// hands one to the command layer.
func rpcErr(t *testing.T, code int, msg string, data any) error {
	t.Helper()
	e := &vibekit.RPCError{Code: code, Message: msg}
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
			err:  &vibekit.TransportError{Err: vibekit.ErrBridgeExited, Retryable: true},
			want: classPipeDeath,
		},
		"dead bridge survives wrapping": {
			err:  fmt.Errorf("prompt: %w", &vibekit.TransportError{Err: vibekit.ErrBridgeExited, Retryable: true}),
			want: classPipeDeath,
		},
		"write failure is transient": {
			err:  &vibekit.TransportError{Err: errors.New("write to ACP: broken pipe"), Retryable: true},
			want: classTransient,
		},
		"non-retryable transport is fatal": {
			err:  &vibekit.TransportError{Err: errors.New("bad frame"), Retryable: false},
			want: classFatal,
		},
		"session busy by sentinel": {
			err:  fmt.Errorf("ACP error -32001: %w", vibekit.ErrNotIdle),
			want: classBusy,
		},
		"session busy by code": {
			err:  rpcErr(t, vibekit.RPCCodeNotIdle, "session is not idle", nil),
			want: classBusy,
		},
		"internal error is transient": {
			err:  rpcErr(t, vibekit.RPCCodeInternal, "Internal error", map[string]string{"details": "transient upstream fault"}),
			want: classTransient,
		},
		// Retrying an expired token is pure latency, and KAS collapses auth
		// onto the same -32603 as a genuine fault.
		"auth failure is fatal, not transient": {
			err:  rpcErr(t, vibekit.RPCCodeInternal, "Internal error", map[string]string{"details": "not logged in"}),
			want: classFatal,
		},
		"expired token is fatal": {
			err:  rpcErr(t, vibekit.RPCCodeInternal, "ExpiredToken: refresh required", nil),
			want: classFatal,
		},
		// KAS reports a throttle on the same -32000 vibekit uses for its own
		// bridge-exited constant. The data payload is the distinguisher.
		"throttle is not retryable": {
			err: rpcErr(t, vibekit.RPCCodeBridgeExited, "Too many requests, please wait.", mappedErrorData{
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
			err: rpcErr(t, vibekit.RPCCodeBridgeExited, "The request was invalid.", mappedErrorData{
				ErrorType:      "GenericValidationError",
				RetryErrorType: "CLIENT_ERROR",
			}),
			want: classFatal,
		},
		"access denied is not a throttle": {
			err: rpcErr(t, vibekit.RPCCodeBridgeExited, "Access denied.", mappedErrorData{
				ErrorType:      "AccessDeniedError",
				RetryErrorType: "CLIENT_ERROR",
			}),
			want: classFatal,
		},
		"server error is not a throttle": {
			err: rpcErr(t, vibekit.RPCCodeBridgeExited, "The service failed.", mappedErrorData{
				ErrorType:      "InternalServerError",
				RetryErrorType: "SERVER_ERROR",
			}),
			want: classFatal,
		},
		"mapped error with no retry classification is fatal": {
			err:  rpcErr(t, vibekit.RPCCodeBridgeExited, "some other mapped failure", nil),
			want: classFatal,
		},
		"nil is fatal":         {err: nil, want: classFatal},
		"plain error is fatal": {err: errors.New("something else"), want: classFatal},
		// A validation refusal is the one -32603 that must not be retried: the
		// request IS what was refused, so the two extra attempts upload the same
		// rejected bytes again. One case per shape the backend can refuse on.
		"oversized image is rejected": {
			err: rpcErr(t, vibekit.RPCCodeInternal, "Internal error", map[string]string{
				"details": "ImageSizeExceeded: image exceeds 5 MB maximum: 6714372 bytes > 5242880",
			}),
			want: classRejected,
		},
		"oversized prompt is rejected": {
			err: rpcErr(t, vibekit.RPCCodeInternal, "Internal error", map[string]string{
				"details": "PromptTooLong",
			}),
			want: classRejected,
		},
		"unsupported document type is rejected": {
			err: rpcErr(t, vibekit.RPCCodeInternal, "Internal error", map[string]string{
				"details": "DisallowedFileType",
			}),
			want: classRejected,
		},
		// The name can also arrive in the MESSAGE rather than in data, so the
		// predicate must search both halves the way isAuthShaped does.
		"validation name in the message is rejected": {
			err:  rpcErr(t, vibekit.RPCCodeInternal, "ImageDimensionExceeded", nil),
			want: classRejected,
		},
		// The other side of the same coin: adding the table must not swallow the
		// transient arm, which is still the right answer for a -32603 nobody has
		// classified.
		"unclassified internal error is still transient": {
			err: rpcErr(t, vibekit.RPCCodeInternal, "Internal error", map[string]string{
				"details": "upstream connection reset",
			}),
			want: classTransient,
		},
		// Quota is deliberately NOT in the table: it is not a statement about the
		// payload, so it must not inherit the shrink-your-prompt remedy. It keeps
		// whatever the unclassified arm gives it.
		"a spent monthly allowance is not a validation refusal": {
			err: rpcErr(t, vibekit.RPCCodeInternal, "Internal error", map[string]string{
				"details": "MonthlyRequestCount limit reached",
			}),
			want: classTransient,
		},
		// Untyped errors fail CLOSED even when their text looks retryable. The
		// bridge layer is what types a retryable condition; matching on
		// substrings here would make the classifier guess from prose.
		"untyped not-idle text is fatal": {err: errors.New("agent is not idle right now"), want: classFatal},
		"untyped internal text is fatal": {err: errors.New("Internal error"), want: classFatal},
		// Even the validation vocabulary must not classify an UNTYPED error: the
		// name carries no authority outside an RPC error's own payload.
		"untyped validation text is fatal": {err: errors.New("ImageSizeExceeded"), want: classFatal},
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
	dead := &vibekit.TransportError{Err: vibekit.ErrBridgeExited, Retryable: true}
	if retriesFor(dead) {
		t.Error("a dead bridge is reported retryable; every attempt fails instantly against a closed done channel")
	}

	throttle := rpcErr(t, vibekit.RPCCodeBridgeExited, "Too many requests.", mappedErrorData{
		ErrorType:      "ClientThrottleError",
		RetryErrorType: "THROTTLING",
	})
	if retriesFor(throttle) {
		t.Error("a throttle is reported retryable; kiro-cli already exhausted its own adaptive attempts")
	}

	// The two classes that SHOULD still retry, so the fix cannot be "return
	// false for everything".
	if !retriesFor(fmt.Errorf("ACP error -32001: %w", vibekit.ErrNotIdle)) {
		t.Error("a busy session must still be retried")
	}
	if !retriesFor(&vibekit.TransportError{Err: errors.New("write to ACP"), Retryable: true}) {
		t.Error("a transient write failure must still be retried")
	}
}

// TestPromptFailureReason_NamesAThrottle guards the user-facing half. Before
// this, a rate-limited turn surfaced as the literal string "Internal error",
// which describes neither the cause nor the remedy, on the one failure where
// both are known.
func TestPromptFailureReason_NamesAThrottle(t *testing.T) {
	const kasMsg = "Too many requests, please wait before trying again."
	err := rpcErr(t, vibekit.RPCCodeBridgeExited, kasMsg, mappedErrorData{
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
	other := rpcErr(t, vibekit.RPCCodeBridgeExited, "The request was invalid.", mappedErrorData{
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
	blank := rpcErr(t, vibekit.RPCCodeBridgeExited, "", mappedErrorData{
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

// TestPromptFailureReason_NamesTheRefusalAsTerminal guards the other half of the
// validation class: what the user is told.
//
// The backend's own account of WHAT was wrong already reaches them through
// rpcerr.Text. The thing it cannot know, and the thing a user cannot infer, is
// that this failure is terminal for these bytes — every other prompt failure they
// have met clears on a second Send, so without a sentence saying otherwise the
// rational next move is to press Send again and watch it fail identically.
func TestPromptFailureReason_NamesTheRefusalAsTerminal(t *testing.T) {
	const cause = "ImageSizeExceeded: image exceeds 5 MB maximum: 6714372 bytes > 5242880"
	err := rpcErr(t, vibekit.RPCCodeInternal, "Internal error", map[string]string{"details": cause})
	got := promptFailureReason(err)

	// The backend's account survives: it carries the numbers, which is the only
	// place a user learns how much smaller the attachment has to get.
	if !strings.Contains(got, cause) {
		t.Errorf("reason %q dropped the backend's own account of the refusal", got)
	}
	for _, want := range []string{"refused as sent", "smaller"} {
		if !strings.Contains(got, want) {
			t.Errorf("reason %q does not tell the user the refusal is terminal (missing %q)", got, want)
		}
	}

	// An unclassified -32603 must NOT gain the advice. It is the retryable class,
	// so telling that user to shrink their prompt would send them after the wrong
	// bug — the same defect shape as the throttle regression above, one class over.
	other := rpcErr(t, vibekit.RPCCodeInternal, "Internal error", map[string]string{
		"details": "upstream connection reset",
	})
	if reason := promptFailureReason(other); strings.Contains(reason, "refused as sent") {
		t.Errorf("a transient internal error was told its request was refused: %q", reason)
	}
}
