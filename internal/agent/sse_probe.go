package agent

import (
	"bytes"
	"errors"
	"net/http"
	"sync/atomic"
)

// The SSE probe: the counters and the close-after hook internal/server's test-only
// control surface (built with -tags vibekit_test) reads and arms. Exported here
// because the server package reaches the runtime only through its role interfaces.

// SSEClientCount is the number of connections the hub is serving right now.
func (rt *Runtime) SSEClientCount() int { return rt.bus.fanout.ClientCount() }

// SSEConnects reports how many connects each wire generation has made: legacy
// (no SSE-Wire header, the v2 bundle) and v3.
func (rt *Runtime) SSEConnects() (legacy, v3 uint64) {
	return rt.bus.legacyConnects.Load(), rt.bus.v3Connects.Load()
}

// CloseNextSSEAfter arms the next SSE connection to be cut after its n-th data
// frame is on the wire: the write that would carry frame n+1 fails, Serve ends,
// and the peer sees the stream close between two frames. Zero or negative disarms.
func (rt *Runtime) CloseNextSSEAfter(n int) {
	rt.bus.closeAfter.Store(int64(n))
}

// errCloseAfter is what the cut connection's writer answers once its budget is spent.
var errCloseAfter = errors.New("sse: connection cut by the close-after hook")

// keepaliveFrameStart opens every keepalive write. The named keepalive carries a
// data: line, so it is skipped by name; the hub refuses to publish under that name.
var keepaliveFrameStart = []byte("event: " + keepaliveEventName + "\n")

// closeAfterWriter counts data frames through Write and fails the first write past
// the budget. The retry line carries no data: line and a keepalive is skipped.
type closeAfterWriter struct {
	http.ResponseWriter
	remaining int
}

func (w *closeAfterWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("data: ")) && !bytes.HasPrefix(p, keepaliveFrameStart) {
		if w.remaining <= 0 {
			return 0, errCloseAfter
		}
		w.remaining--
	}
	return w.ResponseWriter.Write(p)
}

// Unwrap exposes the underlying writer so http.ResponseController reaches its
// Flusher and write deadlines through the wrapper.
func (w *closeAfterWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// armedWriter returns w wrapped for the cut when a close-after is armed, and
// disarms it: the hook is for the NEXT connection only.
func armedWriter(closeAfter *atomic.Int64, w http.ResponseWriter) http.ResponseWriter {
	n := closeAfter.Swap(0)
	if n <= 0 {
		return w
	}
	return &closeAfterWriter{ResponseWriter: w, remaining: int(n)}
}
