package bridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/slogx/capture"
	"github.com/cplieger/vibekit/internal/api"
)

func TestNew_FieldsAndAccessors(t *testing.T) {
	b := New("/opt/kiro", "/work")
	// cliPath and workDir are copy-ins from New; no accessor exists,
	// so pin them directly. Channel/map init is implicitly verified
	// by TestStop_Idempotent (Stop closes notifCh; closing a nil
	// channel panics) and TestNew_AccessorsReturnZeroValues.
	if b.cliPath != "/opt/kiro" {
		t.Errorf("cliPath = %q", b.cliPath)
	}
	if b.workDir != "/work" {
		t.Errorf("workDir = %q", b.workDir)
	}
}

// TestNew_AccessorsReturnZeroValues pins the zero-value contract for a
// freshly-constructed bridge that has never run through session/new or
// session/load: accessor strings are empty, slice accessors return
// non-nil zero-length slices safe to range over.
func TestNew_AccessorsReturnZeroValues(t *testing.T) {
	b := New("/opt/kiro", "/work")
	if got := b.SessionID(); got != "" {
		t.Errorf("SessionID() = %q, want empty", got)
	}
	if got := b.ModelID(); got != "" {
		t.Errorf("ModelID() = %q, want empty", got)
	}
	if got := b.CurrentMode(); got != "" {
		t.Errorf("CurrentMode() = %q, want empty", got)
	}
	if got := b.Modes(); len(got) != 0 {
		t.Errorf("Modes() = %v, want empty slice", got)
	}
	if got := b.Models(); len(got) != 0 {
		t.Errorf("Models() = %v, want empty slice", got)
	}
}

// --- Pure helpers ---

func TestIsDeprecatedOrLegacy(t *testing.T) {
	cases := []struct {
		desc string
		want bool
	}{
		{desc: "", want: false},
		{desc: "Claude 3.5 Sonnet", want: false},
		{desc: "Claude 3.5 Sonnet [Internal]", want: false},
		{desc: "Claude 3.5 Sonnet [Preview]", want: false},
		{desc: "Claude 3.5 Sonnet [Deprecated]", want: true},
		{desc: "Claude 3.5 Sonnet [DEPRECATED]", want: true},
		{desc: "Claude 3.5 Sonnet [Legacy]", want: true},
		{desc: "Claude 3.5 Sonnet [LEGACY]", want: true},
		// Bare "deprecated" in prose must NOT trigger the filter —
		// kiro-cli descriptions are free to reference older models
		// by name ("Successor to the deprecated Claude 2 family").
		{desc: "Successor to the deprecated Claude 2 family", want: false},
		{desc: "Old model, deprecated 2024-10-01", want: false},
		// Same for "legacy".
		{desc: "Model for legacyapi users", want: false},
	}
	for _, tc := range cases {
		if got := api.TagExcluded(tc.desc, api.HiddenTags); got != tc.want {
			t.Errorf("TagExcluded(%q, HiddenTags) = %v, want %v", tc.desc, got, tc.want)
		}
	}
}

func TestValidIdent(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{in: "", want: true}, // empty = caller decides whether to emit flag
		{in: "kiro_default", want: true},
		{in: "kiro-planner", want: true},
		{in: "my.custom.agent", want: true},
		{in: "Claude3_5", want: true},
		{in: "a", want: true},
		{in: "kiro..planner", want: true}, // interior dots are still fine
		{in: strings.Repeat("a", 128), want: true},
		{in: strings.Repeat("a", 129), want: false},
		{in: "../../../etc/passwd", want: false},
		{in: "agent/with/slashes", want: false},
		{in: "agent with spaces", want: false},
		{in: "agent;rm -rf /", want: false},
		{in: "agent\nname", want: false},
		{in: "agent\x00name", want: false},
		{in: "agent$(whoami)", want: false},
		// Defense-in-depth: path-adjacent values that match identRe
		// but would read as "current/parent dir" or "hidden entry"
		// to any downstream consumer must be rejected.
		{in: ".", want: false},
		{in: "..", want: false},
		{in: "...", want: false},
		{in: ".hidden", want: false},
		{in: "-flag", want: false},
	}
	for _, tc := range cases {
		if got := validIdent(tc.in); got != tc.want {
			t.Errorf("validIdent(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// --- applySessionResultLocked: modes/models translation + deprecation filter ---

func TestApplySessionResult_CopiesModesAndModels(t *testing.T) {
	b := &Bridge{}
	// v3 shape: modes block + the model catalog carried inside the
	// configOptions "model" select (currentValue + options[] with each
	// choice's rate multiplier under _meta.kiro).
	var r sessionCreated
	if err := json.Unmarshal([]byte(`{
		"sessionId": "sess-1",
		"modes": {"currentModeId": "mode-b", "availableModes": [
			{"id": "mode-a", "name": "Alpha", "description": "first"},
			{"id": "mode-b", "name": "Beta", "description": "second"}
		]},
		"configOptions": [{"id": "model", "currentValue": "claude-sonnet", "options": [
			{"value": "claude-sonnet", "name": "Sonnet", "description": "general", "_meta": {"kiro": {"rateMultiplier": 1.0}}},
			{"value": "old-opus", "name": "Opus", "description": "[Deprecated] old", "_meta": {"kiro": {"rateMultiplier": 3.0}}},
			{"value": "preview", "name": "Preview", "description": "[Internal] experimental", "_meta": {"kiro": {"rateMultiplier": 1.5}}},
			{"value": "legacy", "name": "Legacy", "description": "[Legacy] v1", "_meta": {"kiro": {"rateMultiplier": 1.0}}}
		]}]
	}`), &r); err != nil {
		t.Fatalf("unmarshal session result: %v", err)
	}

	b.mu.Lock()
	b.applySessionResultLocked(r, "fallback-model")
	b.mu.Unlock()

	if got := b.CurrentMode(); got != "mode-b" {
		t.Errorf("currentMode = %q, want mode-b", got)
	}
	if got := b.Modes(); len(got) != 2 {
		t.Errorf("modes len = %d, want 2", len(got))
	}
	if got := b.ModelID(); got != "claude-sonnet" {
		t.Errorf("modelID = %q, want claude-sonnet", got)
	}
	// Deprecated and legacy dropped; internal kept.
	models := b.Models()
	if len(models) != 2 {
		t.Errorf("models len = %d, want 2 (1 kept + 1 internal), got %v", len(models), models)
	}
	seen := map[string]bool{}
	for _, m := range models {
		seen[m.ID] = true
	}
	if !seen["claude-sonnet"] || !seen["preview"] {
		t.Errorf("models = %v, want claude-sonnet + preview", models)
	}
	if seen["old-opus"] || seen["legacy"] {
		t.Errorf("filtered-out models leaked through: %v", models)
	}
}

// When the response has no modes or models blocks and the bridge has
// no model id yet, the fallback model wins.
func TestApplySessionResult_FallbackModelWhenMissing(t *testing.T) {
	b := &Bridge{}
	b.mu.Lock()
	b.applySessionResultLocked(sessionCreated{}, "fallback")
	b.mu.Unlock()
	if got := b.ModelID(); got != "fallback" {
		t.Errorf("modelID = %q, want fallback", got)
	}
	if got := b.Modes(); len(got) != 0 {
		t.Errorf("modes = %v, want empty", got)
	}
	if got := b.Models(); len(got) != 0 {
		t.Errorf("models = %v, want empty", got)
	}
}

// Fallback does NOT overwrite a current model set by the response.
func TestApplySessionResult_FallbackIgnoredWhenCurrentPresent(t *testing.T) {
	b := &Bridge{}
	r := sessionCreated{
		ConfigOptions: []sessionConfigOption{
			{ID: "model", CurrentValue: json.RawMessage(`"real-model"`)},
		},
	}
	b.mu.Lock()
	b.applySessionResultLocked(r, "fallback")
	b.mu.Unlock()
	if got := b.ModelID(); got != "real-model" {
		t.Errorf("modelID = %q, want real-model (fallback must not override)", got)
	}
}

// TestModes_ReturnedSliceIsDefensiveCopy asserts the Modes accessor
// returns a copy. A future refactor that drops the copy (e.g. returns
// b.modes directly) would pass every other test in this file; this
// one pins the behaviour-contract.
func TestModes_ReturnedSliceIsDefensiveCopy(t *testing.T) {
	b := &Bridge{}
	r := sessionCreated{
		Modes: &sessionModes{
			CurrentModeID: "m1",
			AvailableModes: []sessionMode{
				{ID: "m1", Name: "One", Description: "first"},
				{ID: "m2", Name: "Two", Description: "second"},
			},
		},
	}
	b.mu.Lock()
	b.applySessionResultLocked(r, "")
	b.mu.Unlock()

	first := b.Modes()
	if len(first) != 2 {
		t.Fatalf("first Modes() len = %d, want 2", len(first))
	}
	// Verify the slice is consistent across calls (same pointer —
	// no allocation on the read path).
	second := b.Modes()
	if &first[0] != &second[0] {
		t.Errorf("Modes() returned different backing arrays; expected same frozen slice")
	}
}

// TestModels_ReturnedSliceIsDefensiveCopy mirrors the Modes test for
// the Models accessor.
func TestModels_ReturnedSliceIsDefensiveCopy(t *testing.T) {
	b := &Bridge{}
	var r sessionCreated
	if err := json.Unmarshal([]byte(`{
		"sessionId": "sess-1",
		"configOptions": [{"id": "model", "currentValue": "a", "options": [
			{"value": "a", "name": "Alpha", "description": "x", "_meta": {"kiro": {"rateMultiplier": 1}}},
			{"value": "b", "name": "Beta", "description": "y", "_meta": {"kiro": {"rateMultiplier": 2}}}
		]}]
	}`), &r); err != nil {
		t.Fatalf("unmarshal session result: %v", err)
	}
	b.mu.Lock()
	b.applySessionResultLocked(r, "")
	b.mu.Unlock()

	first := b.Models()
	if len(first) != 2 {
		t.Fatalf("first Models() len = %d, want 2", len(first))
	}
	// Verify the slice is consistent across calls (same pointer —
	// no allocation on the read path).
	second := b.Models()
	if &first[0] != &second[0] {
		t.Errorf("Models() returned different backing arrays; expected same frozen slice")
	}
}

// --- Stop idempotency ---

func TestStop_Idempotent(t *testing.T) {
	b := New("/nonexistent/cli", "/work")
	// Don't Start (no subprocess); Stop should be safe on an
	// unstarted bridge and safe to call multiple times.
	b.Stop()
	b.Stop() // would panic on close of closed channel without sync.Once
	b.Stop()
}

// TestCall_ReturnsBridgeExitedAfterStop pins Call's post-Stop contract:
// a Call parked on the select returns errBridgeExited when Stop closes
// b.done. Without this test a future refactor that drops the done
// branch could silently regress "Stop races a fresh Call" into a
// permanent hang.
func TestCall_ReturnsBridgeExitedAfterStop(t *testing.T) {
	b := New("/nonexistent", "/work")
	// Wire a blocking pipe so writeFrame returns without error (the
	// write is immediate) but no readLoop reads the framed bytes.
	// Call parks on select{ch, b.done}.
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = pr.Close()
	})
	b.stdin = pw

	type result struct {
		resp *api.RPCResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		r, e := b.Call(context.Background(), "x", nil)
		done <- result{r, e}
	}()
	// Give Call time to register in b.pending and park on
	// select{ch, b.done}.
	time.Sleep(10 * time.Millisecond)
	b.Stop()
	select {
	case r := <-done:
		if !errors.Is(r.err, errBridgeExited) {
			t.Errorf("err = %v, want errBridgeExited", r.err)
		}
		if r.resp != nil {
			t.Errorf("resp = %+v, want nil", r.resp)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Call did not unblock after Stop")
	}
}

// --- Respond tests (g59) ---

// respondBridge returns a Bridge wired to a pipe so Respond output can
// be read back. The caller must close pr when done.
func respondBridge(t *testing.T) (*Bridge, *os.File) {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pr.Close() })
	b := New("/nonexistent", "/work")
	b.stdin = pw
	return b, pr
}

func TestRespond_SuccessResult(t *testing.T) {
	b, pr := respondBridge(t)
	result := map[string]string{"content": "hello"}
	if err := b.Respond(context.Background(), 42, result, nil); err != nil {
		t.Fatal(err)
	}
	_ = b.stdin.Close() // signal EOF so the read below terminates

	buf := make([]byte, 4096)
	n, _ := pr.Read(buf)
	var got map[string]json.RawMessage
	if err := json.Unmarshal(buf[:n], &got); err != nil {
		t.Fatalf("unmarshal: %v (raw: %s)", err, buf[:n])
	}
	if string(got["jsonrpc"]) != `"2.0"` {
		t.Errorf("jsonrpc = %s, want \"2.0\"", got["jsonrpc"])
	}
	if string(got["id"]) != "42" {
		t.Errorf("id = %s, want 42", got["id"])
	}
	if _, hasErr := got["error"]; hasErr {
		t.Errorf("unexpected error field: %s", got["error"])
	}
	var res map[string]string
	if err := json.Unmarshal(got["result"], &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if res["content"] != "hello" {
		t.Errorf("result.content = %q, want %q", res["content"], "hello")
	}
}

func TestRespond_GenericError(t *testing.T) {
	b, pr := respondBridge(t)
	if err := b.Respond(context.Background(), 7, nil, errors.New("something broke")); err != nil {
		t.Fatal(err)
	}
	_ = b.stdin.Close()

	buf := make([]byte, 4096)
	n, _ := pr.Read(buf)
	var got struct {
		Error struct {
			Message string `json:"message"`
			Code    int    `json:"code"`
		} `json:"error"`
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(buf[:n], &got); err != nil {
		t.Fatalf("unmarshal: %v (raw: %s)", err, buf[:n])
	}
	if got.ID != 7 {
		t.Errorf("id = %d, want 7", got.ID)
	}
	if got.Error.Code != -32603 {
		t.Errorf("error.code = %d, want -32603", got.Error.Code)
	}
	if got.Error.Message != "something broke" {
		t.Errorf("error.message = %q, want %q", got.Error.Message, "something broke")
	}
}

func TestRespond_TypedRPCError(t *testing.T) {
	b, pr := respondBridge(t)
	rpcErr := &api.RPCError{Code: -32001, Message: "custom error"}
	if err := b.Respond(context.Background(), 99, nil, rpcErr); err != nil {
		t.Fatal(err)
	}
	_ = b.stdin.Close()

	buf := make([]byte, 4096)
	n, _ := pr.Read(buf)
	var got struct {
		Error struct {
			Message string `json:"message"`
			Code    int    `json:"code"`
		} `json:"error"`
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(buf[:n], &got); err != nil {
		t.Fatalf("unmarshal: %v (raw: %s)", err, buf[:n])
	}
	if got.ID != 99 {
		t.Errorf("id = %d, want 99", got.ID)
	}
	if got.Error.Code != -32001 {
		t.Errorf("error.code = %d, want -32001", got.Error.Code)
	}
	if got.Error.Message != "custom error" {
		t.Errorf("error.message = %q, want %q", got.Error.Message, "custom error")
	}
}

// --- Call tests (g60) ---

func TestCall_HappyPath(t *testing.T) {
	b := New("/nonexistent", "/work")
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pr.Close() })
	b.stdin = pw

	type result struct {
		resp *api.RPCResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		r, e := b.Call(context.Background(), "test/method", map[string]string{"key": "val"})
		done <- result{r, e}
	}()

	// Give Call time to register in b.pending.
	time.Sleep(10 * time.Millisecond)

	// Simulate readLoop dispatching a successful response.
	b.pendingMu.Lock()
	var id int64
	var ch chan *api.RPCResponse
	for k, v := range b.pending {
		id = k
		ch = v
		break
	}
	delete(b.pending, id)
	b.pendingMu.Unlock()

	successResp := &api.RPCResponse{}
	raw := json.RawMessage(`{"ok":true}`)
	successResp.Result = raw
	ch <- successResp

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("unexpected error: %v", r.err)
		}
		if r.resp != successResp {
			t.Errorf("resp = %+v, want the injected response", r.resp)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Call did not return")
	}
}

func TestCall_ErrorResponse(t *testing.T) {
	b := New("/nonexistent", "/work")
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pr.Close() })
	b.stdin = pw

	type result struct {
		resp *api.RPCResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		r, e := b.Call(context.Background(), "test/method", nil)
		done <- result{r, e}
	}()

	time.Sleep(10 * time.Millisecond)

	b.pendingMu.Lock()
	var id int64
	var ch chan *api.RPCResponse
	for k, v := range b.pending {
		id = k
		ch = v
		break
	}
	delete(b.pending, id)
	b.pendingMu.Unlock()

	errResp := &api.RPCResponse{
		Error: &api.RPCError{Code: -32600, Message: "invalid request"},
	}
	ch <- errResp

	select {
	case r := <-done:
		if r.err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(r.err.Error(), "invalid request") {
			t.Errorf("err = %v, want to contain 'invalid request'", r.err)
		}
		if r.resp != errResp {
			t.Errorf("resp should be returned even on error")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Call did not return")
	}
}

func TestCall_BridgeExitedSentinel(t *testing.T) {
	b := New("/nonexistent", "/work")
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pr.Close() })
	b.stdin = pw

	type result struct {
		resp *api.RPCResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		r, e := b.Call(context.Background(), "test/method", nil)
		done <- result{r, e}
	}()

	time.Sleep(10 * time.Millisecond)

	// Simulate readLoop drain: send bridgeExitedResp to the pending channel.
	b.pendingMu.Lock()
	var ch chan *api.RPCResponse
	for _, v := range b.pending {
		ch = v
		break
	}
	b.pendingMu.Unlock()

	ch <- bridgeExitedResp

	select {
	case r := <-done:
		if !errors.Is(r.err, errBridgeExited) {
			t.Errorf("err = %v, want errBridgeExited", r.err)
		}
		if r.resp != nil {
			t.Errorf("resp = %+v, want nil on bridge-exited", r.resp)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Call did not return")
	}
}

// BenchmarkBridgeReadLoop measures throughput of the JSON-RPC message
// parsing hot path in readLoop. It feeds pre-serialized JSON-RPC
// messages (a mix of responses and notifications) through a pipe into
// a Bridge's readLoop and measures dispatch throughput, catching
// allocation regressions from json.Unmarshal or map operations.
func BenchmarkBridgeReadLoop(b *testing.B) {
	// Pre-build message payloads: half responses, half notifications.
	respID := int64(1)
	respMsg, _ := json.Marshal(api.RPCResponse{
		JSONRPC: "2.0",
		ID:      &respID,
		Result:  json.RawMessage(`{"status":"ok"}`),
	})
	notifMsg, _ := json.Marshal(api.RPCResponse{
		JSONRPC: "2.0",
		Method:  "session/update",
		Params:  json.RawMessage(`{"sessionId":"s1","delta":{"type":"text","text":"hi"}}`),
	})
	respLine := append(respMsg, '\n')
	notifLine := append(notifMsg, '\n')

	b.ResetTimer()
	for range b.N {
		b.StopTimer()

		pr, pw, _ := os.Pipe()
		br := &Bridge{
			stdout:  bufio.NewScanner(pr),
			pending: make(map[int64]chan *api.RPCResponse),
			notifCh: make(chan *api.RPCResponse, 1024),
			done:    make(chan struct{}),
		}

		// Pre-register pending entries for response messages.
		// We'll write msgCount messages total: alternate resp/notif.
		const msgCount = 500
		for id := int64(1); id <= msgCount/2; id++ {
			br.pending[id] = make(chan *api.RPCResponse, 1)
		}

		// Write all messages into the pipe, then close to signal EOF.
		go func() {
			for j := range msgCount {
				if j%2 == 0 {
					// Response with incrementing ID.
					id := int64(j/2 + 1)
					msg, _ := json.Marshal(api.RPCResponse{
						JSONRPC: "2.0",
						ID:      &id,
						Result:  json.RawMessage(`{"status":"ok"}`),
					})
					pw.Write(append(msg, '\n'))
				} else {
					pw.Write(notifLine)
				}
			}
			pw.Close()
		}()

		// Drain notifCh so readLoop doesn't block.
		drainDone := make(chan struct{})
		go func() {
			for range br.notifCh {
			}
			close(drainDone)
		}()

		b.StartTimer()
		br.readLoop()
		<-drainDone
	}
	// Prevent compiler from optimizing away.
	_ = respLine
	_ = notifLine
}

// TestRealBridge_Contract runs BridgeContractTest (from hub/shared_test.go)
// against the real bridge.Bridge using a pipe-based fake kiro-cli script.
// This catches interface drift between the fake and real implementations
// at the Start/Stop/NotifCh lifecycle level without requiring a real
// kiro-cli binary.
//
// The fake script reads JSON-RPC requests from stdin and responds with
// minimal valid responses for initialize, session/new, session/load, and
// session/prompt. Any other id'd request (e.g. v3's
// session/set_config_option) gets a generic empty result via the default
// case.
func TestRealBridge_Contract(t *testing.T) {
	// Write a fake kiro-cli script that speaks minimal JSON-RPC.
	script := `#!/bin/sh
# Fake kiro-cli ACP subprocess for contract testing.
# Reads JSON-RPC requests from stdin, responds with minimal valid results.
while IFS= read -r line; do
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  method=$(echo "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"serverInfo":{"name":"fake-kiro"}}}\n' "$id"
      ;;
    session/new)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"test-sess-001","modes":{"currentModeId":"default","availableModes":[{"id":"default","name":"Default","description":"default mode"}]},"configOptions":[{"id":"model","currentValue":"model-1","options":[{"value":"model-1","name":"Test Model","description":"A test model","_meta":{"kiro":{"rateMultiplier":1.0}}}]}]}}\n' "$id"
      ;;
    session/load)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"existing-sess","modes":{"currentModeId":"default","availableModes":[{"id":"default","name":"Default","description":"default mode"}]},"configOptions":[{"id":"model","currentValue":"model-1","options":[{"value":"model-1","name":"Test Model","description":"A test model","_meta":{"kiro":{"rateMultiplier":1.0}}}]}]}}\n' "$id"
      ;;
    session/prompt)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"status":"ok"}}\n' "$id"
      ;;
    *)
      # Notifications (no id) or unknown methods — ignore.
      if [ -n "$id" ]; then
        printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id"
      fi
      ;;
  esac
done
`
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "fake-kiro-cli")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake script: %v", err)
	}

	newBridge := func() api.ACPBridge {
		return New(scriptPath, dir)
	}

	t.Run("Start_sets_session_id", func(t *testing.T) {
		b := newBridge()
		if err := b.Start(context.Background(), &api.StartOpts{Model: "model"}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer b.Stop()
		if id := b.SessionID(); id == "" {
			t.Error("SessionID empty after Start")
		}
	})

	t.Run("Start_with_existing_session", func(t *testing.T) {
		b := newBridge()
		if err := b.Start(context.Background(), &api.StartOpts{SessionID: "existing-sess", Model: "model"}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer b.Stop()
		if id := b.SessionID(); id != "existing-sess" {
			t.Errorf("SessionID = %q, want existing-sess", id)
		}
	})

	t.Run("Call_returns_response", func(t *testing.T) {
		b := newBridge()
		if err := b.Start(context.Background(), &api.StartOpts{Model: "model"}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer b.Stop()
		resp, err := b.Call(context.Background(), "session/prompt", nil)
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
		if resp == nil {
			t.Fatal("Call returned nil response")
		}
	})

	t.Run("Notify_does_not_error", func(t *testing.T) {
		b := newBridge()
		if err := b.Start(context.Background(), &api.StartOpts{Model: "model"}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer b.Stop()
		if err := b.Notify(context.Background(), "session/update", nil); err != nil {
			t.Errorf("Notify: %v", err)
		}
	})

	t.Run("Stop_closes_NotifCh", func(t *testing.T) {
		b := newBridge()
		if err := b.Start(context.Background(), &api.StartOpts{Model: "model"}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		ch := b.NotifCh()
		b.Stop()
		select {
		case _, ok := <-ch:
			if ok {
				for range ch {
				}
			}
		case <-time.After(2 * time.Second):
			t.Error("NotifCh not closed after Stop")
		}
	})

	t.Run("ModelID_returns_value", func(t *testing.T) {
		b := newBridge()
		if err := b.Start(context.Background(), &api.StartOpts{Model: "model"}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer b.Stop()
		if id := b.ModelID(); id == "" {
			t.Error("ModelID empty after Start")
		}
	})
}

// --- parseErrTracker state machine tests (tarch-b7-c3-p4, consolidated tarch-b4-c4-p4) ---

func TestParseErrTracker(t *testing.T) {
	cases := []struct {
		setup      func(*parseErrTracker)
		name       string
		calls      int
		wantAction parseErrAction
	}{
		{
			name:       "burst phase returns parseErrLog",
			setup:      func(_ *parseErrTracker) {},
			calls:      parseErrBurst,
			wantAction: parseErrLog,
		},
		{
			name: "suppress after burst within window",
			setup: func(tr *parseErrTracker) {
				for range parseErrBurst {
					tr.Record()
				}
			},
			calls:      1,
			wantAction: parseErrSuppress,
		},
		{
			name: "summarize after window expires",
			setup: func(tr *parseErrTracker) {
				for range parseErrBurst {
					tr.Record()
				}
				tr.windowStart = time.Now().Add(-parseErrWindow - time.Second)
			},
			calls:      1,
			wantAction: parseErrSummarize,
		},
		{
			name:       "circuit break at consecutive threshold",
			setup:      func(_ *parseErrTracker) {},
			calls:      parseErrMaxConsecutive,
			wantAction: parseErrCircuitBreak,
		},
		{
			name: "reset clears consecutive counter",
			setup: func(tr *parseErrTracker) {
				for range 5 {
					tr.Record()
				}
				tr.Reset()
			},
			calls:      parseErrMaxConsecutive,
			wantAction: parseErrCircuitBreak,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var tr parseErrTracker
			tc.setup(&tr)
			var action parseErrAction
			for range tc.calls {
				action = tr.Record()
			}
			if action != tc.wantAction {
				t.Errorf("after %d Record() calls: got %d, want %d", tc.calls, action, tc.wantAction)
			}
		})
	}

	// SummaryCount sub-test: after burst + N more, count should be N.
	t.Run("summary count tracks suppressed errors", func(t *testing.T) {
		var tr parseErrTracker
		for range parseErrBurst {
			tr.Record()
		}
		extra := 7
		for range extra {
			tr.Record()
		}
		if got := tr.SummaryCount(); got != extra {
			t.Errorf("SummaryCount() = %d, want %d", got, extra)
		}
	})
}

// --- tarch-b11-c7-p7: Bridge lifecycle contract test ---

// TestBridge_LifecycleContract verifies the accessor stability guarantee
// across the full state machine: New → Start → Running → Stop.
// Accessors must retain last-known values after Stop (no reset to zero).
func TestBridge_LifecycleContract(t *testing.T) {
	// Write a fake kiro-cli script that speaks minimal JSON-RPC.
	script := `#!/bin/sh
while IFS= read -r line; do
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  method=$(echo "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"serverInfo":{"name":"fake"}}}\n' "$id"
      ;;
    session/new)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"lifecycle-001","modes":{"currentModeId":"agent","availableModes":[{"id":"agent","name":"Agent","description":"agent mode"},{"id":"code","name":"Code","description":"code mode"}]},"configOptions":[{"id":"model","currentValue":"sonnet","options":[{"value":"sonnet","name":"Sonnet","description":"fast","_meta":{"kiro":{"rateMultiplier":1.0}}}]}]}}\n' "$id"
      ;;
    *)
      if [ -n "$id" ]; then
        printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id"
      fi
      ;;
  esac
done
`
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "fake-kiro")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	b := New(scriptPath, dir)

	// Pre-start: all accessors return zero values.
	if id := b.SessionID(); id != "" {
		t.Errorf("pre-start SessionID = %q, want empty", id)
	}
	if id := b.ModelID(); id != "" {
		t.Errorf("pre-start ModelID = %q, want empty", id)
	}
	if m := b.CurrentMode(); m != "" {
		t.Errorf("pre-start CurrentMode = %q, want empty", m)
	}

	// Start: accessors populated.
	if err := b.Start(context.Background(), &api.StartOpts{Model: "m"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if id := b.SessionID(); id != "lifecycle-001" {
		t.Errorf("post-start SessionID = %q, want lifecycle-001", id)
	}
	if id := b.ModelID(); id != "sonnet" {
		t.Errorf("post-start ModelID = %q, want sonnet", id)
	}
	if m := b.CurrentMode(); m != "agent" {
		t.Errorf("post-start CurrentMode = %q, want agent", m)
	}
	if modes := b.Modes(); len(modes) != 2 {
		t.Errorf("post-start Modes len = %d, want 2", len(modes))
	}
	if models := b.Models(); len(models) != 1 {
		t.Errorf("post-start Models len = %d, want 1", len(models))
	}

	// Stop: accessors retain last-known values.
	b.Stop()
	if id := b.SessionID(); id != "lifecycle-001" {
		t.Errorf("post-stop SessionID = %q, want lifecycle-001 (must not reset)", id)
	}
	if id := b.ModelID(); id != "sonnet" {
		t.Errorf("post-stop ModelID = %q, want sonnet (must not reset)", id)
	}
	if m := b.CurrentMode(); m != "agent" {
		t.Errorf("post-stop CurrentMode = %q, want agent (must not reset)", m)
	}

	// Double-Stop: no panic.
	b.Stop()
}

// --- tarch-b7-c7-p5: RPC error classification table test ---

// TestBridgeRPC_ErrorClassification verifies that Call classifies
// JSON-RPC error codes into the correct sentinel/category.
func TestBridgeRPC_ErrorClassification(t *testing.T) {
	cases := []struct {
		name        string
		message     string
		wantMsg     string
		code        int
		wantNotIdle bool
	}{
		{name: "not-idle by code", code: -32001, message: "session busy", wantNotIdle: true},
		{name: "not-idle by message only", code: -32099, message: "not idle", wantNotIdle: false, wantMsg: "not idle"},
		{name: "parse error", code: -32700, message: "parse error", wantNotIdle: false, wantMsg: "parse error"},
		{name: "invalid request", code: -32600, message: "invalid request", wantNotIdle: false, wantMsg: "invalid request"},
		{name: "method not found", code: -32601, message: "method not found", wantNotIdle: false, wantMsg: "method not found"},
		{name: "internal error", code: -32603, message: "internal error", wantNotIdle: false, wantMsg: "internal error"},
		{name: "server-defined error", code: -32050, message: "custom server err", wantNotIdle: false, wantMsg: "custom server err"},
		{name: "positive code", code: 1, message: "unknown positive", wantNotIdle: false, wantMsg: "unknown positive"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := New("/nonexistent", "/work")
			pr, pw, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = pr.Close() })
			b.stdin = pw

			type result struct {
				resp *api.RPCResponse
				err  error
			}
			done := make(chan result, 1)
			go func() {
				r, e := b.Call(context.Background(), "test/method", nil)
				done <- result{r, e}
			}()

			time.Sleep(10 * time.Millisecond)

			// Inject error response.
			b.pendingMu.Lock()
			var ch chan *api.RPCResponse
			for _, v := range b.pending {
				ch = v
				break
			}
			b.pendingMu.Unlock()

			errResp := &api.RPCResponse{
				Error: &api.RPCError{Code: tc.code, Message: tc.message},
			}
			ch <- errResp

			select {
			case r := <-done:
				if r.err == nil {
					t.Fatal("expected error, got nil")
				}
				if tc.wantNotIdle {
					if !errors.Is(r.err, api.ErrNotIdle) {
						t.Errorf("err = %v, want api.ErrNotIdle", r.err)
					}
				} else {
					if errors.Is(r.err, api.ErrNotIdle) {
						t.Errorf("err = %v, should NOT be api.ErrNotIdle", r.err)
					}
					if !strings.Contains(r.err.Error(), tc.wantMsg) {
						t.Errorf("err = %v, want to contain %q", r.err, tc.wantMsg)
					}
				}
				if r.resp != errResp {
					t.Errorf("resp should be returned even on error")
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatal("Call did not return")
			}
		})
	}
}

func BenchmarkBridgeRespond(b *testing.B) {
	// Create a Bridge with a pipe-based writer (no real process).
	_, pw, err := os.Pipe()
	if err != nil {
		b.Fatalf("os.Pipe: %v", err)
	}
	defer pw.Close()

	br := &Bridge{
		stdin:   pw,
		done:    make(chan struct{}),
		pending: make(map[int64]chan *api.RPCResponse),
		notifCh: make(chan *api.RPCResponse, 16),
	}

	ctx := context.Background()
	// Typical tool result payload (~500 bytes).
	result := map[string]any{
		"content": strings.Repeat("x", 400),
		"status":  "success",
		"meta":    map[string]string{"tool": "file_write", "path": "/workspace/main.go"},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := br.Respond(ctx, 42, result, nil); err != nil {
			b.Fatalf("Respond: %v", err)
		}
	}
}

// --- shared mutation-guard test doubles ---

// logCapture is a slog.Handler that records emitted record messages so a
// test can assert whether a particular log line was (or was not) produced
// by the code under test.
// captureWriter is an io.WriteCloser standing in for the bridge's stdin.
// It records whether (and what) was written, and can be configured to
// fail every Write with a sentinel error.
type captureWriter struct {
	failErr error
	buf     bytes.Buffer
	mu      sync.Mutex
	writes  int
}

func (w *captureWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failErr != nil {
		return 0, w.failErr
	}
	w.writes++
	return w.buf.Write(p)
}

func (w *captureWriter) Close() error { return nil }

func (w *captureWriter) wrote() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writes > 0
}

// errReader yields the configured error on the first Read. A non-EOF
// error surfaces via bufio.Scanner.Err(); io.EOF terminates the scan
// cleanly with a nil Err().
type errReader struct{ failErr error }

func (r errReader) Read([]byte) (int, error) { return 0, r.failErr }

// readLoopBridge builds the minimal Bridge that readLoop needs.
func readLoopBridge(r io.Reader) *Bridge {
	return &Bridge{
		stdout:  bufio.NewScanner(r),
		pending: make(map[int64]chan *api.RPCResponse),
		notifCh: make(chan *api.RPCResponse, 1),
		done:    make(chan struct{}),
	}
}

// runLoadSession drives loadSession against an injected RPC response: it
// spawns loadSession, waits for the pending request to register, sends
// resp on the matching pending channel, and returns loadSession's error.
func runLoadSession(t *testing.T, b *Bridge, fallback string, resp *api.RPCResponse) error {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pr.Close() })
	b.stdin = pw

	done := make(chan error, 1)
	go func() {
		done <- b.loadSession(context.Background(), "acp-session-xyz", fallback)
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

// --- bridge_parse_err.go: parseErrTracker.Record ---

// The burst-th Record() sets the window start; the next call falls inside
// the window and is suppressed.
func TestParseErrTracker_WindowStartSetAtBurst(t *testing.T) {
	var tr parseErrTracker
	var got parseErrAction
	for range parseErrBurst + 1 {
		got = tr.Record()
	}
	if got != parseErrSuppress {
		t.Errorf("Record() call #%d = %v, want parseErrSuppress (%v)", parseErrBurst+1, got, parseErrSuppress)
	}
}

// --- bridge_process.go: Stop ---

// Stop must not dereference a nil Process (an unstarted command).
func TestStop_SkipsKillWhenProcessNil(t *testing.T) {
	b := New("cli", "work")
	b.cmd = exec.Command("sleep", "30") // never Start()ed -> Process is nil
	b.Stop()                            // must return without panicking
	if b.cmd == nil {
		t.Fatal("b.cmd unexpectedly nil after Stop")
	}
}

// Stop emits no "kill kiro-cli" error log when killing a live process
// succeeds.
func TestStop_NoKillErrorLogOnLiveProcess(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep binary not available")
	}
	c := capture.Default(t)

	b := New("cli", "work")
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	b.cmd = cmd
	t.Cleanup(func() {
		if b.cmd != nil && b.cmd.Process != nil {
			_ = b.cmd.Process.Kill()
		}
	})

	b.Stop()

	if c.CountExact("kill kiro-cli") > 0 {
		t.Errorf(`Stop emitted "kill kiro-cli" error log on a successful Kill, want none`)
	}
}

// --- bridge_process.go: startProcess ---

// startProcess substitutes context.Background() for a nil lifecycleCtx
// (the state right after New), so it must not panic on a nil ctx.
func TestStartProcess_NilLifecycleCtxFallsBackToBackground(t *testing.T) {
	bogus := filepath.Join(t.TempDir(), "no-such-kiro-cli")
	b := New(bogus, t.TempDir())
	t.Cleanup(b.Stop)
	err := b.startProcess("", "", "")
	if err == nil {
		t.Fatal("startProcess with a nonexistent binary returned nil error, want a start failure")
	}
}

// startProcess assigns a 5s WaitDelay before the (failing) Start.
func TestStartProcess_SetsWaitDelayToFiveSeconds(t *testing.T) {
	bogus := filepath.Join(t.TempDir(), "no-such-kiro-cli")
	b := New(bogus, t.TempDir())
	b.lifecycleCtx = context.Background()
	t.Cleanup(b.Stop)
	_ = b.startProcess("", "", "")
	if b.cmd == nil {
		t.Fatal("b.cmd is nil; startProcess did not reach CommandContext")
	}
	if b.cmd.WaitDelay != 5*time.Second {
		t.Errorf("b.cmd.WaitDelay = %v, want %v", b.cmd.WaitDelay, 5*time.Second)
	}
}

// --- bridge_process.go: classifyStderrLevel ---

// A structured JSON line maps to its declared level; an unknown or
// missing level and plain text fall back to keyword classification.
func TestClassifyStderrLevel(t *testing.T) {
	cases := []struct {
		name string
		line string
		want slog.Level
	}{
		{"json_warn", `{"level":"WARN"}`, slog.LevelWarn},
		{"json_error", `{"level":"ERROR"}`, slog.LevelError},
		{"json_debug", `{"level":"DEBUG"}`, slog.LevelDebug},
		{"json_unknown_level_is_info", `{"level":"trace"}`, slog.LevelInfo},
		{"json_without_level_field_is_info", `{"msg":"hello"}`, slog.LevelInfo},
		{"plain_error_keyword_is_error", "error: boom", slog.LevelError},
		{"plain_no_keyword_is_info", "all good here", slog.LevelInfo},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyStderrLevel(tc.line)
			if got != tc.want {
				t.Errorf("classifyStderrLevel(%q) = %v, want %v", tc.line, got, tc.want)
			}
		})
	}
}

// --- bridge_process.go: matchesKeyword ---

// matchesKeyword matches the keyword only at a real word boundary: a
// preceding letter makes it a substring (no match); a non-letter before,
// the string end, or a separator after, all count as boundaries.
func TestMatchesKeyword(t *testing.T) {
	const kw = "error"
	cases := []struct {
		name string
		low  string
		want bool
	}{
		{"keyword_at_start_then_separator", "error:", true},
		{"preceded_by_letter_a_is_substring", "aerror", false},
		{"preceded_by_letter_z_is_substring", "zerror", false},
		{"preceded_by_dot_is_boundary", "..error", true},
		{"preceded_by_brace_is_boundary", "a{error", true},
		{"trailing_letter_no_match", ".errorx", false},
		{"keyword_at_end_of_string", ".error", true},
		{"trailing_colon_is_boundary", ".error:", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := matchesKeyword(tc.low, kw)
			if got != tc.want {
				t.Errorf("matchesKeyword(%q, %q) = %v, want %v", tc.low, kw, got, tc.want)
			}
		})
	}
}

// Each of '[', ']', ' ' and '=' is a word-boundary separator after the
// keyword.
func TestMatchesKeyword_SeparatorChain(t *testing.T) {
	const kw = "error"
	cases := []struct {
		name string
		low  string
		want bool
	}{
		{"open_bracket_is_boundary", "error[x", true},
		{"close_bracket_is_boundary", "error]x", true},
		{"space_is_boundary", "error x", true},
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

// --- bridge_rpc.go: readLoop end-of-scan error log ---

// readLoop logs "ACP read" on a real (non-EOF) scanner error and reaps
// the bridge.
func TestReadLoop_LogsACPReadOnScanError(t *testing.T) {
	c := capture.Default(t)
	b := readLoopBridge(errReader{failErr: errors.New("read boom")})

	b.readLoop()
	select {
	case <-b.done:
	case <-time.After(time.Second):
		t.Fatal("readLoop did not reap bridge (done not closed)")
	}

	if c.CountExact("ACP read") == 0 {
		t.Errorf(`readLoop on a scanner error did not log "ACP read"; want it present`)
	}
}

// readLoop logs nothing on a clean EOF.
func TestReadLoop_NoACPReadOnCleanEOF(t *testing.T) {
	c := capture.Default(t)
	b := readLoopBridge(errReader{failErr: io.EOF})

	b.readLoop()
	select {
	case <-b.done:
	case <-time.After(time.Second):
		t.Fatal("readLoop did not reap bridge (done not closed)")
	}

	if c.CountExact("ACP read") > 0 {
		t.Errorf(`readLoop on a clean EOF logged "ACP read"; want it absent`)
	}
}

// --- bridge_rpc.go: Notify ---

// Notify returns the context error and writes nothing when ctx is already
// canceled.
func TestNotify_CanceledCtxReturnsErrNoWrite(t *testing.T) {
	b := New("/nonexistent", "/work")
	w := &captureWriter{}
	b.stdin = w

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := b.Notify(ctx, "session/update", nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Notify(canceled ctx) err = %v, want context.Canceled", err)
	}
	if w.wrote() {
		t.Errorf("Notify(canceled ctx) wrote to stdin; want no write")
	}
}

// Notify marshals and writes a frame on the happy path.
func TestNotify_GoodCtxValidParamsWritesFrame(t *testing.T) {
	b := New("/nonexistent", "/work")
	w := &captureWriter{}
	b.stdin = w

	if err := b.Notify(context.Background(), "session/update", map[string]any{"k": "v"}); err != nil {
		t.Fatalf("Notify(good ctx, valid params) err = %v, want nil", err)
	}
	if !w.wrote() {
		t.Errorf("Notify(good ctx, valid params) wrote nothing; want a frame written to stdin")
	}
}

// Notify returns the marshal error and writes nothing for unmarshalable
// params.
func TestNotify_MarshalErrorReturnsErrNoWrite(t *testing.T) {
	b := New("/nonexistent", "/work")
	w := &captureWriter{}
	b.stdin = w

	err := b.Notify(context.Background(), "session/update", map[string]any{"bad": make(chan int)})
	if err == nil {
		t.Errorf("Notify(unmarshalable params) err = nil, want a marshal error")
	}
	if w.wrote() {
		t.Errorf("Notify(unmarshalable params) wrote to stdin; want no write")
	}
}

// --- bridge_rpc.go: writeFrame ---

// writeFrame surfaces the underlying writer's error verbatim.
func TestWriteFrame_ReturnsUnderlyingWriteError(t *testing.T) {
	sentinel := errors.New("write boom")
	b := New("/nonexistent", "/work")
	b.stdin = &captureWriter{failErr: sentinel}

	err := b.writeFrame([]byte("hello\n"))
	if err == nil {
		t.Fatalf("writeFrame with a failing writer returned nil, want an error")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("writeFrame err = %v, want the underlying write error %v", err, sentinel)
	}
}

// --- bridge_session.go: loadSession ---

// loadSession applies a well-formed result (the parsed model, not the
// fallback).
func TestLoadSession_AppliesParsedResult(t *testing.T) {
	b := New("/nonexistent", "/work")
	resp := &api.RPCResponse{
		Result: json.RawMessage(`{"sessionId":"acp-session-xyz","configOptions":[{"id":"model","currentValue":"parsed-model","options":[{"value":"parsed-model","name":"Parsed","description":"ok","_meta":{"kiro":{"rateMultiplier":1}}}]}]}`),
	}
	if err := runLoadSession(t, b, "fb-model", resp); err != nil {
		t.Fatalf("loadSession returned error: %v", err)
	}
	if got := b.ModelID(); got != "parsed-model" {
		t.Errorf("loadSession ModelID() = %q, want %q (parsed result must be applied, not fallback)", got, "parsed-model")
	}
}

// loadSession warns and falls back when the result can't be parsed.
func TestLoadSession_WarnsOnUnparseableResult(t *testing.T) {
	c := capture.Default(t)
	b := New("/nonexistent", "/work")
	resp := &api.RPCResponse{Result: json.RawMessage(`{"sessionId":"x"`)} // truncated -> parse error
	if err := runLoadSession(t, b, "fb-model", resp); err != nil {
		t.Fatalf("loadSession returned error: %v", err)
	}
	if c.CountExact("session/load: unparseable result, using fallback") == 0 {
		t.Errorf("loadSession on an unparseable result did not log the fallback warn; want it present")
	}
}

// loadSession with an unparseable result must fill an empty model from
// the provided fallback. The `b.modelID == ""` gate in loadSession is
// what applies the fallback on the parse-failure path;
// TestLoadSession_WarnsOnUnparseableResult only checks the warn log, so
// this pins the model that actually ends up applied.
func TestLoadSession_FallbackModelAppliedOnUnparseableResult(t *testing.T) {
	b := New("/nonexistent", "/work")
	resp := &api.RPCResponse{Result: json.RawMessage(`{"sessionId":"x"`)} // truncated -> parse error
	if err := runLoadSession(t, b, "fb-model", resp); err != nil {
		t.Fatalf("loadSession returned error: %v", err)
	}
	if got := b.ModelID(); got != "fb-model" {
		t.Errorf("loadSession ModelID() = %q, want %q (an empty model must be filled from the fallback on an unparseable result)", got, "fb-model")
	}
}

// TestInitialize_HooksCapabilityOptIn verifies that StartOpts.EnableHooks
// controls the _meta.kiro.hooks opt-in in the initialize handshake. When true
// the bridge declares {enabled:true,v2:true} so KAS's v2 hook engine autofires
// the workspace's .kiro/hooks/*.json hooks during a turn (chat bridges set this
// in hub/bridge_coord.go; KAS then loads and runs the hooks internally, with no
// executeHook callback to the client). When false (the zero value) the opt-in
// is omitted, while the always-on openExternalUrl + infrastructureSafety kiro
// capabilities are still declared either way.
func TestInitialize_HooksCapabilityOptIn(t *testing.T) {
	// Fake kiro-cli that appends the raw initialize request to $INIT_CAPTURE
	// so the test can assert on the exact clientCapabilities vibekit sent.
	script := `#!/bin/sh
while IFS= read -r line; do
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  method=$(echo "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  case "$method" in
    initialize)
      printf '%s\n' "$line" >> "$INIT_CAPTURE"
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"serverInfo":{"name":"fake"}}}\n' "$id"
      ;;
    session/new)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"sess_hooktest","modes":{"currentModeId":"default","availableModes":[{"id":"default","name":"Default","description":"d"}]},"configOptions":[{"id":"model","currentValue":"m","options":[{"value":"m","name":"M","description":"x","_meta":{"kiro":{"rateMultiplier":1.0}}}]}]}}\n' "$id"
      ;;
    *)
      if [ -n "$id" ]; then printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id"; fi
      ;;
  esac
done
`
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "fake-kiro-cli")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake script: %v", err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	run := func(t *testing.T, enableHooks bool) string {
		t.Helper()
		capture := filepath.Join(t.TempDir(), "init.jsonl")
		t.Setenv("INIT_CAPTURE", capture)
		b := New(scriptPath, dir)
		if err := b.Start(context.Background(), &api.StartOpts{Model: "m", EnableHooks: enableHooks}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer b.Stop()
		data, err := os.ReadFile(capture)
		if err != nil {
			t.Fatalf("read init capture: %v", err)
		}
		return string(data)
	}

	t.Run("enabled declares v2 hooks opt-in", func(t *testing.T) {
		got := run(t, true)
		if !strings.Contains(got, `"hooks":{"enabled":true,"v2":true}`) {
			t.Errorf("initialize missing hooks opt-in; got: %s", got)
		}
		if !strings.Contains(got, `"openExternalUrl":true`) || !strings.Contains(got, `"infrastructureSafety":true`) {
			t.Errorf("initialize missing base kiro capabilities; got: %s", got)
		}
		// Each settings key is asserted INDEPENDENTLY rather than as one exact
		// `"settings":{...}` substring. Three reasons: Go marshals map keys
		// sorted, so an exact match breaks whenever a key is added; the old exact
		// form is what hid the two MISSING keys, since it asserted the map's
		// contents were complete when they were not; and each key gates a
		// different KAS subsystem, so a per-key assertion says which one broke.
		//
		// All of them are read with an absent-key-means-false resolver on the
		// KAS side, so dropping one costs a whole capability with nothing in any
		// log to say so: codeIntelligence removes the native code tool,
		// knowledge removes the Knowledge tool (leaving vibekit's whole
		// knowledge UI with no retrieval half), subagentOrchestration downgrades
		// the delegation tool, and goal makes typed /goal reach the model as
		// prose instead of launching a run.
		//
		// `workflows` is deliberately NOT in this list any more: KAS resolves it
		// per session, so it moved to the session door and is asserted by
		// TestSessionNewCarriesWorkflowsAtSessionDoor. Asserting it here is what
		// this test used to do while the key resolved absent-to-false on every
		// session — a green assertion over a dead key.
		for _, key := range []string{"codeIntelligence", "knowledge", "subagentOrchestration", "goal"} {
			if !strings.Contains(got, `"`+key+`":{"enabled":true}`) {
				t.Errorf("initialize missing the %s settings opt-in; got: %s", key, got)
			}
		}
		// Both flags are read by KAS with a strict `=== true` against the TOP
		// level of _meta.kiro, and both fail SILENTLY when absent: without
		// backgroundProcesses the agent simply has no control_bash_process /
		// list_processes / get_process_output tool (probed: it answers "no such
		// tool" instead of erroring), and without knowledge the system prompt
		// carries no knowledge-base listing. Nesting either one or dropping it
		// costs the capability with nothing in any log to say so.
		if !strings.Contains(got, `"backgroundProcesses":true`) {
			t.Errorf("initialize missing the background-process opt-in; got: %s", got)
		}
		if !strings.Contains(got, `"knowledge":true`) {
			t.Errorf("initialize missing the knowledge opt-in; got: %s", got)
		}
	})

	t.Run("disabled omits the hooks opt-in", func(t *testing.T) {
		got := run(t, false)
		if strings.Contains(got, `"hooks"`) {
			t.Errorf("initialize should omit hooks when EnableHooks=false; got: %s", got)
		}
		if !strings.Contains(got, `"openExternalUrl":true`) {
			t.Errorf("initialize missing base kiro capabilities; got: %s", got)
		}
	})
}

// --- The session door ---

// envAgentWorkflows is the operator off switch for the workflows capability,
// declared in internal/kascap/table.go. Named here as a literal because the
// declaration is unexported and this package must not widen kascap's surface to
// reach it. Every test below pins it EMPTY, which envx reads as unset: without
// that the assertions depend on the ambient environment, and a machine carrying
// the variable would fail them for a reason the diff does not show.
const envAgentWorkflows = "VIBEKIT_AGENT_WORKFLOWS"

// sessionDoorScript is a fake kiro-cli that appends EVERY request to
// $RPC_CAPTURE, one JSON line each, so a test can pick out the session call
// rather than matching anywhere in the stream. That distinction is the point:
// initialize carries an _meta.kiro block of its own, so a substring search over
// the whole capture would pass on the connection door's payload and prove
// nothing about the session door.
const sessionDoorScript = `#!/bin/sh
while IFS= read -r line; do
  printf '%s\n' "$line" >> "$RPC_CAPTURE"
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  method=$(echo "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"serverInfo":{"name":"fake"}}}\n' "$id"
      ;;
    session/new|session/load)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"sess_doortest","modes":{"currentModeId":"default","availableModes":[{"id":"default","name":"Default","description":"d"}]},"configOptions":[{"id":"model","currentValue":"m","options":[{"value":"m","name":"M","description":"x","_meta":{"kiro":{"rateMultiplier":1.0}}}]}]}}\n' "$id"
      ;;
    *)
      if [ -n "$id" ]; then printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id"; fi
      ;;
  esac
done
`

// captureRequest starts a bridge against sessionDoorScript and returns the raw
// request line for one method, failing when the method was never sent.
//
// Fails rather than returns empty on a miss, because "the call did not happen"
// and "the call carried nothing" are different defects and only one of them is
// about the session door.
func captureRequest(t *testing.T, method string, opts *api.StartOpts) string {
	t.Helper()
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "fake-kiro-cli")
	if err := os.WriteFile(scriptPath, []byte(sessionDoorScript), 0o755); err != nil {
		t.Fatalf("write fake script: %v", err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv(envAgentWorkflows, "")
	capturePath := filepath.Join(t.TempDir(), "rpc.jsonl")
	t.Setenv("RPC_CAPTURE", capturePath)

	b := New(scriptPath, dir)
	if err := b.Start(context.Background(), opts); err != nil {
		t.Fatalf("Start: %v", err)
	}
	b.Stop()

	data, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read rpc capture: %v", err)
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		if strings.Contains(line, `"method":"`+method+`"`) {
			return line
		}
	}
	t.Fatalf("no %s request in the capture; got:\n%s", method, data)
	return ""
}

// metaKiroSettings digs _meta.kiro.settings out of a captured request by
// walking the decoded JSON one level at a time.
//
// A walk rather than a substring match, because the failure this guards is a key
// nested at the wrong depth: `settings` beside `kiro` instead of inside it, or
// `_meta.settings` with no `kiro` at all. Both would satisfy a
// strings.Contains(`"workflows":{"enabled":true}`) and both resolve to nothing on
// the KAS side. Each missing level is named so a failure says which one broke.
func metaKiroSettings(t *testing.T, line string) map[string]any {
	t.Helper()
	var req map[string]any
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		t.Fatalf("captured request is not JSON: %v\n%s", err, line)
	}
	node := req
	for _, level := range []string{"params", "_meta", "kiro", "settings"} {
		next, ok := node[level].(map[string]any)
		if !ok {
			t.Fatalf("captured request has no %s object (%T); the session door's block is missing or misnested:\n%s",
				level, node[level], line)
		}
		node = next
	}
	return node
}

// TestSessionNewCarriesWorkflowsAtSessionDoor pins that session/new carries the
// workflows settings opt-in, at the exact depth KAS reads it from.
//
// This is the wire half of the defect the kascap row records. KAS resolves this
// key ONLY per session — createNewSessionState calls resolveWorkflows over
// parseSettings(kiroMeta?.settings) off this call's own _meta, with no
// connection-level fallback — so while it rode initialize it resolved
// absent-to-false on every session and the agent had no run_workflow,
// inspect_workflow, update_workflow, validate_workflow or send_message tool, and
// no workflow steering doc. Nothing logged it and no method 404'd, so a fixture
// on this exact call is the only thing that notices.
func TestSessionNewCarriesWorkflowsAtSessionDoor(t *testing.T) {
	line := captureRequest(t, "session/new", &api.StartOpts{Model: "m"})
	settings := metaKiroSettings(t, line)
	got, ok := settings["workflows"].(map[string]any)
	if !ok {
		t.Fatalf(`session/new carried no workflows settings entry (%T). If %s is set to a false
value in this environment that is the cause; otherwise the row left the session
door. Captured:
%s`, settings["workflows"], envAgentWorkflows, line)
	}
	// The object, not a bare true: isSettingEnabled returns val.enabled for an
	// object and false for everything else, so `workflows: true` reads as
	// DISABLED and looks correct in a diff.
	if got["enabled"] != true {
		t.Errorf("session/new sent workflows=%v, want {\"enabled\":true}", got)
	}
}

// TestSessionLoadCarriesWorkflowsAtSessionDoor pins the same key on session/load.
//
// Not a duplicate of the test above. KAS resolves a session key from the call's
// own _meta first and falls back to what the session persisted when it was
// CREATED, so every session created before this row existed carries
// workflowsEnabled false on disk. Sending it only on session/new would mean a
// fresh chat has the workflow tools and a resumed one silently does not, which is
// the worst shape of the two: it looks like the fix landed.
func TestSessionLoadCarriesWorkflowsAtSessionDoor(t *testing.T) {
	line := captureRequest(t, "session/load", &api.StartOpts{Model: "m", SessionID: "sess_resume_door"})
	settings := metaKiroSettings(t, line)
	got, ok := settings["workflows"].(map[string]any)
	if !ok {
		t.Fatalf(`session/load carried no workflows settings entry (%T); a resumed chat would
lose a capability a fresh one has. Captured:
%s`, settings["workflows"], line)
	}
	if got["enabled"] != true {
		t.Errorf("session/load sent workflows=%v, want {\"enabled\":true}", got)
	}
}

// TestSessionDoorOmitsMetaWhenDisabled pins the other half of the wiring: the
// _meta key is absent entirely when the session door's projection is empty.
//
// Driven through the operator off switch, which is the only way to empty the
// projection at runtime — and that makes this test do double duty, since it also
// proves the env override reaches the real wire rather than only the projection
// its own package tests. An empty `_meta.kiro` on a call that needs none is bytes
// on every session start, and worse, it would read to the next person as though
// the door carried something.
func TestSessionDoorOmitsMetaWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "fake-kiro-cli")
	if err := os.WriteFile(scriptPath, []byte(sessionDoorScript), 0o755); err != nil {
		t.Fatalf("write fake script: %v", err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv(envAgentWorkflows, "false")
	capturePath := filepath.Join(t.TempDir(), "rpc.jsonl")
	t.Setenv("RPC_CAPTURE", capturePath)

	b := New(scriptPath, dir)
	if err := b.Start(context.Background(), &api.StartOpts{Model: "m"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	b.Stop()

	data, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read rpc capture: %v", err)
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		if !strings.Contains(line, `"method":"session/new"`) {
			continue
		}
		if strings.Contains(line, `"_meta"`) {
			t.Errorf("session/new carried an _meta key with the session door disabled:\n%s", line)
		}
		return
	}
	t.Fatalf("no session/new request in the capture; got:\n%s", data)
}

// --- _meta.title: the wire shape KAS actually sends ---

// TestApplySessionResult_TakesFlatMetaTitle pins that the session title is read
// from a FLAT `_meta.title`, not from `_meta.kiro.title`.
//
// Every other `_meta` vibekit decodes on this wire is nested under `kiro`
// (sessionConfigChoice's rateMultiplier, the prompt metadata), so `_meta.kiro`
// is the shape a reader expects — and moving the tag there compiles cleanly and
// silently yields "". Probed 2026-08-02 against a live kiro-cli: session/new and
// session/load both spread KAS's session-metadata object directly onto `_meta`,
// so `title` sits at its top level alongside `id`, `agentMode` and
// `workspacePaths`. The second case is the trap.
func TestApplySessionResult_TakesFlatMetaTitle(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "flat _meta.title is adopted",
			body: `{"sessionId":"s1","_meta":{"id":"s1","title":"Reaper live-session exemption","agentMode":"vibe"}}`,
			want: "Reaper live-session exemption",
		},
		{
			name: "nested _meta.kiro.title is NOT the wire shape",
			body: `{"sessionId":"s1","_meta":{"kiro":{"title":"wrong nesting"}}}`,
			want: "",
		},
		{
			name: "session/new placeholder arrives verbatim for the caller to reject",
			body: `{"sessionId":"s1","_meta":{"title":"New Session"}}`,
			want: "New Session",
		},
		{
			name: "absent _meta leaves the title empty",
			body: `{"sessionId":"s1"}`,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var r sessionCreated
			if err := json.Unmarshal([]byte(tc.body), &r); err != nil {
				t.Fatalf("unmarshal session result: %v", err)
			}
			b := &Bridge{}
			b.mu.Lock()
			b.applySessionResultLocked(r, "")
			b.mu.Unlock()
			if got := b.SessionTitle(); got != tc.want {
				t.Errorf("SessionTitle() = %q, want %q", got, tc.want)
			}
		})
	}
}

// --- R1: the bridge's Cancel must close stdin, not just signal the head ---

// TestCancelClosesStdinSoTheTreeSeesEOF is the R1 regression.
//
// vibekit runs `kiro-cli acp` on pipes and the head passes its stdio down, so
// the tree (kiro-cli -> kiro-cli-chat -> node, ~300 MB) stays in ONE session
// with no setsid(). Closing vibekit's write end therefore delivers EOF to the
// whole chain, and that — not the signal — is what reclaims it: WaitDelay's
// SIGKILL escalation targets the head only. Measured on kiro-cli 2.16.0,
// signal-without-close leaked 2/2 trials at ~250 MB each while
// close-then-signal leaked 0/2.
//
// The bait isolates the close from the signal: the head IGNORES SIGTERM, so the
// grandchild can only be reclaimed by stdin reaching EOF. If Cancel goes back to
// a bare Signal(SIGTERM), the grandchild survives and this fails.
func TestCancelClosesStdinSoTheTreeSeesEOF(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")
	// The head IGNORES SIGTERM and blocks forever. Its child reads vibekit's
	// stdin pipe, and `head -c 1` returns the moment that pipe closes with no
	// data. So the grandchild's death proves an EOF reached the TREE, and
	// nothing else can cause it inside the deadline: WaitDelay's 5s SIGKILL
	// escalation is past it, and Wait — which would close the pipes itself — is
	// not called until Stop.
	//
	// `exec 3<&0` is required, not decoration: POSIX redirects an asynchronous
	// command's stdin from /dev/null when job control is off, so a plain
	// `head &` would read EOF instantly and the test would pass vacuously
	// (observed). fd 3 carries the real pipe past that rule.
	script := "#!/bin/sh\ntrap '' TERM\nexec 3<&0\nhead -c 1 <&3 >/dev/null &\n" +
		"echo $! > " + pidFile + "\nwhile :; do sleep 0.05; done\n"
	scriptPath := filepath.Join(dir, "fake-kiro-cli")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write bait script: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	b := New(scriptPath, dir)
	// Start's ACP handshake never completes (the bait speaks no ACP), so drive
	// the spawn directly — this test is about teardown, not the handshake.
	// lifecycleCtx is what CommandContext binds to, which is the path Cancel
	// fires from.
	b.lifecycleCtx = ctx
	if err := b.startProcess("", "model", ""); err != nil {
		t.Fatalf("startProcess: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		b.Stop()
	})

	grandchild := waitForBridgePID(t, pidFile)
	if syscall.Kill(grandchild, 0) != nil {
		t.Fatalf("bait grandchild %d not alive before cancel; the test proves nothing", grandchild)
	}

	cancel() // fires cmd.Cancel

	deadline := time.Now().Add(3 * time.Second)
	for syscall.Kill(grandchild, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d survived ctx cancel; Cancel signalled the head without closing stdin, so the tree never saw EOF", grandchild)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitForBridgePID polls for the bait script's pid file and returns the pid.
func waitForBridgePID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		raw, err := os.ReadFile(path) // #nosec G304 -- t.TempDir path
		if err == nil {
			if pid, cErr := strconv.Atoi(strings.TrimSpace(string(raw))); cErr == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("bait never wrote its grandchild pid to %s", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestSessionParams_CarryNoMCPServers pins BOTH halves of the MCP session
// param contract, on the wire.
//
// PRESENCE: kiro-cli 2.16's session/new schema requires `mcpServers` as a
// non-optional array — omitting the key fails every session with "Invalid
// params: expected array, received undefined". So the key must be there.
//
// EMPTINESS: vibekit used to send its server set inline. It renders KAS's
// own hot-reloading config file instead, and KAS merges client entries over
// file entries PER NAME — a surviving inline entry OUTRANKS the file: the
// file would still reload, the agent would keep using the inline copy, and
// every edit in the UI would look like it did nothing. That failure is
// silent and would present as "MCP settings don't work". An EMPTY array
// carries no names, so the file-based set is untouched.
//
// It asserts on the RAW request bytes, not on a Go struct, because the bug
// it guards against is a re-added or re-dropped map key — something a typed
// assertion on StartOpts would not see.
func TestSessionParams_CarryNoMCPServers(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "requests.log")

	// The fake logs every line it receives, then answers the three methods Start
	// needs. `tee -a` is the whole instrumentation.
	script := `#!/bin/sh
while IFS= read -r line; do
  printf '%s\n' "$line" >> "` + logPath + `"
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  method=$(echo "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"serverInfo":{"name":"fake"}}}\n' "$id"
      ;;
    session/new)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"sess-new"}}\n' "$id"
      ;;
    session/load)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"sess-load"}}\n' "$id"
      ;;
    *)
      if [ -n "$id" ]; then
        printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id"
      fi
      ;;
  esac
done
`
	scriptPath := filepath.Join(dir, "fake-kiro-cli")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake script: %v", err)
	}

	cases := []struct {
		name   string
		opts   *api.StartOpts
		method string
	}{
		{"session/new", &api.StartOpts{Model: "m"}, "session/new"},
		{"session/load", &api.StartOpts{SessionID: "existing", Model: "m"}, "session/load"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(logPath, nil, 0o600); err != nil {
				t.Fatalf("reset log: %v", err)
			}
			b := New(scriptPath, dir)
			if err := b.Start(context.Background(), tc.opts); err != nil {
				t.Fatalf("Start: %v", err)
			}
			b.Stop()

			data, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("read log: %v", err)
			}
			var found bool
			for line := range strings.SplitSeq(string(data), "\n") {
				if !strings.Contains(line, `"method":"`+tc.method+`"`) {
					continue
				}
				found = true
				if !strings.Contains(line, `"mcpServers":[]`) {
					t.Errorf("%s must carry an EMPTY mcpServers array (2.16 requires the key; entries would outrank KAS's config file):\n%s", tc.method, line)
				}
			}
			if !found {
				t.Fatalf("no %s request in the log; the test proved nothing:\n%s", tc.method, data)
			}
		})
	}
}

// coreIOToolIDs is KAS's own CORE_IO_TOOL_IDS set, read off the 2.16.1 bundle
// (acp-server.js:464015-464022).
//
// KAS ships TWO ExecuteBash implementations and picks between them with
// `hasClientIOTools = clientTools.some(t => CORE_IO_TOOL_IDS.has(t.id))`. vibekit
// gets the CLAMPED one — `min(input.timeout ?? 120000, 1800000)`, a 30 minute
// ceiling on every agent shell command — precisely because it declares none of
// these ids.
var coreIOToolIDs = []string{
	"execute_bash", "read_file", "fs_write", "str_replace", "grep_search", "file_search",
}

// TestInitialize_DeclaresNoCoreIOTool guards a bound vibekit does not own and
// cannot see.
//
// Registering any client tool named above flips `hasClientIOTools` and silently
// promotes the agent to the UNBOUNDED ExecuteBash. Nothing logs the switch and no
// behaviour changes until some command runs long, so the 30 minute ceiling would
// vanish as a side effect of an unrelated feature and the symptom would be "a
// turn hung forever" months later.
//
// The constraint was already written down beside the capability map
// (bridge.go's clientCapabilities block). A comment does not fail a build, which
// is what this test is for. If a future change genuinely wants one of these
// tools, it has to delete this test — and that deletion is the conversation about
// reintroducing the bound deliberately.
//
// Asserts on the RAW initialize bytes rather than a Go value: the ids can reach
// KAS through clientCapabilities or through _meta, and only the wire sees both.
func TestInitialize_DeclaresNoCoreIOTool(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "init.log")

	script := `#!/bin/sh
while IFS= read -r line; do
  printf '%s\n' "$line" >> "` + logPath + `"
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  method=$(echo "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  case "$method" in
    initialize)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"serverInfo":{"name":"fake"}}}\n' "$id"
      ;;
    session/new)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"sess-new"}}\n' "$id"
      ;;
    *)
      if [ -n "$id" ]; then
        printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id"
      fi
      ;;
  esac
done
`
	scriptPath := filepath.Join(dir, "fake-kiro-cli")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake script: %v", err)
	}

	b := New(scriptPath, dir)
	if err := b.Start(context.Background(), &api.StartOpts{Model: "m"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	b.Stop()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var initLine string
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.Contains(line, `"method":"initialize"`) {
			initLine = line
			break
		}
	}
	if initLine == "" {
		t.Fatalf("no initialize request in the log; the test proved nothing:\n%s", data)
	}
	for _, id := range coreIOToolIDs {
		if strings.Contains(initLine, `"`+id+`"`) {
			t.Errorf(`initialize declares %q, which is in KAS's CORE_IO_TOOL_IDS.
That flips hasClientIOTools and swaps the agent onto the UNBOUNDED ExecuteBash,
removing the 30 minute ceiling on every shell command. If this is intended, the
bound has to be reintroduced deliberately (see bridge.go's clientCapabilities).
initialize was:
%s`, id, initLine)
		}
	}
}

// --- The initialize wire contract ---

// initializeGoldenPath is the committed byte-for-byte capture of every
// initialize request vibekit can put on the wire.
const initializeGoldenPath = "testdata/initialize.golden"

// initializeGoldenCmd is the regeneration command, quoted in every failure
// message this fixture can produce.
const initializeGoldenCmd = "UPDATE_GOLDEN=1 go test ./internal/bridge/ -run TestInitializeDeclaresExactly"

// initGateCases is the COMPLETE matrix of runtime gates on the _meta.kiro
// block: StartOpts.SecretStorage decides secretStorage's VALUE (the key is
// present either way) and StartOpts.EnableHooks decides whether the hooks key
// is present AT ALL. Those are two different mechanisms, which is why both
// need a row rather than one shared "capabilities on/off" case. Nothing else
// in the initialize payload varies at runtime, so these four rows are
// exhaustive.
//
// The slice order IS the golden's line order. Reordering it rewrites the
// fixture without changing the wire, which would destroy the fixture's value.
var initGateCases = []struct {
	name          string
	secretStorage bool
	enableHooks   bool
}{
	{"gates off", false, false},
	{"secret storage only", true, false},
	{"hooks only", false, true},
	{"both gates on", true, true},
}

// TestInitializeDeclaresExactly pins the exact bytes of every initialize
// request vibekit can send, against a committed golden.
//
// The fixture was originally captured from the PRE-kascap code, so that it
// witnessed one claim nothing else could: moving the _meta.kiro block into
// internal/kascap changed no byte on the wire. That claim is DISCHARGED and is
// not re-checked here — the fixture has since been regenerated for the
// capability-door change (workflows left this door for the session door, goal and
// workspaceTrusted joined it), and a golden regenerated after a change proves
// only that the change agrees with itself.
//
// What it pins now is the current connection-door contract, which is worth as
// much: every failure mode this fixture exists for is silent on the wire. A
// settings key dropped to a bare true resolves false, a capability renamed by a
// KAS bump simply never matches, and a key nested one level wrong is ignored.
// None of those produce an error, a log line or a -32601.
//
// The capture is the raw JSON-RPC line vibekit wrote to the subprocess's
// stdin, not a re-marshalling of an intermediate map, so it cannot agree with
// the code while disagreeing with the wire. The session door has its own
// fixtures beside it: TestSessionNewCarriesWorkflowsAtSessionDoor and its
// session/load twin.
//
// Two things make it deterministic and both are pinned elsewhere: the RPC id
// is 1 because initialize is the first Call of a bridge's life (Call does
// nextID.Add(1), and Start calls initialize before session/new), and
// clientInfo.version is "dev" because version.Build only leaves its default
// under the image build's -ldflags, which version.TestBuildDefaultsToDev pins.
// Neither is masked; a change to either is a real wire change and should fail
// here.
//
// Regenerate with:
//
//	UPDATE_GOLDEN=1 go test ./internal/bridge/ -run TestInitializeDeclaresExactly
func TestInitializeDeclaresExactly(t *testing.T) {
	// Fake kiro-cli that appends the raw initialize request to $INIT_CAPTURE
	// and answers the rest of Start's handshake with the minimum KAS shape.
	script := `#!/bin/sh
while IFS= read -r line; do
  id=$(echo "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  method=$(echo "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  case "$method" in
    initialize)
      printf '%s\n' "$line" >> "$INIT_CAPTURE"
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"serverInfo":{"name":"fake"}}}\n' "$id"
      ;;
    session/new)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"sess_golden","modes":{"currentModeId":"default","availableModes":[{"id":"default","name":"Default","description":"d"}]},"configOptions":[{"id":"model","currentValue":"m","options":[{"value":"m","name":"M","description":"x","_meta":{"kiro":{"rateMultiplier":1.0}}}]}]}}\n' "$id"
      ;;
    *)
      if [ -n "$id" ]; then printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id"; fi
      ;;
  esac
done
`
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "fake-kiro-cli")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake script: %v", err)
	}
	t.Setenv("HOME", t.TempDir())

	var got strings.Builder
	for _, tc := range initGateCases {
		capturePath := filepath.Join(t.TempDir(), "init.jsonl")
		t.Setenv("INIT_CAPTURE", capturePath)
		b := New(scriptPath, dir)
		err := b.Start(context.Background(), &api.StartOpts{
			Model:         "m",
			SecretStorage: tc.secretStorage,
			EnableHooks:   tc.enableHooks,
		})
		if err != nil {
			t.Fatalf("%s: Start: %v", tc.name, err)
		}
		b.Stop()
		data, err := os.ReadFile(capturePath)
		if err != nil {
			t.Fatalf("%s: read init capture: %v", tc.name, err)
		}
		if len(data) == 0 {
			t.Fatalf("%s: captured no initialize request; an empty capture would pass forever", tc.name)
		}
		got.Write(data)
	}

	// Write-then-compare rather than write-and-return: the comparison below is
	// what proves the write landed, so there is one code path either way.
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(initializeGoldenPath), 0o750); err != nil {
			t.Fatalf("create testdata dir: %v", err)
		}
		if err := os.WriteFile(initializeGoldenPath, []byte(got.String()), 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("wrote %s (%d bytes, %d cases)", initializeGoldenPath, got.Len(), len(initGateCases))
	}

	want, err := os.ReadFile(initializeGoldenPath)
	if err != nil {
		t.Fatalf("read golden %s (regenerate with: %s): %v", initializeGoldenPath, initializeGoldenCmd, err)
	}
	if string(want) == got.String() {
		return
	}
	wantLines := strings.Split(strings.TrimSuffix(string(want), "\n"), "\n")
	gotLines := strings.Split(strings.TrimSuffix(got.String(), "\n"), "\n")
	if len(wantLines) != len(gotLines) {
		t.Fatalf(`initialize golden has %d request(s), the code produced %d.
A case was added to or removed from initGateCases without regenerating.
Regenerate with: %s`, len(wantLines), len(gotLines), initializeGoldenCmd)
	}
	for i := range wantLines {
		if wantLines[i] == gotLines[i] {
			continue
		}
		name := "case " + strconv.Itoa(i)
		if i < len(initGateCases) {
			name = initGateCases[i].name
		}
		t.Errorf(`initialize wire bytes changed for %q.
This is a WIRE change, not a refactor. If it is deliberate, regenerate with:
  %s
--- want
%s
+++ got
%s`, name, initializeGoldenCmd, wantLines[i], gotLines[i])
	}
}
