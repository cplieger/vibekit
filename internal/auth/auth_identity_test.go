package auth

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// countingReader is an identity read that records how often it ran and answers
// whatever the test set. A function seam rather than a subprocess: these tests
// are about the cache's staleness and coalescing rules, not about kiro-cli.
type countingReader struct {
	// block, when non-nil, holds a read open so a test can observe the cache
	// mid-refresh.
	block chan struct{}
	resp  WhoamiResponse
	mu    sync.Mutex
	calls int
}

func (r *countingReader) read(context.Context) WhoamiResponse {
	r.mu.Lock()
	r.calls++
	resp := r.resp
	block := r.block
	r.mu.Unlock()
	if block != nil {
		<-block
	}
	return resp
}

func (r *countingReader) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *countingReader) setResp(resp WhoamiResponse) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resp = resp
}

// signedIn is the arm a successful read produces, for the tests below.
func signedIn(email string) WhoamiResponse {
	return WhoamiResponse{State: WhoamiSignedIn, Email: email}
}

// waitForCalls polls until the reader has run at least n times, failing with a
// diagnostic at the deadline. A poll rather than a sleep: the refresh runs in a
// goroutine the cache owns, so there is no handle to join.
func waitForCalls(t *testing.T, r *countingReader, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for r.count() < n {
		if time.Now().After(deadline) {
			t.Fatalf("read ran %d times in 2s, want at least %d", r.count(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestIdentityCache_ColdSnapshotIsUnavailable(t *testing.T) {
	r := &countingReader{resp: signedIn("u@example.com")}
	c := newIdentityCache(r.read, time.Second)

	got := c.snapshot()

	// The seed, not a sign-out: nobody has asked kiro-cli yet, and saying
	// signed_out here is the defect the third arm exists to remove.
	if got.State != WhoamiUnavailable {
		t.Errorf("State = %q, want %q before the first read lands", got.State, WhoamiUnavailable)
	}
	if got.Reason != reasonNotRead {
		t.Errorf("Reason = %q, want %q", got.Reason, reasonNotRead)
	}
}

func TestIdentityCache_ColdSnapshotKicksARead(t *testing.T) {
	r := &countingReader{resp: signedIn("u@example.com")}
	c := newIdentityCache(r.read, time.Second)

	c.snapshot()
	waitForCalls(t, r, 1)

	if got := c.snapshot(); got.Email != "u@example.com" {
		t.Errorf("Email after the kicked read = %q, want u@example.com", got.Email)
	}
}

func TestIdentityCache_FreshSnapshotReadsNothing(t *testing.T) {
	r := &countingReader{resp: signedIn("u@example.com")}
	c := newIdentityCache(r.read, time.Hour)
	c.refresh()

	for range 20 {
		if got := c.snapshot(); got.Email != "u@example.com" {
			t.Fatalf("Email = %q, want u@example.com", got.Email)
		}
	}

	if got := r.count(); got != 1 {
		t.Errorf("20 snapshots ran the read %d times, want the 1 from refresh", got)
	}
}

func TestIdentityCache_StaleSnapshotRefreshesBehindTheAnswer(t *testing.T) {
	r := &countingReader{resp: signedIn("first@example.com")}
	c := newIdentityCache(r.read, time.Hour)
	c.refresh()
	r.setResp(signedIn("second@example.com"))
	c.invalidate()

	// The stale answer comes back immediately: the reader must never wait on a
	// kiro-cli fork.
	if got := c.snapshot(); got.Email != "first@example.com" {
		t.Errorf("Email = %q, want the held first@example.com while the refresh runs", got.Email)
	}
	waitForCalls(t, r, 2)
	if got := c.snapshot(); got.Email != "second@example.com" {
		t.Errorf("Email after the background refresh = %q, want second@example.com", got.Email)
	}
}

func TestIdentityCache_InvalidateKeepsTheHeldIdentity(t *testing.T) {
	// The login window's rule: every poll must revalidate, and every poll must
	// still get the last known answer rather than an `unavailable` the UI would
	// render as a banner over a working app.
	r := &countingReader{resp: signedIn("u@example.com")}
	c := newIdentityCache(r.read, time.Hour)
	c.refresh()

	c.invalidate()

	// snapshot returns the held value under the same lock that launches the
	// refresh, so this answer is the pre-refresh one by construction.
	if got := c.snapshot(); got.State != WhoamiSignedIn || got.Email != "u@example.com" {
		t.Errorf("snapshot after invalidate = %+v, want the held signed_in identity", got)
	}
}

func TestIdentityCache_PublishOverwritesWithoutReading(t *testing.T) {
	r := &countingReader{resp: signedIn("u@example.com")}
	c := newIdentityCache(r.read, time.Hour)
	c.refresh()
	// Stale first, so the freshness half of the assertion below can only be
	// satisfied by the publish. The reader would answer signed_in again, which
	// is what makes a revalidation visible in the state rather than only in a
	// call count.
	c.invalidate()

	c.publish(signedOutIdentity())

	if got := c.snapshot(); got.State != WhoamiSignedOut {
		t.Fatalf("State = %q, want %q", got.State, WhoamiSignedOut)
	}
	// The publish must leave the entry FRESH, or the logout answer is
	// immediately revalidated back to signed_in by a fork that has nothing new
	// to learn. Polled rather than read once: a stale entry's refresh runs in a
	// goroutine, so the wrong answer arrives shortly AFTER the read above.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if got := c.snapshot(); got.State != WhoamiSignedOut {
			t.Fatalf("State became %q, want the published %q to stand: publish must mark the entry fresh",
				got.State, WhoamiSignedOut)
		}
		time.Sleep(time.Millisecond)
	}
	if got := r.count(); got != 1 {
		t.Errorf("read ran %d times, want the 1 from refresh", got)
	}
}

func TestIdentityCache_ConcurrentSnapshotsRunOneRead(t *testing.T) {
	// The endpoint fires on every page load and every SSE reconnect, so a burst
	// of cold readers must not fork a kiro-cli each.
	r := &countingReader{resp: signedIn("u@example.com"), block: make(chan struct{})}
	c := newIdentityCache(r.read, time.Hour)

	var wg sync.WaitGroup
	for range 25 {
		wg.Go(func() { c.snapshot() })
	}
	wg.Wait()
	close(r.block)

	if got := r.count(); got > 1 {
		t.Errorf("25 concurrent snapshots ran the read %d times, want at most 1", got)
	}
}

func TestRun_PrimesThenStops(t *testing.T) {
	r := &countingReader{resp: signedIn("u@example.com")}
	h := NewHandler(fixedPath("/bin/true"))
	h.identity = newIdentityCache(r.read, time.Hour)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Run(ctx)
	}()

	waitForCalls(t, r, 1)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of ctx cancellation")
	}

	if got := h.identity.snapshot().Email; got != "u@example.com" {
		t.Errorf("Email after the prime = %q, want u@example.com", got)
	}
}

func TestUnavailableIdentity_SanitizesTheReason(t *testing.T) {
	// Every reason is a constant today. The sanitize sits in the constructor so
	// a future reason built from upstream bytes cannot skip it, and this is what
	// pins that.
	got := unavailableIdentity("bad\u202ereason\nwith\rcontrols")

	for _, bad := range []string{"\u202e", "\n", "\r"} {
		if strings.Contains(got.Reason, bad) {
			t.Errorf("Reason = %q, want %q folded out", got.Reason, bad)
		}
	}
	if got.State != WhoamiUnavailable {
		t.Errorf("State = %q, want %q", got.State, WhoamiUnavailable)
	}
}
