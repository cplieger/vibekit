package chat

// Unit vibekit-u22: kills surviving gremlins mutants in package chat by
// adding tests only. Each test documents the targeted file:line and the
// mutation it kills. All new identifiers are prefixed gk_vibekit_u22_ to
// avoid collisions with sibling units that may share this package.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// --- shared helpers ---

func gk_vibekit_u22_newStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

// gk_vibekit_u22_createChat creates an (empty) chat file via the real
// Mutate create path so existence checks pass.
func gk_vibekit_u22_createChat(t *testing.T, s *Store, id api.ChatID) {
	t.Helper()
	if err := s.Mutate(context.Background(), id, func(_ *api.Chat, _ bool) bool { return true }); err != nil {
		t.Fatalf("create chat %q: %v", id, err)
	}
}

// gk_vibekit_u22_truncFile creates a (sparse) file of exactly size bytes.
func gk_vibekit_u22_truncFile(t *testing.T, path string, size int64) {
	t.Helper()
	f, err := os.Create(path) //nolint:gosec // test-local path under t.TempDir()
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	if err := f.Truncate(size); err != nil {
		_ = f.Close()
		t.Fatalf("truncate %s to %d: %v", path, size, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}

// --- slog capture (for log-only mutants) ---

type gk_vibekit_u22_logRecord struct {
	msg   string
	attrs map[string]slog.Value
}

type gk_vibekit_u22_capHandler struct {
	mu   *sync.Mutex
	recs *[]gk_vibekit_u22_logRecord
}

func (h gk_vibekit_u22_capHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h gk_vibekit_u22_capHandler) Handle(_ context.Context, r slog.Record) error {
	rec := gk_vibekit_u22_logRecord{msg: r.Message, attrs: make(map[string]slog.Value)}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = a.Value
		return true
	})
	h.mu.Lock()
	*h.recs = append(*h.recs, rec)
	h.mu.Unlock()
	return nil
}

func (h gk_vibekit_u22_capHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h gk_vibekit_u22_capHandler) WithGroup(string) slog.Handler      { return h }

// gk_vibekit_u22_captureLogs redirects slog.Default for the test and
// returns a snapshot accessor. Not parallel-safe; callers must not call
// t.Parallel().
func gk_vibekit_u22_captureLogs(t *testing.T) func() []gk_vibekit_u22_logRecord {
	t.Helper()
	var mu sync.Mutex
	recs := &[]gk_vibekit_u22_logRecord{}
	prev := slog.Default()
	slog.SetDefault(slog.New(gk_vibekit_u22_capHandler{mu: &mu, recs: recs}))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return func() []gk_vibekit_u22_logRecord {
		mu.Lock()
		defer mu.Unlock()
		out := make([]gk_vibekit_u22_logRecord, len(*recs))
		copy(out, *recs)
		return out
	}
}

func gk_vibekit_u22_hasLogMsg(recs []gk_vibekit_u22_logRecord, msg string) bool {
	for _, r := range recs {
		if r.msg == msg {
			return true
		}
	}
	return false
}

func gk_vibekit_u22_findLog(recs []gk_vibekit_u22_logRecord, msg string) (gk_vibekit_u22_logRecord, bool) {
	for _, r := range recs {
		if r.msg == msg {
			return r, true
		}
	}
	return gk_vibekit_u22_logRecord{}, false
}

// --- errors.go:49 CONDITIONALS_NEGATION (`e.Detail != ""` in ErrKindTooLarge) ---
// Detail non-empty must include the detail; empty must omit it. The
// inverted `==` swaps the two outputs.

func Test_gk_vibekit_u22_StoreError_TooLargeDetailBranch(t *testing.T) {
	withDetail := (&StoreError{Kind: ErrKindTooLarge, Detail: "5 bytes"}).Error()
	if withDetail != "plan draft too large: 5 bytes" {
		t.Errorf("StoreError.Error() with detail = %q, want %q",
			withDetail, "plan draft too large: 5 bytes")
	}
	noDetail := (&StoreError{Kind: ErrKindTooLarge}).Error()
	if noDetail != "plan draft too large" {
		t.Errorf("StoreError.Error() no detail = %q, want %q",
			noDetail, "plan draft too large")
	}
}

// --- io.go:37 (`size > max`), io.go:41 (`max+1`), io.go:45 (`len > max`) ---
// One file of exactly maxChatFileBytes distinguishes all three: the
// original returns the full data with no error; `>`->`>=` (37 or 45)
// turns it into an error, and `+1`->`-1` (41) drops the last byte.

func Test_gk_vibekit_u22_readCappedFile_exactMaxBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exactmax.json")
	gk_vibekit_u22_truncFile(t, path, maxChatFileBytes)

	data, err := readCappedFile(path, "chat exactmax")
	if err != nil {
		t.Fatalf("readCappedFile(exactly maxChatFileBytes) error = %v, want nil", err)
	}
	if int64(len(data)) != maxChatFileBytes {
		t.Errorf("readCappedFile len = %d, want %d", len(data), int64(maxChatFileBytes))
	}
}

// --- plan_draft.go:49 (`size > max`), plan_draft.go:53 (`max+1`) in GetPlanDraft ---
// A draft of exactly maxPlanDraftBytes must read back in full with no
// error; `>`->`>=` errors, `+1`->`-1` returns one byte short.

func Test_gk_vibekit_u22_GetPlanDraft_exactMaxBoundary(t *testing.T) {
	s := gk_vibekit_u22_newStore(t)
	id := api.ChatID("gku22getmax")
	draftPath := filepath.Join(s.Dir(), string(id)+planDraftSuffix)
	gk_vibekit_u22_truncFile(t, draftPath, maxPlanDraftBytes)

	got, err := s.GetPlanDraft(context.Background(), id)
	if err != nil {
		t.Fatalf("GetPlanDraft(exactly maxPlanDraftBytes) error = %v, want nil", err)
	}
	if len(got) != maxPlanDraftBytes {
		t.Errorf("GetPlanDraft len = %d, want %d", len(got), maxPlanDraftBytes)
	}
}

// --- plan_draft.go:72 (`len(content) > max`) in SetPlanDraft ---
// content of exactly maxPlanDraftBytes must be ACCEPTED; `>`->`>=` rejects
// it as ErrKindTooLarge.

func Test_gk_vibekit_u22_SetPlanDraft_exactMaxAccepted(t *testing.T) {
	s := gk_vibekit_u22_newStore(t)
	id := api.ChatID("gku22setmax")
	gk_vibekit_u22_createChat(t, s, id)

	content := strings.Repeat("a", maxPlanDraftBytes)
	if err := s.SetPlanDraft(context.Background(), id, content); err != nil {
		t.Fatalf("SetPlanDraft(len==maxPlanDraftBytes) error = %v, want nil (boundary must be accepted)", err)
	}
}

// --- plan_draft.go:108 (`err != nil` in DeletePlanDraft) ---
// A non-ErrNotExist os.Remove error must propagate. A non-empty directory
// at the draft path makes os.Remove fail with ENOTEMPTY; `!=`->`==` would
// swallow it and return nil.

func Test_gk_vibekit_u22_DeletePlanDraft_propagatesRemoveError(t *testing.T) {
	s := gk_vibekit_u22_newStore(t)
	id := api.ChatID("gku22del")
	draftPath := filepath.Join(s.Dir(), string(id)+planDraftSuffix)
	if err := os.Mkdir(draftPath, 0o755); err != nil {
		t.Fatalf("mkdir draft-as-dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(draftPath, "child"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write child: %v", err)
	}

	if err := s.DeletePlanDraft(context.Background(), id); err == nil {
		t.Fatal("DeletePlanDraft returned nil; want non-nil for a non-empty-dir remove error")
	}
}

// --- router_handlers.go:72 (`n <= 500` limit boundary) in handleOne ---
// limit=500 must be honored; `<=`->`<` falls back to the default 50. A
// 60-message chat returns all 60 with limit=500, only 50 with the default.

func Test_gk_vibekit_u22_handleOne_limit500Boundary(t *testing.T) {
	s := gk_vibekit_u22_newStore(t)
	id := api.ChatID("gku22limit")
	if err := s.Mutate(context.Background(), id, func(c *api.Chat, _ bool) bool {
		for i := 0; i < 60; i++ {
			c.Messages = append(c.Messages, api.Message{
				ID:      fmt.Sprintf("m%d", i),
				Role:    api.RoleUser,
				Content: "x",
				Ts:      int64(i + 1),
			})
		}
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	rt := NewRouter(s)
	req := httptest.NewRequest(http.MethodGet, "/api/chats/"+string(id)+"?limit=500", nil)
	w := httptest.NewRecorder()
	rt.handleOne(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp struct {
		Messages []api.Message `json:"messages"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Messages) != 60 {
		t.Errorf("messages returned = %d, want 60 (limit=500 must be accepted)", len(resp.Messages))
	}
}

// --- router_handlers.go:209 (`maxPlanDraftBytes+4096` in LimitBody) ---
// A 260014-byte body fits the original read cap (266240) but exceeds the
// mutated cap (258048); content 260000 is also under SetPlanDraft's
// 262144 cap, so the original path saves and returns 200.

func Test_gk_vibekit_u22_putPlanDraft_limitBodyBoundary(t *testing.T) {
	s := gk_vibekit_u22_newStore(t)
	id := api.ChatID("gku22put209")
	gk_vibekit_u22_createChat(t, s, id)

	content := strings.Repeat("a", 260000)
	body := `{"content":"` + content + `"}` // 260014 bytes total
	rt := NewRouter(s)
	req := httptest.NewRequest(http.MethodPut, "/api/chats/"+string(id)+"/plan-draft", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	rt.handlePlanDraft(w, req, id)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (a %d-byte body must fit the max+4096 cap)", w.Code, len(body))
	}
}

// --- router_handlers.go:218 (`maxPlanDraftBytes+4096` logged limit) ---
// An oversized body trips the "body too large" warn; the logged "limit"
// attribute must be 266240 (256*1024 + 4096). `+`->`-` logs 258048.

func Test_gk_vibekit_u22_putPlanDraft_logsExactLimit(t *testing.T) {
	const wantLimit = int64(266240) // 256*1024 + 4096

	snap := gk_vibekit_u22_captureLogs(t)
	s := gk_vibekit_u22_newStore(t)
	id := api.ChatID("gku22put218")

	content := strings.Repeat("a", 270000)
	body := `{"content":"` + content + `"}` // 270014 bytes, exceeds the cap
	rt := NewRouter(s)
	req := httptest.NewRequest(http.MethodPut, "/api/chats/"+string(id)+"/plan-draft", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	rt.handlePlanDraft(w, req, id)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (body must exceed the read cap)", w.Code)
	}
	rec, ok := gk_vibekit_u22_findLog(snap(), "chat plan_draft: body too large")
	if !ok {
		t.Fatal(`no "chat plan_draft: body too large" log record captured`)
	}
	lim, ok := rec.attrs["limit"]
	if !ok {
		t.Fatal(`log record has no "limit" attribute`)
	}
	if lim.Kind() != slog.KindInt64 {
		t.Fatalf("limit attr kind = %v, want Int64", lim.Kind())
	}
	if lim.Int64() != wantLimit {
		t.Errorf("logged limit = %d, want %d", lim.Int64(), wantLimit)
	}
}

// --- store.go:93 (`err != nil` for os.Chmod) in NewStore ---
// On a writable dir chmod succeeds, so the original logs NO chmod warn;
// the inverted `== nil` logs it on success.

func Test_gk_vibekit_u22_NewStore_noChmodWarnOnWritableDir(t *testing.T) {
	snap := gk_vibekit_u22_captureLogs(t)
	if _, err := NewStore(t.TempDir()); err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if gk_vibekit_u22_hasLogMsg(snap(), "chat store: chmod") {
		t.Error(`NewStore logged "chat store: chmod" on a writable dir; the chmod-error branch is inverted`)
	}
}

// --- store.go:291 (`rmDraftErr != nil` in Delete) ---
// When the draft removal succeeds (rmDraftErr == nil), the original logs
// NO warn; the inverted `== nil` logs "...removal failed" on a clean
// removal.

func Test_gk_vibekit_u22_Delete_noDraftWarnOnCleanRemoval(t *testing.T) {
	s := gk_vibekit_u22_newStore(t)
	id := api.ChatID("gku22del291")
	gk_vibekit_u22_createChat(t, s, id)
	draftPath := filepath.Join(s.Dir(), string(id)+planDraftSuffix)
	if err := os.WriteFile(draftPath, []byte("draft body"), 0o600); err != nil {
		t.Fatalf("write draft: %v", err)
	}

	snap := gk_vibekit_u22_captureLogs(t)
	if err := s.Delete(context.Background(), id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if gk_vibekit_u22_hasLogMsg(snap(), "chat delete: plan-draft removal failed") {
		t.Error(`Delete logged "chat delete: plan-draft removal failed" on a clean draft removal; the error branch is inverted`)
	}
}

// --- handlers_archive.go:18 (`headers == nil` in GET) ---
// With one archived chat ListArchived returns a non-nil slice, which the
// original passes through. The inverted `!= nil` would replace it with an
// empty slice and drop the chat.

func Test_gk_vibekit_u22_handleArchivedChats_listIncludesArchived(t *testing.T) {
	s := gk_vibekit_u22_newStore(t)
	ctx := context.Background()
	id := api.ChatID("gku22arch")
	gk_vibekit_u22_createChat(t, s, id)
	if err := s.Archive(ctx, id); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	rt := NewRouter(s)
	req := httptest.NewRequest(http.MethodGet, "/api/chats/archived", nil)
	w := httptest.NewRecorder()
	rt.handleArchivedChats(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp struct {
		Chats []api.ChatHeader `json:"chats"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Chats) != 1 {
		t.Fatalf("archived chats = %d, want 1 (a non-nil list must pass through)", len(resp.Chats))
	}
	if resp.Chats[0].ID != string(id) {
		t.Errorf("archived chat id = %q, want %q", resp.Chats[0].ID, id)
	}
}
