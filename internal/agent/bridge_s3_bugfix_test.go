package agent

// Regression tests for the S3 (fs + terminal + auth) A→C handler bugs.
//
// These drive the REAL dispatch entry point (h.translateACPEvent), which
// sets up a per-event context and cancels it via defer the instant it
// returns. The pre-existing fs tests call the handlers directly with
// context.Background() and use a Respond that ignores its context, so
// they cannot observe the per-event-cancellation class of bug (C1/M2).
// The bridges here mimic the real Bridge.Respond, which DROPS the write
// when its context is already cancelled (internal/bridge/bridge_rpc.go).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// --- Shared harness ---

// hubWithBridge wires a runtime whose chat "c1" bridge is br, mirroring
// hubForFSTest but generic over the bridge implementation so a test can
// supply a context-aware or recording bridge.
func hubWithBridge(t *testing.T, workDir string, br ACPBridge) *Runtime {
	t.Helper()
	cs := newFakeChatStore()
	factory := func() ACPBridge { return br }
	h := New(t.Context(), workDir, factory, cs)
	cs.Bus = h
	if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	sb, err := h.coord.OpenBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("getOrCreateBridge: %v", err)
	}
	sb.bridge = br
	h.bridge.mgr.mu.Lock()
	h.bridge.mgr.bridges["c1"] = sb
	h.bridge.mgr.mu.Unlock()
	return h
}

// --- ctxAwareBridge: reproduces the real Respond ctx-drop (C1/M2) ---

// ctxAwareBridge mimics the real Bridge.Respond, which returns early
// (dropping the write) when the passed context is already cancelled. A
// gate lets the test hold Respond until AFTER translateACPEvent has
// returned (and fired its defer cancel()), making the ctx state at the
// point of the check deterministic: cancelled before the fix, fresh
// after it.
type ctxAwareBridge struct {
	*fakeBridge
	gate      chan struct{}
	done      chan struct{}
	result    any
	rpcErr    error
	ctxErr    error
	mu        sync.Mutex
	delivered bool
}

func newCtxAwareBridge() *ctxAwareBridge {
	return &ctxAwareBridge{
		fakeBridge: newFakeBridge(),
		gate:       make(chan struct{}),
		done:       make(chan struct{}, 1),
	}
}

func (b *ctxAwareBridge) Respond(ctx context.Context, _ int64, result any, err error) error {
	<-b.gate // released by the test once the per-event ctx has been cancelled
	b.mu.Lock()
	defer b.mu.Unlock()
	defer func() {
		select {
		case b.done <- struct{}{}:
		default:
		}
	}()
	if cErr := ctx.Err(); cErr != nil {
		b.ctxErr = cErr // the real bridge drops the write here
		return cErr
	}
	b.delivered = true
	b.result = result
	b.rpcErr = err
	return nil
}

// C1: fs/read_text_file must respond with the file content even though
// translateACPEvent's per-event ctx is cancelled before the async
// handler runs its Respond. Fails before the C1 fix (response dropped),
// passes after.
func TestTranslateACPEvent_FSReadRespondsAfterEventCtxCancel_C1(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "c1.txt"), []byte("C1-content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	br := newCtxAwareBridge()
	h := hubWithBridge(t, work, br)
	id := int64(1)
	msg := &vibekit.RPCResponse{
		ID:     &id,
		Method: vibekit.MethodFSRead,
		Params: mustJSON(t, map[string]any{"path": "c1.txt"}),
	}

	h.translateACPEvent("c1", msg) // returns; defer cancel() has now fired
	close(br.gate)                 // let Respond observe the (post-cancel) ctx

	select {
	case <-br.done:
	case <-time.After(3 * time.Second):
		t.Fatal("fs read never responded")
	}
	br.mu.Lock()
	defer br.mu.Unlock()
	if !br.delivered {
		t.Fatalf("C1: fs read response was DROPPED (ctxErr=%v) — the async handler used the cancelled per-event ctx", br.ctxErr)
	}
	res, ok := br.result.(map[string]any)
	if !ok || res["content"] != "C1-content\n" {
		t.Errorf("unexpected fs read result: %+v (err=%v)", br.result, br.rpcErr)
	}
}

// C1: fs/write_text_file must respond (and persist) after the per-event
// ctx is cancelled. Fails before the C1 fix, passes after.
func TestTranslateACPEvent_FSWriteRespondsAfterEventCtxCancel_C1(t *testing.T) {
	work := t.TempDir()
	br := newCtxAwareBridge()
	h := hubWithBridge(t, work, br)
	id := int64(2)
	msg := &vibekit.RPCResponse{
		ID:     &id,
		Method: vibekit.MethodFSWrite,
		Params: mustJSON(t, map[string]any{"path": "c1-out.txt", "content": "C1-written"}),
	}

	h.translateACPEvent("c1", msg)
	close(br.gate)

	select {
	case <-br.done:
	case <-time.After(3 * time.Second):
		t.Fatal("fs write never responded")
	}
	br.mu.Lock()
	delivered := br.delivered
	ctxErr := br.ctxErr
	br.mu.Unlock()
	if !delivered {
		t.Fatalf("C1: fs write response was DROPPED (ctxErr=%v) — async handler used the cancelled per-event ctx", ctxErr)
	}
	data, err := os.ReadFile(filepath.Join(work, "c1-out.txt"))
	if err != nil || string(data) != "C1-written" {
		t.Errorf("fs write did not persist: %q err=%v", string(data), err)
	}
}

// --- recordingTermBridge: captures every Respond (ctx-agnostic) ---

type recordedResp struct {
	result any
	err    error
	id     int64
}

type recordingTermBridge struct {
	*fakeBridge
	respCh chan struct{}
	resps  []recordedResp
	mu     sync.Mutex
}

func newRecordingTermBridge() *recordingTermBridge {
	return &recordingTermBridge{fakeBridge: newFakeBridge(), respCh: make(chan struct{}, 32)}
}

func (b *recordingTermBridge) Respond(_ context.Context, id int64, result any, err error) error {
	b.mu.Lock()
	b.resps = append(b.resps, recordedResp{id: id, result: result, err: err})
	b.mu.Unlock()
	select {
	case b.respCh <- struct{}{}:
	default:
	}
	return nil
}

func (b *recordingTermBridge) lastResponse() (recordedResp, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.resps) == 0 {
		return recordedResp{}, false
	}
	return b.resps[len(b.resps)-1], true
}

func termCreateMsg(t *testing.T, id int64, command string, args []string, env []map[string]string) *vibekit.RPCResponse {
	t.Helper()
	params := map[string]any{"command": command}
	if len(args) > 0 {
		params["args"] = args
	}
	if env != nil {
		params["env"] = env
	}
	return &vibekit.RPCResponse{ID: &id, Method: methodTermCreate, Params: mustJSON(t, params)}
}

// singleTerm returns the sole registered agent terminal.
func singleTerm(t *testing.T, h *Runtime) *agentTerminal {
	t.Helper()
	h.agentTerms.mu.Lock()
	defer h.agentTerms.mu.Unlock()
	for _, tm := range h.agentTerms.terms {
		return tm
	}
	t.Fatal("no agent terminal registered")
	return nil
}

func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not complete within 5s", what)
	}
}

// C2: a command spawned by terminal/create must survive translateACPEvent
// returning. Before the fix the command context is a child of the
// per-event ctx, so it is SIGTERM'd the instant translateACPEvent returns
// (mid-sleep) — a signal death, and the marker file is never written.
// After the fix it runs to completion (exit 0, marker written).
func TestTranslateACPEvent_TerminalSurvivesEventCtxCancel_C2(t *testing.T) {
	work := t.TempDir()
	br := newRecordingTermBridge()
	h := hubWithBridge(t, work, br)
	msg := termCreateMsg(t, 1, "sh", []string{"-c", "sleep 1; printf ok > c2.txt"}, nil)

	h.translateACPEvent("c1", msg) // per-event ctx cancels on return

	term := singleTerm(t, h)
	waitClosed(t, term.done, "terminal")

	term.mu.Lock()
	sig := term.signal
	code := term.exitCode
	term.mu.Unlock()
	if sig != "" {
		t.Fatalf("C2: command was signal-killed (%q) right after spawn — the command ctx was tied to the per-event ctx", sig)
	}
	if code != 0 {
		t.Fatalf("C2: command exit code = %d, want 0 (clean run)", code)
	}
	if data, err := os.ReadFile(filepath.Join(work, "c2.txt")); err != nil || string(data) != "ok" {
		t.Errorf("C2: marker file = %q err=%v; command did not run to completion", string(data), err)
	}
}

// H1: a signal-killed terminal must report the signal (not exitCode:-1),
// since KAS's zTerminalExitStatus requires exitCode>=0. The exit-status
// object carries `signal` and omits `exitCode`.
func TestTerminalSignalDeath_ReportsSignalNotExitCodeMinus1_H1(t *testing.T) {
	work := t.TempDir()
	br := newRecordingTermBridge()
	h := hubWithBridge(t, work, br)
	msg := termCreateMsg(t, 1, "sh", []string{"-c", "sleep 30"}, nil)

	h.translateACPEvent("c1", msg)
	term := singleTerm(t, h)
	if term.cmd.Process == nil {
		t.Fatal("process not started")
	}
	_ = term.cmd.Process.Kill() // signal death

	waitClosed(t, term.done, "terminal")

	obj := term.exitStatusObject()
	if _, hasExit := obj[keyExitCode]; hasExit {
		t.Errorf("H1: signal death still reports exitCode %+v (KAS rejects a negative exitCode)", obj)
	}
	if sig, ok := obj[keySignal].(string); !ok || sig == "" {
		t.Errorf("H1: signal death missing signal string: %+v", obj)
	}
	term.mu.Lock()
	code := term.exitCode
	term.mu.Unlock()
	if code < 0 {
		t.Errorf("H1: term.exitCode = %d, must never be negative", code)
	}

	// The terminal_exited SSE mirrors the same rule: Signal set, ExitCode nil.
	code2, signal := exitStatusFromState(term.cmd.ProcessState)
	if signal == "" {
		t.Errorf("H1: exitStatusFromState returned no signal for a killed process")
	}
	if code2 < 0 {
		t.Errorf("H1: exitStatusFromState exitCode = %d, must be >=0", code2)
	}
}

// H2: terminal/output for an unknown terminalId must respond with an
// error, not silently drop it. Before the fix the not-found path used an
// empty chatID, so respondErr's bridge lookup missed and the agent's Call
// hung. Driven through translateACPEvent so the real chatID is threaded.
func TestTranslateACPEvent_TermOutputUnknownID_RespondsError_H2(t *testing.T) {
	work := t.TempDir()
	br := newRecordingTermBridge()
	h := hubWithBridge(t, work, br)
	id := int64(1)
	msg := &vibekit.RPCResponse{
		ID:     &id,
		Method: methodTermOutput,
		Params: mustJSON(t, map[string]any{"terminalId": "does-not-exist"}),
	}

	h.translateACPEvent("c1", msg) // output is synchronous

	select {
	case <-br.respCh:
	case <-time.After(2 * time.Second):
		t.Fatal("H2: no Respond for unknown terminalId — error was dropped (empty chatID missed the bridge lookup)")
	}
	last, ok := br.lastResponse()
	if !ok || last.err == nil {
		t.Errorf("H2: expected an error response for unknown terminalId, got result=%+v err=%v", last.result, last.err)
	}
}

// M1: terminal/create must populate cmd.Env from the ACP `env` array so
// env-dependent agent commands see the requested variables.
func TestTerminalEnv_PopulatesCommandEnv_M1(t *testing.T) {
	work := t.TempDir()
	br := newRecordingTermBridge()
	h := hubWithBridge(t, work, br)
	const sentinel = "vibekit-env-sentinel-42"
	env := []map[string]string{{"name": "VIBEKIT_TEST_ENV", "value": sentinel}}
	msg := termCreateMsg(t, 1, "sh", []string{"-c", `printf '%s' "$VIBEKIT_TEST_ENV" > envout.txt`}, env)

	h.translateACPEvent("c1", msg)
	term := singleTerm(t, h)
	waitClosed(t, term.done, "terminal")

	data, err := os.ReadFile(filepath.Join(work, "envout.txt"))
	if err != nil {
		t.Fatalf("M1: env output file missing: %v", err)
	}
	if string(data) != sentinel {
		t.Errorf("M1: command saw env %q, want %q — env not populated into cmd.Env", string(data), sentinel)
	}
}

// TestTermCreate_RefusesExecutionRedirectingEnv is the WIRING test for the
// environment guard, and it exists because the unit tests beside
// screenAgentEnv cannot fail if the call is deleted from create.
//
// Two assertions, and the second is the one that matters: the request is answered
// with an error, and NO terminal is created — so nothing was spawned before the
// refusal. It drives the real handler through translateACPEvent rather than
// calling screenAgentEnv, which is the whole point.
//
// LD_PRELOAD with a real sentinel path: had this been allowed, cmd.Env would carry
// it last and the loader would win over the process environment.
func TestTermCreate_RefusesExecutionRedirectingEnv(t *testing.T) {
	work := t.TempDir()
	br := newRecordingTermBridge()
	h := hubWithBridge(t, work, br)
	env := []map[string]string{{"name": "LD_PRELOAD", "value": "/tmp/evil.so"}}
	msg := termCreateMsg(t, 1, "sh", []string{"-c", "true"}, env)

	h.translateACPEvent("c1", msg)

	h.agentTerms.mu.Lock()
	live := len(h.agentTerms.terms)
	h.agentTerms.mu.Unlock()
	if live != 0 {
		t.Errorf("a terminal was created despite a refused environment (%d live); "+
			"the screen must run before anything is spawned", live)
	}
	resp, ok := br.lastResponse()
	if !ok {
		t.Fatal("terminal/create went unanswered; the agent would block on a request nothing will answer")
	}
	if resp.err == nil {
		t.Fatalf("terminal/create was answered with a success (%v), want an error naming the variable", resp.result)
	}
	if !strings.Contains(resp.err.Error(), "LD_PRELOAD") {
		t.Errorf("refusal %q does not name LD_PRELOAD; the agent cannot tell what to drop", resp.err)
	}
}

// TestTermCreate_AllowsInertPagerEnv is the other half: the guard must not refuse
// the shape an agent actually uses. `GIT_PAGER=cat` is how anything
// non-interactive stops git paging, and a guard that blocks it gets switched off.
func TestTermCreate_AllowsInertPagerEnv(t *testing.T) {
	work := t.TempDir()
	br := newRecordingTermBridge()
	h := hubWithBridge(t, work, br)
	env := []map[string]string{{"name": "GIT_PAGER", "value": "cat"}}
	msg := termCreateMsg(t, 1, "sh", []string{"-c", "true"}, env)

	h.translateACPEvent("c1", msg)

	term := singleTerm(t, h)
	waitClosed(t, term.done, "terminal")
}

// termEnv layers requested vars on top of os.Environ and returns nil when
// none are requested (so cmd.Env stays nil and inherits the environment).
func TestTermEnv(t *testing.T) {
	t.Parallel()
	if termEnv(nil) != nil {
		t.Error("termEnv(nil) = non-nil, want nil (inherit os.Environ)")
	}
	if termEnv([]termEnvVar{}) != nil {
		t.Error("termEnv(empty) = non-nil, want nil")
	}
	got := termEnv([]termEnvVar{{Name: "VIBEKIT_A", Value: "1"}})
	if len(got) <= len(os.Environ()) {
		t.Errorf("termEnv should layer on os.Environ (len=%d, environ=%d)", len(got), len(os.Environ()))
	}
	found := false
	for _, e := range got {
		if e == "VIBEKIT_A=1" {
			found = true
		}
	}
	if !found {
		t.Error("termEnv missing VIBEKIT_A=1")
	}
}
