package hub

// Mutant-killing tests for unit vibekit-u2 (internal/hub).
//
// Targets surviving gremlins mutants in bridge_fs_staging.go,
// bridge_fs_write.go, bridge_manager.go, bridge_partial.go, byte_ring.go,
// chat_summary.go, checkpoint_http.go, hook_status.go, mcp_registry.go,
// replay_ring.go, and translate.go. Tests only; no production code is
// edited. Reuses the package's existing shared fakes/helpers (newTestHub,
// hubForFSTest, newHubWithConfigDir, writePartialFile, newCheckpointHub,
// shaOfContent, mustJSON, newFakeBridge, respondingBridge).

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/pending"
)

// --- helpers (prefixed to avoid collisions with sibling units) ---

// gk_vibekit_u2_safeBuf is a mutex-guarded byte buffer so the capturing
// slog handler is race-free under -race.
type gk_vibekit_u2_safeBuf struct {
	buf bytes.Buffer
	mu  sync.Mutex
}

func (b *gk_vibekit_u2_safeBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *gk_vibekit_u2_safeBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// gk_vibekit_u2_captureLogs swaps the default slog logger for one writing
// JSON into a buffer and restores it at test end. Tests using it must NOT
// call t.Parallel (global slog default).
func gk_vibekit_u2_captureLogs(t *testing.T) *gk_vibekit_u2_safeBuf {
	t.Helper()
	out := &gk_vibekit_u2_safeBuf{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(out, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return out
}

// gk_vibekit_u2_newBM builds a bare bridgeManager whose factory returns a
// fresh fakeBridge each call, tracking Stop goroutines on wg.
func gk_vibekit_u2_newBM(wg *sync.WaitGroup) *bridgeManager {
	return newBridgeManager(func() api.ACPBridge { return newFakeBridge() }, wg)
}

// --- bridge_fs_staging.go:176 — readStagedOld size guard ---

// Kills 176:18 (CONDITIONALS_BOUNDARY on `info.Size() > fsReadCap`). A
// file of exactly fsReadCap bytes must read successfully: the original
// uses strict `>` so size==cap passes the guard and returns the content.
// The `>=` mutant rejects an exactly-cap file with the cap error.
func TestGkVibekitU2_ReadStagedOld_ExactCapBoundary(t *testing.T) {
	dir := t.TempDir()

	exact := filepath.Join(dir, "gk_exact.bin")
	if err := os.WriteFile(exact, bytes.Repeat([]byte("a"), fsReadCap), 0o644); err != nil {
		t.Fatal(err)
	}
	oldText, kind, err := readStagedOld(exact)
	if err != nil {
		t.Fatalf("readStagedOld(exact cap) err = %v, want nil (boundary is strict >)", err)
	}
	if len(oldText) != fsReadCap {
		t.Errorf("readStagedOld(exact cap) len = %d, want %d", len(oldText), fsReadCap)
	}
	if kind != pending.KindEdit {
		t.Errorf("readStagedOld(exact cap) kind = %q, want %q", kind, pending.KindEdit)
	}

	// Sanity: one byte over the cap is rejected (both > and >= agree here).
	over := filepath.Join(dir, "gk_over.bin")
	if err := os.WriteFile(over, bytes.Repeat([]byte("b"), fsReadCap+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readStagedOld(over); err == nil {
		t.Errorf("readStagedOld(cap+1) err = nil, want a cap-exceeded error")
	}
}

// --- bridge_fs_write.go:137 — WriteFile error check ---

// Kills 137:60 (CONDITIONALS_NEGATION on `err != nil` after os.WriteFile).
// Both branches are exercised:
//   - write_failure: target is an existing directory so WriteFile fails.
//     The original (err!=nil) responds with an error; the mutant (err==nil)
//     falls through and reports success.
//   - write_success: a normal write succeeds. The original responds with
//     the success map result; the mutant routes through respondFSError and
//     responds with a nil result.
func TestGkVibekitU2_RespondFSWrite_ErrCheck(t *testing.T) {
	t.Run("write_failure_reports_error", func(t *testing.T) {
		work := t.TempDir()
		if err := os.Mkdir(filepath.Join(work, "gk_dir"), 0o755); err != nil {
			t.Fatal(err)
		}
		h, br := hubForFSTest(t, work)
		id := int64(8137)
		msg := &api.RPCResponse{
			ID:     &id,
			Method: api.MethodFSWrite,
			Params: mustJSON(t, map[string]any{"path": "gk_dir", "content": "x"}),
		}
		h.respondFSWrite(context.Background(), "c1", msg)
		select {
		case <-br.done:
		case <-time.After(3 * time.Second):
			t.Fatal("respondFSWrite did not respond")
		}
		br.respMu.Lock()
		gotErr := br.response.err
		br.respMu.Unlock()
		if gotErr == nil {
			t.Errorf("respondFSWrite(dir target) response.err = nil, want non-nil (mutant skips the error branch)")
		}
	})

	t.Run("write_success_reports_result_map", func(t *testing.T) {
		work := t.TempDir()
		h, br := hubForFSTest(t, work)
		id := int64(8138)
		msg := &api.RPCResponse{
			ID:     &id,
			Method: api.MethodFSWrite,
			Params: mustJSON(t, map[string]any{"path": "gk_ok.txt", "content": "hello"}),
		}
		h.respondFSWrite(context.Background(), "c1", msg)
		select {
		case <-br.done:
		case <-time.After(3 * time.Second):
			t.Fatal("respondFSWrite did not respond")
		}
		br.respMu.Lock()
		gotErr := br.response.err
		gotRes := br.response.result
		br.respMu.Unlock()
		if gotErr != nil {
			t.Fatalf("respondFSWrite(success) response.err = %v, want nil", gotErr)
		}
		if _, ok := gotRes.(map[string]any); !ok {
			t.Errorf("respondFSWrite(success) result = %T, want map[string]any (mutant responds nil via respondFSError)", gotRes)
		}
		data, rErr := os.ReadFile(filepath.Join(work, "gk_ok.txt"))
		if rErr != nil || string(data) != "hello" {
			t.Errorf("file content = %q, err=%v, want %q", string(data), rErr, "hello")
		}
	})
}

// --- bridge_manager.go:74 — removeIfSame ---

// Kills 74:46 (CONDITIONALS_NEGATION on `cur == sb`). removeIfSame must
// remove only when the stored bridge IS the given one. The original
// (`==`) removes on a match and skips on a mismatch; the mutant (`!=`)
// inverts both.
func TestGkVibekitU2_BridgeManager_RemoveIfSame(t *testing.T) {
	var wg sync.WaitGroup
	bm := gk_vibekit_u2_newBM(&wg)
	sb1, _ := bm.getOrInsert("c1")
	sb2, _ := bm.getOrInsert("c2")

	// Mismatch must NOT remove and must return false.
	if removed := bm.removeIfSame("c1", sb2); removed {
		t.Errorf("removeIfSame(c1, other) = true, want false")
	}
	if bm.get("c1") == nil {
		t.Errorf("removeIfSame(c1, other) wrongly removed c1")
	}

	// Match must remove and return true.
	if removed := bm.removeIfSame("c1", sb1); !removed {
		t.Errorf("removeIfSame(c1, c1) = false, want true")
	}
	if bm.get("c1") != nil {
		t.Errorf("removeIfSame(c1, c1) did not remove c1")
	}
}

// --- bridge_manager.go:86 — removeIfBridge ---

// Kills 86:51 (CONDITIONALS_NEGATION on `sb.bridge == bridge`).
// removeIfBridge must remove only when the stored bridge instance matches.
func TestGkVibekitU2_BridgeManager_RemoveIfBridge(t *testing.T) {
	var wg sync.WaitGroup
	bm := gk_vibekit_u2_newBM(&wg)
	sb, _ := bm.getOrInsert("c1")
	stored := sb.bridge
	other := newFakeBridge()

	// Mismatched bridge instance must NOT remove.
	if removed := bm.removeIfBridge("c1", other); removed {
		t.Errorf("removeIfBridge(c1, other) = true, want false")
	}
	if bm.get("c1") == nil {
		t.Errorf("removeIfBridge(c1, other) wrongly removed c1")
	}

	// Matching bridge instance must remove.
	if removed := bm.removeIfBridge("c1", stored); !removed {
		t.Errorf("removeIfBridge(c1, stored) = false, want true")
	}
	if bm.get("c1") != nil {
		t.Errorf("removeIfBridge(c1, stored) did not remove c1")
	}
}

// --- bridge_manager.go:116 — closeAndStop empty guard ---

// Kills 116:14 (CONDITIONALS_NEGATION on `len(ids) == 0`).
//   - non-empty: the original proceeds, removes the ids, and returns the
//     culled entries. The mutant (`!= 0`) returns nil immediately and
//     leaves the bridges registered.
//   - nil: the original returns nil; the mutant returns a non-nil empty
//     slice.
func TestGkVibekitU2_BridgeManager_CloseAndStop(t *testing.T) {
	var wg sync.WaitGroup
	bm := gk_vibekit_u2_newBM(&wg)
	bm.getOrInsert("c1")

	culled := bm.closeAndStop([]api.ChatID{"c1"})
	wg.Wait() // let the Stop goroutines finish before the test ends.
	if len(culled) != 1 {
		t.Fatalf("closeAndStop([c1]) returned %d entries, want 1 (mutant returns nil)", len(culled))
	}
	if culled[0].chatID != "c1" {
		t.Errorf("culled[0].chatID = %q, want %q", culled[0].chatID, "c1")
	}
	if bm.get("c1") != nil {
		t.Errorf("closeAndStop([c1]) did not remove c1 from the map")
	}

	// Empty input: original returns nil, mutant returns a non-nil empty slice.
	if got := bm.closeAndStop(nil); got != nil {
		t.Errorf("closeAndStop(nil) = %v (len %d), want nil", got, len(got))
	}
}

// --- bridge_partial.go:48,58,61 — RecoverPartials error branches ---

// Kills 48:65, 58:65 (CONDITIONALS_NEGATION on the two AppendMessage err
// checks) and 61:40 (CONDITIONALS_NEGATION on the os.Remove err check). On
// the success path (store appends succeed, file removes cleanly) the
// original logs none of the three errors; each mutant inverts its check
// and logs its error message on success.
func TestGkVibekitU2_RecoverPartials_NoErrorLogsOnSuccess(t *testing.T) {
	cfg := t.TempDir()
	h, cs := newHubWithConfigDir(t, cfg)
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	writePartialFile(t, cfg, "c1", buffer.PartialSnapshot{
		MessageID: "gk-m",
		Content:   "half-written content",
		Ts:        1_700_000_000_000,
	})

	logs := gk_vibekit_u2_captureLogs(t)
	h.RecoverPartials()

	got := logs.String()
	for _, bad := range []string{
		"partial recovery: append failed",            // 48 mutant signature
		"partial recovery: append interrupted",       // 58 mutant signature
		"partial recovery: remove and rename failed", // 61 mutant signature
	} {
		if strings.Contains(got, bad) {
			t.Errorf("unexpected error log on RecoverPartials success path: %q in %s", bad, got)
		}
	}

	// Sanity: recovery actually ran (so the success branches were taken).
	c, _ := cs.Get(context.Background(), "c1")
	if len(c.Messages) != 2 {
		t.Fatalf("recovered messages = %d, want 2 (assistant + interrupted)", len(c.Messages))
	}
	if _, err := os.Stat(filepath.Join(cfg, "chats", "c1.partial")); !os.IsNotExist(err) {
		t.Errorf("partial file still present after recovery: err=%v", err)
	}
}

// --- byte_ring.go: wrap behaviour (regression coverage) ---

// byteRing.Write loop + wrap behaviour. The line-40 boundary mutant and
// the line-50 arithmetic/invert mutants are equivalent (see report): the
// `copy` clamps to the destination length, so the value of `space` cannot
// change which bytes land in the buffer, and a write of exactly capacity
// leaves identical state in either branch. This test pins the real
// wrap-around output as regression coverage.
func TestGkVibekitU2_ByteRing_WrapBehaviour(t *testing.T) {
	r := newByteRing(4)
	r.Write([]byte("ab")) // partial fill, pos=2
	r.Write([]byte("cd")) // fills + wraps, pos=0, full
	r.Write([]byte("ef")) // overwrites oldest two
	if got := r.String(); got != "cdef" {
		t.Errorf("byteRing wrap String() = %q, want %q", got, "cdef")
	}
	if !r.Truncated() {
		t.Errorf("byteRing.Truncated() = false, want true after wrap")
	}
	if got := r.Len(); got != 4 {
		t.Errorf("byteRing.Len() = %d, want 4", got)
	}
}

// --- chat_summary.go:127 — buildSummaryPrompt message numbering ---

// Kills 127:48 (ARITHMETIC_BASE on `i+1`). With a single assistant message
// the original numbers it "message 1"; the `i-1` mutant numbers it
// "message -1".
func TestGkVibekitU2_BuildSummaryPrompt_MessageNumbering(t *testing.T) {
	c := &api.Chat{
		Name:     "Topic",
		Messages: []api.Message{{Role: api.RoleAssistant, Content: "did the thing"}},
	}
	got := buildSummaryPrompt(c)
	if !strings.Contains(got, "-- message 1 --") {
		t.Errorf("buildSummaryPrompt missing %q in:\n%s", "-- message 1 --", got)
	}
	if strings.Contains(got, "-- message -1 --") {
		t.Errorf("buildSummaryPrompt contains mutant signature %q in:\n%s", "-- message -1 --", got)
	}
}

// --- chat_summary.go:147,153 — postprocessSummary boundaries ---

// Kills 147:13 (CONDITIONALS_BOUNDARY on `len(s) >= 2`) and 153:31
// (CONDITIONALS_BOUNDARY on `RuneCount(s) > summaryMaxChars`).
//   - empty_quotes: a bare `""` (length 2) must be stripped to "". The
//     `> 2` mutant breaks the strip loop and returns `""` unchanged.
//   - exact_cap: a string of exactly summaryMaxChars runes must pass
//     through unchanged. The `>=` mutant truncates it to N-1 runes + "…".
func TestGkVibekitU2_PostprocessSummary_Boundaries(t *testing.T) {
	exact := strings.Repeat("a", summaryMaxChars)
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"empty_quotes_stripped", "\"\"", ""},
		{"exact_cap_unchanged", exact, exact},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := postprocessSummary(tc.raw); got != tc.want {
				t.Errorf("postprocessSummary(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// --- hook_status.go:56 — cache size-term invalidation ---

// Kills 56:61 (CONDITIONALS_NEGATION on `info.Size() == c.size`). The cache
// is primed with a stale value whose mtime matches disk but whose cached
// size differs from disk. The original treats the size mismatch as a cache
// MISS and re-reads the fresh (false) value; the mutant (`!=`) treats it as
// a HIT and returns the stale (true) cached value.
func TestGkVibekitU2_HookStatusCache_SizeTermInvalidates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"k":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	c := newCachedBoolField(path, "k", false)
	// Prime the cache to claim value=true with a matching mtime but a
	// DIFFERENT size, isolating the size term in the cache-hit check.
	c.value = true
	c.valid = true
	c.mtime = info.ModTime()
	c.size = info.Size() + 1

	if got := c.get(); got != false {
		t.Errorf("cachedBoolField.get() = %v, want false (mutant returns the stale cached true on a size mismatch)", got)
	}
}

// --- replay_ring.go:89 — Events() start-index arithmetic ---

// Kills both 89:27 (ARITHMETIC_BASE on the `+ r.cap`) and 89:36
// (ARITHMETIC_BASE on the `% r.cap`). After overflowing a cap-3 ring the
// oldest event is evicted; Events() must return the remaining three in
// oldest→newest order. The `-` mutant produces a negative index (panic);
// the `*` mutant reorders the slice.
func TestGkVibekitU2_ReplayRing_EventsWrappedOrder(t *testing.T) {
	r := newReplayRing(3)
	for _, id := range []uint64{1, 2, 3, 4} {
		r.Append(sseEvent{eventID: id})
	}
	got := r.Events()
	want := []uint64{2, 3, 4}
	if len(got) != len(want) {
		t.Fatalf("Events() len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].eventID != want[i] {
			t.Errorf("Events()[%d].eventID = %d, want %d (full order: %v)", i, got[i].eventID, want[i], gk_vibekit_u2_ids(got))
		}
	}
}

func gk_vibekit_u2_ids(evs []sseEvent) []uint64 {
	out := make([]uint64, len(evs))
	for i, e := range evs {
		out[i] = e.eventID
	}
	return out
}

// --- translate.go:106 — FS request routing guard ---

// Kills 106:12 (CONDITIONALS_NEGATION on `msg.ID != nil`). An fs/read
// request (ID != nil) must be routed to the FS handler, which responds
// back to the bridge. The mutant (`== nil`) short-circuits the &&, never
// dispatches the FS handler, and no response is produced.
func TestGkVibekitU2_TranslateACPEvent_RoutesFSRequest(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "gk_r.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	h, br := hubForFSTest(t, work)
	id := int64(7106)
	msg := &api.RPCResponse{
		ID:     &id,
		Method: api.MethodFSRead,
		Params: mustJSON(t, map[string]any{"path": "gk_r.txt"}),
	}
	h.translateACPEvent("c1", msg)
	select {
	case <-br.done:
	case <-time.After(3 * time.Second):
		t.Fatal("fs/read request was not routed to the FS handler (mutant skips FS dispatch)")
	}
}

// --- translate.go:110 — terminal request routing guard ---

// Kills 110:12 (CONDITIONALS_NEGATION on `msg.ID != nil`). A terminal/*
// request (ID != nil) must be routed to handleTerminalRequest, which
// responds back to the bridge. The mutant (`== nil`) short-circuits the &&
// and the request falls through unhandled with no response.
func TestGkVibekitU2_TranslateACPEvent_RoutesTerminalRequest(t *testing.T) {
	h, br := hubForFSTest(t, t.TempDir())
	// Pre-register a terminal owned by "c1" so termOutput resolves it and
	// responds through the registered respondingBridge.
	h.agentTerms.mu.Lock()
	h.agentTerms.terms["gk-term"] = &agentTerminal{
		done:   make(chan struct{}),
		output: newByteRing(64),
		chatID: "c1",
	}
	h.agentTerms.mu.Unlock()

	id := int64(7110)
	msg := &api.RPCResponse{
		ID:     &id,
		Method: methodTermOutput,
		Params: mustJSON(t, map[string]any{"terminalId": "gk-term"}),
	}
	h.translateACPEvent("c1", msg)
	select {
	case <-br.done:
	case <-time.After(3 * time.Second):
		t.Fatal("terminal/output request was not routed to the terminal handler (mutant skips terminal dispatch)")
	}
}

// --- checkpoint_http.go:195 — blob write error log ---

// Kills 195:34 (CONDITIONALS_NEGATION on `err != nil` after w.Write). A
// successful blob write (httptest recorder never errors) must NOT log a
// write failure. The mutant (`== nil`) logs "checkpoint: blob write
// failed" on every success.
func TestGkVibekitU2_CheckpointBlob_NoWriteErrorLogOnSuccess(t *testing.T) {
	h, s, _ := newCheckpointHub(t)
	ctx := context.Background()
	s.AdvanceTurn(ctx, "c1", 0)
	if _, err := s.Snapshot(ctx, "c1", "gk_f.txt", []byte("blobdata"), 1); err != nil {
		t.Fatal(err)
	}
	sha := shaOfContent([]byte("blobdata"))

	logs := gk_vibekit_u2_captureLogs(t)
	req := httptest.NewRequest(http.MethodGet, "/api/checkpoints/c1/blob/"+sha, nil)
	rec := httptest.NewRecorder()
	h.handleCheckpoint(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("blob status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "blobdata" {
		t.Fatalf("blob body = %q, want %q (success path not exercised)", rec.Body.String(), "blobdata")
	}
	if got := logs.String(); strings.Contains(got, "blob write failed") {
		t.Errorf("unexpected blob-write error log on success: %s", got)
	}
}
