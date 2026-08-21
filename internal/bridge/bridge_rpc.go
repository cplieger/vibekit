package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

const jsonRPCVersion = "2.0"

// bridgeExitedResp is the pointer-identity sentinel readLoop's drain
// sends into each pending channel on exit. Call's ch-receive branch
// compares pointer identity against this value and translates it to
// errBridgeExited, so "kiro-cli died on its own" and "Stop() races a
// fresh Call" return the same sentinel without string comparison.
var bridgeExitedResp = &vibekit.RPCResponse{
	Error: &vibekit.RPCError{Code: vibekit.RPCCodeBridgeExited, Message: "ACP bridge exited"},
}

// frameTooLargeResp is the second pointer-identity sentinel, pushed into every
// pending channel when an oversize stdout frame was dropped. Separate from
// bridgeExitedResp because the two mean opposite things about the process: this
// one leaves it running and the session usable, so Call must translate it to a
// NON-retryable error rather than the retryable dead-bridge one.
var frameTooLargeResp = &vibekit.RPCResponse{
	Error: &vibekit.RPCError{Code: vibekit.RPCCodeInternal, Message: vibekit.ErrFrameTooLarge.Error()},
}

func (b *Bridge) sendNotif(msg *vibekit.RPCResponse) {
	select {
	case b.notifCh <- msg:
	case <-b.done:
	}
}

func (b *Bridge) readLoop() {
	defer b.drainPendingAndClose()
	var tracker parseErrTracker
	for {
		line, dropped, err := b.stdout.readFrame()
		if dropped > 0 {
			b.reportDroppedFrame(dropped)
		}
		if err != nil {
			logReadError(err)
			break
		}
		if dropped > 0 {
			continue // the frame's bytes are gone; there is nothing to parse
		}
		var msg vibekit.RPCResponse
		if uErr := json.Unmarshal(line, &msg); uErr != nil {
			if b.recordParseError(&tracker, len(line), uErr) {
				return
			}
			continue
		}
		tracker.Reset()
		b.dispatch(&msg)
	}
	// Reap the subprocess and unblock any Call waiters that
	// arrived between the drain and this point. Async because
	// Stop waits for cmd.Wait, which is downstream of this
	// goroutine's exit (cmd.Wait blocks on stdout/stderr drain
	// which we are). Fires on both clean EOF and error exits
	// so the kiro-cli process entry does not leak as a zombie.
	go b.Stop()
}

// reportDroppedFrame is what makes an oversize frame a visible loss rather than
// a silent one, and it is the half of the port Crew did not need.
//
// The dropped bytes are gone, so the bridge cannot know whether they were a
// notification (transcript content, nothing pending) or the RESPONSE to one of
// our own requests: the id was inside them. So it assumes the worse case and
// fails every pending request. Continuing instead would leave a pending
// session/prompt Call waiting forever, because Call has no client-side deadline
// by design, and that is strictly worse than the whole-bridge death this change
// replaces.
//
// Failing them is also how the user hears about it: the prompt path finalizes
// through its ordinary failure route (AbandonInFlightTurn plus
// error{prompt_failed}) carrying vibekit.ErrFrameTooLarge's wording. The process and
// the ACP session both stay alive, so the chat is immediately promptable. One
// large tool result now kills the TURN instead of the SESSION.
//
// A frame dropped while nothing is pending surfaces in this log line only. That
// is the residual: notifications arrive during a turn, so in practice a prompt
// Call is pending, but nothing on the wire re-sends a lost notification and
// vibekit cannot invent its content.
func (b *Bridge) reportDroppedFrame(dropped int) {
	failed := b.failPending(frameTooLargeResp)
	slog.Error("ACP read: frame exceeds the size cap; dropped it and failed the pending requests",
		"cap", scannerLineCap,
		"dropped_bytes", dropped,
		"failed_requests", failed)
}

// drainPendingAndClose unblocks any in-flight Call waiters so a dead
// worker does not permanently wedge the bridge, then closes notifCh.
// Using bridgeExitedResp's pointer identity lets Call translate the
// signal to errBridgeExited without string comparison, unifying the
// drain path and the done-channel race-guard on the same sentinel.
// Runs as readLoop's deferred cleanup.
func (b *Bridge) drainPendingAndClose() {
	b.failPending(bridgeExitedResp)
	close(b.notifCh)
}

// failPending hands resp to every waiting Call and clears the map, returning how
// many waiters it answered. Two callers with different sentinels: the exit drain
// above, and the oversize-frame report which keeps the bridge alive.
//
// The non-blocking send is deliberate — every pending channel is buffered with
// capacity 1 and a Call that already left through ctx.Done or b.done deregisters
// itself, so a full or abandoned channel must not stall this loop.
func (b *Bridge) failPending(resp *vibekit.RPCResponse) int {
	b.pendingMu.Lock()
	n := len(b.pending)
	for id, ch := range b.pending {
		select {
		case ch <- resp:
		default:
		}
		delete(b.pending, id)
	}
	b.pendingMu.Unlock()
	return n
}

// recordParseError feeds an unmarshal failure to the parse-error tracker
// and emits the appropriate log line. It returns true when the
// consecutive-error circuit breaker has tripped, signalling readLoop to
// reap the bridge and return.
func (b *Bridge) recordParseError(tracker *parseErrTracker, lineLen int, err error) bool {
	switch tracker.Record() {
	case parseErrLog:
		slog.Error("ACP parse", "error", err, "line_len", lineLen)
	case parseErrSummarize:
		slog.Error("ACP parse storm",
			"count", tracker.SummaryCount(),
			"window_s", int(parseErrWindow/time.Second))
	case parseErrCircuitBreak:
		slog.Error("ACP parse: consecutive-error ceiling reached; reaping bridge",
			"consecutive", tracker.consecutive)
		go b.Stop()
		return true
	}
	return false
}

// dispatch routes a successfully-decoded frame: a response to one of our
// requests is handed to the waiting Call; a request from kiro-cli or a
// server-sent notification is forwarded on notifCh.
func (b *Bridge) dispatch(msg *vibekit.RPCResponse) {
	switch {
	case msg.ID != nil && msg.Method == "":
		// Response to one of our requests.
		b.pendingMu.Lock()
		ch, ok := b.pending[*msg.ID]
		if ok {
			delete(b.pending, *msg.ID)
		}
		b.pendingMu.Unlock()
		if ok {
			ch <- msg
		}
	case msg.ID != nil:
		// Request FROM kiro-cli (fs/read_text_file, terminal/*);
		// the runtime will eventually call Respond(*msg.ID, ...).
		b.sendNotif(msg)
	case msg.Method != "":
		// Server-sent notification (session/update, etc.).
		slog.Debug("ACP notification", "method", msg.Method)
		b.sendNotif(msg)
	}
}

// logReadError reports the error that ended the read loop. A clean EOF is the
// ordinary teardown and logs nothing; an exhausted drain budget gets a distinct
// message, because that one says the stream stopped being newline-delimited JSON
// rather than that the process went away.
func logReadError(err error) {
	if err == nil || errors.Is(err, io.EOF) {
		return
	}
	if errors.Is(err, errFrameDrainExhausted) {
		slog.Error("ACP read: a single frame never terminated within the drain budget; reaping bridge",
			"budget_bytes", oversizeDrainCap)
		return
	}
	slog.Error("ACP read", "error", err)
}

// deregisterPending removes a pending request from the map. Used by
// Call's error paths to avoid repeating the lock/delete/unlock pattern.
func (b *Bridge) deregisterPending(id int64) {
	b.pendingMu.Lock()
	delete(b.pending, id)
	b.pendingMu.Unlock()
}

// Call sends a JSON-RPC request and waits for the matching response.
// Blocks until the response arrives or the bridge's readLoop exits
// (which unblocks all pending waiters with a sentinel error). Agent
// turns can legitimately run for hours; the caller owns turn
// cancellation via Notify("session/cancel", ...). Shutdown ordering is
// enforced by Stop → readLoop fanout; no in-Call timeout is needed.
func (b *Bridge) Call(ctx context.Context, method string, params any) (*vibekit.RPCResponse, error) {
	id := b.nextID.Add(1)
	req := vibekit.RPCRequest{JSONRPC: jsonRPCVersion, ID: id, Method: method, Params: params}
	ch := make(chan *vibekit.RPCResponse, 1)
	b.pendingMu.Lock()
	b.pending[id] = ch
	b.pendingMu.Unlock()
	data, err := json.Marshal(req)
	if err != nil {
		b.deregisterPending(id)
		return nil, err
	}
	data = append(data, '\n')
	if writeErr := b.writeFrame(data); writeErr != nil {
		b.deregisterPending(id)
		return nil, &vibekit.TransportError{Err: fmt.Errorf("write to ACP: %w", writeErr), Retryable: true}
	}
	select {
	case resp := <-ch:
		if resp == bridgeExitedResp {
			return nil, &vibekit.TransportError{Err: errBridgeExited, Retryable: true}
		}
		if resp == frameTooLargeResp {
			// NOT retryable: the same prompt would very likely produce the same
			// oversize payload, so retries buy a re-run of an expensive turn and
			// the same failure. See vibekit.ErrFrameTooLarge.
			return nil, &vibekit.TransportError{Err: vibekit.ErrFrameTooLarge, Retryable: false}
		}
		if resp.Error != nil {
			// Classify "not idle" at the bridge layer so callers can
			// use errors.Is(err, vibekit.ErrNotIdle) without string matching.
			if resp.Error.Code == vibekit.RPCCodeNotIdle {
				return resp, fmt.Errorf("ACP error %d: %w", resp.Error.Code, vibekit.ErrNotIdle)
			}
			return resp, fmt.Errorf("ACP error %d: %w", resp.Error.Code, resp.Error)
		}
		return resp, nil
	case <-b.done:
		b.deregisterPending(id)
		return nil, &vibekit.TransportError{Err: errBridgeExited, Retryable: true}
	case <-ctx.Done():
		b.deregisterPending(id)
		return nil, ctx.Err()
	}
}

// Notify sends a JSON-RPC notification (no response expected).
func (b *Bridge) Notify(ctx context.Context, method string, params any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	req := vibekit.RPCNotification{JSONRPC: jsonRPCVersion, Method: method, Params: params}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return b.writeFrame(data)
}

// Respond writes a JSON-RPC response to a request we received from
// kiro-cli (e.g. fs/read_text_file, fs/write_text_file). Pass a non-nil
// result for success, a non-nil err for failure; exactly one must be
// set. Errors from the ACP namespace use code -32603 (internal error)
// unless err unwraps to an *vibekit.RPCError with a specific code.
func (b *Bridge) Respond(ctx context.Context, id int64, result any, err error) error {
	if cErr := ctx.Err(); cErr != nil {
		return cErr
	}
	resp := vibekit.RPCResponseOut{JSONRPC: jsonRPCVersion, ID: id}
	if err != nil {
		code := vibekit.RPCCodeInternal
		msg := err.Error()
		if re, ok := errors.AsType[*vibekit.RPCError](err); ok {
			code = re.Code
			msg = re.Message
		}
		resp.Error = &vibekit.RPCErrorOut{Code: code, Message: msg}
	} else {
		resp.Result = result
	}
	data, mErr := json.Marshal(resp)
	if mErr != nil {
		return mErr
	}
	data = append(data, '\n')
	return b.writeFrame(data)
}

// writeFrame serialises stdin writes across Call/Notify/Respond and
// treats any short write as an error. io.Writer's contract permits a
// short write with err==nil on some pipe edge conditions; a truncated
// JSON-RPC frame would desync kiro-cli's stdin scanner and corrupt
// framing for every subsequent write, so we surface it as a real error
// instead of silently flushing a partial object.
func (b *Bridge) writeFrame(data []byte) error {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	n, err := b.stdin.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return fmt.Errorf("short write to ACP stdin: %d of %d bytes", n, len(data))
	}
	return nil
}
