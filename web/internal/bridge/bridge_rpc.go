package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"vibekit/internal/api"
)

const jsonRPCVersion = "2.0"

// bridgeExitedResp is the pointer-identity sentinel readLoop's drain
// sends into each pending channel on exit. Call's ch-receive branch
// compares pointer identity against this value and translates it to
// errBridgeExited, so "kiro-cli died on its own" and "Stop() races a
// fresh Call" return the same sentinel without string comparison.
var bridgeExitedResp = &api.RPCResponse{
	Error: &api.RPCError{Code: api.RPCCodeBridgeExited, Message: "ACP bridge exited"},
}

func (b *Bridge) sendNotif(msg *api.RPCResponse) {
	select {
	case b.notifCh <- msg:
	case <-b.done:
	}
}

func (b *Bridge) readLoop() {
	defer func() {
		// Unblock any in-flight Call waiters so a dead worker does not
		// permanently wedge the bridge. Using bridgeExitedResp's
		// pointer identity lets Call translate the signal to
		// errBridgeExited without string comparison, unifying the
		// drain path and the done-channel race-guard on the same
		// sentinel.
		b.pendingMu.Lock()
		for id, ch := range b.pending {
			select {
			case ch <- bridgeExitedResp:
			default:
			}
			delete(b.pending, id)
		}
		b.pendingMu.Unlock()
		close(b.notifCh)
	}()
	var tracker parseErrTracker
	for b.stdout.Scan() {
		line := b.stdout.Bytes()
		var msg api.RPCResponse
		if err := json.Unmarshal(line, &msg); err != nil {
			switch tracker.Record() {
			case parseErrLog:
				slog.Error("ACP parse", "error", err, "line_len", len(line))
			case parseErrSummarize:
				slog.Error("ACP parse storm",
					"count", tracker.SummaryCount(),
					"window_s", int(parseErrWindow/time.Second))
			case parseErrCircuitBreak:
				slog.Error("ACP parse: consecutive-error ceiling reached; reaping bridge",
					"consecutive", tracker.consecutive)
				go b.Stop()
				return
			}
			continue
		}
		tracker.Reset()
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
				ch <- &msg
			}
		case msg.ID != nil:
			// Request FROM kiro-cli (fs/read_text_file, terminal/*);
			// the hub will eventually call Respond(*msg.ID, ...).
			b.sendNotif(&msg)
		case msg.Method != "":
			// Server-sent notification (session/update, etc.).
			slog.Debug("ACP notification", "method", msg.Method)
			b.sendNotif(&msg)
		}
	}
	if err := b.stdout.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			slog.Error("ACP read: JSON line exceeds scanner cap; reaping bridge",
				"cap", scannerLineCap)
		} else {
			slog.Error("ACP read", "error", err)
		}
	}
	// Reap the subprocess and unblock any Call waiters that
	// arrived between the drain and this point. Async because
	// Stop waits for cmd.Wait, which is downstream of this
	// goroutine's exit (cmd.Wait blocks on stdout/stderr drain
	// which we are). Fires on both clean EOF and error exits
	// so the kiro-cli process entry does not leak as a zombie.
	go b.Stop()
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
func (b *Bridge) Call(ctx context.Context, method string, params any) (*api.RPCResponse, error) {
	id := b.nextID.Add(1)
	req := api.RPCRequest{JSONRPC: jsonRPCVersion, ID: id, Method: method, Params: params}
	ch := make(chan *api.RPCResponse, 1)
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
		return nil, &TransportError{Err: fmt.Errorf("write to ACP: %w", writeErr), Retryable: true}
	}
	select {
	case resp := <-ch:
		if resp == bridgeExitedResp {
			return nil, &TransportError{Err: errBridgeExited, Retryable: true}
		}
		if resp.Error != nil {
			// Classify "not idle" at the bridge layer so callers can
			// use errors.Is(err, api.ErrNotIdle) without string matching.
			if resp.Error.Code == api.RPCCodeNotIdle {
				return resp, fmt.Errorf("ACP error %d: %w", resp.Error.Code, ErrNotIdle)
			}
			return resp, fmt.Errorf("ACP error %d: %w", resp.Error.Code, resp.Error)
		}
		return resp, nil
	case <-b.done:
		b.deregisterPending(id)
		return nil, &TransportError{Err: errBridgeExited, Retryable: true}
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
	req := api.RPCNotification{JSONRPC: jsonRPCVersion, Method: method, Params: params}
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
// unless err unwraps to an *api.RPCError with a specific code.
func (b *Bridge) Respond(ctx context.Context, id int64, result any, err error) error {
	if cErr := ctx.Err(); cErr != nil {
		return cErr
	}
	resp := api.RPCResponseOut{JSONRPC: jsonRPCVersion, ID: id}
	if err != nil {
		code := api.RPCCodeInternal
		msg := err.Error()
		if re, ok := errors.AsType[*api.RPCError](err); ok {
			code = re.Code
			msg = re.Message
		}
		resp.Error = &api.RPCErrorOut{Code: code, Message: msg}
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
