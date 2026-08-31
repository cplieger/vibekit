package bridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/slogx/capture"
	"github.com/cplieger/vibekit/internal/modeltext"
	"github.com/cplieger/vibekit/internal/vibekit"
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
		if got := modeltext.Hidden(tc.desc); got != tc.want {
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
	// Wire a pipe so writeFrame succeeds. Nothing plays readLoop, so no
	// response ever reaches the pending channel and Call parks on
	// select{ch, b.done}.
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = pr.Close()
	})
	b.stdin = pw

	type result struct {
		resp *vibekit.RPCResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		r, e := b.Call(t.Context(), "x", nil)
		done <- result{r, e}
	}()
	waitPending(t, b, 1)
	// Read the framed request before stopping. Call registers its pending
	// entry BEFORE it marshals and writes, so waitPending alone proves only
	// registration: Stop closing stdin inside that window makes writeFrame
	// fail with "file already closed" and Call returns that transport error
	// instead of ever reaching the select this test is about. Draining the
	// frame is the handshake that proves the write landed. Nothing answers
	// on the pending channel, so Call stays parked on b.done.
	readFrame(t, pr)
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
	if err := b.Respond(t.Context(), 42, result, nil); err != nil {
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
	if err := b.Respond(t.Context(), 7, nil, errors.New("something broke")); err != nil {
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
	rpcErr := &vibekit.RPCError{Code: -32001, Message: "custom error"}
	if err := b.Respond(t.Context(), 99, nil, rpcErr); err != nil {
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
		resp *vibekit.RPCResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		r, e := b.Call(t.Context(), "test/method", map[string]string{"key": "val"})
		done <- result{r, e}
	}()

	waitPending(t, b, 1)

	// Simulate readLoop dispatching a successful response.
	b.pendingMu.Lock()
	var id int64
	var ch chan pendingReply
	for k, v := range b.pending {
		id = k
		ch = v
		break
	}
	delete(b.pending, id)
	b.pendingMu.Unlock()

	successResp := &vibekit.RPCResponse{}
	raw := json.RawMessage(`{"ok":true}`)
	successResp.Result = raw
	ch <- pendingReply{resp: successResp}

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
		resp *vibekit.RPCResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		r, e := b.Call(t.Context(), "test/method", nil)
		done <- result{r, e}
	}()

	waitPending(t, b, 1)

	b.pendingMu.Lock()
	var id int64
	var ch chan pendingReply
	for k, v := range b.pending {
		id = k
		ch = v
		break
	}
	delete(b.pending, id)
	b.pendingMu.Unlock()

	errResp := &vibekit.RPCResponse{
		Error: &vibekit.RPCError{Code: -32600, Message: "invalid request"},
	}
	ch <- pendingReply{resp: errResp}

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
		resp *vibekit.RPCResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		r, e := b.Call(t.Context(), "test/method", nil)
		done <- result{r, e}
	}()

	waitPending(t, b, 1)

	// Simulate readLoop drain: send bridgeExitedResp to the pending channel.
	b.pendingMu.Lock()
	var ch chan pendingReply
	for _, v := range b.pending {
		ch = v
		break
	}
	b.pendingMu.Unlock()

	ch <- pendingReply{resp: bridgeExitedResp}

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
	respMsg, _ := json.Marshal(vibekit.RPCResponse{
		JSONRPC: "2.0",
		ID:      &respID,
		Result:  json.RawMessage(`{"status":"ok"}`),
	})
	notifMsg, _ := json.Marshal(vibekit.RPCResponse{
		JSONRPC: "2.0",
		Method:  "session/update",
		Params:  json.RawMessage(`{"sessionId":"s1","delta":{"type":"text","text":"hi"}}`),
	})
	respLine := append(respMsg, '\n')
	notifLine := append(notifMsg, '\n')

	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()

		pr, pw, _ := os.Pipe()
		br := &Bridge{
			stdout:  newFrameReader(bufio.NewReaderSize(pr, stdoutBufSize)),
			pending: make(map[int64]chan pendingReply),
			notifCh: make(chan vibekit.Notification, 1024),
			done:    make(chan struct{}),
		}

		// Pre-register pending entries for response messages.
		// We'll write msgCount messages total: alternate resp/notif.
		const msgCount = 500
		for id := int64(1); id <= msgCount/2; id++ {
			br.pending[id] = make(chan pendingReply, 1)
		}

		// Write all messages into the pipe, then close to signal EOF.
		go func() {
			for j := range msgCount {
				if j%2 == 0 {
					// Response with incrementing ID.
					id := int64(j/2 + 1)
					msg, _ := json.Marshal(vibekit.RPCResponse{
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

// TestRealBridge_Contract runs BridgeContractTest (from agent/shared_test.go)
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

	// The concrete type, not an interface: this package's own test has no
	// reason to go through one, and the contract suite that does is
	// bridge_contract_test.go.
	newBridge := func() *Bridge {
		return New(scriptPath, dir)
	}

	t.Run("Start_sets_session_id", func(t *testing.T) {
		b := newBridge()
		if err := b.Start(t.Context(), &vibekit.StartOpts{Lifetime: t.Context(), Model: "model"}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer b.Stop()
		if id := b.SessionID(); id == "" {
			t.Error("SessionID empty after Start")
		}
	})

	t.Run("Start_with_existing_session", func(t *testing.T) {
		b := newBridge()
		if err := b.Start(t.Context(), &vibekit.StartOpts{Lifetime: t.Context(), SessionID: "existing-sess", Model: "model"}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer b.Stop()
		if id := b.SessionID(); id != "existing-sess" {
			t.Errorf("SessionID = %q, want existing-sess", id)
		}
	})

	t.Run("Call_returns_response", func(t *testing.T) {
		b := newBridge()
		if err := b.Start(t.Context(), &vibekit.StartOpts{Lifetime: t.Context(), Model: "model"}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer b.Stop()
		resp, err := b.Call(t.Context(), "session/prompt", nil)
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
		if resp == nil {
			t.Fatal("Call returned nil response")
		}
	})

	t.Run("Notify_does_not_error", func(t *testing.T) {
		b := newBridge()
		if err := b.Start(t.Context(), &vibekit.StartOpts{Lifetime: t.Context(), Model: "model"}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer b.Stop()
		if err := b.Notify(t.Context(), "session/update", nil); err != nil {
			t.Errorf("Notify: %v", err)
		}
	})

	t.Run("Stop_closes_NotifCh", func(t *testing.T) {
		b := newBridge()
		if err := b.Start(t.Context(), &vibekit.StartOpts{Lifetime: t.Context(), Model: "model"}); err != nil {
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
		if err := b.Start(t.Context(), &vibekit.StartOpts{Lifetime: t.Context(), Model: "model"}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer b.Stop()
		if id := b.ModelID(); id == "" {
			t.Error("ModelID empty after Start")
		}
	})
}

// --- parseErrTracker state machine tests (tarch-b7-c3-p4, consolidated tarch-b4-c4-p4) ---
//
// The count-driven cases are a table; the two CLOCK-driven ones are synctest
// bubbles below it. The window case used to sit in this table and reach in to
// assign tr.windowStart directly, which asserted the branch without ever
// exercising the comparison that selects it; the decay branch had no case at all,
// because a real-clock test for it costs five minutes. Both are now driven
// through time.Now on the bubble's synthetic clock at zero real cost.

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

// TestParseErrTracker_WindowCadenceIsDrivenByTheClock exercises the comparison
// that selects parseErrSummarize, rather than assigning the field it reads.
//
// The case this replaces did `tr.windowStart = time.Now().Add(-parseErrWindow -
// time.Second)`, which reaches past the state machine to stage its own answer: a
// Record that stopped consulting windowStart at all would still have passed.
// Here the clock moves and Record decides.
func TestParseErrTracker_WindowCadenceIsDrivenByTheClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var tr parseErrTracker
		for range parseErrBurst {
			if got := tr.Record(); got != parseErrLog {
				t.Fatalf("burst Record() = %v, want parseErrLog", got)
			}
		}
		// Inside the window: suppressed, however many arrive.
		synctest.Sleep(parseErrWindow / 2)
		if got := tr.Record(); got != parseErrSuppress {
			t.Errorf("Record() inside the window = %v, want parseErrSuppress", got)
		}
		// Past it: exactly one summary line, then suppressed again — the window
		// restarts from this instant, so a second summary must not follow.
		synctest.Sleep(parseErrWindow)
		if got := tr.Record(); got != parseErrSummarize {
			t.Errorf("Record() past the window = %v, want parseErrSummarize", got)
		}
		if got := tr.Record(); got != parseErrSuppress {
			t.Errorf("Record() straight after a summary = %v, want parseErrSuppress: "+
				"the window must restart at the summary, or a storm emits one line per frame", got)
		}
		// The edge belongs to the window it closes: an error arriving exactly one
		// cadence after the last summary is still inside it, and only the instant
		// after that is due for the next line. A strict comparison is what keeps
		// the cadence a floor rather than an approximation.
		synctest.Sleep(parseErrWindow)
		if got := tr.Record(); got != parseErrSuppress {
			t.Errorf("Record() exactly %v after the summary = %v, want parseErrSuppress", parseErrWindow, got)
		}
		synctest.Sleep(time.Nanosecond)
		if got := tr.Record(); got != parseErrSummarize {
			t.Errorf("Record() a nanosecond past the cadence = %v, want parseErrSummarize", got)
		}
	})
}

// The summary window opens when the verbatim burst ENDS, not when the storm
// began. A storm that trickles — most of the burst, a long quiet spell, then the
// last verbatim line — must still get a full window before its first summary; a
// window anchored earlier would summarize the very next line and lose the
// cadence for the rest of the storm.
func TestParseErrTracker_TheWindowOpensWhenTheBurstEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var tr parseErrTracker
		for range parseErrBurst - 1 {
			if got := tr.Record(); got != parseErrLog {
				t.Fatalf("burst Record() = %v, want parseErrLog", got)
			}
		}
		// Long enough that a window anchored at the storm's start is already
		// stale, short enough that the storm has not decayed.
		synctest.Sleep(parseErrWindow + time.Second)
		if got := tr.Record(); got != parseErrLog {
			t.Fatalf("the last verbatim Record() = %v, want parseErrLog", got)
		}
		if got := tr.Record(); got != parseErrSuppress {
			t.Errorf("Record() straight after the burst = %v, want parseErrSuppress: "+
				"nothing is due to be summarized until a window has passed since the burst ended", got)
		}
	})
}

// TestParseErrTracker_DecayRestartsTheBurstButNotTheBreaker covers the
// parseErrDecay branch, which had no test — a real-clock one costs five minutes —
// and pins the half the old comment got wrong.
//
// Decay resets the storm WINDOW, so a bridge that saw a storm hours ago gets its
// verbatim burst back instead of staying summary-only for the life of the
// process. It deliberately leaves `consecutive` alone: Reset clears that on every
// frame that parses, so parseErrMaxConsecutive frames with not one valid frame
// between them is a dead stream at any pace, and decaying the count would stop
// the breaker firing on a stream that fails totally but slowly. The old comment
// claimed decay prevented "false circuit-breaks", which is the opposite of what
// the second half asserts here.
func TestParseErrTracker_DecayRestartsTheBurstButNotTheBreaker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var tr parseErrTracker
		for range parseErrBurst + 5 {
			tr.Record()
		}
		if got := tr.Record(); got != parseErrSuppress {
			t.Fatalf("Record() past the burst = %v, want parseErrSuppress", got)
		}

		// The edge belongs to the storm: quiet for exactly parseErrDecay is not yet
		// a decayed storm, so this line is still part of it and gets a summary
		// rather than the verbatim treatment a fresh burst would earn.
		synctest.Sleep(parseErrDecay)
		if got := tr.Record(); got != parseErrSummarize {
			t.Errorf("Record() after exactly %v of quiet = %v, want parseErrSummarize: "+
				"the burst must not restart until the decay is exceeded", parseErrDecay, got)
		}

		synctest.Sleep(parseErrDecay + time.Second)

		// The burst is back: the storm window was reset, so this line is emitted
		// verbatim rather than suppressed.
		if got := tr.Record(); got != parseErrLog {
			t.Errorf("Record() after %v of quiet = %v, want parseErrLog: "+
				"decay must restart the burst", parseErrDecay, got)
		}
		if tr.total != 1 {
			t.Errorf("total = %d after decay, want 1", tr.total)
		}

		// The breaker is NOT back. consecutive has counted every error, decay
		// included, so one more than the ceiling away from it still trips.
		if tr.consecutive != parseErrBurst+8 {
			t.Fatalf("consecutive = %d, want %d: decay must not touch the breaker's count",
				tr.consecutive, parseErrBurst+8)
		}
		for range parseErrMaxConsecutive - tr.consecutive - 1 {
			tr.Record()
		}
		if got := tr.Record(); got != parseErrCircuitBreak {
			t.Errorf("Record() at consecutive=%d = %v, want parseErrCircuitBreak: "+
				"decay must not spare a stream that fails totally but slowly",
				tr.consecutive, got)
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
	// No Model is requested, so the session/new result is the only model source
	// and "sonnet" is the value under test. A requested model would legitimately
	// override it via session/set_config_option — see
	// TestNewSession_AppliesRequestedModelAndEffort.
	if err := b.Start(t.Context(), &vibekit.StartOpts{Lifetime: t.Context()}); err != nil {
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
				resp *vibekit.RPCResponse
				err  error
			}
			done := make(chan result, 1)
			go func() {
				r, e := b.Call(t.Context(), "test/method", nil)
				done <- result{r, e}
			}()

			waitPending(t, b, 1)

			// Inject error response.
			b.pendingMu.Lock()
			var ch chan pendingReply
			for _, v := range b.pending {
				ch = v
				break
			}
			b.pendingMu.Unlock()

			errResp := &vibekit.RPCResponse{
				Error: &vibekit.RPCError{Code: tc.code, Message: tc.message},
			}
			ch <- pendingReply{resp: errResp}

			select {
			case r := <-done:
				if r.err == nil {
					t.Fatal("expected error, got nil")
				}
				if tc.wantNotIdle {
					if !errors.Is(r.err, vibekit.ErrNotIdle) {
						t.Errorf("err = %v, want vibekit.ErrNotIdle", r.err)
					}
				} else {
					if errors.Is(r.err, vibekit.ErrNotIdle) {
						t.Errorf("err = %v, should NOT be vibekit.ErrNotIdle", r.err)
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
	// A pipe-based writer (no real process), and its read end MUST be drained.
	// This used to discard it (`_, pw, err := os.Pipe()`), so writeFrame filled
	// the 64 KiB pipe buffer and blocked forever in os.(*File).Write —
	// measured: the benchmark completes at -benchtime=10x and HANGS at 50x and
	// above, so `go test -bench .` on this package never terminated. Nothing
	// caught it because `go test` without -bench never runs a benchmark.
	// Draining is also the faithful fixture: a real bridge's stdin is drained by
	// kiro-cli.
	pr, pw, err := os.Pipe()
	if err != nil {
		b.Fatalf("os.Pipe: %v", err)
	}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		_, _ = io.Copy(io.Discard, pr)
	}()
	b.Cleanup(func() {
		_ = pw.Close() // EOF for the drain
		<-drained      // join it, so nothing outlives the benchmark
		_ = pr.Close()
	})

	br := &Bridge{
		stdin:   pw,
		done:    make(chan struct{}),
		pending: make(map[int64]chan pendingReply),
		notifCh: make(chan vibekit.Notification, 16),
	}

	ctx := b.Context()
	// Typical tool result payload (~500 bytes).
	result := map[string]any{
		"content": strings.Repeat("x", 400),
		"status":  "success",
		"meta":    map[string]string{"tool": "file_write", "path": "/workspace/main.go"},
	}

	b.ReportAllocs()
	for b.Loop() {
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

// errReader yields the configured error on the first Read. A non-EOF error
// surfaces from readFrame and is logged by logReadError; io.EOF terminates the
// read loop as the ordinary teardown and logs nothing.
type errReader struct{ failErr error }

func (r errReader) Read([]byte) (int, error) { return 0, r.failErr }

// readLoopBridge builds the minimal Bridge that readLoop needs.
func readLoopBridge(r io.Reader) *Bridge {
	return &Bridge{
		stdout:  newFrameReader(bufio.NewReaderSize(r, stdoutBufSize)),
		pending: make(map[int64]chan pendingReply),
		notifCh: make(chan vibekit.Notification, 1),
		done:    make(chan struct{}),
	}
}

// runLoadSession drives loadSession against an injected RPC response: it
// spawns loadSession, waits for the pending request to register, sends
// waitPending polls until Call has registered at least n pending requests.
// This is the state the tests below need before they reach into b.pending to
// inject a response: an empty map leaves ch nil, and a send on a nil channel
// blocks forever, so the test fails as an unexplained "Call did not return"
// timeout instead of naming the real cause. Deadline-bounded, fails closed.
// readFrame drains one newline-delimited frame the bridge wrote to the pipe,
// so a test can synchronize on the write itself rather than on a state the
// write only follows. Bounded: a missing write fails the test with a
// diagnostic instead of hanging until the package timeout.
func readFrame(t *testing.T, pr *os.File) []byte {
	t.Helper()
	if err := pr.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline on the bridge pipe: %v", err)
	}
	line, err := bufio.NewReader(pr).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read the framed request from the bridge pipe: %v (read %q)", err, line)
	}
	return line
}

func waitPending(t *testing.T, b *Bridge, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		b.pendingMu.Lock()
		got := len(b.pending)
		b.pendingMu.Unlock()
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("bridge registered %d pending requests, want >=%d within 2s", got, n)
		}
		time.Sleep(time.Millisecond)
	}
}

// resp on the matching pending channel, and returns loadSession's error.
func runLoadSession(t *testing.T, b *Bridge, fallback string, resp *vibekit.RPCResponse) error {
	t.Helper()
	_, err := runLoadSessionOpts(t, b,
		&vibekit.StartOpts{SessionID: "acp-session-xyz", Model: fallback}, resp)
	return err
}

// runNewSession drives newSession the way runLoadSessionOpts drives loadSession:
// answers the session/new with resp, answers every follow-up repair with a bare
// success, and returns every frame the bridge wrote so a test can assert what the
// session door carried and what it did NOT have to send afterwards.
func runNewSession(
	t *testing.T, b *Bridge, opts *vibekit.StartOpts, resp *vibekit.RPCResponse,
) ([]byte, error) {
	t.Helper()
	return driveSessionCall(t, b, resp, func(ctx context.Context) error {
		return b.newSession(ctx, opts)
	})
}

// runLoadSessionOpts is runLoadSession with the whole StartOpts in the test's
// hands, returning every frame the bridge wrote so a test can assert WHICH
// config options the post-load re-assert sent.
func runLoadSessionOpts(
	t *testing.T, b *Bridge, opts *vibekit.StartOpts, resp *vibekit.RPCResponse,
) ([]byte, error) {
	t.Helper()
	return driveSessionCall(t, b, resp, func(ctx context.Context) error {
		return b.loadSession(ctx, opts)
	})
}

// driveSessionCall runs one session-creating call against an injected first
// response, answering every LATER request with a bare success and returning
// everything the bridge wrote to stdin.
//
// The answer-everything half is load-bearing: neither verb makes exactly one call
// any more (the chat's model and level are repaired against what the result
// reported), and a helper that answers only the first one hangs the test with
// "did not return" while naming nothing about the cause.
func driveSessionCall(
	t *testing.T, b *Bridge, resp *vibekit.RPCResponse, run func(context.Context) error,
) ([]byte, error) {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pr.Close() })
	b.stdin = pw

	done := make(chan error, 1)
	go func() {
		done <- run(t.Context())
	}()

	answer := resp
	deadline := time.Now().Add(2 * time.Second)
	for {
		select {
		case callErr := <-done:
			_ = pw.Close()
			sent, _ := io.ReadAll(pr)
			return sent, callErr
		default:
		}
		var ch chan pendingReply
		b.pendingMu.Lock()
		for id, v := range b.pending {
			ch = v
			delete(b.pending, id)
			break
		}
		b.pendingMu.Unlock()
		if ch != nil {
			ch <- pendingReply{resp: answer}
			answer = &vibekit.RPCResponse{Result: json.RawMessage(`{}`)}
			continue
		}
		if time.Now().After(deadline) {
			t.Fatal("the session call did not return, and no pending request was waiting for an answer")
		}
		time.Sleep(2 * time.Millisecond)
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

// startProcess reports a spawn failure rather than swallowing it.
//
// This was TestStartProcess_NilLifecycleCtxFallsBackToBackground, which pinned a
// context.Background() substitution for a nil lifecycleCtx — the state right
// after New. That fallback is deleted: Start refuses a nil StartOpts.Lifetime
// (TestStart_RefusesNilLifetime), so the only path into startProcess has already
// assigned a real lifetime, and a nil arriving here would be a bug worth the
// panic rather than a case to absorb into an uncancellable subprocess. The
// assertion that stood on its own survives.
func TestStartProcess_ReportsSpawnFailure(t *testing.T) {
	bogus := filepath.Join(t.TempDir(), "no-such-kiro-cli")
	b := New(bogus, t.TempDir())
	// Not t.Context(): lifecycleCtx is what CommandContext binds the subprocess
	// to, and it must outlive the t.Cleanup(b.Stop) teardown below.
	b.lifecycleCtx = context.Background()
	t.Cleanup(b.Stop)
	err := b.startProcess("")
	if err == nil {
		t.Fatal("startProcess with a nonexistent binary returned nil error, want a start failure")
	}
}

// startProcess assigns a 5s WaitDelay before the (failing) Start.
func TestStartProcess_SetsWaitDelayToFiveSeconds(t *testing.T) {
	bogus := filepath.Join(t.TempDir(), "no-such-kiro-cli")
	b := New(bogus, t.TempDir())
	// Not t.Context(): lifecycleCtx is what CommandContext binds the subprocess
	// to, and it must outlive the t.Cleanup(b.Stop) teardown below —
	// t.Context() is already cancelled by the time cleanup funcs run.
	b.lifecycleCtx = context.Background()
	t.Cleanup(b.Stop)
	_ = b.startProcess("")
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

// --- bridge_rpc.go: what a dying bridge says about the calls it stranded ---

// The exit drain reports HOW MANY in-flight calls it answered, and stays quiet
// when it answered none.
//
// The number is the difference between a bridge that exited idle and one that
// stranded a turn, and it is otherwise unobtainable from the logs: session/prompt
// carries no client-side deadline, so a wedged kiro-cli's whole on-the-wire
// signature is one prompt line followed by silence.
func TestDrainPendingAndClose_ReportsTheStrandedCalls(t *testing.T) {
	t.Run("names the count", func(t *testing.T) {
		c := capture.Default(t)
		b := New("/nonexistent", "/work")
		for _, id := range []int64{1, 2} {
			b.pending[id] = make(chan pendingReply, 1)
		}

		b.drainPendingAndClose()

		if !c.HasAttr("in flight", "failed_requests", "2") {
			t.Errorf(`the exit drain did not report failed_requests=2; the count failPending
already computes is being discarded`)
		}
	})

	t.Run("silent on an idle exit", func(t *testing.T) {
		c := capture.Default(t)
		b := New("/nonexistent", "/work")

		b.drainPendingAndClose()

		if c.Count("in flight") > 0 {
			t.Errorf("the exit drain logged on an idle teardown; every tab close would carry the line")
		}
	})
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

	if err := b.Notify(t.Context(), "session/update", map[string]any{"k": "v"}); err != nil {
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

	err := b.Notify(t.Context(), "session/update", map[string]any{"bad": make(chan int)})
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
	// A plain write error is NOT a framing loss: nothing partial reached the
	// scanner, and readLoop reaps on the EOF a dead peer produces. Reaping here
	// would take a bridge down on a transient error the caller can retry.
	select {
	case <-b.done:
		t.Errorf("writeFrame reaped the bridge on a plain write error; only a partial frame is unrecoverable")
	default:
	}
}

// shortenWriteDeadline replaces the shipped stdin write deadline for the duration
// of one test.
//
// The budget is a package var for this reason and no other, exactly as
// handshakeBudget is: driving the expiry through the SHIPPED value means the
// assertion fails when the timer is removed, where a test that arranged its own
// deadline would pass with the deadline deleted. Nothing in production writes it,
// so a test that calls this must not run in parallel with anything that writes a
// frame.
func shortenWriteDeadline(t *testing.T, d time.Duration) {
	t.Helper()
	orig := writeDeadline
	writeDeadline = d
	t.Cleanup(func() { writeDeadline = orig })
}

// waitReaped polls until the bridge's done channel closes. Bounded, so a missing
// reap reports rather than hanging: Stop runs asynchronously, because it waits on
// cmd.Wait which is downstream of the read loop.
func waitReaped(t *testing.T, b *Bridge, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		select {
		case <-b.done:
			return
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: the bridge was never reaped", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestWriteFrame_ReapsTheBridgeWhenStdinStopsDraining pins both halves of the
// write bound: the write is bounded at all, and its expiry kills the bridge.
//
// The reap is the load-bearing half. An expiry leaves a PARTIAL frame in
// kiro-cli's stdin scanner (measured: 65,536 of 1,048,576 bytes on a 64 KiB pipe
// buffer), which writeFrame's own comment calls an unrecoverable desync — so a
// deadline that returned an error and left the bridge running would be strictly
// worse than the permanent stall it replaces, because every later frame would
// land in a scanner that can no longer parse one.
func TestWriteFrame_ReapsTheBridgeWhenStdinStopsDraining(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	// pr is deliberately NEVER drained: that is the wedge under test, a peer that
	// keeps the read end open and stops reading.
	t.Cleanup(func() {
		_ = pw.Close()
		_ = pr.Close()
	})
	shortenWriteDeadline(t, 50*time.Millisecond)

	b := New("/nonexistent", "/work")
	b.stdin = pw
	// Larger than the pipe buffer, so the write cannot complete however long it
	// waits.
	err = b.writeFrame(make([]byte, 256*1024))
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("writeFrame against an undrained pipe err = %v, want %v; the write is unbounded",
			err, os.ErrDeadlineExceeded)
	}
	waitReaped(t, b, "a write deadline expired, leaving a partial frame on the wire")
}

// shortWriter reports fewer bytes than it was given with a nil error, which
// io.Writer's contract permits on some pipe edge conditions and which leaves the
// same partial frame a deadline expiry does.
type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }
func (shortWriter) Close() error                { return nil }

// TestWriteFrame_ReapsTheBridgeOnAShortWrite pins that the short-write branch
// reaps too. It detected the desync and then left the bridge running, which is the
// same unrecoverable state a deadline expiry produces by a different route.
func TestWriteFrame_ReapsTheBridgeOnAShortWrite(t *testing.T) {
	b := New("/nonexistent", "/work")
	b.stdin = shortWriter{}

	if err := b.writeFrame([]byte("hello\n")); err == nil {
		t.Fatalf("writeFrame swallowed a short write; a truncated frame desyncs kiro-cli's scanner")
	}
	waitReaped(t, b, "a short write left a partial frame on the wire")
}

// TestWriteFrame_WritesThroughAWriterWithNoDeadline pins that the deadline is
// applied only where the writer supports one.
//
// cmd.StdinPipe returns an *os.File, which does; the package's own fixtures are a
// bytes.Buffer and an error injector, which do not. An unconditional
// SetWriteDeadline call would make every one of them a compile-time or runtime
// failure, so the interface check is what keeps the production path bounded
// without the fixtures having to impersonate a file.
func TestWriteFrame_WritesThroughAWriterWithNoDeadline(t *testing.T) {
	shortenWriteDeadline(t, time.Nanosecond)
	w := &captureWriter{}
	b := New("/nonexistent", "/work")
	b.stdin = w

	if err := b.writeFrame([]byte("hello\n")); err != nil {
		t.Fatalf("writeFrame through a deadline-less writer: %v", err)
	}
	if !w.wrote() {
		t.Errorf("writeFrame wrote nothing through a deadline-less writer")
	}
}

// --- bridge_session.go: the session/new door ---

// session/new carries the chat's model and reasoning-effort level inside
// _meta.kiro, so the session is created already correct.
//
// This is not only two saved round trips. The `auto` model KAS starts a session on
// has NO effort tiers, so a level sent afterwards was silently dropped (probed on
// 2.19.1: success, no effortLevel option, `effortLevel: null` persisted), and
// KAS's own first-prompt model pin then applied that model's DEFAULT tier. Choosing
// the model before the session exists closes that window.
func TestNewSession_SendsTheModelAndEffortInSessionMeta(t *testing.T) {
	b := New("/nonexistent", "/work")
	sent, err := runNewSession(t, b,
		&vibekit.StartOpts{Model: "claude-opus-5", Effort: "max"},
		&vibekit.RPCResponse{Result: json.RawMessage(
			`{"sessionId":"acp-session-xyz","configOptions":[` +
				`{"id":"model","currentValue":"claude-opus-5","options":[{"value":"claude-opus-5","name":"O5","_meta":{"kiro":{"rateMultiplier":1}}}]},` +
				`{"id":"effortLevel","currentValue":"max","options":[{"value":"max","name":"max"}]}]}`,
		)})
	if err != nil {
		t.Fatalf("newSession returned error: %v", err)
	}

	first, _, _ := bytes.Cut(sent, []byte("\n"))
	for _, want := range []string{`"modelId":"claude-opus-5"`, `"effortLevel":"max"`} {
		if !bytes.Contains(first, []byte(want)) {
			t.Errorf("session/new params carry no %s; the frame was:\n%s", want, first)
		}
	}
	// And nothing follows: the result reported both back, so each repair sees a
	// match. A second frame here would be a round trip the door already paid for.
	if _, rest, found := bytes.Cut(sent, []byte("\n")); found && len(bytes.TrimSpace(rest)) != 0 {
		t.Errorf("session/new made follow-up calls after the door reported a match:\n%s", rest)
	}
}

// A build that ignores the meta keys still converges: the repair sees the result
// reporting a different level and sends it.
func TestNewSession_RepairsWhenTheDoorWasIgnored(t *testing.T) {
	b := New("/nonexistent", "/work")
	sent, err := runNewSession(t, b,
		&vibekit.StartOpts{Effort: "max"},
		// The session came back at the model's default rather than at max.
		&vibekit.RPCResponse{Result: json.RawMessage(
			`{"sessionId":"acp-session-xyz","configOptions":[{"id":"effortLevel","currentValue":"high","options":[{"value":"high"},{"value":"max"}]}]}`,
		)})
	if err != nil {
		t.Fatalf("newSession returned error: %v", err)
	}

	_, rest, _ := bytes.Cut(sent, []byte("\n"))
	if !bytes.Contains(rest, []byte(`"configId":"effortLevel"`)) || !bytes.Contains(rest, []byte(`"value":"max"`)) {
		t.Errorf("newSession did not repair the level the result reported; later frames were:\n%s", rest)
	}
}

// A malformed level never reaches the session door, for the same reason SetEffort
// drops one: config.json is user-editable. Shape only — an unknown-but-well-formed
// tier flows, because the vocabulary is per model and KAS's to judge.
func TestNewSession_OmitsAMalformedLevelFromTheDoor(t *testing.T) {
	b := New("/nonexistent", "/work")
	sent, err := runNewSession(t, b,
		&vibekit.StartOpts{Effort: "TURBO"},
		&vibekit.RPCResponse{Result: json.RawMessage(`{"sessionId":"acp-session-xyz"}`)})
	if err != nil {
		t.Fatalf("newSession returned error: %v", err)
	}
	if bytes.Contains(sent, []byte("effortLevel")) {
		t.Errorf("a malformed level reached the wire; frames were:\n%s", sent)
	}
}

// A well-formed tier vibekit's own constants do not name still reaches the door:
// gpt-luna ships a "none" tier, and a closed local set rejecting it is the bug
// (the catalog is upstream-owned, so a model shipped after this build must work).
func TestNewSession_SendsAWellFormedUnknownLevel(t *testing.T) {
	b := New("/nonexistent", "/work")
	sent, err := runNewSession(t, b,
		&vibekit.StartOpts{Effort: "none"},
		&vibekit.RPCResponse{Result: json.RawMessage(`{"sessionId":"acp-session-xyz"}`)})
	if err != nil {
		t.Fatalf("newSession returned error: %v", err)
	}
	if !bytes.Contains(sent, []byte(`"effortLevel":"none"`)) {
		t.Errorf("a well-formed tier did not reach the session door; frames were:\n%s", sent)
	}
}

// --- bridge_session.go: loadSession ---

// loadSession applies a well-formed result (the parsed model, not the
// fallback).
func TestLoadSession_AppliesParsedResult(t *testing.T) {
	b := New("/nonexistent", "/work")
	resp := &vibekit.RPCResponse{
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
	resp := &vibekit.RPCResponse{Result: json.RawMessage(`{"sessionId":"x"`)} // truncated -> parse error
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
	resp := &vibekit.RPCResponse{Result: json.RawMessage(`{"sessionId":"x"`)} // truncated -> parse error
	if err := runLoadSession(t, b, "fb-model", resp); err != nil {
		t.Fatalf("loadSession returned error: %v", err)
	}
	if got := b.ModelID(); got != "fb-model" {
		t.Errorf("loadSession ModelID() = %q, want %q (an empty model must be filled from the fallback on an unparseable result)", got, "fb-model")
	}
}

// A session that resolves settings.workflows against what vibekit declared says
// so in the log, because nothing else would.
//
// The session door works only because KiroSessionMetaSchema ends in
// `.passthrough()`, which is somebody else's schema property. If it goes, vibekit
// keeps sending the key and every send-side test keeps passing while the agent
// loses its whole workflowChatTools array with no error and no -32601 — the exact
// defect the workflows row exists to fix, recurring with no signal.
func TestApplySessionResult_ReportsAWorkflowsDisagreement(t *testing.T) {
	for name, tc := range map[string]struct {
		result   string
		wantWarn bool
	}{
		// The declared value is true by default (the row is send:true with no env
		// override set), so a resolved false is the failure.
		"resolved false against a declared true": {
			result:   `{"sessionId":"s","_meta":{"workflowsEnabled":false}}`,
			wantWarn: true,
		},
		"agreement is silent": {
			result:   `{"sessionId":"s","_meta":{"workflowsEnabled":true}}`,
			wantWarn: false,
		},
		// A build that does not report the member says nothing about it, and a
		// warning there would fire on every session against every older KAS.
		"an absent member is not a disagreement": {
			result:   `{"sessionId":"s","_meta":{}}`,
			wantWarn: false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := capture.Default(t)
			b := New("/nonexistent", "/work")
			if err := runLoadSession(t, b, "fb-model",
				&vibekit.RPCResponse{Result: json.RawMessage(tc.result)}); err != nil {
				t.Fatalf("loadSession returned error: %v", err)
			}
			got := c.CountExact("session resolved the workflows setting against what vibekit declared; "+
				"the agent's workflow tools are not what this spawn asked for") > 0
			if got != tc.wantWarn {
				t.Errorf("warn logged = %v, want %v", got, tc.wantWarn)
			}
		})
	}
}

// A load result that omits the `model` option leaves the previous catalog
// standing, and so does one that omits the `modes` block.
//
// This is the common case rather than an edge: KAS resolves ListAvailableModels
// asynchronously, so a session/load result routinely carries `mode`, `autopilot`
// and `contentCollection` with `model` ABSENT and the full catalog arrives on the
// config_option_update notification afterwards (measured on kiro-cli 2.20.0). An
// EXPIRED auth token produces the same shape from a different cause.
//
// applyModelConfigOptionLocked implements the keep by `continue`ing past every
// other option id and never reaching its body, which is the kind of behaviour a
// refactor removes without noticing — nothing pinned it before, and the layer
// above (agent.tryLoadSession) was overwriting its result anyway.
func TestLoadSession_AbsentCatalogKeepsThePreviousOne(t *testing.T) {
	b := New("/nonexistent", "/work")

	// Seed the catalog the way a live session does, then resume.
	seeded := &vibekit.RPCResponse{
		Result: json.RawMessage(`{"sessionId":"acp-session-xyz",` +
			`"modes":{"currentModeId":"vibe","availableModes":[{"id":"vibe","name":"Default"}]},` +
			`"configOptions":[{"id":"model","currentValue":"seeded-model","options":[` +
			`{"value":"seeded-model","name":"Seeded","description":"ok"},` +
			`{"value":"gone-model","name":"Gone","description":"[Deprecated] retired"}]}]}`),
	}
	if _, err := runNewSession(t, b, &vibekit.StartOpts{}, seeded); err != nil {
		t.Fatalf("seeding newSession returned error: %v", err)
	}
	if len(b.Models()) != 1 || len(b.ServedModels()) != 2 || len(b.Modes()) != 1 {
		t.Fatalf("seed did not take: models=%v served=%v modes=%v", b.Models(), b.ServedModels(), b.Modes())
	}

	// Two resume shapes, because ABSENT and PRESENT-BUT-EMPTY are guarded by
	// different code and only the first is reachable by deleting a gate: the
	// modes block is guarded by `r.Modes != nil` when it is missing and by the
	// length check when it arrives empty.
	for name, result := range map[string]string{
		// The measured 2.20.0 shape: three options, `model` absent, no modes block.
		"no block at all": `{"sessionId":"acp-session-xyz","configOptions":[` +
			`{"id":"mode","currentValue":"vibe"},` +
			`{"id":"autopilot","currentValue":"on"},` +
			`{"id":"contentCollection","currentValue":"on"}]}`,
		// A block that reports no catalog rather than an empty catalog.
		"empty block": `{"sessionId":"acp-session-xyz",` +
			`"modes":{"currentModeId":"vibe","availableModes":[]},` +
			`"configOptions":[{"id":"model","currentValue":"seeded-model","options":[]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			resumed := &vibekit.RPCResponse{Result: json.RawMessage(result)}
			if _, err := runLoadSessionOpts(t, b,
				&vibekit.StartOpts{SessionID: "acp-session-xyz"}, resumed); err != nil {
				t.Fatalf("loadSession returned error: %v", err)
			}

			if got := b.Models(); len(got) != 1 || got[0].ID != "seeded-model" {
				t.Errorf("Models() = %v, want the seeded catalog kept (an absent option is not an empty one)", got)
			}
			if got := b.ServedModels(); len(got) != 2 {
				t.Errorf("ServedModels() = %v, want the seeded served set kept: it gates entitlement, so emptying it refuses a model the account holds", got)
			}
			if got := b.Modes(); len(got) != 1 || got[0].ID != "vibe" {
				t.Errorf("Modes() = %v, want the seeded mode list kept; nothing refreshes modes afterwards, so this loss is permanent for the session", got)
			}
		})
	}
}

// A resumed session gets the chat's reasoning-effort level re-applied.
//
// Nothing reconciles Chat.Effort against what a loaded session reports (unlike
// the mode and the model, which tryLoadSession copies back onto the record), so
// without this a chat at max resumed at whatever level KAS had while the record
// and the pill both still read max.
func TestLoadSession_ReAppliesTheChatsEffort(t *testing.T) {
	b := New("/nonexistent", "/work")
	resp := &vibekit.RPCResponse{Result: json.RawMessage(`{"sessionId":"acp-session-xyz"}`)}

	sent, err := runLoadSessionOpts(t, b,
		&vibekit.StartOpts{SessionID: "acp-session-xyz", Effort: "max"}, resp)
	if err != nil {
		t.Fatalf("loadSession returned error: %v", err)
	}

	if !bytes.Contains(sent, []byte(`"configId":"effortLevel"`)) {
		t.Errorf("session/load sent no effortLevel option; frames were:\n%s", sent)
	}
	if !bytes.Contains(sent, []byte(`"value":"max"`)) {
		t.Errorf("session/load sent no max level; frames were:\n%s", sent)
	}
}

// A chat that has chosen no level costs a resume nothing: no effort option is
// sent, so an ordinary reopen is one round trip as before.
func TestLoadSession_SendsNoEffortWhenTheChatChoseNone(t *testing.T) {
	b := New("/nonexistent", "/work")
	resp := &vibekit.RPCResponse{Result: json.RawMessage(`{"sessionId":"acp-session-xyz"}`)}

	sent, err := runLoadSessionOpts(t, b,
		&vibekit.StartOpts{SessionID: "acp-session-xyz"}, resp)
	if err != nil {
		t.Fatalf("loadSession returned error: %v", err)
	}

	if bytes.Contains(sent, []byte("effortLevel")) {
		t.Errorf("session/load sent an effortLevel option for a chat that chose no level; frames were:\n%s", sent)
	}
}

// --- bridge.go: EnsureEffort ---

// A malformed level never reaches the wire: config.json is user-editable, so the
// shape guard is at the one door every effort call goes through. Well-formed
// tiers flow whatever their name — the vocabulary is per model and KAS's.
func TestEnsureEffort_DropsAMalformedLevel(t *testing.T) {
	for _, level := range []string{"", "HIGH", "max ", "9max"} {
		t.Run(level, func(t *testing.T) {
			b := New("/nonexistent", "/work")
			sent, err := driveSessionCall(t, b, &vibekit.RPCResponse{Result: json.RawMessage(`{}`)},
				func(ctx context.Context) error { return b.EnsureEffort(ctx, level) })
			if err != nil {
				t.Errorf("EnsureEffort(%q) = %v, want nil", level, err)
			}
			if len(sent) != 0 {
				t.Errorf("EnsureEffort(%q) reached the wire; frames were:\n%s", level, sent)
			}
		})
	}
}

// A level the session already reports costs no round trip, which is what lets the
// prompt path call this per prompt.
func TestEnsureEffort_SkipsTheLevelTheSessionReports(t *testing.T) {
	b := New("/nonexistent", "/work")
	b.mu.Lock()
	b.effortLevel = "max"
	b.mu.Unlock()

	sent, err := driveSessionCall(t, b, &vibekit.RPCResponse{Result: json.RawMessage(`{}`)},
		func(ctx context.Context) error { return b.EnsureEffort(ctx, "max") })
	if err != nil {
		t.Fatalf("EnsureEffort(the reported level) = %v, want nil", err)
	}
	if len(sent) != 0 {
		t.Errorf("EnsureEffort sent a call for the level already reported; frames were:\n%s", sent)
	}
}

// A model swap invalidates the cached level, so the next EnsureEffort asserts
// instead of matching a level KAS may have replaced. Measured on 2.19.1: a swap to
// a model offering the same tiers keeps the level, a swap to `auto` destroys it,
// and the bridge sees neither outcome.
func TestSetModel_ClearsTheCachedEffortLevel(t *testing.T) {
	b := New("/nonexistent", "/work")
	b.mu.Lock()
	b.effortLevel = "max"
	b.mu.Unlock()

	sent, err := driveSessionCall(t, b, &vibekit.RPCResponse{Result: json.RawMessage(`{}`)},
		func(ctx context.Context) error { return b.SetModel(ctx, "claude-sonnet-5") })
	if err != nil {
		t.Fatalf("SetModel returned error: %v", err)
	}
	if !bytes.Contains(sent, []byte(`"configId":"model"`)) {
		t.Fatalf("SetModel sent no model option; frames were:\n%s", sent)
	}

	b.mu.Lock()
	got := b.effortLevel
	b.mu.Unlock()
	if got != "" {
		t.Errorf("cached effort level after a swap = %q, want it cleared", got)
	}
}

// TestInitialize_HooksCapabilityOptIn verifies that StartOpts.EnableHooks
// controls the _meta.kiro.hooks opt-in in the initialize handshake. When true
// the bridge declares {enabled:true,v2:true} so KAS's v2 hook engine autofires
// the workspace's .kiro/hooks/*.json hooks during a turn (chat bridges set this
// in agent/bridge_coord.go; KAS then loads and runs the hooks internally, with no
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
		// Knowledge on, because this test asserts the base capabilities survive the
		// hooks gate and both knowledge keys are gated on their own field now. The
		// key still has to appear with a true for the per-key loop below to be
		// checking anything.
		if err := b.Start(t.Context(), &vibekit.StartOpts{Lifetime: t.Context(), Model: "m", EnableHooks: enableHooks, Knowledge: true}); err != nil {
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
//
// alsoContains narrows the match for a method a single start sends more than once:
// session/set_config_option carries the model, the effort level and autopilot on
// the same method name, so a caller after one of them names the configId too
// rather than depending on which applier ran first.
func captureRequest(t *testing.T, method string, opts *vibekit.StartOpts, alsoContains ...string) string {
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
	if err := b.Start(t.Context(), opts); err != nil {
		t.Fatalf("Start: %v", err)
	}
	b.Stop()

	data, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read rpc capture: %v", err)
	}
	needles := append([]string{`"method":"` + method + `"`}, alsoContains...)
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		matched := true
		for _, needle := range needles {
			if !strings.Contains(line, needle) {
				matched = false
				break
			}
		}
		if matched {
			return line
		}
	}
	t.Fatalf("no %s request matching %q in the capture; got:\n%s", method, alsoContains, data)
	return ""
}

// digObject walks a captured request down a chain of nested objects, failing at
// the first level that is absent or not an object.
//
// A walk rather than a substring match, because the failure this guards is a key
// nested at the wrong depth: `settings` beside `kiro` instead of inside it, or
// `_meta.settings` with no `kiro` at all. Both would satisfy a
// strings.Contains(`"workflows":{"enabled":true}`) and both resolve to nothing on
// the KAS side. Each missing level is named, and `what` names the subject, so a
// failure says which door's block broke rather than only which key.
func digObject(t *testing.T, what, line string, levels ...string) map[string]any {
	t.Helper()
	var req map[string]any
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		t.Fatalf("captured request is not JSON: %v\n%s", err, line)
	}
	node := req
	for _, level := range levels {
		next, ok := node[level].(map[string]any)
		if !ok {
			t.Fatalf("captured request has no %s object (%T); %s is missing or misnested:\n%s",
				level, node[level], what, line)
		}
		node = next
	}
	return node
}

// metaKiroSettings digs _meta.kiro.settings out of a captured SESSION request.
func metaKiroSettings(t *testing.T, line string) map[string]any {
	t.Helper()
	return digObject(t, "the session door's block", line, "params", "_meta", "kiro", "settings")
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
	line := captureRequest(t, "session/new", &vibekit.StartOpts{Lifetime: t.Context(), Model: "m"})
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
	line := captureRequest(t, "session/load", &vibekit.StartOpts{Lifetime: t.Context(), Model: "m", SessionID: "sess_resume_door"})
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

// TestSessionDoorOmitsSettingsWhenDisabled pins two properties of the session
// door on the REAL wire: the operator off switch reaches these bytes rather than
// only the projection its own package tests, and the settings container is
// DERIVED from the rows rather than always emitted — with workflows off there is
// no session-door settings row left, so the container must be absent, not `{}`.
//
// This test used to assert something stronger and can no longer reach it. Its
// subject was the `len(meta) > 0` guard in withSessionMeta — an empty projection
// must add no `_meta` at all — and the env switch was then the only way to empty
// the projection, because workflows was the only session-door row. policyPreset
// is now also on that door, and since 2026-08-25 it is GATED on the active
// security profile's preset set rather than unconditional, so this test supplies
// one below to keep a second row present. That is what makes the assertions a
// statement about the OVERRIDE's blast radius rather than about a door that
// happens to be empty.
//
// The guard STAYS, and the cost of not testing it is recorded here rather than
// papered over: it is now reachable only by a table with no session-door rows,
// which is a state the table cannot be put in from a test without a seam this
// does not earn. What survives is the half that still has a wire consequence, and
// it is the half that would actually regress — a settings container emitted empty
// would be bytes on every session start and would read to the next person as
// though the door carried something.
func TestSessionDoorOmitsSettingsWhenDisabled(t *testing.T) {
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
	if err := b.Start(t.Context(), &vibekit.StartOpts{
		Lifetime: t.Context(), Model: "m",
		// A non-empty set so the policyPreset row rides. Without it the door
		// carries only workflows, the env override empties the whole projection,
		// and the assertion below would pass for the wrong reason.
		Presets: []string{"read-workspace"},
	}); err != nil {
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
		// The env override reached the wire: no workflows key anywhere in the
		// request. Asserted on the raw line rather than the parsed settings
		// object, because the object is what must be ABSENT.
		if strings.Contains(line, `"workflows"`) {
			t.Errorf("session/new carried a workflows key with the override set to false:\n%s", line)
		}
		// The container is derived, so with its last member gone it must not
		// appear at all.
		if strings.Contains(line, `"settings"`) {
			t.Errorf(`session/new carried an empty settings container with its only
session-door member disabled; buildDoor adds the key only when a row landed in
it, so this is bytes on every session start that read as a door carrying
something:
%s`, line)
		}
		// The preset row still rides, which is what makes the two
		// assertions above a statement about the OVERRIDE rather than about the
		// door being empty.
		if !strings.Contains(line, `"policyPreset"`) {
			t.Errorf(`session/new lost policyPreset when the workflows override was set to
false. The override is per-row; if it can empty the whole door, a custom-agent
chat silently loses every search result on a deployment that set it, and every
security profile stops reaching the session:
%s`, line)
		}
		return
	}
	t.Fatalf("no session/new request in the capture; got:\n%s", data)
}

// --- supervised mode: the autopilot config-option VALUE ---

// TestApplySupervised_SendsTheStringKASDeclares pins that autopilot travels as
// the string KAS's select declares, on the real wire.
//
// A boolean is REFUSED: probed on the pinned kiro-cli 2.20.0,
// {"configId":"autopilot","value":false} answers -32602 Invalid params and the
// session stays in autopilot, so a supervised chat ran every turn without asking
// while the switch read on and the only signal was one log line. Asserted on the
// captured bytes rather than on a params map, because `false` and `"off"` are both
// a legal map value and a unit assertion cannot tell the accepted shape from the
// refused one.
func TestApplySupervised_SendsTheStringKASDeclares(t *testing.T) {
	line := captureRequest(t, "session/set_config_option",
		&vibekit.StartOpts{Lifetime: t.Context(), Model: "m", Supervised: true},
		`"configId":"`+vibekit.ConfigOptionAutopilot+`"`)
	if !strings.Contains(line, `"value":"`+vibekit.ConfigValueAutopilotOff+`"`) {
		t.Errorf(`autopilot did not carry "value":%q on the wire; KAS refuses every other
shape with -32602 and leaves the session in autopilot. Captured:
%s`, vibekit.ConfigValueAutopilotOff, line)
	}
	// And the decoded value is a STRING, so a future spelling that happens to
	// contain the same characters (a "off" nested somewhere else, a boolean with a
	// type discriminator) cannot satisfy the byte check above alone.
	params := digObject(t, "the autopilot set_config_option params", line, "params")
	if got, ok := params[keyConfigValue].(string); !ok || got != vibekit.ConfigValueAutopilotOff {
		t.Errorf("autopilot value = %#v (%T), want the string %q",
			params[keyConfigValue], params[keyConfigValue], vibekit.ConfigValueAutopilotOff)
	}
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

	// Not t.Context(): this context is the subprocess lifetime and the cancel
	// below fires from inside t.Cleanup, which runs after t.Context() is
	// already cancelled.
	ctx, cancel := context.WithCancel(context.Background())
	b := New(scriptPath, dir)
	// Start's ACP handshake never completes (the bait speaks no ACP), so drive
	// the spawn directly — this test is about teardown, not the handshake.
	// lifecycleCtx is what CommandContext binds to, which is the path Cancel
	// fires from.
	b.lifecycleCtx = ctx
	if err := b.startProcess(""); err != nil {
		t.Fatalf("startProcess: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		b.Stop()
	})

	grandchild := waitForBridgePID(t, pidFile)
	if !processAlive(grandchild) {
		t.Fatalf("bait grandchild %d not alive before cancel; the test proves nothing", grandchild)
	}

	cancel() // fires cmd.Cancel

	deadline := time.Now().Add(3 * time.Second)
	for processAlive(grandchild) {
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d survived ctx cancel; Cancel signalled the head without closing stdin, so the tree never saw EOF", grandchild)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// processAlive reports whether pid is a live (non-zombie) process, read from
// /proc/<pid>/stat.
//
// A null-signal poll is NOT usable, and the test above used to be one. `kill(pid,
// 0)` answers "alive" for a zombie, and the pid here is the bait shell's
// backgrounded child: the shell reaps it on its next wait, so a null-signal poll
// asserts the SHELL's reaping latency on top of the property under test. That
// happens to hold today because the bait ignores SIGTERM and stays alive to reap
// — one fixture edit that lets the shell die with the group orphans the child onto
// PID 1 and the poll then measures whatever init this suite runs under, which in
// the vibekit container never reaps. A zombie already proves the EOF reached the
// tree, which is the whole property.
//
// The state field follows the LAST ')' — comm is parenthesized and may itself
// contain spaces or parens, so the prefix has to be skipped from the right rather
// than split on whitespace.
func processAlive(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)) // #nosec G304 -- pid from the test's own child
	if err != nil {
		return false // no /proc entry: reaped and gone
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 >= len(s) {
		return false
	}
	return s[i+2] != 'Z'
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
		opts   *vibekit.StartOpts
		method string
	}{
		{"session/new", &vibekit.StartOpts{Lifetime: t.Context(), Model: "m"}, "session/new"},
		{"session/load", &vibekit.StartOpts{Lifetime: t.Context(), SessionID: "existing", Model: "m"}, "session/load"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(logPath, nil, 0o600); err != nil {
				t.Fatalf("reset log: %v", err)
			}
			b := New(scriptPath, dir)
			if err := b.Start(t.Context(), tc.opts); err != nil {
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
	if err := b.Start(t.Context(), &vibekit.StartOpts{Lifetime: t.Context(), Model: "m"}); err != nil {
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
// block, and there are four: StartOpts.SecretStorage decides secretStorage's
// VALUE (the key is present either way), StartOpts.EnableHooks decides whether
// the hooks key is present AT ALL, StartOpts.ToolSearch decides presence for
// settings.toolSearch, and StartOpts.Knowledge decides the VALUE of two keys at
// once (the knowledge capability and settings.knowledge). Four different
// mechanisms, which is why each needs its own axis rather than one shared
// "capabilities on/off" case.
//
// Exhaustive over the two original booleans, representative over the two added
// ones — the same argument internal/kascap's spawnMatrix makes for the same
// reason: the added gates key on their own fields and write their own keys, so a
// full 16-row product would add twelve rows differing from a row already here by
// one key. What each added gate needs is both of its states plus a line where it
// coexists with the originals, and Presets is deliberately absent from this door
// (it rides the session door, pinned by its own session/new and session/load
// fixtures).
//
// The slice order IS the golden's line order. Reordering it rewrites the
// fixture without changing the wire, which would destroy the fixture's value.
var initGateCases = []struct {
	name          string
	secretStorage bool
	enableHooks   bool
	toolSearch    bool
	knowledge     bool
}{
	{"gates off", false, false, false, false},
	{"secret storage only", true, false, false, false},
	{"hooks only", false, true, false, false},
	{"both gates on", true, true, false, false},
	{"tool search on", false, false, true, false},
	{"knowledge on", false, false, false, true},
	{"every gate on", true, true, true, true},
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
		err := b.Start(t.Context(), &vibekit.StartOpts{
			Lifetime:      t.Context(),
			Model:         "m",
			SecretStorage: tc.secretStorage,
			EnableHooks:   tc.enableHooks,
			ToolSearch:    tc.toolSearch,
			Knowledge:     tc.knowledge,
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
