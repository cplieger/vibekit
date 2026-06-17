package bridge

// Mutation-killing tests for unit vibekit-u8 (package internal/bridge).
// Targets surviving gremlins mutants in bridge.go, bridge_parse_err.go,
// and bridge_process.go. Tests only; no production code is modified.

import (
	"context"
	"encoding/json"
	"log/slog"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// gk_vibekit_u8_logCapture is a slog.Handler that records emitted record
// messages so a test can assert whether a particular log line was (or was
// not) produced by the code under test.
type gk_vibekit_u8_logCapture struct {
	msgs []string
	mu   sync.Mutex
}

func (h *gk_vibekit_u8_logCapture) Enabled(context.Context, slog.Level) bool { return true }

func (h *gk_vibekit_u8_logCapture) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.msgs = append(h.msgs, r.Message)
	return nil
}

func (h *gk_vibekit_u8_logCapture) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *gk_vibekit_u8_logCapture) WithGroup(string) slog.Handler      { return h }

func (h *gk_vibekit_u8_logCapture) gk_vibekit_u8_has(msg string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Contains(h.msgs, msg)
}

// gk_vibekit_u8_installCapture redirects slog to a capturing handler for
// the duration of the test, restoring the previous default afterwards.
func gk_vibekit_u8_installCapture(t *testing.T) *gk_vibekit_u8_logCapture {
	t.Helper()
	c := &gk_vibekit_u8_logCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(c))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return c
}

// --- bridge.go: capturePromptCapabilities / SupportsDocuments ---
// Kills 209:10 (resp == nil), 209:37 (len(resp.Result) == 0),
// 217:52 (err != nil). Negating any of these makes the function return
// early and never store the capabilities, so SupportsDocuments() reads
// false instead of true.

func Test_gk_vibekit_u8_capturePromptCapabilities_storesEmbeddedContext(t *testing.T) {
	b := New("cli", "work")
	if b.SupportsDocuments() {
		t.Fatalf("SupportsDocuments() = true before capture, want false")
	}
	resp := &api.RPCResponse{
		Result: json.RawMessage(`{"agentCapabilities":{"promptCapabilities":{"embeddedContext":true}}}`),
	}
	b.capturePromptCapabilities(resp)
	if !b.SupportsDocuments() {
		t.Errorf("SupportsDocuments() after capturing embeddedContext:true = false, want true")
	}
}

func Test_gk_vibekit_u8_capturePromptCapabilities_leavesUnset(t *testing.T) {
	cases := []struct {
		resp *api.RPCResponse
		name string
	}{
		{name: "nil_response", resp: nil},
		{name: "empty_result", resp: &api.RPCResponse{Result: nil}},
		{name: "malformed_json", resp: &api.RPCResponse{Result: json.RawMessage(`{not valid`)}},
		{
			name: "embedded_context_false",
			resp: &api.RPCResponse{Result: json.RawMessage(`{"agentCapabilities":{"promptCapabilities":{"embeddedContext":false}}}`)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := New("cli", "work")
			b.capturePromptCapabilities(tc.resp)
			if b.SupportsDocuments() {
				t.Errorf("SupportsDocuments() = true for %s, want false", tc.name)
			}
		})
	}
}

// --- bridge_parse_err.go: parseErrTracker.Record ---
// Kills 62:14 (t.total == parseErrBurst). The burst-th call sets
// windowStart; the next call then suppresses because time.Since(windowStart)
// is small. If the equality is negated, windowStart stays zero and the next
// call summarizes instead.

func Test_gk_vibekit_u8_parseErrTracker_windowStartSetAtBurst(t *testing.T) {
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

// Kills 79:36 (b.cmd.Process != nil). With an unstarted command (nil
// Process), the original guard skips the kill block; a negated guard
// dereferences the nil Process and panics.
func Test_gk_vibekit_u8_Stop_skipsKillWhenProcessNil(t *testing.T) {
	b := New("cli", "work")
	b.cmd = exec.Command("sleep", "30") // never Start()ed -> Process is nil
	b.Stop()                            // must return without panicking
	if b.cmd == nil {
		t.Fatal("b.cmd unexpectedly nil after Stop")
	}
}

// Kills 83:40 (err != nil guard around the kill-error log). Killing a live
// process returns nil, so the original guard suppresses the log. A negated
// guard logs "kill kiro-cli" with a nil error.
func Test_gk_vibekit_u8_Stop_noKillErrorLogOnLiveProcess(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep binary not available")
	}
	c := gk_vibekit_u8_installCapture(t)

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

	if c.gk_vibekit_u8_has("kill kiro-cli") {
		t.Errorf(`Stop emitted "kill kiro-cli" error log on a successful Kill, want none`)
	}
}

// --- bridge_process.go: startProcess ---

// Kills 132:9 (ctx == nil). lifecycleCtx is nil right after New, so the
// guard substitutes context.Background(); a negated guard leaves ctx nil
// and exec.CommandContext(nil, ...) panics.
func Test_gk_vibekit_u8_startProcess_nilLifecycleCtxFallsBackToBackground(t *testing.T) {
	bogus := filepath.Join(t.TempDir(), "no-such-kiro-cli")
	b := New(bogus, t.TempDir())
	t.Cleanup(b.Stop)
	err := b.startProcess("", "", "", nil)
	if err == nil {
		t.Fatal("startProcess with a nonexistent binary returned nil error, want a start failure")
	}
}

// Kills 144:22 (5 * time.Second). startProcess fails at Start (missing
// binary) but assigns b.cmd.WaitDelay first. Replacing * with / yields 0
// instead of 5s.
func Test_gk_vibekit_u8_startProcess_setsWaitDelayToFiveSeconds(t *testing.T) {
	bogus := filepath.Join(t.TempDir(), "no-such-kiro-cli")
	b := New(bogus, t.TempDir())
	b.lifecycleCtx = context.Background()
	t.Cleanup(b.Stop)
	_ = b.startProcess("", "", "", nil)
	if b.cmd == nil {
		t.Fatal("b.cmd is nil; startProcess did not reach CommandContext")
	}
	if b.cmd.WaitDelay != 5*time.Second {
		t.Errorf("b.cmd.WaitDelay = %v, want %v", b.cmd.WaitDelay, 5*time.Second)
	}
}

// --- bridge_process.go: classifyStderrLevel ---
// Kills 234:27 (line[0] == '{'), 238:48 (json.Unmarshal(...) == nil),
// 238:75 (structured.Level != ""). For a structured JSON line each of
// these negations diverts control to the keyword path, which does not
// match the quoted level value, returning LevelInfo instead of the mapped
// JSON level.

func Test_gk_vibekit_u8_classifyStderrLevel(t *testing.T) {
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
// Kills 263:10 (pos < 0), the boundary-before checks on 268, the
// boundary-after check on 274, and the separator check on 278.

func Test_gk_vibekit_u8_matchesKeyword(t *testing.T) {
	const kw = "error"
	cases := []struct {
		name string
		low  string
		want bool
	}{
		// 263:10 (pos < 0): keyword at index 0; mutated `pos <= 0` returns
		// false instead of matching.
		{"keyword_at_start_then_separator", "error:", true},
		// 268:28 (>= 'a' boundary and negation) and 268:49 (<= 'z'
		// negation): 'a' immediately before keyword -> treated as a
		// substring (skipped) -> false.
		{"preceded_by_letter_a_is_substring", "aerror", false},
		// 268:49 (<= 'z' boundary): 'z' before keyword -> substring -> false.
		{"preceded_by_letter_z_is_substring", "zerror", false},
		// 268:24 (- in low[pos-1] of >= 'a'): '.' before keyword (index 1)
		// -> not a letter -> real boundary -> true. Mutated `pos+1` reads a
		// letter -> skipped -> false.
		{"preceded_by_dot_is_boundary", "..error", true},
		// 268:45 (- in low[pos-1] of <= 'z'): '{' before keyword -> > 'z'
		// -> real boundary -> true. Mutated `pos+1` reads a letter -> false.
		{"preceded_by_brace_is_boundary", "a{error", true},
		// 274:10 (end >= len(low) negation): trailing non-separator char ->
		// no match -> false. Mutated `end < len` returns true.
		{"trailing_letter_no_match", ".errorx", false},
		// 274:10 (end >= len(low) boundary): keyword exactly at end ->
		// true. Mutated `end > len` reads past the string.
		{"keyword_at_end_of_string", ".error", true},
		// 278:9 (ch == ':'): trailing ':' is a boundary separator -> true.
		// Mutated `ch != ':'` falls through and returns false.
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
