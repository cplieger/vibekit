package command

// The retry LOOP, in synthetic time.
//
// prompt_failure_test.go covers the retry POLICY — which failure classes get a
// second attempt — through a helper that mirrors callPromptWithRetry's
// predicate. Nothing covered the loop that consumes it: how many attempts it
// spends, how long it waits between them, or what it returns when the turn
// context dies mid-wait. On a real clock it could not be covered cheaply, since
// the shipped delay is 2s and the shipped attempt count is 2, so a faithful test
// costs four seconds of wall time and can still only assert a tolerance.
//
// retry touches no process, socket or PTY — it calls a closure and waits on a
// timer — so a synctest bubble reaches all of it. Each assertion below is an
// EXACT equality on the bubble's synthetic clock against the SHIPPED
// promptRetryDelay: a delay one nanosecond off fails, where a real-clock
// tolerance wide enough not to flake would have admitted a loop that skipped its
// wait entirely.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

var errRetryable = errors.New("retryable")

// countingFn returns a fn that fails with errRetryable until the nth call
// (1-based), which succeeds. n <= 0 never succeeds.
func countingFn(succeedOn int, calls *int) func() (*vibekit.RPCResponse, error) {
	return func() (*vibekit.RPCResponse, error) {
		*calls++
		if succeedOn > 0 && *calls >= succeedOn {
			return &vibekit.RPCResponse{}, nil
		}
		return nil, errRetryable
	}
}

func alwaysRetry(error) bool { return true }

// TestRetry_SpendsOneDelayPerAttemptAndNoMore pins the loop's whole cost.
//
// maxAttempts is the number of RETRIES, so a call that never succeeds invokes fn
// 1+maxAttempts times and waits exactly maxAttempts delays. Both halves matter:
// an off-by-one in the loop bound changes how many times an expensive prompt is
// re-sent, and a missing wait would hammer a busy session as fast as the
// scheduler allows.
func TestRetry_SpendsOneDelayPerAttemptAndNoMore(t *testing.T) {
	for _, maxAttempts := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("maxAttempts=%d", maxAttempts), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				start := time.Now()
				_, err := retry(t.Context(), maxAttempts, alwaysRetry, countingFn(0, &calls))

				if !errors.Is(err, errRetryable) {
					t.Errorf("retry(maxAttempts=%d) err = %v, want the last fn error", maxAttempts, err)
				}
				if want := 1 + maxAttempts; calls != want {
					t.Errorf("retry(maxAttempts=%d) called fn %d times, want %d "+
						"(one attempt plus maxAttempts retries)", maxAttempts, calls, want)
				}
				if got, want := time.Since(start), time.Duration(maxAttempts)*promptRetryDelay; got != want {
					t.Errorf("retry(maxAttempts=%d) waited %v, want exactly %v", maxAttempts, got, want)
				}
			})
		})
	}
}

// TestRetry_StopsAtTheFirstSuccess: a retry loop that kept going after a
// successful attempt would re-send a prompt whose turn is already running.
func TestRetry_StopsAtTheFirstSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		start := time.Now()
		resp, err := retry(t.Context(), 3, alwaysRetry, countingFn(2, &calls))

		if err != nil || resp == nil {
			t.Fatalf("retry() = (%v, %v), want a response and no error", resp, err)
		}
		if calls != 2 {
			t.Errorf("retry() called fn %d times, want 2 (it succeeded on the second)", calls)
		}
		if got := time.Since(start); got != promptRetryDelay {
			t.Errorf("retry() waited %v, want exactly one delay (%v)", got, promptRetryDelay)
		}
	})
}

// TestRetry_DoesNotWaitOnAClassItWillNotRetry is the four seconds the classifier
// exists to save. Before the failure classes were split apart, a dead bridge was
// reported retryable and the loop spent both delays failing instantly against a
// closed done channel; the assertion here is that a non-retryable first error
// costs no wait at all.
func TestRetry_DoesNotWaitOnAClassItWillNotRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		start := time.Now()
		_, err := retry(t.Context(), 2, func(error) bool { return false }, countingFn(0, &calls))

		if !errors.Is(err, errRetryable) {
			t.Errorf("retry() err = %v, want the fn error passed straight through", err)
		}
		if calls != 1 {
			t.Errorf("retry() called fn %d times, want 1", calls)
		}
		if got := time.Since(start); got != 0 {
			t.Errorf("retry() waited %v on a class it will not retry, want 0", got)
		}
	})
}

// TestRetry_CancellationAbandonsTheWaitAndKeepsTheUPSTREAMError pins the two
// halves of the cancellation path, and the second is the one a reader gets
// wrong.
//
// The wait is abandoned the instant the context dies rather than run to term —
// that is what lets a client disconnect free the chat's prompt slot instead of
// holding it for the rest of the budget. And the error returned is the LAST fn
// error, NOT ctx.Err(): the prompt path runs its result through
// classifyPromptFailure and reports it to the user, so surfacing
// "context canceled" would replace the cause with the symptom and classify the
// turn as fatal when it was throttled or busy.
func TestRetry_CancellationAbandonsTheWaitAndKeepsTheUpstreamError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), promptRetryDelay/2)
		defer cancel()

		calls := 0
		start := time.Now()
		_, err := retry(ctx, 3, alwaysRetry, countingFn(0, &calls))

		if got := time.Since(start); got != promptRetryDelay/2 {
			t.Errorf("retry() returned after %v, want exactly %v — the wait must be "+
				"abandoned when the context dies, not run to term", got, promptRetryDelay/2)
		}
		if calls != 1 {
			t.Errorf("retry() called fn %d times after cancellation, want 1", calls)
		}
		if !errors.Is(err, errRetryable) {
			t.Errorf("retry() err = %v, want the upstream error; returning ctx.Err() "+
				"would hide the cause classifyPromptFailure has to read", err)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("retry() surfaced the context deadline instead of the upstream failure: %v", err)
		}
	})
}
