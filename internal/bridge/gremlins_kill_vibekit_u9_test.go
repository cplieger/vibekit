package bridge

// Mutation-killing tests for unit vibekit-u9 (package internal/bridge).
// Targets surviving CONDITIONALS_NEGATION gremlins mutants in
// bridge_process.go (matchesKeyword separator chain), bridge_rpc.go
// (readLoop end-of-scan error log, Notify ctx/marshal guards, writeFrame
// write-error guard), and bridge_session.go (loadSession result/parse
// gates). Tests only; no production code is modified.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// --- shared test doubles ---

// gk_vibekit_u9_captureWriter is an io.WriteCloser standing in for the
// bridge's stdin. It records whether (and what) was written, and can be
// configured to fail every Write with a sentinel error.
type gk_vibekit_u9_captureWriter struct {
	failErr error
	buf     bytes.Buffer
	mu      sync.Mutex
	writes  int
}

func (w *gk_vibekit_u9_captureWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failErr != nil {
		return 0, w.failErr
	}
	w.writes++
	return w.buf.Write(p)
}

func (w *gk_vibekit_u9_captureWriter) Close() error { return nil }

func (w *gk_vibekit_u9_captureWriter) gk_vibekit_u9_wrote() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writes > 0
}

// gk_vibekit_u9_logCapture is a slog.Handler that records emitted record
// messages so a test can assert whether a particular log line was (or was
// not) produced by the code under test.
type gk_vibekit_u9_logCapture struct {
	msgs []string
	mu   sync.Mutex
}

func (h *gk_vibekit_u9_logCapture) Enabled(context.Context, slog.Level) bool { return true }

func (h *gk_vibekit_u9_logCapture) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.msgs = append(h.msgs, r.Message)
	return nil
}

func (h *gk_vibekit_u9_logCapture) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *gk_vibekit_u9_logCapture) WithGroup(string) slog.Handler      { return h }

func (h *gk_vibekit_u9_logCapture) gk_vibekit_u9_has(msg string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Contains(h.msgs, msg)
}

// gk_vibekit_u9_installCapture redirects slog to a capturing handler for
// the duration of the test, restoring the previous default afterwards.
func gk_vibekit_u9_installCapture(t *testing.T) *gk_vibekit_u9_logCapture {
	t.Helper()
	c := &gk_vibekit_u9_logCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(c))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return c
}

// gk_vibekit_u9_errReader yields the configured error on the first Read.
// With a non-EOF error the bufio.Scanner surfaces it via Err(); with
// io.EOF the scan terminates cleanly and Err() is nil.
type gk_vibekit_u9_errReader struct{ failErr error }

func (r gk_vibekit_u9_errReader) Read([]byte) (int, error) { return 0, r.failErr }

// gk_vibekit_u9_readLoopBridge builds the minimal Bridge readLoop needs.
func gk_vibekit_u9_readLoopBridge(r io.Reader) *Bridge {
	return &Bridge{
		stdout:  bufio.NewScanner(r),
		pending: make(map[int64]chan *api.RPCResponse),
		notifCh: make(chan *api.RPCResponse, 1),
		done:    make(chan struct{}),
	}
}

// --- bridge_process.go:278 — matchesKeyword separator chain ---
// Line 278: `if ch == ':' || ch == '[' || ch == ']' || ch == ' ' || ch == '='`.
// u9 owns the four CONDITIONALS_NEGATION mutants at cols 22/35/48/61, i.e.
// the '[' , ']' , ' ' and '=' terms (the ':' term is u8's). For each input
// the boundary-after char is exactly one distinct separator, which is the
// only true term in the OR chain; negating that term (==X -> !=X) makes the
// chain false, the loop find no further keyword, and matchesKeyword return
// false instead of true.
func Test_gk_vibekit_u9_matchesKeyword_separatorChain(t *testing.T) {
	const kw = "error"
	cases := []struct {
		name string
		low  string
		want bool
	}{
		// 278:22 (ch == '['): negating it drops the only matching term.
		{"open_bracket_is_boundary", "error[x", true},
		// 278:35 (ch == ']').
		{"close_bracket_is_boundary", "error]x", true},
		// 278:48 (ch == ' ').
		{"space_is_boundary", "error x", true},
		// 278:61 (ch == '=').
		{"equals_is_boundary", "error=x", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchesKeyword(tc.low, kw); got != tc.want {
				t.Errorf("matchesKeyword(%q, %q) = %v, want %v", tc.low, kw, got, tc.want)
			}
		})
	}
}

// --- bridge_rpc.go:95:32 — readLoop end-of-scan error log ---
// Line 95: `if err := b.stdout.Err(); err != nil {`. On a real (non-EOF)
// scanner error the original logs "ACP read"; negating to `== nil` skips
// the block and logs nothing. On a clean EOF the original logs nothing;
// the negation enters the block and logs "ACP read" with a nil error.
// Both directions are asserted.

func Test_gk_vibekit_u9_readLoop_logsACPReadOnScanError(t *testing.T) {
	c := gk_vibekit_u9_installCapture(t)
	b := gk_vibekit_u9_readLoopBridge(gk_vibekit_u9_errReader{failErr: errors.New("gk_vibekit_u9 read boom")})

	b.readLoop()
	select {
	case <-b.done:
	case <-time.After(time.Second):
		t.Fatal("readLoop did not reap bridge (done not closed)")
	}

	if !c.gk_vibekit_u9_has("ACP read") {
		t.Errorf(`readLoop on a scanner error did not log "ACP read"; want it present`)
	}
}

func Test_gk_vibekit_u9_readLoop_noACPReadOnCleanEOF(t *testing.T) {
	c := gk_vibekit_u9_installCapture(t)
	b := gk_vibekit_u9_readLoopBridge(gk_vibekit_u9_errReader{failErr: io.EOF})

	b.readLoop()
	select {
	case <-b.done:
	case <-time.After(time.Second):
		t.Fatal("readLoop did not reap bridge (done not closed)")
	}

	if c.gk_vibekit_u9_has("ACP read") {
		t.Errorf(`readLoop on a clean EOF logged "ACP read"; want it absent`)
	}
}

// --- bridge_rpc.go:168:27 — Notify ctx-cancellation guard ---
// Line 168: `if err := ctx.Err(); err != nil { return err }`.
// Canceled ctx: original returns the context error and writes nothing;
// negation (`== nil`) skips the early return and proceeds to write.
func Test_gk_vibekit_u9_Notify_canceledCtxReturnsErrNoWrite(t *testing.T) {
	b := New("/nonexistent", "/work")
	w := &gk_vibekit_u9_captureWriter{}
	b.stdin = w

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := b.Notify(ctx, "session/update", nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Notify(canceled ctx) err = %v, want context.Canceled", err)
	}
	if w.gk_vibekit_u9_wrote() {
		t.Errorf("Notify(canceled ctx) wrote to stdin; want no write")
	}
}

// --- bridge_rpc.go:168:27 + 173:9 — Notify happy path proceeds to write ---
// Good ctx + marshalable params: original marshals and writes a frame and
// returns nil. Negating the ctx guard (168, `== nil`) or the marshal guard
// (173, `== nil`) makes Notify return early WITHOUT writing, so the absence
// of a write distinguishes both mutants from the original.
func Test_gk_vibekit_u9_Notify_goodCtxValidParamsWritesFrame(t *testing.T) {
	b := New("/nonexistent", "/work")
	w := &gk_vibekit_u9_captureWriter{}
	b.stdin = w

	if err := b.Notify(context.Background(), "session/update", map[string]any{"k": "v"}); err != nil {
		t.Fatalf("Notify(good ctx, valid params) err = %v, want nil", err)
	}
	if !w.gk_vibekit_u9_wrote() {
		t.Errorf("Notify(good ctx, valid params) wrote nothing; want a frame written to stdin")
	}
}

// --- bridge_rpc.go:173:9 — Notify marshal-error guard ---
// Line 173: `if err != nil { return err }` after json.Marshal.
// Unmarshalable params (a channel) make Marshal fail: original returns the
// marshal error and writes nothing; negation (`== nil`) skips the early
// return and writes the (empty) buffer, returning nil.
func Test_gk_vibekit_u9_Notify_marshalErrorReturnsErrNoWrite(t *testing.T) {
	b := New("/nonexistent", "/work")
	w := &gk_vibekit_u9_captureWriter{}
	b.stdin = w

	err := b.Notify(context.Background(), "session/update", map[string]any{"bad": make(chan int)})
	if err == nil {
		t.Errorf("Notify(unmarshalable params) err = nil, want a marshal error")
	}
	if w.gk_vibekit_u9_wrote() {
		t.Errorf("Notify(unmarshalable params) wrote to stdin; want no write")
	}
}

// --- bridge_rpc.go:219:9 — writeFrame write-error guard ---
// Line 219: `if err != nil { return err }` after b.stdin.Write.
// A writer that fails every Write returns (0, sentinel): the original
// surfaces that exact error; negation (`== nil`) skips it and instead falls
// to the short-write branch (n=0 != len), returning a different error.
func Test_gk_vibekit_u9_writeFrame_returnsUnderlyingWriteError(t *testing.T) {
	sentinel := errors.New("gk_vibekit_u9 write boom")
	b := New("/nonexistent", "/work")
	b.stdin = &gk_vibekit_u9_captureWriter{failErr: sentinel}

	err := b.writeFrame([]byte("hello\n"))
	if err == nil {
		t.Fatalf("writeFrame with a failing writer returned nil, want an error")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("writeFrame err = %v, want the underlying write error %v", err, sentinel)
	}
}

// gk_vibekit_u9_runLoadSession drives loadSession against an injected RPC
// response: it spawns loadSession, waits for the pending request to
// register, sends resp on the matching pending channel, and returns
// loadSession's error.
func gk_vibekit_u9_runLoadSession(t *testing.T, b *Bridge, fallback string, resp *api.RPCResponse) error {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pr.Close() })
	b.stdin = pw

	done := make(chan error, 1)
	go func() {
		done <- b.loadSession(context.Background(), "acp-session-xyz", fallback, nil)
	}()

	var ch chan *api.RPCResponse
	deadline := time.Now().Add(time.Second)
	for ch == nil {
		b.pendingMu.Lock()
		for id, v := range b.pending {
			ch = v
			delete(b.pending, id)
		}
		b.pendingMu.Unlock()
		if ch == nil {
			if time.Now().After(deadline) {
				t.Fatal("loadSession never registered a pending request")
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	ch <- resp

	select {
	case e := <-done:
		return e
	case <-time.After(time.Second):
		t.Fatal("loadSession did not return")
		return nil
	}
}

// --- bridge_session.go:92:17 + 95:15 — loadSession applies a parsed result ---
// Line 92: `if resp.Result != nil`; line 95: `if parseErr == nil`.
// A non-nil, well-formed result carrying currentModelId="parsed-model" must
// be applied. Negating 92 (`== nil`) skips the whole block; negating 95
// (`!= nil`) skips the apply+return — both fall through to the fallback
// model. Asserting the parsed model (not the fallback) kills both.
func Test_gk_vibekit_u9_loadSession_appliesParsedResult(t *testing.T) {
	b := New("/nonexistent", "/work")
	resp := &api.RPCResponse{
		Result: json.RawMessage(`{"sessionId":"acp-session-xyz","models":{"currentModelId":"parsed-model","availableModels":[{"modelId":"parsed-model","name":"Parsed","description":"ok","rateMultiplier":1}]}}`),
	}
	if err := gk_vibekit_u9_runLoadSession(t, b, "fb-model", resp); err != nil {
		t.Fatalf("loadSession returned error: %v", err)
	}
	if got := b.ModelID(); got != "parsed-model" {
		t.Errorf("loadSession ModelID() = %q, want %q (parsed result must be applied, not fallback)", got, "parsed-model")
	}
}

// --- bridge_session.go:92:17 + 95:15 — loadSession warns on unparseable result ---
// A non-nil but malformed result makes parseErr non-nil: the original
// enters the block (92 true), fails the parse, and logs the "unparseable"
// warn (95 false -> skip apply -> warn). Negating 92 skips the block (no
// warn); negating 95 applies the zero result and returns before the warn
// (no warn). Asserting the warn is logged kills both from the other side.
func Test_gk_vibekit_u9_loadSession_warnsOnUnparseableResult(t *testing.T) {
	c := gk_vibekit_u9_installCapture(t)
	b := New("/nonexistent", "/work")
	resp := &api.RPCResponse{Result: json.RawMessage(`{"sessionId":"x"`)} // truncated -> parse error
	if err := gk_vibekit_u9_runLoadSession(t, b, "fb-model", resp); err != nil {
		t.Fatalf("loadSession returned error: %v", err)
	}
	if !c.gk_vibekit_u9_has("session/load: unparseable result, using fallback") {
		t.Errorf("loadSession on an unparseable result did not log the fallback warn; want it present")
	}
}
