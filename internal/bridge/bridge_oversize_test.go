package bridge

// readLoop's behaviour on an oversize stdout frame: the loss is SURFACED and the
// session survives. Before D24b one frame past the cap ended the scan, readLoop
// treated the bridge as exited, and the whole chat session died with it.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/slogx/capture"
	"github.com/cplieger/vibekit/internal/api"
)

// pendingCall registers one pending request id and returns its channel, the way
// Call does. Lets a test observe what readLoop hands a waiter without standing up
// a subprocess.
func pendingCall(b *Bridge, id int64) chan *api.RPCResponse {
	ch := make(chan *api.RPCResponse, 1)
	b.pendingMu.Lock()
	b.pending[id] = ch
	b.pendingMu.Unlock()
	return ch
}

// The loss must not be silent. A dropped frame's bytes are gone, so the bridge
// cannot tell a notification from the response to a pending request; it fails
// every pending request rather than leave one waiting forever, which is how the
// prompt path finalizes the turn and tells the user.
func TestReadLoop_OversizeFrameFailsPendingCalls(t *testing.T) {
	c := capture.Default(t)
	huge := strings.Repeat("x", scannerLineCap+16)
	b := readLoopBridge(strings.NewReader(huge + "\n"))
	ch := pendingCall(b, 7)

	b.readLoop()

	select {
	case got := <-ch:
		if got != frameTooLargeResp {
			t.Fatalf("pending call got %#v, want the frameTooLargeResp sentinel", got)
		}
	default:
		t.Fatal("pending call was left waiting after an oversize frame; Call has no deadline, so it would hang forever")
	}
	if c.CountExact("ACP read: frame exceeds the size cap; dropped it and failed the pending requests") == 0 {
		t.Error("the dropped frame was not logged; the loss has to be visible somewhere")
	}
}

// The frame after an oversize one still reaches dispatch, which is the difference
// between killing the TURN and killing the SESSION.
func TestReadLoop_ResumesDispatchAfterAnOversizeFrame(t *testing.T) {
	_ = capture.Default(t)
	huge := strings.Repeat("x", scannerLineCap+16)
	b := readLoopBridge(strings.NewReader(
		huge + "\n" + `{"jsonrpc":"2.0","method":"session/update","params":{}}` + "\n"))
	b.notifCh = make(chan *api.RPCResponse, 4)

	b.readLoop()

	select {
	case msg := <-b.notifCh:
		if msg == nil || msg.Method != api.MethodSessionUpdate {
			t.Fatalf("notification after the oversize frame = %#v, want session/update", msg)
		}
	default:
		t.Fatal("the frame after an oversize one never reached dispatch; the stream did not resynchronise")
	}
}

// Call translates the sentinel into a NON-retryable transport error carrying
// api.ErrFrameTooLarge. Retryability is the load-bearing half: retrying would
// re-run an expensive turn to produce the same oversize payload, and the wording
// is what promptFailureReason puts in front of the user.
func TestCall_FrameTooLargeIsNonRetryableAndNamed(t *testing.T) {
	b := New("/nonexistent", "/work")
	b.stdin = &captureWriter{}

	done := make(chan error, 1)
	go func() {
		_, err := b.Call(t.Context(), api.MethodPrompt, nil)
		done <- err
	}()
	waitPending(t, b, 1)
	b.failPending(frameTooLargeResp)

	select {
	case err := <-done:
		if !errors.Is(err, api.ErrFrameTooLarge) {
			t.Fatalf("Call error = %v, want api.ErrFrameTooLarge", err)
		}
		if errors.Is(err, api.ErrBridgeExited) {
			t.Error("a dropped frame must not read as a dead bridge: the process and the session are still alive")
		}
		te, ok := errors.AsType[*api.TransportError](err)
		if !ok {
			t.Fatalf("Call error %T, want *api.TransportError", err)
		}
		if te.Retryable {
			t.Error("frame-too-large was marked retryable; the same prompt reproduces the same oversize payload")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Call did not return after the pending answer")
	}
}

// An unterminated blob still reaps the bridge, because there is no frame boundary
// left to resynchronise on. The distinct log line is what tells an operator this
// happened rather than the process going away.
func TestReadLoop_ExhaustedDrainReapsWithItsOwnLogLine(t *testing.T) {
	c := capture.Default(t)
	b := readLoopBridge(&endlessReader{b: 'q'})

	b.readLoop()

	select {
	case <-b.done:
	case <-time.After(2 * time.Second):
		t.Fatal("readLoop did not reap the bridge after the drain budget was exhausted")
	}
	if c.CountExact("ACP read: a single frame never terminated within the drain budget; reaping bridge") == 0 {
		t.Error("the exhausted-drain case did not get its own log line")
	}
}
