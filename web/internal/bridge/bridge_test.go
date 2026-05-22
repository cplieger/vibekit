package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"vibekit/internal/api"
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

func TestNormalizeMCPServers_NilReturnsEmptySlice(t *testing.T) {
	got := normalizeMCPServers(nil)
	if got == nil {
		t.Fatal("normalizeMCPServers(nil) returned nil, want empty slice")
	}
	if len(got) != 0 {
		t.Errorf("normalizeMCPServers(nil) len = %d, want 0", len(got))
	}
	// Marshal-round-trip check: must serialize as [] not null.
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(data) != "[]" {
		t.Errorf("JSON = %s, want []", data)
	}
}

func TestNormalizeMCPServers_EmptyReturnsEmptySlice(t *testing.T) {
	got := normalizeMCPServers([]map[string]any{})
	if got == nil || len(got) != 0 {
		t.Errorf("normalizeMCPServers(empty) = %v, want empty non-nil slice", got)
	}
}

func TestNormalizeMCPServers_PreservesEntriesAndOrder(t *testing.T) {
	in := []map[string]any{
		{"name": "github", "command": "npx"},
		{"name": "linear", "url": "https://..."},
	}
	got := normalizeMCPServers(in)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	first, ok := got[0].(map[string]any)
	if !ok {
		t.Fatalf("got[0] type = %T, want map[string]any", got[0])
	}
	if first["name"] != "github" {
		t.Errorf("got[0].name = %v, want github", first["name"])
	}
	second, ok := got[1].(map[string]any)
	if !ok {
		t.Fatalf("got[1] type = %T, want map[string]any", got[1])
	}
	if second["name"] != "linear" {
		t.Errorf("got[1].name = %v, want linear", second["name"])
	}
}

// TestNormalizeMCPServers_PreservesAllFieldsAndLength pins the contract
// beyond the name field: every entry's arbitrary fields (command, args,
// url, headers, disabled) must round-trip intact, and the output
// length must equal the input length for ≥3 entries.
func TestNormalizeMCPServers_PreservesAllFieldsAndLength(t *testing.T) {
	in := []map[string]any{
		{"name": "a", "command": "npx", "args": []string{"-y", "pkg"}},
		{"name": "b", "url": "https://example", "headers": map[string]string{"k": "v"}},
		{"name": "c", "disabled": true},
	}
	got := normalizeMCPServers(in)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for i, want := range in {
		m, ok := got[i].(map[string]any)
		if !ok {
			t.Errorf("got[%d] type = %T, want map[string]any", i, got[i])
			continue
		}
		for k, v := range want {
			if !reflect.DeepEqual(m[k], v) {
				t.Errorf("got[%d][%q] = %v, want %v", i, k, m[k], v)
			}
		}
	}
}

// --- applySessionResultLocked: modes/models translation + deprecation filter ---

func TestApplySessionResult_CopiesModesAndModels(t *testing.T) {
	b := &Bridge{}
	r := sessionCreated{
		Modes: &sessionModes{
			CurrentModeID: "mode-b",
			AvailableModes: []sessionMode{
				{ID: "mode-a", Name: "Alpha", Description: "first"},
				{ID: "mode-b", Name: "Beta", Description: "second"},
			},
		},
		Models: &sessionModels{
			CurrentModelID: "claude-sonnet",
			AvailableModels: []sessionModel{
				{ModelID: "claude-sonnet", Name: "Sonnet", Description: "general", RateMultiplier: 1.0},
				{ModelID: "old-opus", Name: "Opus", Description: "[Deprecated] old", RateMultiplier: 3.0},
				{ModelID: "preview", Name: "Preview", Description: "[Internal] experimental", RateMultiplier: 1.5},
				{ModelID: "legacy", Name: "Legacy", Description: "[Legacy] v1", RateMultiplier: 1.0},
			},
		},
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
		Models: &sessionModels{CurrentModelID: "real-model"},
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
	r := sessionCreated{
		Models: &sessionModels{
			CurrentModelID: "a",
			AvailableModels: []sessionModel{
				{ModelID: "a", Name: "Alpha", Description: "x", RateMultiplier: 1},
				{ModelID: "b", Name: "Beta", Description: "y", RateMultiplier: 2},
			},
		},
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

// TestCleanupStaleSessions_InvalidIDsIgnored pins the validSessionID
// gate on the CleanupStaleSessions side. Lock filenames whose derived
// id would read as "." or ".." must be skipped rather than driving
// filesystem operations.
func TestCleanupStaleSessions_InvalidIDsIgnored(t *testing.T) {
	dir := setFakeHome(t)
	// `..lock` → id = `.` (invalid). `...lock` → id = `..` (invalid).
	// Both should remain untouched after the cleanup pass.
	invalid := []string{"..lock", "...lock"}
	for _, name := range invalid {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(`{"pid":1}`), 0o644); err != nil {
			t.Fatalf("plant %q: %v", name, err)
		}
	}

	CleanupStaleSessions(context.Background())

	for _, name := range invalid {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("invalid-id lock %q was touched: %v", name, err)
		}
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
			Code    int    `json:"code"`
			Message string `json:"message"`
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
			Code    int    `json:"code"`
			Message string `json:"message"`
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
// minimal valid responses for initialize, session/new, session/load,
// session/prompt, and session/set_model.
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
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"test-sess-001","modes":{"currentModeId":"default","availableModes":[{"id":"default","name":"Default","description":"default mode"}]},"models":{"currentModelId":"model-1","availableModels":[{"modelId":"model-1","name":"Test Model","description":"A test model","rateMultiplier":1.0}]}}}\n' "$id"
      ;;
    session/load)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"existing-sess","modes":{"currentModeId":"default","availableModes":[{"id":"default","name":"Default","description":"default mode"}]},"models":{"currentModelId":"model-1","availableModels":[{"modelId":"model-1","name":"Test Model","description":"A test model","rateMultiplier":1.0}]}}}\n' "$id"
      ;;
    session/prompt)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"status":"ok"}}\n' "$id"
      ;;
    session/set_model)
      printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id"
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

	// Set HOME so lockfile operations don't interfere with real sessions.
	home := t.TempDir()
	t.Setenv("HOME", home)
	sessDir := filepath.Join(home, ".kiro", "sessions", "cli")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}

	newBridge := func() api.ACPBridge {
		return New(scriptPath, dir)
	}

	t.Run("Start_sets_session_id", func(t *testing.T) {
		b := newBridge()
		if err := b.Start(context.Background(), &api.StartOpts{Agent: "agent", Model: "model"}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer b.Stop()
		if id := b.SessionID(); id == "" {
			t.Error("SessionID empty after Start")
		}
	})

	t.Run("Start_with_existing_session", func(t *testing.T) {
		b := newBridge()
		if err := b.Start(context.Background(), &api.StartOpts{SessionID: "existing-sess", Agent: "agent", Model: "model"}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer b.Stop()
		if id := b.SessionID(); id != "existing-sess" {
			t.Errorf("SessionID = %q, want existing-sess", id)
		}
	})

	t.Run("Call_returns_response", func(t *testing.T) {
		b := newBridge()
		if err := b.Start(context.Background(), &api.StartOpts{Agent: "agent", Model: "model"}); err != nil {
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
		if err := b.Start(context.Background(), &api.StartOpts{Agent: "agent", Model: "model"}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		defer b.Stop()
		if err := b.Notify(context.Background(), "session/update", nil); err != nil {
			t.Errorf("Notify: %v", err)
		}
	})

	t.Run("Stop_closes_NotifCh", func(t *testing.T) {
		b := newBridge()
		if err := b.Start(context.Background(), &api.StartOpts{Agent: "agent", Model: "model"}); err != nil {
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
		if err := b.Start(context.Background(), &api.StartOpts{Agent: "agent", Model: "model"}); err != nil {
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
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"lifecycle-001","modes":{"currentModeId":"agent","availableModes":[{"id":"agent","name":"Agent","description":"agent mode"},{"id":"code","name":"Code","description":"code mode"}]},"models":{"currentModelId":"sonnet","availableModels":[{"modelId":"sonnet","name":"Sonnet","description":"fast","rateMultiplier":1.0}]}}}\n' "$id"
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
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".kiro", "sessions", "cli"), 0o755); err != nil {
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
	if err := b.Start(context.Background(), &api.StartOpts{Agent: "a", Model: "m"}); err != nil {
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
		{name: "not-idle by message", code: -32099, message: "not idle", wantNotIdle: true},
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
					if !errors.Is(r.err, ErrNotIdle) {
						t.Errorf("err = %v, want ErrNotIdle", r.err)
					}
				} else {
					if errors.Is(r.err, ErrNotIdle) {
						t.Errorf("err = %v, should NOT be ErrNotIdle", r.err)
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

// --- tarch-b8-c7-p2: Fuzz normalizeMCPServers ---

// FuzzNormalizeMCPServers exercises normalizeMCPServers with arbitrary
// map slices asserting: output length == input length, output is never
// nil, no panic, JSON marshal never errors.
func FuzzNormalizeMCPServers(f *testing.F) {
	f.Add(0)
	f.Add(1)
	f.Add(5)
	f.Add(50)
	f.Fuzz(func(t *testing.T, n int) {
		if n < 0 || n > 200 {
			return
		}
		in := make([]map[string]any, n)
		for i := range in {
			in[i] = map[string]any{
				"name":    fmt.Sprintf("server-%d", i),
				"command": "npx",
				"nil_val": nil,
				"nested":  map[string]any{"k": i},
				"num":     float64(i),
			}
		}
		got := normalizeMCPServers(in)
		if got == nil {
			t.Fatal("normalizeMCPServers returned nil, want non-nil")
		}
		if len(got) != len(in) {
			t.Fatalf("len(got) = %d, want %d", len(got), len(in))
		}
		if _, err := json.Marshal(got); err != nil {
			t.Fatalf("json.Marshal(output) failed: %v", err)
		}
	})
}
