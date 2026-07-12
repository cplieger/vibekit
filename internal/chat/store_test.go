package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// fakeBroadcaster captures broadcasts for assertions. Access is guarded
// by mu so concurrent appends from parallel AppendMessage goroutines
// don't race the slice header.
type fakeBroadcaster struct {
	events []api.ServerEvent
	count  atomic.Int32
	mu     sync.Mutex
}

var _ api.Broadcaster = (*fakeBroadcaster)(nil)

func (f *fakeBroadcaster) Broadcast(_ context.Context, e api.ServerEvent) {
	f.mu.Lock()
	f.events = append(f.events, e)
	f.mu.Unlock()
	f.count.Add(1)
}

// snapshot returns a copy of the captured events under the mutex.
// Tests should use this instead of reading f.events directly so a
// future asynchronous broadcaster doesn't race a bare slice read.
func (f *fakeBroadcaster) snapshot() []api.ServerEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.events)
}

// reset clears the event log under the mutex. Tests that want to
// observe only post-setup broadcasts should use this instead of a
// bare `f.events = nil` assignment.
func (f *fakeBroadcaster) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = nil
}

func newTestStore(t *testing.T) (*Store, *fakeBroadcaster) {
	t.Helper()
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	b := &fakeBroadcaster{}
	s.SetBroadcaster(b)
	return s, b
}

// ageArchivedChat rewrites an archived chat file so both its ArchivedAt
// stamp and its mtime predate `ago`, making it eligible for purge. Purge
// ages from ArchivedAt (mtime is only a legacy fallback), and Archive now
// stamps ArchivedAt to the archive moment, so a test that wants an entry
// purged must age the stamped field — os.Chtimes on the mtime alone no
// longer suffices.
func ageArchivedChat(t *testing.T, s *Store, id string, ago time.Duration) {
	t.Helper()
	path := filepath.Join(s.dir, "archive", id+chatFileSuffix)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read archived chat %s: %v", id, err)
	}
	var c api.Chat
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("unmarshal archived chat %s: %v", id, err)
	}
	old := time.Now().Add(-ago)
	c.ArchivedAt = old.UnixMilli()
	out, err := json.MarshalIndent(&c, "", "  ")
	if err != nil {
		t.Fatalf("marshal archived chat %s: %v", id, err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatalf("write archived chat %s: %v", id, err)
	}
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("chtimes archived chat %s: %v", id, err)
	}
}

// badChatIDs is the canonical set of invalid chat identifiers used
// across all RejectsBadChatID / InvalidChatIDRejected tests. Adding a
// new invalid pattern here automatically covers every method.
var badChatIDs = []api.ChatID{"", "a/b", "..", "a\x00b", "a b", api.ChatID(strings.Repeat("x", 200))}

// assertRejectsBadChatIDs iterates badChatIDs and asserts that fn
// returns a non-nil error for each. Use in table-driven subtests to
// eliminate duplicated bad-id slices across store method tests.
func assertRejectsBadChatIDs(t *testing.T, fn func(id api.ChatID) error) {
	t.Helper()
	for _, bad := range badChatIDs {
		if err := fn(bad); err == nil {
			t.Errorf("accepted bad id %q", bad)
		}
	}
}

// --- Create + Get ---

func TestMutate_CreatesChatAndBroadcasts(t *testing.T) {
	s, b := newTestStore(t)
	err := s.Mutate(context.Background(), "c1", func(c *api.Chat, exists bool) bool {
		if exists {
			t.Error("exists = true on fresh chat")
		}
		c.Name = "Hello"
		c.Model = "claude"
		return true
	})
	if err != nil {
		t.Fatalf("Mutate error = %v", err)
	}
	got, ok := s.Get(context.Background(), "c1")
	if !ok {
		t.Fatal("Get returned false for created chat")
	}
	if got.Name != "Hello" || got.Model != "claude" {
		t.Errorf("fields: %+v", got)
	}
	if got.CreatedAt == 0 || got.UpdatedAt == 0 {
		t.Error("timestamps not set")
	}
	if len(b.events) != 1 || b.events[0].Type != "chat_created" {
		t.Errorf("broadcasts: %+v", b.events)
	}
}

func TestMutate_UpdatesChatAndBroadcasts(t *testing.T) {
	s, b := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	// Second Mutate should broadcast chat_updated.
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, exists bool) bool {
		if !exists {
			t.Error("exists = false on existing chat")
		}
		c.Name = "B"
		return true
	})
	got, _ := s.Get(context.Background(), "c1")
	if got.Name != "B" {
		t.Errorf("name = %q", got.Name)
	}
	if len(b.events) != 2 || b.events[1].Type != "chat_updated" {
		t.Errorf("second event should be chat_updated: %+v", b.events)
	}
}

func TestMutate_AbortDoesNotBroadcast(t *testing.T) {
	s, b := newTestStore(t)
	err := s.Mutate(context.Background(), "c1", func(*api.Chat, bool) bool { return false })
	if err != nil {
		t.Fatalf("Mutate error = %v", err)
	}
	if _, ok := s.Get(context.Background(), "c1"); ok {
		t.Error("chat created despite abort")
	}
	if len(b.events) != 0 {
		t.Errorf("broadcast despite abort: %+v", b.events)
	}
}

func TestMutate_RejectsBadChatID(t *testing.T) {
	s, _ := newTestStore(t)
	assertRejectsBadChatIDs(t, func(id api.ChatID) error {
		return s.Mutate(context.Background(), id, func(*api.Chat, bool) bool { return true })
	})
}

// --- Broadcaster contract ---

// BroadcasterContractTest verifies that a Broadcaster implementation
// satisfies the expected semantics under concurrent Broadcast calls:
//  1. Events appear in the order they were submitted (when serialised
//     by a single goroutine) — i.e. the implementation is linearisable
//     from a single-writer perspective.
//  2. Broadcast never blocks indefinitely (completes within a timeout).
//
// The test is parameterised so it can be reused against any Broadcaster
// implementation (fakeBroadcaster today, SSE broadcaster in integration
// tests later).
func BroadcasterContractTest(t *testing.T, newBroadcaster func() api.Broadcaster) {
	t.Helper()

	t.Run("ConcurrentBroadcastsDoNotBlock", func(t *testing.T) {
		b := newBroadcaster()
		const N = 100
		done := make(chan struct{})
		go func() {
			var wg sync.WaitGroup
			wg.Add(N)
			for i := range N {
				go func(i int) {
					defer wg.Done()
					b.Broadcast(context.Background(), api.ServerEvent{
						Type:   "test_event",
						ChatID: api.ChatID(fmt.Sprintf("c%d", i)),
					})
				}(i)
			}
			wg.Wait()
			close(done)
		}()
		select {
		case <-done:
			// All N concurrent broadcasts completed without blocking.
		case <-time.After(5 * time.Second):
			t.Fatal("Broadcast blocked: N concurrent calls did not complete within 5s")
		}
	})

	t.Run("SingleWriterOrderPreserved", func(t *testing.T) {
		b := newBroadcaster()
		const N = 200
		for i := range N {
			b.Broadcast(context.Background(), api.ServerEvent{
				Type:   "order_test",
				ChatID: api.ChatID(fmt.Sprintf("%d", i)),
			})
		}
		// Verify ordering via the concrete type's snapshot if available.
		type snapshotter interface {
			snapshot() []api.ServerEvent
		}
		if s, ok := b.(snapshotter); ok {
			evs := s.snapshot()
			if len(evs) != N {
				t.Fatalf("len(events) = %d, want %d", len(evs), N)
			}
			for i, e := range evs {
				want := api.ChatID(fmt.Sprintf("%d", i))
				if e.ChatID != want {
					t.Errorf("event[%d].ChatID = %q, want %q (order violated)", i, e.ChatID, want)
					break
				}
			}
		}
	})
}

func TestFakeBroadcaster_ContractCompliance(t *testing.T) {
	BroadcasterContractTest(t, func() api.Broadcaster {
		return &fakeBroadcaster{}
	})
}

// --- chatIDPattern ---

func TestChatIDPattern(t *testing.T) {
	valid := []string{
		"abc",
		"ABC",
		"01HXYZ",                               // ULID-like
		"550e8400-e29b-41d4-a716-446655440000", // UUID
		"chat-1716000000000",                   // legacy chat-<ms>
		"a_b",                                  // underscore
		"a-b",                                  // hyphen
		strings.Repeat("x", 128),               // max length
	}
	for _, id := range valid {
		if !chatIDPattern(api.ChatID(id)) {
			t.Errorf("chatIDPattern(%q) = false, want true", id)
		}
	}

	invalid := []string{
		"",                       // empty
		"a/b",                    // slash
		"..",                     // traversal
		"a.b",                    // dot
		"a b",                    // space
		"a\x00b",                 // null byte
		"a\nb",                   // newline
		strings.Repeat("x", 129), // over max length
	}
	for _, id := range invalid {
		if chatIDPattern(api.ChatID(id)) {
			t.Errorf("chatIDPattern(%q) = true, want false", id)
		}
	}
}

func TestGet_MissingChat(t *testing.T) {
	s, _ := newTestStore(t)
	if _, ok := s.Get(context.Background(), "nonexistent"); ok {
		t.Error("Get returned true for missing chat")
	}
}

// --- List ---

func TestList_SortsByUpdatedAtDesc(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "a", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	// Nudge the wall clock between mutations so the UpdatedAt ms
	// timestamps don't collide on fast machines and the sort order
	// is deterministic.
	time.Sleep(2 * time.Millisecond)
	_ = s.Mutate(context.Background(), "b", func(c *api.Chat, _ bool) bool { c.Name = "B"; return true })
	time.Sleep(2 * time.Millisecond)
	_ = s.Mutate(context.Background(), "a", func(c *api.Chat, _ bool) bool { return true }) // bump updated_at
	headers := s.List(context.Background())
	if len(headers) != 2 {
		t.Fatalf("len = %d, want 2", len(headers))
	}
	if headers[0].ID != "a" {
		t.Errorf("first = %q, want a (most recently updated)", headers[0].ID)
	}
}

func TestList_IgnoresNonChatFiles(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "a", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	// Non-.json file → skipped by the suffix filter.
	if err := os.WriteFile(filepath.Join(s.dir, "random.txt"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Valid .json suffix but invalid chat id (contains a '.') → skipped
	// by the chatIDPattern filter in List.
	if err := os.WriteFile(filepath.Join(s.dir, "bad.id.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	headers := s.List(context.Background())
	if len(headers) != 1 || headers[0].ID != "a" {
		t.Errorf("headers = %+v", headers)
	}
}

func TestList_SkipsMalformedChatFile(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "good", func(c *api.Chat, _ bool) bool { c.Name = "ok"; return true })
	// Drop a file that matches chatIDPattern but isn't valid JSON.
	// List must log and skip it, not panic or return a zero-value
	// header that confuses clients.
	badPath := filepath.Join(s.dir, "bad.json")
	if err := os.WriteFile(badPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	headers := s.List(context.Background())
	if len(headers) != 1 || headers[0].ID != "good" {
		t.Errorf("List() = %+v, want only the good chat", headers)
	}
}

// --- AppendMessage ---

func TestAppendMessage_AddsAndBroadcasts(t *testing.T) {
	s, b := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	b.reset()

	msg := &api.Message{ID: "m1", Role: api.RoleUser, Content: "hi"}
	if err := s.AppendMessage(context.Background(), "c1", msg); err != nil {
		t.Fatalf("AppendMessage error = %v", err)
	}

	got, _ := s.Get(context.Background(), "c1")
	if len(got.Messages) != 1 || got.Messages[0].Content != "hi" {
		t.Errorf("messages = %+v", got.Messages)
	}
	if got.Messages[0].Ts == 0 {
		t.Error("Ts not auto-filled")
	}
	// Two events: chat_updated (from Mutate's save-success broadcast)
	// then message_appended (fired after Mutate returns, so clients
	// never see a message echo referencing content that wasn't saved).
	evs := b.snapshot()
	if len(evs) != 2 {
		t.Fatalf("events = %+v", evs)
	}
	if evs[0].Type != "chat_updated" {
		t.Errorf("first event = %q, want chat_updated", evs[0].Type)
	}
	if evs[1].Type != "message_appended" {
		t.Errorf("second event = %q, want message_appended", evs[1].Type)
	}
}

func TestAppendMessage_NoOpOnMissingChat(t *testing.T) {
	s, b := newTestStore(t)
	err := s.AppendMessage(context.Background(), "nonexistent", &api.Message{ID: "m1", Role: api.RoleUser})
	if err != nil {
		t.Errorf("error on missing chat: %v", err)
	}
	if len(b.events) != 0 {
		t.Errorf("events: %+v", b.events)
	}
}

// --- UpdateMessage ---

func TestUpdateMessage_MutatesInPlace(t *testing.T) {
	s, b := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = s.AppendMessage(context.Background(), "c1", &api.Message{ID: "m1", Role: api.RoleAssistant, Content: "old"})
	b.reset()

	err := s.UpdateMessage(context.Background(), "c1", "m1", func(m *api.Message) { m.Content = "new" })
	if err != nil {
		t.Fatalf("UpdateMessage error = %v", err)
	}
	got, _ := s.Get(context.Background(), "c1")
	if got.Messages[0].Content != "new" {
		t.Errorf("content = %q", got.Messages[0].Content)
	}
	// Two events: chat_updated (from Mutate's save-success broadcast)
	// then message_updated (fired after Mutate returns — same
	// save-before-broadcast discipline as AppendMessage).
	evs := b.snapshot()
	if len(evs) != 2 {
		t.Fatalf("events: %+v", evs)
	}
	if evs[0].Type != "chat_updated" {
		t.Errorf("first event = %q, want chat_updated", evs[0].Type)
	}
	if evs[1].Type != "message_updated" {
		t.Errorf("second event = %q, want message_updated", evs[1].Type)
	}
}

func TestUpdateMessage_NoOpOnMissingMessage(t *testing.T) {
	s, b := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	b.reset()

	_ = s.UpdateMessage(context.Background(), "c1", "nonexistent", func(*api.Message) {})
	if evs := b.snapshot(); len(evs) != 0 {
		t.Errorf("events: %+v", evs)
	}
}

// --- Delete ---

func TestDelete_RemovesFileAndBroadcasts(t *testing.T) {
	s, b := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	b.reset()

	if err := s.Delete(context.Background(), "c1"); err != nil {
		t.Fatalf("Delete error = %v", err)
	}
	if _, ok := s.Get(context.Background(), "c1"); ok {
		t.Error("chat still exists after delete")
	}
	if len(b.events) != 1 || b.events[0].Type != "chat_deleted" {
		t.Errorf("events: %+v", b.events)
	}
}

func TestDelete_MissingChatIsNoOp(t *testing.T) {
	s, b := newTestStore(t)
	if err := s.Delete(context.Background(), "nonexistent"); err != nil {
		t.Errorf("error on missing chat: %v", err)
	}
	// Still broadcasts (so multi-device sees the delete even if stale).
	if len(b.events) != 1 {
		t.Errorf("events: %+v", b.events)
	}
}

// --- Tombstone (delete-during-turn race guard) ---

func TestDelete_TombstonesChatID(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = s.Delete(context.Background(), "c1")
	if !s.isTombstoned("c1") {
		t.Error("tombstone not set after Delete")
	}
}

func TestMutate_RefusesToCreateTombstonedChat(t *testing.T) {
	s, b := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = s.Delete(context.Background(), "c1")
	b.reset()

	// Simulate a late handler racing the delete — it tries to Mutate
	// the just-deleted id. The call must return nil (no error, no
	// resurrection) so the caller treats it as a benign no-op.
	err := s.Mutate(context.Background(), "c1", func(c *api.Chat, exists bool) bool {
		c.Name = "resurrected"
		c.Messages = append(c.Messages, api.Message{Role: api.RoleUser, Content: "ghost"})
		return true
	})
	if err != nil {
		t.Fatalf("Mutate on tombstoned chat returned error: %v", err)
	}
	if _, ok := s.Get(context.Background(), "c1"); ok {
		t.Error("chat was resurrected despite tombstone")
	}
	// No chat_created / chat_updated event should have been emitted.
	for _, e := range b.snapshot() {
		if e.Type == "chat_created" || e.Type == "chat_updated" {
			t.Errorf("unexpected event after tombstoned mutate: %+v", e)
		}
	}
}

func TestMutate_UpdatingExistingChatIsNotBlockedByTombstone(t *testing.T) {
	// Tombstone only blocks the "create if missing" path. An existing
	// chat whose id happens to share a string with a tombstoned id
	// would be vanishingly rare (chat IDs are client-random), but
	// verify the code path: once the chat exists, tombstone is
	// irrelevant because we never consult it.
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = s.Delete(context.Background(), "c1")
	// Tombstone is now live for c1. Re-Create via a different id
	// shouldn't be affected.
	err := s.Mutate(context.Background(), "c2", func(c *api.Chat, _ bool) bool { c.Name = "B"; return true })
	if err != nil {
		t.Fatalf("unrelated chat blocked by unrelated tombstone: %v", err)
	}
	if _, ok := s.Get(context.Background(), "c2"); !ok {
		t.Error("c2 was not created")
	}
}

func TestAppendMessage_OnTombstonedChatIsNoOp(t *testing.T) {
	s, b := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = s.Delete(context.Background(), "c1")
	b.reset()

	// Mutate's tombstone check runs before the mutator when the chat
	// doesn't exist, so AppendMessage's `if !exists { return false }`
	// early-return never fires here — the tombstone guard short-circuits
	// first. This test pins that path: after Delete, AppendMessage must
	// produce no events and not resurrect the chat even though the
	// mutator would have happily returned false anyway.
	err := s.AppendMessage(context.Background(), "c1", &api.Message{Role: api.RoleUser, Content: "ghost"})
	if err != nil {
		t.Fatalf("AppendMessage error: %v", err)
	}
	if _, ok := s.Get(context.Background(), "c1"); ok {
		t.Error("chat was recreated via AppendMessage after delete")
	}
	for _, e := range b.snapshot() {
		if e.Type == "message_appended" || e.Type == "chat_created" {
			t.Errorf("unexpected event: %+v", e)
		}
	}
}

// --- BuildHistory ---

func TestBuildHistory_FormatsAllRoles(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Messages = []api.Message{
			{Role: api.RoleUser, Content: "hello"},
			{
				Role:    api.RoleAssistant,
				Content: "thinking",
				ToolCalls: []api.ToolCall{
					{Title: "grep", Status: api.ToolCompleted},
					{Title: "edit", Status: api.ToolFailed},
				},
			},
			{Role: api.RoleEvent, EventKind: api.EventCancelled, Content: "user aborted"},
			{Role: api.RoleEvent, EventKind: api.EventCompacted, Content: "summary text"},
		}
		return true
	})
	got := s.BuildHistory(context.Background(), "c1")
	want := "User: hello\n" +
		"Assistant: thinking\n  [tool: grep status=completed]\n  [tool: edit status=failed]\n" +
		"[cancelled] user aborted\n" +
		"[compacted] summary text\n"
	if got != want {
		t.Errorf("BuildHistory(c1) =\n%q\nwant\n%q", got, want)
	}
}

func TestBuildHistory_EmptyChatReturnsEmptyString(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	if got := s.BuildHistory(context.Background(), "c1"); got != "" {
		t.Errorf("BuildHistory on empty-message chat = %q, want empty", got)
	}
}

func TestBuildHistory_EmptyForMissingChat(t *testing.T) {
	s, _ := newTestStore(t)
	if s.BuildHistory(context.Background(), "nonexistent") != "" {
		t.Error("expected empty string")
	}
}

// --- HTTP endpoints ---

func TestHandleList_ReturnsHeaders(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "One"
		c.Messages = []api.Message{{ID: "m1", Role: api.RoleUser, Content: "x"}}
		return true
	})

	req := httptest.NewRequest(http.MethodGet, "/api/chats", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleList(rec, req)

	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"message_count":1`) {
		t.Errorf("body = %q", body)
	}
	// Header response should NOT include raw messages.
	if strings.Contains(body, "messages") && strings.Contains(body, `"role":"user"`) {
		t.Errorf("messages leaked into list response: %q", body)
	}
}

func TestHandleOne_ReturnsChatAndMessages(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "One"
		c.Messages = []api.Message{
			{ID: "m1", Role: api.RoleUser, Content: "a", Ts: 100},
			{ID: "m2", Role: api.RoleAssistant, Content: "b", Ts: 200},
		}
		return true
	})

	req := httptest.NewRequest(http.MethodGet, "/api/chats/c1", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)

	if rec.Code != 200 {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"role":"user"`) || !strings.Contains(body, `"role":"assistant"`) {
		t.Errorf("missing messages: %q", body)
	}
}

func TestHandleOne_Pagination(t *testing.T) {
	cases := []struct {
		name        string
		query       string
		wantIDs     []string
		wantHasMore bool
	}{
		{"no_params_returns_all", "", []string{"a", "b", "c", "d", "e"}, false},
		{"limit_clamps_window_and_flags_more", "?limit=2", []string{"d", "e"}, true},
		{"before_exclusive_of_matching_ts", "?before=130", []string{"a", "b", "c"}, false},
		{"before_past_newest_ts_returns_all", "?before=9999", []string{"a", "b", "c", "d", "e"}, false},
		{"before_before_oldest_ts_returns_none", "?before=50", []string{}, false},
		{"limit_and_before_combined", "?before=140&limit=2", []string{"c", "d"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestStore(t)
			_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
				c.Name = "A"
				c.Messages = []api.Message{
					{ID: "a", Role: api.RoleUser, Content: "1", Ts: 100},
					{ID: "b", Role: api.RoleUser, Content: "2", Ts: 110},
					{ID: "c", Role: api.RoleUser, Content: "3", Ts: 120},
					{ID: "d", Role: api.RoleUser, Content: "4", Ts: 130},
					{ID: "e", Role: api.RoleUser, Content: "5", Ts: 140},
				}
				return true
			})
			req := httptest.NewRequest(http.MethodGet, "/api/chats/c1"+tc.query, nil)
			rec := httptest.NewRecorder()
			NewRouter(s).handleOne(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
			}
			var got struct {
				Messages []api.Message `json:"messages"`
				HasMore  bool          `json:"has_more"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			ids := make([]string, len(got.Messages))
			for i, m := range got.Messages {
				ids[i] = m.ID
			}
			if !slices.Equal(ids, tc.wantIDs) {
				t.Errorf("ids = %v, want %v", ids, tc.wantIDs)
			}
			if got.HasMore != tc.wantHasMore {
				t.Errorf("has_more = %v, want %v", got.HasMore, tc.wantHasMore)
			}
		})
	}
}

func TestHandleOne_NotFound(t *testing.T) {
	s, _ := newTestStore(t)
	req := httptest.NewRequest(http.MethodGet, "/api/chats/nope", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("code = %d", rec.Code)
	}
}

func TestHandleOne_RejectsUnknownSubResource(t *testing.T) {
	// With sub-resource routing, /api/chats/a/b treats "b" as a sub-
	// resource name. Unknown sub-resources return 404. Historically this
	// was a 400 "slash in id" — the new behaviour is correct because
	// plan-draft is a valid sub-resource.
	s, _ := newTestStore(t)
	req := httptest.NewRequest(http.MethodGet, "/api/chats/a/b", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rec.Code)
	}
}

// --- Persistence ---

func TestStoreSurvivesReopen(t *testing.T) {
	dir := t.TempDir()

	s1, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	_ = s1.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "Saved"
		c.ACPSessionID = "acp-1"
		c.Messages = []api.Message{{ID: "m1", Role: api.RoleUser, Content: "hi"}}
		return true
	})

	// Reopen.
	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore reopen: %v", err)
	}
	got, ok := s2.Get(context.Background(), "c1")
	if !ok {
		t.Fatal("chat not found after reopen")
	}
	if got.Name != "Saved" || got.ACPSessionID != "acp-1" {
		t.Errorf("reloaded chat: %+v", got)
	}
	if len(got.Messages) != 1 {
		t.Errorf("messages lost: %+v", got.Messages)
	}
}

// --- Plan drafts ---

func TestPlanDraft_RoundTrip(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	if err := s.SetPlanDraft(context.Background(), "c1", "# Plan\n- [ ] step 1\n"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := s.GetPlanDraft(context.Background(), "c1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != "# Plan\n- [ ] step 1\n" {
		t.Errorf("got = %q", got)
	}
}

func TestPlanDraft_GetMissingReturnsEmpty(t *testing.T) {
	s, _ := newTestStore(t)
	got, err := s.GetPlanDraft(context.Background(), "c-never-had-draft")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty content for missing draft, got %q", got)
	}
}

func TestPlanDraft_SetRejectsNonexistentChat(t *testing.T) {
	// Adversarial: a client could otherwise write 256KB plan drafts
	// keyed by arbitrary chat ids to pollute the chats directory with
	// orphan files. SetPlanDraft now refuses unless the chat exists.
	s, _ := newTestStore(t)
	err := s.SetPlanDraft(context.Background(), "c-ghost", "# orphan")
	if err == nil {
		t.Fatal("expected error when writing draft for nonexistent chat")
	}
}

func TestPlanDraft_SetRejectsTombstonedChat(t *testing.T) {
	// Delete-during-edit race: chat existed when the editor opened
	// but was deleted from another tab before the draft PUT landed.
	// Tombstone prevents the draft from being resurrected next to
	// the now-missing chat file.
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = s.Delete(context.Background(), "c1")
	err := s.SetPlanDraft(context.Background(), "c1", "# stale")
	if err == nil {
		t.Fatal("expected error when writing draft for tombstoned chat")
	}
}

func TestPlanDraft_DeleteRemovesFile(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	if err := s.SetPlanDraft(context.Background(), "c1", "# p"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePlanDraft(context.Background(), "c1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, _ := s.GetPlanDraft(context.Background(), "c1")
	if got != "" {
		t.Errorf("after delete, got = %q", got)
	}
}

func TestPlanDraft_OverCapRejected(t *testing.T) {
	s, _ := newTestStore(t)
	big := make([]byte, maxPlanDraftBytes+1)
	for i := range big {
		big[i] = 'x'
	}
	err := s.SetPlanDraft(context.Background(), "c1", string(big))
	if err == nil {
		t.Fatalf("expected size cap to reject overlarge draft")
	}
	// Pin the Kind: the handler discriminates 413 from 500 via
	// errors.As(err, *StoreError) + switch on Kind, not
	// string-prefix sniffing. A future rename of the formatted
	// message must still match.
	var ce *StoreError
	if !errors.As(err, &ce) || ce.Kind != ErrKindTooLarge {
		t.Errorf("err = %v, want *StoreError{Kind: ErrKindTooLarge}", err)
	}
}

func TestPlanDraft_InvalidChatIDRejected(t *testing.T) {
	s, _ := newTestStore(t)
	assertRejectsBadChatIDs(t, func(id api.ChatID) error {
		return s.SetPlanDraft(context.Background(), id, "x")
	})
}

func TestPlanDraft_DeletedWithChat(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	if err := s.SetPlanDraft(context.Background(), "c1", "# plan"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(context.Background(), "c1"); err != nil {
		t.Fatal(err)
	}
	// Draft should be cleaned up alongside the chat file.
	draftPath, _ := s.planDraftPathFor("c1")
	if _, err := os.Stat(draftPath); !os.IsNotExist(err) {
		t.Errorf("plan draft not cleaned up after Delete: %v", err)
	}
}

func TestHandlePlanDraft_GETMissingReturnsEmpty(t *testing.T) {
	s, _ := newTestStore(t)
	req := httptest.NewRequest(http.MethodGet, "/api/chats/c1/plan-draft", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"content":""`) {
		t.Errorf("body = %q, want empty content", body)
	}
}

func TestHandlePlanDraft_PUTThenGET(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	req := httptest.NewRequest(http.MethodPut, "/api/chats/c1/plan-draft",
		strings.NewReader(`{"content":"# Plan\n- [ ] a\n"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT code = %d, body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/chats/c1/plan-draft", nil)
	rec = httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET code = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "# Plan") {
		t.Errorf("GET body missing content: %q", body)
	}
}

func TestHandlePlanDraft_PUTRejectedWithoutChat(t *testing.T) {
	// Adversarial: client POSTs a plan-draft for a chat id it invented.
	// Must be rejected to prevent orphan files in the chats directory.
	s, _ := newTestStore(t)
	req := httptest.NewRequest(http.MethodPut, "/api/chats/c-orphan/plan-draft",
		strings.NewReader(`{"content":"# hacker plan"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "chat not found") {
		t.Errorf("body = %q, want 'chat not found' sentinel", rec.Body.String())
	}
}

func TestHandlePlanDraft_PUTRejectedWhenTombstoned(t *testing.T) {
	// Tombstone variant of the orphan-rejection test: chat existed but
	// was deleted while the editor was open. Kind=ErrKindTombstoned
	// must also map to 404 so the client sees a uniform "chat not
	// found" signal.
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = s.Delete(context.Background(), "c1")
	req := httptest.NewRequest(http.MethodPut, "/api/chats/c1/plan-draft",
		strings.NewReader(`{"content":"# stale"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rec.Code)
	}
}

func TestHandlePlanDraft_DELETE(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	if err := s.SetPlanDraft(context.Background(), "c1", "x"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodDelete, "/api/chats/c1/plan-draft", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE code = %d", rec.Code)
	}
	got, _ := s.GetPlanDraft(context.Background(), "c1")
	if got != "" {
		t.Errorf("after DELETE, draft still present: %q", got)
	}
}

func TestHandlePlanDraft_PUTRejections(t *testing.T) {
	cases := []struct {
		name             string
		body             string
		contentType      string
		wantBodyContains string
		wantCode         int
		needsChat        bool
	}{
		{
			name:        "invalid json",
			needsChat:   false,
			body:        `{not json`,
			contentType: "application/json",
			wantCode:    http.StatusBadRequest,
		},
		{
			name:             "trailing json objects",
			needsChat:        true,
			body:             `{"content":"first"}{"content":"second"}`,
			contentType:      "application/json",
			wantCode:         http.StatusBadRequest,
			wantBodyContains: "trailing data",
		},
		{
			name:             "content over maxPlanDraftBytes",
			needsChat:        true,
			body:             `{"content":"` + strings.Repeat("x", maxPlanDraftBytes+1) + `"}`,
			contentType:      "application/json",
			wantCode:         http.StatusRequestEntityTooLarge,
			wantBodyContains: "too large",
		},
		{
			name:             "body over MaxBytesReader limit",
			needsChat:        true,
			body:             `{"content":"` + strings.Repeat("x", maxPlanDraftBytes+8192) + `"}`,
			contentType:      "application/json",
			wantCode:         http.StatusRequestEntityTooLarge,
			wantBodyContains: "request body too large",
		},
		{
			name:        "unexpected content-type",
			needsChat:   true,
			body:        `{"content":"x"}`,
			contentType: "text/plain",
			wantCode:    http.StatusBadRequest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestStore(t)
			if tc.needsChat {
				_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
			}
			req := httptest.NewRequest(http.MethodPut, "/api/chats/c1/plan-draft",
				strings.NewReader(tc.body))
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			rec := httptest.NewRecorder()
			NewRouter(s).handleOne(rec, req)
			if rec.Code != tc.wantCode {
				t.Errorf("code = %d, want %d", rec.Code, tc.wantCode)
			}
			if tc.wantBodyContains != "" && !strings.Contains(rec.Body.String(), tc.wantBodyContains) {
				t.Errorf("body = %q, want substring %q", rec.Body.String(), tc.wantBodyContains)
			}
		})
	}
}

func TestHandlePlanDraft_MethodNotAllowed(t *testing.T) {
	s, _ := newTestStore(t)
	req := httptest.NewRequest(http.MethodPatch, "/api/chats/c1/plan-draft", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("code = %d, want 405", rec.Code)
	}
}

// --- Extended HTTP handler coverage ---

func TestHandleList_EmptyStoreReturnsEmptyArrayNotNull(t *testing.T) {
	// Regression: Go's json.Marshal of a nil slice emits `null`. The
	// frontend wire decoder rejects null for fields typed as array
	// ("$.chat_list.chats: expected array, got null") and the chat
	// list quietly stops working after a fresh container init.
	// List() must always return a non-nil slice so JSON encodes `[]`.
	s, _ := newTestStore(t)

	req := httptest.NewRequest(http.MethodGet, "/api/chats", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"chats":[]`) {
		t.Errorf("body should contain `\"chats\":[]`, got: %s", body)
	}
	if strings.Contains(body, `"chats":null`) {
		t.Errorf("body must not contain `\"chats\":null`: %s", body)
	}
}

func TestHandleArchived_EmptyStoreReturnsEmptyArrayNotNull(t *testing.T) {
	// Sister regression for the archive list — same nil-slice trap.
	s, _ := newTestStore(t)

	req := httptest.NewRequest(http.MethodGet, "/api/chats/archived", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleArchivedChats(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	// The archived endpoint wraps under "chats" too (see
	// handleArchivedChats); update the assertion if that ever
	// diverges.
	if strings.Contains(body, `:null`) {
		t.Errorf("archived body has a null field: %s", body)
	}
}

func TestHandleList_RejectsNonGET(t *testing.T) {
	s, _ := newTestStore(t)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req := httptest.NewRequest(method, "/api/chats", nil)
		rec := httptest.NewRecorder()
		NewRouter(s).handleList(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("handleList(%s) code = %d, want 405", method, rec.Code)
		}
	}
}

func TestHandleOne_RejectsEmptyOrLeadingSlashPath(t *testing.T) {
	s, _ := newTestStore(t)
	cases := []struct {
		name string
		path string
	}{
		{"empty_id", "/api/chats/"},
		{"leading_slash", "/api/chats//c1"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		rec := httptest.NewRecorder()
		NewRouter(s).handleOne(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("handleOne(%s) code = %d, want 400", tc.name, rec.Code)
		}
	}
}

func TestHandleOne_BaseRejectsNonGET(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req := httptest.NewRequest(method, "/api/chats/c1", nil)
		rec := httptest.NewRecorder()
		NewRouter(s).handleOne(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("handleOne(%s /api/chats/c1) code = %d, want 405", method, rec.Code)
		}
	}
}

func TestHandleOne_IgnoresInvalidQueryParams(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.Messages = []api.Message{{ID: "m1", Role: api.RoleUser, Content: "x", Ts: 100}}
		return true
	})
	cases := []struct {
		name  string
		query string
	}{
		{"limit_non_numeric", "?limit=abc"},
		{"limit_zero", "?limit=0"},
		{"limit_negative", "?limit=-5"},
		{"limit_over_max", "?limit=10000"},
		{"before_non_numeric", "?before=abc"},
		{"before_negative", "?before=-1"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/chats/c1"+tc.query, nil)
		rec := httptest.NewRecorder()
		NewRouter(s).handleOne(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("handleOne(%s) code = %d, want 200 (invalid params should fall back)", tc.name, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"id":"m1"`) {
			t.Errorf("handleOne(%s) did not return messages: %s", tc.name, rec.Body.String())
		}
	}
}

func TestRegisterRoutes_WiresListAndOneHandlers(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	mux := http.NewServeMux()
	s.RegisterRoutes(mux)

	// /api/chats reaches handleList
	req := httptest.NewRequest(http.MethodGet, "/api/chats", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/chats code = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"id":"c1"`) {
		t.Errorf("GET /api/chats body missing c1: %s", rec.Body.String())
	}

	// /api/chats/c1 reaches handleOne
	req = httptest.NewRequest(http.MethodGet, "/api/chats/c1", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/chats/c1 code = %d, want 200", rec.Code)
	}
}

// --- Concurrency ---

func TestMutate_SerializesSameChatConcurrentAppends(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	for i := range N {
		go func(i int) {
			defer wg.Done()
			_ = s.AppendMessage(context.Background(), "c1", &api.Message{
				ID: fmt.Sprintf("m%d", i), Role: api.RoleUser, Content: "x",
			})
		}(i)
	}
	wg.Wait()
	got, _ := s.Get(context.Background(), "c1")
	if len(got.Messages) != N {
		t.Errorf("concurrent appends: len = %d, want %d (per-chat mutex should serialize)", len(got.Messages), N)
	}
}

func TestMutate_DifferentChatsAreIndependent(t *testing.T) {
	// Two chats should not block each other. We assert completion of
	// N mutations on each chat runs to success with no deadlock.
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "a", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = s.Mutate(context.Background(), "b", func(c *api.Chat, _ bool) bool { c.Name = "B"; return true })

	const N = 20
	var wg sync.WaitGroup
	wg.Add(2 * N)
	for i := range N {
		go func(i int) {
			defer wg.Done()
			_ = s.AppendMessage(context.Background(), "a", &api.Message{ID: fmt.Sprintf("a%d", i), Role: api.RoleUser, Content: "x"})
		}(i)
		go func(i int) {
			defer wg.Done()
			_ = s.AppendMessage(context.Background(), "b", &api.Message{ID: fmt.Sprintf("b%d", i), Role: api.RoleUser, Content: "x"})
		}(i)
	}
	wg.Wait()
	a, _ := s.Get(context.Background(), "a")
	b, _ := s.Get(context.Background(), "b")
	if len(a.Messages) != N || len(b.Messages) != N {
		t.Errorf("independent chats: a=%d b=%d want %d each", len(a.Messages), len(b.Messages), N)
	}
}

// --- Tombstone prune paths ---

func TestIsTombstoned_ExpiredEntryIsPrunedAndReturnsFalse(t *testing.T) {
	s, _ := newTestStore(t)
	// Inject an expired tombstone (older than tombstoneTTL).
	s.tombMu.Lock()
	s.tombstone["c1"] = time.Now().Add(-2 * tombstoneTTL)
	s.tombMu.Unlock()

	if s.isTombstoned("c1") {
		t.Error("isTombstoned returned true for expired tombstone")
	}
	// And the expired entry is now pruned.
	s.tombMu.Lock()
	_, still := s.tombstone["c1"]
	s.tombMu.Unlock()
	if still {
		t.Error("expired tombstone was not pruned by isTombstoned")
	}
}

func TestMutate_ExpiredTombstoneDoesNotBlockRecreation(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = s.Delete(context.Background(), "c1")

	// Age the tombstone past its TTL.
	s.tombMu.Lock()
	s.tombstone["c1"] = time.Now().Add(-2 * tombstoneTTL)
	s.tombMu.Unlock()

	err := s.Mutate(context.Background(), "c1", func(c *api.Chat, exists bool) bool {
		if exists {
			t.Error("exists = true after expired tombstone")
		}
		c.Name = "reborn"
		return true
	})
	if err != nil {
		t.Fatalf("Mutate after tombstone expiry: %v", err)
	}
	got, ok := s.Get(context.Background(), "c1")
	if !ok || got.Name != "reborn" {
		t.Errorf("expected recreation, got ok=%v chat=%+v", ok, got)
	}
}

func TestMarkDeleted_PrunesExpiredEntries(t *testing.T) {
	s, _ := newTestStore(t)
	// Seed 3 expired + 1 fresh tombstone.
	now := time.Now()
	s.tombMu.Lock()
	s.tombstone["expired-1"] = now.Add(-2 * tombstoneTTL)
	s.tombstone["expired-2"] = now.Add(-3 * tombstoneTTL)
	s.tombstone["expired-3"] = now.Add(-5 * tombstoneTTL)
	s.tombstone["fresh"] = now.Add(-time.Second)
	s.tombMu.Unlock()

	// markDeleted runs prune on every call.
	s.markDeleted("new-delete")

	s.tombMu.Lock()
	defer s.tombMu.Unlock()
	for _, id := range []string{"expired-1", "expired-2", "expired-3"} {
		if _, ok := s.tombstone[api.ChatID(id)]; ok {
			t.Errorf("markDeleted did not prune expired entry %q", id)
		}
	}
	if _, ok := s.tombstone["fresh"]; !ok {
		t.Error("markDeleted pruned a non-expired entry")
	}
	if _, ok := s.tombstone["new-delete"]; !ok {
		t.Error("markDeleted did not record the new tombstone")
	}
}

// --- Delete on missing chat: phantom tombstone guard ---

func TestDelete_MissingChatDoesNotTombstone(t *testing.T) {
	// A stale DELETE from a second device (or a client-driven retry)
	// can hit a chat id the server never knew about. Broadcasting the
	// delete is intentional (multi-device UI consistency) but
	// tombstoning a phantom id would block a future legitimate
	// create on that id for 10 minutes.
	s, _ := newTestStore(t)
	_ = s.Delete(context.Background(), "never-existed")
	if s.isTombstoned("never-existed") {
		t.Error("phantom delete tombstoned a chat that never existed")
	}
	// Creating a new chat with that id must succeed.
	err := s.Mutate(context.Background(), "never-existed", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	if err != nil {
		t.Fatalf("Mutate after phantom delete: %v", err)
	}
	if _, ok := s.Get(context.Background(), "never-existed"); !ok {
		t.Error("chat not created after phantom delete")
	}
}

// --- NewStore error propagation ---

func TestNewStore_MkdirFailurePropagatesError(t *testing.T) {
	// MkdirAll fails when a path component is a regular file — a
	// real-world misconfiguration users can hit by bind-mounting a file
	// onto the chats directory. The constructor must surface the error
	// so the process fails startup instead of silently running with a
	// broken store.
	base := t.TempDir()
	blocker := filepath.Join(base, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// "blocker" is a file; asking for "<base>/blocker/chats" forces
	// MkdirAll to error because it can't descend through a file.
	_, err := NewStore(filepath.Join(blocker, "chats"))
	if err == nil {
		t.Fatal("NewStore(path-through-file) = nil error, want non-nil")
	}
	if !strings.Contains(err.Error(), "chat store: mkdir") {
		t.Errorf("NewStore error = %q, want wrapping message with prefix %q",
			err.Error(), "chat store: mkdir")
	}
}

// --- Size cap on load ---

func TestGet_RejectsOversizeChatFile(t *testing.T) {
	// 32 MiB cap is a security control: a corrupted or adversarial
	// chat file must not OOM the process via List walking every chat.
	// Sparse-file via os.Truncate avoids writing 32 MiB of real bytes
	// while still triggering the st.Size() > maxChatFileBytes branch.
	s, _ := newTestStore(t)
	path := filepath.Join(s.dir, "big.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = f.Close()
	// One byte over the cap so the branch strictly triggers.
	if err := os.Truncate(path, int64(maxChatFileBytes)+1); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, ok := s.Get(context.Background(), "big"); ok {
		t.Error("Get on oversize chat file returned ok=true, want false")
	}
}

func TestList_SkipsOversizeChatFile(t *testing.T) {
	// The same guardrail must keep one oversize file from erasing the
	// sidebar for every other chat. List should log-and-skip, not fail.
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "good", func(c *api.Chat, _ bool) bool { c.Name = "ok"; return true })
	path := filepath.Join(s.dir, "big.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = f.Close()
	if err := os.Truncate(path, int64(maxChatFileBytes)+1); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	headers := s.List(context.Background())
	if len(headers) != 1 || headers[0].ID != "good" {
		t.Errorf("List() = %+v, want only the good chat (oversize skipped)", headers)
	}
}

// --- Parse error must not silently overwrite ---

func TestMutate_PropagatesParseErrorDoesNotOverwrite(t *testing.T) {
	// A corrupted chat file (manual edit, partial write from a prior
	// crash) must not be silently overwritten by Mutate's auto-create
	// path. Mutate must surface the parse error so callers fail loudly
	// instead of losing the user's history to an implicit "rewrite from
	// empty" operation.
	s, _ := newTestStore(t)
	badPath := filepath.Join(s.dir, "c1.json")
	const garbage = "{not json"
	if err := os.WriteFile(badPath, []byte(garbage), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	err := s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "would-stomp-history"
		return true
	})
	if err == nil {
		t.Fatal("Mutate on malformed chat file returned nil error, want parse error")
	}
	got, readErr := os.ReadFile(badPath)
	if readErr != nil {
		t.Fatalf("read-back: %v", readErr)
	}
	if string(got) != garbage {
		t.Errorf("Mutate stomped malformed file: got %q, want original %q",
			string(got), garbage)
	}
}

// --- Mutator must not reassign c.ID ---

func TestMutate_RefusesMutatorReassigningChatID(t *testing.T) {
	// Defensive invariant: a mutator that retargets c.ID would save
	// the chat to a different file under a different per-chat mutex,
	// allowing concurrent writes under mismatched locks. Mutate must
	// refuse and surface the error so the broken caller is visible.
	s, _ := newTestStore(t)
	err := s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.ID = "c2" // broken mutator
		c.Name = "stolen"
		return true
	})
	if err == nil {
		t.Fatal("Mutate accepted mutator that reassigned c.ID")
	}
	if !strings.Contains(err.Error(), "reassigned id") {
		t.Errorf("error = %q, want mention of reassigned id", err.Error())
	}
	// Neither chat should have been written.
	if _, ok := s.Get(context.Background(), "c1"); ok {
		t.Error("c1 was written despite mutator reassigning id")
	}
	if _, ok := s.Get(context.Background(), "c2"); ok {
		t.Error("c2 was written via the reassigned id")
	}
}

// --- UpdateMessage no-op on missing chat ---

func TestUpdateMessage_NoOpOnMissingChat(t *testing.T) {
	// A late tool_call_update racing a chat delete would otherwise
	// auto-create the chat via Mutate's `!exists` path. UpdateMessage
	// must early-return from its mutator when the chat doesn't exist
	// so no ghost file is written and no broadcast is emitted.
	s, b := newTestStore(t)
	err := s.UpdateMessage(context.Background(), "never-existed", "m1", func(m *api.Message) {
		m.Content = "ghost"
	})
	if err != nil {
		t.Errorf("UpdateMessage on missing chat = %v, want nil", err)
	}
	if _, ok := s.Get(context.Background(), "never-existed"); ok {
		t.Error("UpdateMessage resurrected a never-existed chat")
	}
	if evs := b.snapshot(); len(evs) != 0 {
		t.Errorf("UpdateMessage on missing chat broadcast events: %+v", evs)
	}
}

// --- Plan-draft getters/deleters reject invalid chat IDs ---

func TestGetPlanDraft_InvalidChatIDRejected(t *testing.T) {
	// Defence-in-depth: even if the handler's chatIDPattern check is
	// ever bypassed, the store-level guard must reject a traversal id
	// before it reaches os.ReadFile on an arbitrary filesystem path.
	s, _ := newTestStore(t)
	assertRejectsBadChatIDs(t, func(id api.ChatID) error {
		_, err := s.GetPlanDraft(context.Background(), id)
		return err
	})
}

func TestDeletePlanDraft_InvalidChatIDRejected(t *testing.T) {
	// Same guard on the delete side — reject before touching the FS.
	s, _ := newTestStore(t)
	assertRejectsBadChatIDs(t, func(id api.ChatID) error {
		return s.DeletePlanDraft(context.Background(), id)
	})
}

// --- handlePlanDraft adversarial paths ---

func TestHandlePlanDraft_InvalidChatIDRejected(t *testing.T) {
	// The sub-route handler re-validates the chat id defensively,
	// even though handleOne's Cut-and-route step is upstream. A
	// refactor that changes handleOne's routing must not silently
	// let a traversal id through to SetPlanDraft / ReadFile.
	// Note: the URL parser rejects literal ".." in the path segment
	// before it reaches chatIDPattern, so we use a syntactically
	// valid but pattern-failing id (contains '.').
	s, _ := newTestStore(t)
	req := httptest.NewRequest(http.MethodGet, "/api/chats/bad.id/plan-draft", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400 (invalid chat id)", rec.Code)
	}
}

// --- handleOne pre-validation of chat id ---

func TestHandleOne_RejectsInvalidChatID(t *testing.T) {
	// The chat id pre-validation ensures malformed ids (bot probes,
	// typos) return 400 without emitting an slog.Error from load's
	// pathFor rejection. Matches the defensive check in handlePlanDraft.
	// We stick to ids that httptest.NewRequest accepts literally —
	// characters like ' ' or '%00' fail at URL parsing before reaching
	// the handler.
	s, _ := newTestStore(t)
	for _, bad := range []string{"bad.id", "with@sign", "plus+sign"} {
		req := httptest.NewRequest(http.MethodGet, "/api/chats/"+bad, nil)
		rec := httptest.NewRecorder()
		NewRouter(s).handleOne(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("handleOne(%q) = %d, want 400", bad, rec.Code)
		}
	}
}

// --- Additional coverage for chat-id guards and error-path plumbing ---

// skipIfRoot skips the current test when the effective UID is 0.
// Root bypasses POSIX file permissions, so chmod-to-readonly tests
// designed to trip EACCES on os.Remove produce false negatives
// (remove succeeds, branch never executes). CI runs as non-root
// ubuntu; local WSL is also non-root. Docker-as-root skips.
func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("chmod-based permission tests skipped when running as root")
	}
}

func TestDelete_RejectsBadChatID(t *testing.T) {
	// Parallel to TestMutate_RejectsBadChatID; pathFor in Delete
	// guards against directory-traversal ids before any filesystem op.
	// A refactor that moves the check after os.Remove would silently
	// lose the pre-validation contract, so lock the current behaviour:
	// every invalid id produces an error AND zero broadcasts.
	s, b := newTestStore(t)
	assertRejectsBadChatIDs(t, func(id api.ChatID) error {
		return s.Delete(context.Background(), id)
	})
	// Defensive assertion: rejected ids never emit chat_deleted.
	// A broken refactor that moves pathFor after os.Remove would
	// pass the error tests but still broadcast — catch it here.
	if evs := b.snapshot(); len(evs) != 0 {
		t.Errorf("invalid chat id deletes broadcast events: %+v", evs)
	}
}

func TestGetPlanDraft_RejectsOversizeFile(t *testing.T) {
	// 256 KiB cap is a resource-exhaustion control: a draft file
	// planted out-of-band (e.g. via volume mount) must not OOM
	// the process when handlePlanDraft GET reads it. Sparse-file
	// via os.Truncate avoids writing 256 KiB of real bytes.
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	draftPath, err := s.planDraftPathFor("c1")
	if err != nil {
		t.Fatalf("planDraftPathFor: %v", err)
	}
	f, err := os.Create(draftPath)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = f.Close()
	// One byte over the cap so the branch strictly triggers.
	if err := os.Truncate(draftPath, int64(maxPlanDraftBytes)+1); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	_, err = s.GetPlanDraft(context.Background(), "c1")
	if err == nil {
		t.Fatal("GetPlanDraft(oversize) = nil error, want size-cap error")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("err = %q, want mention of 'too large'", err.Error())
	}
}

func TestHandlePlanDraft_GETReturnsGenericSentinelOnReadFailure(t *testing.T) {
	// When GetPlanDraft returns an error (e.g. oversize file
	// planted out-of-band), the handler must log the raw error via
	// slog.Error and return a 500 with a generic "read failed"
	// sentinel. Mirrors api.ServeJSONFile's path-leak-avoidance
	// discipline; regressing this would leak filesystem paths to
	// HTTP clients.
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	draftPath, err := s.planDraftPathFor("c1")
	if err != nil {
		t.Fatalf("planDraftPathFor: %v", err)
	}
	f, err := os.Create(draftPath)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = f.Close()
	if err := os.Truncate(draftPath, int64(maxPlanDraftBytes)+1); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/chats/c1/plan-draft", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("code = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "read failed") {
		t.Errorf("body = %q, want 'read failed' sentinel", rec.Body.String())
	}
	// Path-leak defence: the full disk path of the draft file
	// must not appear in the response body — the whole point of
	// the sentinel is to hide it.
	if strings.Contains(rec.Body.String(), draftPath) {
		t.Errorf("response leaked draft path: %q", rec.Body.String())
	}
	// Size-cap message is also internal — must not be echoed.
	if strings.Contains(rec.Body.String(), "plan draft") &&
		strings.Contains(rec.Body.String(), "too large") {
		t.Errorf("response leaked store-level error: %q", rec.Body.String())
	}
}

func TestDeletePlanDraft_SurfacesNonENOENTRemoveError(t *testing.T) {
	skipIfRoot(t)
	// DeletePlanDraft must propagate non-ENOENT remove errors so
	// handlePlanDraft's DELETE branch can log + return a 500
	// "delete failed" sentinel. A silent-swallow refactor would
	// turn orphan-draft IO failures into false-success responses.
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	if err := s.SetPlanDraft(context.Background(), "c1", "# p"); err != nil {
		t.Fatal(err)
	}
	// Readonly parent dir → os.Remove(draft) fails with EACCES.
	if err := os.Chmod(s.dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	// Restore before t.TempDir() cleanup runs.
	t.Cleanup(func() { _ = os.Chmod(s.dir, 0o700) })

	err := s.DeletePlanDraft(context.Background(), "c1")
	if err == nil {
		t.Fatal("DeletePlanDraft on readonly dir = nil error, want EACCES")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, unexpectedly ENOENT (file should still exist)", err)
	}
}

func TestHandlePlanDraft_DELETEReturnsGenericSentinelOnFailure(t *testing.T) {
	skipIfRoot(t)
	// The handler's DELETE branch must log raw error via slog.Error
	// and return 500 {"error":"delete failed"} — no OS-level path
	// or errno strings allowed in the response body. Mirrors the
	// read-failed / save-failed path-leak-avoidance discipline
	// established in api.ServeJSONFile and reiterated at GET
	// (see TestHandlePlanDraft_GETReturnsGenericSentinelOnReadFailure).
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	if err := s.SetPlanDraft(context.Background(), "c1", "# p"); err != nil {
		t.Fatal(err)
	}
	draftPath, _ := s.planDraftPathFor("c1")
	if err := os.Chmod(s.dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(s.dir, 0o700) })

	req := httptest.NewRequest(http.MethodDelete, "/api/chats/c1/plan-draft", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("code = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "delete failed") {
		t.Errorf("body = %q, want 'delete failed' sentinel", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), draftPath) {
		t.Errorf("response leaked draft path: %q", rec.Body.String())
	}
	// OS-level errno strings (EACCES/permission denied) are the
	// other half of the path-leak surface — must also be hidden.
	if strings.Contains(rec.Body.String(), "permission denied") {
		t.Errorf("response leaked OS errno string: %q", rec.Body.String())
	}
}

func TestDelete_SurfacesNonENOENTChatRemoveError(t *testing.T) {
	skipIfRoot(t)
	// Parallel to TestDeletePlanDraft_SurfacesNonENOENTRemoveError,
	// but on the chat file itself. Delete must return the rmErr
	// verbatim when it's neither ENOENT nor nil so upstream handlers
	// can distinguish "chat gone" (no-op) from "filesystem broken"
	// (surface to operator).
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	if err := os.Chmod(s.dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(s.dir, 0o700) })

	err := s.Delete(context.Background(), "c1")
	if err == nil {
		t.Fatal("Delete on readonly parent dir = nil error, want EACCES")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, unexpectedly ENOENT (file should still exist)", err)
	}
}

// --- Archive lifecycle ---

func TestArchive_MovesFileAndBroadcastsAndTombstones(t *testing.T) {
	s, b := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "Archived"
		return true
	})
	b.reset()

	if err := s.Archive(context.Background(), "c1"); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	// Source file gone from active dir.
	if _, ok := s.Get(context.Background(), "c1"); ok {
		t.Error("chat still readable via Get after Archive")
	}
	// Landed in archive subdir.
	archivePath := filepath.Join(s.dir, "archive", "c1.json")
	if _, err := os.Stat(archivePath); err != nil {
		t.Errorf("archive file missing: %v", err)
	}
	// Tombstoned so a racing Mutate can't resurrect the id.
	if !s.isTombstoned("c1") {
		t.Error("Archive did not tombstone the chat id")
	}
	// chat_deleted broadcast (same event the UI uses for delete).
	evs := b.snapshot()
	if len(evs) != 1 || evs[0].Type != "chat_deleted" {
		t.Errorf("events = %+v, want single chat_deleted", evs)
	}
}

func TestArchive_InvokesOnArchiveCallback(t *testing.T) {
	s, _ := newTestStore(t)
	var called atomic.Int32
	var gotID atomic.Value
	WithOnArchive(func(id api.ChatID) {
		called.Add(1)
		gotID.Store(id)
	})(s)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	if err := s.Archive(context.Background(), "c1"); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	if got := called.Load(); got != 1 {
		t.Errorf("onArchive called %d times, want 1", got)
	}
	if got, _ := gotID.Load().(api.ChatID); got != "c1" {
		t.Errorf("onArchive id = %q, want %q", got, "c1")
	}
}

func TestArchive_AlsoMovesPlanDraft(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	if err := s.SetPlanDraft(context.Background(), "c1", "# plan"); err != nil {
		t.Fatal(err)
	}

	if err := s.Archive(context.Background(), "c1"); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	// Draft should follow the chat into the archive directory so
	// RestoreArchived can bring both back together.
	activeDraft := filepath.Join(s.dir, "c1.plan.md")
	if _, err := os.Stat(activeDraft); !os.IsNotExist(err) {
		t.Errorf("plan-draft still in active dir after Archive: %v", err)
	}
	archivedDraft := filepath.Join(s.dir, "archive", "c1.plan.md")
	if _, err := os.Stat(archivedDraft); err != nil {
		t.Errorf("plan-draft not moved to archive: %v", err)
	}
}

func TestArchive_UsesRestrictiveDirMode(t *testing.T) {
	// The archive dir must be 0o700 (same as the parent chats dir)
	// because archived chats carry the same sensitive content.
	s, _ := newTestStore(t)
	if runtimeSkipChmodCheck() {
		t.Skip("filesystem does not honor chmod bits")
	}
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	if err := s.Archive(context.Background(), "c1"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(s.dir, "archive"))
	if err != nil {
		t.Fatalf("stat archive dir: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("archive dir mode = %#o, want 0o700", got)
	}
}

func TestArchive_MissingChatReturnsError(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.Archive(context.Background(), "c-never-existed"); err == nil {
		t.Fatal("Archive on missing chat = nil error, want non-nil")
	}
}

func TestArchive_RejectsBadChatID(t *testing.T) {
	s, _ := newTestStore(t)
	assertRejectsBadChatIDs(t, func(id api.ChatID) error {
		return s.Archive(context.Background(), id)
	})
}

func TestListArchived_ReturnsHeadersSortedByUpdatedAtDesc(t *testing.T) {
	s, _ := newTestStore(t)
	for _, id := range []string{"a", "b", "c"} {
		_ = s.Mutate(context.Background(), api.ChatID(id), func(c *api.Chat, _ bool) bool {
			c.Name = "Name-" + id
			return true
		})
		if err := s.Archive(context.Background(), api.ChatID(id)); err != nil {
			t.Fatalf("Archive(%q): %v", id, err)
		}
		// Force a distinct mtime per file so the sort has something
		// to order on; without the sleep, three rapid Archive calls
		// may land within the filesystem's timestamp granularity.
		time.Sleep(5 * time.Millisecond)
	}

	headers := s.ListArchived(context.Background())
	if len(headers) != 3 {
		t.Fatalf("len = %d, want 3", len(headers))
	}
	for i := 1; i < len(headers); i++ {
		if headers[i-1].UpdatedAt < headers[i].UpdatedAt {
			t.Errorf("not sorted desc: %+v", headers)
		}
	}
	ids := map[string]bool{}
	for _, h := range headers {
		ids[h.ID] = true
	}
	for _, want := range []string{"a", "b", "c"} {
		if !ids[want] {
			t.Errorf("missing archived chat %q in %+v", want, headers)
		}
	}
}

func TestListArchived_EmptyDirReturnsEmptySlice(t *testing.T) {
	// ListArchived now returns a non-nil empty slice so JSON marshals
	// as `[]` rather than `null` (see TestHandleArchived_…NotNull).
	s, _ := newTestStore(t)
	got := s.ListArchived(context.Background())
	if got == nil {
		t.Fatal("ListArchived(no archive dir) = nil, want []")
	}
	if len(got) != 0 {
		t.Errorf("ListArchived(no archive dir) = %v, want empty slice", got)
	}
}

func TestListArchived_SkipsNonJSONAndMalformed(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "good", func(c *api.Chat, _ bool) bool { c.Name = "good"; return true })
	if err := s.Archive(context.Background(), "good"); err != nil {
		t.Fatal(err)
	}
	archiveDir := filepath.Join(s.dir, "archive")
	// Plain-text file and malformed JSON must both be skipped
	// without breaking the listing of the valid entry.
	if err := os.WriteFile(filepath.Join(archiveDir, "note.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archiveDir, "busted.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	headers := s.ListArchived(context.Background())

	if len(headers) != 1 || headers[0].ID != "good" {
		t.Errorf("headers = %+v, want only the good archived chat", headers)
	}
}

func TestListArchived_SkipsOversizeFiles(t *testing.T) {
	// A file larger than maxChatFileBytes must be skipped with a warn,
	// not loaded into memory. Protects against an out-of-band writer
	// (e.g. rogue sync tool) planting a giant file in the archive.
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "good", func(c *api.Chat, _ bool) bool { c.Name = "good"; return true })
	if err := s.Archive(context.Background(), "good"); err != nil {
		t.Fatal(err)
	}
	archiveDir := filepath.Join(s.dir, "archive")
	oversize := make([]byte, maxChatFileBytes+1)
	if err := os.WriteFile(filepath.Join(archiveDir, "huge.json"), oversize, 0o600); err != nil {
		t.Fatal(err)
	}

	headers := s.ListArchived(context.Background())

	if len(headers) != 1 || headers[0].ID != "good" {
		t.Errorf("headers = %+v, want only the good archived chat", headers)
	}
}

func TestRestoreArchived_RoundTripsChatAndDraft(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	if err := s.SetPlanDraft(context.Background(), "c1", "# plan"); err != nil {
		t.Fatal(err)
	}
	if err := s.Archive(context.Background(), "c1"); err != nil {
		t.Fatal(err)
	}

	if err := s.RestoreArchived(context.Background(), "c1"); err != nil {
		t.Fatalf("RestoreArchived: %v", err)
	}

	got, ok := s.Get(context.Background(), "c1")
	if !ok {
		t.Fatal("chat not restored to active dir")
	}
	if got.Name != "A" {
		t.Errorf("name = %q, want A", got.Name)
	}
	draft, err := s.GetPlanDraft(context.Background(), "c1")
	if err != nil {
		t.Fatalf("GetPlanDraft: %v", err)
	}
	if draft != "# plan" {
		t.Errorf("draft = %q, want %q", draft, "# plan")
	}
}

func TestRestoreArchived_ClearsTombstoneAndBroadcasts(t *testing.T) {
	// Archive tombstones the id to block Mutate resurrection. Restore
	// must clear the tombstone (it's the explicit un-delete) and emit
	// chat_created so all connected devices see the sidebar update
	// without a manual refresh.
	s, b := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	if err := s.Archive(context.Background(), "c1"); err != nil {
		t.Fatal(err)
	}
	if !s.isTombstoned("c1") {
		t.Fatal("precondition: Archive should tombstone the id")
	}
	b.reset()

	if err := s.RestoreArchived(context.Background(), "c1"); err != nil {
		t.Fatalf("RestoreArchived: %v", err)
	}

	if s.isTombstoned("c1") {
		t.Error("tombstone still set after RestoreArchived")
	}
	evs := b.snapshot()
	if len(evs) != 1 || evs[0].Type != "chat_created" {
		t.Errorf("events = %+v, want single chat_created", evs)
	}
}

func TestRestoreArchived_MissingEntryReturnsError(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.RestoreArchived(context.Background(), "c-never-archived"); err == nil {
		t.Fatal("RestoreArchived on missing entry = nil error, want non-nil")
	}
}

func TestRestoreArchived_RejectsBadChatID(t *testing.T) {
	// Regression: RestoreArchived used to skip chatIDPattern validation
	// entirely, letting a crafted id like "../foo" escape the archive
	// directory via os.Rename. Every path-composing method now gates on
	// the pattern check before touching the filesystem.
	s, _ := newTestStore(t)
	assertRejectsBadChatIDs(t, func(id api.ChatID) error {
		return s.RestoreArchived(context.Background(), id)
	})
}

func TestUpdateArchivedSummary_PersistsSummary(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	if err := s.Archive(context.Background(), "c1"); err != nil {
		t.Fatal(err)
	}

	const summary = "Summarizes config refactor across 3 files"
	if err := s.UpdateArchivedSummary(context.Background(), "c1", summary); err != nil {
		t.Fatalf("UpdateArchivedSummary: %v", err)
	}

	if err := s.RestoreArchived(context.Background(), "c1"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(context.Background(), "c1")
	if got.Summary != summary {
		t.Errorf("Summary = %q, want %q", got.Summary, summary)
	}
}

func TestUpdateArchivedSummary_RejectsBadChatID(t *testing.T) {
	s, _ := newTestStore(t)
	assertRejectsBadChatIDs(t, func(id api.ChatID) error {
		return s.UpdateArchivedSummary(context.Background(), id, "x")
	})
}

func TestUpdateArchivedSummary_MissingFileReturnsError(t *testing.T) {
	s, _ := newTestStore(t)
	err := s.UpdateArchivedSummary(context.Background(), "c-never-archived", "x")
	if err == nil {
		t.Fatal("UpdateArchivedSummary on missing entry = nil error, want non-nil")
	}
	if !strings.Contains(err.Error(), "load archived chat") {
		t.Errorf("err = %q, want wrapping %q", err.Error(), "load archived chat")
	}
}

func TestUpdateArchivedSummary_RejectsMalformedJSON(t *testing.T) {
	s, _ := newTestStore(t)
	archiveDir := filepath.Join(s.dir, "archive")
	if err := os.MkdirAll(archiveDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archiveDir, "c1.json"), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := s.UpdateArchivedSummary(context.Background(), "c1", "x")
	if err == nil {
		t.Fatal("UpdateArchivedSummary on corrupt archive JSON = nil error, want non-nil")
	}
	if !strings.Contains(err.Error(), "load archived chat") {
		t.Errorf("err = %q, want %q prefix", err.Error(), "load archived chat")
	}
}

func TestPurgeArchived_RemovesAgedOutEntries(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "old", func(c *api.Chat, _ bool) bool { c.Name = "old"; return true })
	_ = s.Mutate(context.Background(), "new", func(c *api.Chat, _ bool) bool { c.Name = "new"; return true })
	if err := s.Archive(context.Background(), "old"); err != nil {
		t.Fatal(err)
	}
	if err := s.Archive(context.Background(), "new"); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(s.dir, "archive", "old.json")
	ageArchivedChat(t, s, "old", 48*time.Hour)

	s.PurgeArchived(context.Background(), 24*time.Hour)

	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf("aged-out entry survived purge: stat err = %v", err)
	}
	newPath := filepath.Join(s.dir, "archive", "new.json")
	if _, err := os.Stat(newPath); err != nil {
		t.Errorf("fresh entry removed by purge: %v", err)
	}
}

func TestPurgeArchived_AlsoRemovesPlanDraft(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	if err := s.SetPlanDraft(context.Background(), "c1", "# plan"); err != nil {
		t.Fatal(err)
	}
	if err := s.Archive(context.Background(), "c1"); err != nil {
		t.Fatal(err)
	}
	archiveDir := filepath.Join(s.dir, "archive")
	ageArchivedChat(t, s, "c1", 48*time.Hour)

	s.PurgeArchived(context.Background(), 24*time.Hour)

	if _, err := os.Stat(filepath.Join(archiveDir, "c1.plan.md")); !os.IsNotExist(err) {
		t.Errorf("orphan plan-draft left after purge: stat err = %v", err)
	}
}

func TestPurgeArchived_InvokesOnPurgeCallback(t *testing.T) {
	s, _ := newTestStore(t)
	var called atomic.Int32
	var gotID atomic.Value
	WithOnPurge(func(id api.ChatID) {
		called.Add(1)
		gotID.Store(id)
	})(s)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = s.Archive(context.Background(), "c1")
	ageArchivedChat(t, s, "c1", 48*time.Hour)

	s.PurgeArchived(context.Background(), 24*time.Hour)

	if got := called.Load(); got != 1 {
		t.Errorf("onPurge called %d times, want 1", got)
	}
	if got, _ := gotID.Load().(api.ChatID); got != "c1" {
		t.Errorf("onPurge id = %q, want %q", got, "c1")
	}
}

func TestPurgeArchived_KeepsRecentEntries(t *testing.T) {
	// Boundary: entry within retention window stays.
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	if err := s.Archive(context.Background(), "c1"); err != nil {
		t.Fatal(err)
	}

	s.PurgeArchived(context.Background(), 24*time.Hour)

	if _, err := os.Stat(filepath.Join(s.dir, "archive", "c1.json")); err != nil {
		t.Errorf("recent entry was wrongly purged: %v", err)
	}
}

// runtimeSkipChmodCheck reports whether the test filesystem silently
// ignores chmod bits (Windows via WSL can present 0o777 for bind mounts
// regardless of stored perms). Mirrors the existing skipIfRoot pattern
// used elsewhere in this file.
func runtimeSkipChmodCheck() bool {
	dir, err := os.MkdirTemp("", "chmod-probe-*")
	if err != nil {
		return true
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.Chmod(dir, 0o700); err != nil {
		return true
	}
	info, err := os.Stat(dir)
	if err != nil {
		return true
	}
	return info.Mode().Perm() != 0o700
}

// --- handleExport ---

func TestHandleExport_JSONFormatReturnsChatJSON(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "Named Chat"
		c.Messages = []api.Message{
			{ID: "m1", Role: api.RoleUser, Content: "hi"},
		}
		return true
	})

	req := httptest.NewRequest(http.MethodGet, "/api/chats/c1/export?format=json", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	disp := rec.Header().Get("Content-Disposition")
	if !strings.Contains(disp, `attachment`) || !strings.Contains(disp, "Named Chat-c1.json") {
		t.Errorf("Content-Disposition = %q, want attachment with <name>-<id>.json", disp)
	}
	if !strings.Contains(rec.Body.String(), `"role":"user"`) {
		t.Errorf("body = %q, want full chat including messages", rec.Body.String())
	}
}

func TestHandleExport_MarkdownIsDefaultFormat(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "Named Chat"
		c.Messages = []api.Message{
			{ID: "m1", Role: api.RoleUser, Content: "hi"},
		}
		return true
	})

	// No ?format= param — Markdown is the default.
	req := httptest.NewRequest(http.MethodGet, "/api/chats/c1/export", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/markdown") {
		t.Errorf("Content-Type = %q, want text/markdown", ct)
	}
	disp := rec.Header().Get("Content-Disposition")
	if !strings.Contains(disp, `attachment`) || !strings.Contains(disp, "Named Chat-c1.md") {
		t.Errorf("Content-Disposition = %q, want attachment with <name>-<id>.md", disp)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "# Named Chat") || !strings.Contains(body, "## User") {
		t.Errorf("body = %q, want Markdown transcript with title and role headings", body)
	}
}

func TestHandleExport_RejectsUnsupportedFormat(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	req := httptest.NewRequest(http.MethodGet, "/api/chats/c1/export?format=xml", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400 for unsupported format", rec.Code)
	}
}

func TestHandleExport_ServesArchivedChat(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "Archived One"
		c.Messages = []api.Message{{ID: "m1", Role: api.RoleUser, Content: "hi"}}
		return true
	})
	if err := s.Archive(context.Background(), "c1"); err != nil {
		t.Fatalf("archive: %v", err)
	}
	// The chat is no longer active, but export falls back to the archive dir.
	if _, ok := s.Get(context.Background(), "c1"); ok {
		t.Fatal("precondition: chat should be archived, not active")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/chats/c1/export?format=md", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 for archived chat export", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "# Archived One") {
		t.Errorf("body = %q, want Markdown transcript for the archived chat", rec.Body.String())
	}
}

func TestHandleExport_FallsBackToChatIDWhenNameEmpty(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { return true })

	req := httptest.NewRequest(http.MethodGet, "/api/chats/c1/export", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	disp := rec.Header().Get("Content-Disposition")
	if !strings.Contains(disp, "c1.md") {
		t.Errorf("Content-Disposition = %q, want chat id fallback filename", disp)
	}
}

func TestHandleExport_NotFoundForMissingChat(t *testing.T) {
	s, _ := newTestStore(t)
	req := httptest.NewRequest(http.MethodGet, "/api/chats/c1/export", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rec.Code)
	}
}

func TestHandleExport_RejectsNonGET(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/api/chats/c1/export", nil)
		rec := httptest.NewRecorder()
		NewRouter(s).handleOne(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("handleExport(%s) = %d, want 405", method, rec.Code)
		}
	}
}

func TestHandleExport_RejectsInvalidChatID(t *testing.T) {
	s, _ := newTestStore(t)
	req := httptest.NewRequest(http.MethodGet, "/api/chats/bad.id/export", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
}

func TestHandleExport_SanitisesAdversarialChatName(t *testing.T) {
	// Regression: chat names with quotes, CR/LF, or path chars used to
	// break the Content-Disposition header via string concatenation.
	// mime.FormatMediaType + safeExportName now handle all of these.
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "evil\"; filename=\"spoof"
		return true
	})

	req := httptest.NewRequest(http.MethodGet, "/api/chats/c1/export", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	disp := rec.Header().Get("Content-Disposition")
	if strings.Contains(disp, `filename="spoof`) && !strings.Contains(disp, `filename=`) {
		t.Errorf("header leaked injected filename= param: %q", disp)
	}
	// The sanitiser must have replaced the embedded double-quote with
	// an underscore so the header stays well-formed.
	if strings.Count(disp, `"`)%2 != 0 {
		t.Errorf("Content-Disposition has unbalanced quotes: %q", disp)
	}
}

// --- handleArchive ---

func TestHandleArchive_POSTMovesChat(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	req := httptest.NewRequest(http.MethodPost, "/api/chats/c1/archive", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	if _, ok := s.Get(context.Background(), "c1"); ok {
		t.Error("chat still active after archive POST")
	}
	if _, err := os.Stat(filepath.Join(s.dir, "archive", "c1.json")); err != nil {
		t.Errorf("archive file missing: %v", err)
	}
}

func TestHandleArchive_RejectsNonPOST(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req := httptest.NewRequest(method, "/api/chats/c1/archive", nil)
		rec := httptest.NewRecorder()
		NewRouter(s).handleOne(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("handleArchive(%s) = %d, want 405", method, rec.Code)
		}
	}
}

func TestHandleArchive_RejectsInvalidChatID(t *testing.T) {
	s, _ := newTestStore(t)
	req := httptest.NewRequest(http.MethodPost, "/api/chats/bad.id/archive", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
}

func TestHandleArchive_MissingChatReturns500(t *testing.T) {
	// Unlike delete (idempotent), archive surfaces the rename error.
	// The handler maps it to InternalError so operators can spot
	// orphan archive attempts in the logs.
	s, _ := newTestStore(t)
	req := httptest.NewRequest(http.MethodPost, "/api/chats/c-never/archive", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("code = %d, want 500", rec.Code)
	}
}

// --- exportFilename ---

func TestExportFilename(t *testing.T) {
	tests := []struct {
		name, id, ext, want string
	}{
		{"", "c1", ".md", "c1.md"},
		{"hello", "c1", ".md", "hello-c1.md"},
		{"Named Chat", "c1", ".json", "Named Chat-c1.json"},
		{"bad/name", "c1", ".md", "bad_name-c1.md"},
		{"with\"quote", "c1", ".md", "with_quote-c1.md"},
		{"win<bad>:chars", "c1", ".md", "win_bad__chars-c1.md"},
		{"   ", "c1", ".md", "c1.md"},
		{"\x01\x02ctrl", "c1", ".md", "__ctrl-c1.md"},
		{"", "", ".md", "chat.md"},
	}
	for _, tc := range tests {
		if got := exportFilename(tc.name, tc.id, tc.ext); got != tc.want {
			t.Errorf("exportFilename(%q, %q, %q) = %q, want %q",
				tc.name, tc.id, tc.ext, got, tc.want)
		}
	}
	// Rune cap on the name stem (id and ext are appended after the cap).
	got := exportFilename(strings.Repeat("x", 200), "c1", ".md")
	if len([]rune(got)) > 80+len("-c1.md") {
		t.Errorf("len(exportFilename(200x)) = %d runes, want name stem capped", len([]rune(got)))
	}
}

// --- Oldest checkpoint tag enrichment ---

func TestHeader_PopulatesOldestCheckpointTagViaCallback(t *testing.T) {
	// The store derives ChatHeader.OldestCheckpointTag from a callback
	// wired at startup (main.go -> h.CheckpointOldestTag). Every header
	// broadcast and List/Get response flows through header(); a silent
	// break (setter becomes no-op, header drops the branch) would ship
	// empty tags to the client and regress the sidebar's Restore-button
	// gating. Pin both: the setter installs the callback, and header()
	// calls it with the correct chat id and writes the result onto the
	// header.
	s, _ := newTestStore(t)

	var mu sync.Mutex
	var gotCalls []string
	WithOldestCheckpointFn(func(ctx context.Context, chatID api.ChatID) string {
		mu.Lock()
		gotCalls = append(gotCalls, string(chatID))
		mu.Unlock()
		if chatID == "c1" {
			return "7.2"
		}
		return ""
	})(s)

	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = s.Mutate(context.Background(), "c2", func(c *api.Chat, _ bool) bool { c.Name = "B"; return true })

	headers := s.List(context.Background())
	if len(headers) != 2 {
		t.Fatalf("len(headers) = %d, want 2", len(headers))
	}
	// Map by id so test isn't order-dependent (List sorts by UpdatedAt
	// desc, which can flip on fast machines with identical ms stamps).
	byID := map[string]string{}
	for _, h := range headers {
		byID[h.ID] = h.OldestCheckpointTag
	}
	if got := byID["c1"]; got != "7.2" {
		t.Errorf("c1 OldestCheckpointTag = %q, want %q", got, "7.2")
	}
	if got := byID["c2"]; got != "" {
		t.Errorf("c2 OldestCheckpointTag = %q, want empty (callback returns empty)", got)
	}
	mu.Lock()
	n := len(gotCalls)
	mu.Unlock()
	if n < 2 {
		t.Errorf("callback invocation count = %d, want >=2 (one per chat)", n)
	}
}

func TestHeader_NilCallbackLeavesTagEmpty(t *testing.T) {
	// Default store (no SetOldestCheckpointFn call) must leave the tag
	// empty. Pins the nil-guard in header() so a future refactor that
	// assumes the callback is always set would fail loudly.
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	headers := s.List(context.Background())
	if len(headers) != 1 {
		t.Fatalf("len = %d, want 1", len(headers))
	}
	if headers[0].OldestCheckpointTag != "" {
		t.Errorf("OldestCheckpointTag = %q, want empty when no callback wired",
			headers[0].OldestCheckpointTag)
	}
}

func TestHeader_CallbackOverwritesPersistedTag(t *testing.T) {
	// The OldestCheckpointTag is derived on every header build — not
	// trusted from disk. Even if a stale value survived in a chat file,
	// header() must overwrite it with the live callback result. Pins
	// the authoritative-source invariant.
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.OldestCheckpointTag = "stale-5.1" // would leak without the callback overwrite
		return true
	})
	WithOldestCheckpointFn(func(context.Context, api.ChatID) string { return "live-8.3" })(s)

	headers := s.List(context.Background())
	if len(headers) != 1 {
		t.Fatalf("len = %d", len(headers))
	}
	if headers[0].OldestCheckpointTag != "live-8.3" {
		t.Errorf("header OldestCheckpointTag = %q, want %q (live callback must override)",
			headers[0].OldestCheckpointTag, "live-8.3")
	}
}

// --- LoadArchived ---

func TestLoadArchived_RoundTripsArchivedChat(t *testing.T) {
	// Public API entry point used by the hub's summariser
	// (hub/chat_summary.go). Must round-trip the full api.Chat, not
	// just the header, so the summariser has access to messages for
	// utility-bridge prompting.
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "Before archive"
		c.Messages = []api.Message{
			{ID: "m1", Role: api.RoleUser, Content: "hi", Ts: 100},
			{ID: "m2", Role: api.RoleAssistant, Content: "there", Ts: 200},
		}
		return true
	})
	if err := s.Archive(context.Background(), "c1"); err != nil {
		t.Fatal(err)
	}

	got, err := s.LoadArchived(context.Background(), "c1")
	if err != nil {
		t.Fatalf("LoadArchived: %v", err)
	}
	if got == nil {
		t.Fatal("LoadArchived returned nil chat with nil error")
	}
	if got.ID != "c1" || got.Name != "Before archive" {
		t.Errorf("chat = %+v, want id=c1 name=%q", got, "Before archive")
	}
	if len(got.Messages) != 2 || got.Messages[0].Content != "hi" {
		t.Errorf("messages = %+v, want full history", got.Messages)
	}
}

func TestLoadArchived_MissingReturnsErrNotExist(t *testing.T) {
	// Callers (hub/chat_summary.go) branch on errors.Is(err,
	// os.ErrNotExist) to distinguish "chat was never archived" (no-op)
	// from "filesystem broken" (surface). Pin the contract so a
	// refactor that wraps the ENOENT can't silently widen the error
	// class.
	s, _ := newTestStore(t)
	_, err := s.LoadArchived(context.Background(), "c-never-archived")
	if err == nil {
		t.Fatal("LoadArchived on missing entry = nil error, want os.ErrNotExist")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want errors.Is(err, os.ErrNotExist)", err)
	}
}

func TestLoadArchived_RejectsBadChatID(t *testing.T) {
	// Defence in depth: LoadArchived's chatIDPattern check gates the
	// path construction in loadArchived. A future refactor that inlines
	// loadArchived must preserve the guard to block traversal ids like
	// "../foo" from reaching os.Open on arbitrary filesystem paths.
	s, _ := newTestStore(t)
	assertRejectsBadChatIDs(t, func(id api.ChatID) error {
		_, err := s.LoadArchived(context.Background(), id)
		return err
	})
}

func TestLoadArchived_SerialisesWithUpdateArchivedSummary(t *testing.T) {
	// LoadArchived takes the per-chat mutex; UpdateArchivedSummary
	// takes the same mutex. This is the contract that keeps the
	// summariser goroutine from reading a torn chat file mid-rewrite.
	// N concurrent Load + Update pairs must all succeed with no
	// corrupted reads.
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.Messages = []api.Message{{ID: "m1", Role: api.RoleUser, Content: "body", Ts: 1}}
		return true
	})
	if err := s.Archive(context.Background(), "c1"); err != nil {
		t.Fatal(err)
	}

	const N = 20
	var wg sync.WaitGroup
	wg.Add(2 * N)
	errs := make(chan error, 2*N)
	for i := range N {
		go func(i int) {
			defer wg.Done()
			got, err := s.LoadArchived(context.Background(), "c1")
			if err != nil {
				errs <- fmt.Errorf("load #%d: %w", i, err)
				return
			}
			if got.ID != "c1" {
				errs <- fmt.Errorf("load #%d: id = %q, want c1", i, got.ID)
			}
		}(i)
		go func(i int) {
			defer wg.Done()
			if err := s.UpdateArchivedSummary(context.Background(), "c1", fmt.Sprintf("summary %d", i)); err != nil {
				errs <- fmt.Errorf("update #%d: %w", i, err)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// --- RestoreArchived id-collision guard ---

func TestRestoreArchived_RefusesToOverwriteActiveChat(t *testing.T) {
	// Regression: RestoreArchived used to os.Rename unconditionally,
	// which silently replaced an active chat file whose id matched the
	// archived one (typical after the tombstone expired and the id was
	// recycled). Now surfaces Kind=ErrKindIDInUse so the caller can
	// show a 409 toast instead of silent data loss.
	s, _ := newTestStore(t)
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "Original"
		c.Messages = []api.Message{{ID: "m1", Role: api.RoleUser, Content: "original", Ts: 1}}
		return true
	})
	if err := s.Archive(context.Background(), "c1"); err != nil {
		t.Fatal(err)
	}
	// Recreate an active chat at the same id. Have to clear the
	// tombstone first because Archive set it (Mutate auto-create would
	// otherwise refuse); the real-world equivalent is waiting 10 min.
	s.tombMu.Lock()
	delete(s.tombstone, "c1")
	s.tombMu.Unlock()
	_ = s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "Recycled"
		c.Messages = []api.Message{{ID: "m2", Role: api.RoleUser, Content: "recycled", Ts: 2}}
		return true
	})

	// Attempting to restore the archived c1 onto the active c1 must
	// fail with Kind=ErrKindIDInUse — not silently overwrite.
	err := s.RestoreArchived(context.Background(), "c1")
	var ce *StoreError
	if !errors.As(err, &ce) || ce.Kind != ErrKindIDInUse {
		t.Fatalf("RestoreArchived: err = %v, want *StoreError{Kind: ErrKindIDInUse}", err)
	}

	// The active chat must be intact.
	got, ok := s.Get(context.Background(), "c1")
	if !ok {
		t.Fatal("active chat disappeared after refused restore")
	}
	if got.Name != "Recycled" || len(got.Messages) != 1 || got.Messages[0].Content != "recycled" {
		t.Errorf("active chat clobbered: name=%q messages=%+v", got.Name, got.Messages)
	}
}

func FuzzCountJSONArrayElements(f *testing.F) {
	// Seed corpus: known shapes.
	f.Add([]byte(`[]`))
	f.Add([]byte(`null`))
	f.Add([]byte(`[1]`))
	f.Add([]byte(`[1,2,3,4,5]`))
	f.Add([]byte(`[{"a":1},{"b":2}]`))
	f.Add([]byte(`[[],"x",null,true,123]`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`"string"`))
	f.Add([]byte(`[`))     // truncated
	f.Add([]byte(`[1,2,`)) // truncated mid-array
	f.Add([]byte(``))      // empty
	f.Add([]byte(`[null]`))

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must never panic and must return >= 0.
		n := countJSONArrayElements(data)
		if n < 0 {
			t.Errorf("countJSONArrayElements returned %d < 0", n)
		}
	})
}

// --- Benchmarks ---

func BenchmarkStoreMutate(b *testing.B) {
	appendMsg := func(n int) func(c *api.Chat, _ bool) bool {
		return func(c *api.Chat, _ bool) bool {
			c.Name = "bench"
			c.Messages = append(c.Messages, api.Message{
				ID:      fmt.Sprintf("m%d", n),
				Role:    api.RoleUser,
				Content: "benchmark message content that is reasonably sized",
			})
			return true
		}
	}

	for _, msgCount := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("append_message/%d_existing", msgCount), func(b *testing.B) {
			s, err := NewStore(b.TempDir())
			if err != nil {
				b.Fatalf("NewStore: %v", err)
			}
			for i := range msgCount {
				_ = s.Mutate(context.Background(), "bench", appendMsg(i))
			}
			b.ResetTimer()
			for i := range b.N {
				_ = s.Mutate(context.Background(), "bench", appendMsg(msgCount+i))
			}
		})

		b.Run(fmt.Sprintf("update_message/%d_existing", msgCount), func(b *testing.B) {
			s, err := NewStore(b.TempDir())
			if err != nil {
				b.Fatalf("NewStore: %v", err)
			}
			for i := range msgCount {
				_ = s.Mutate(context.Background(), "bench", appendMsg(i))
			}
			b.ResetTimer()
			for range b.N {
				_ = s.UpdateMessage(context.Background(), "bench", "m0", func(m *api.Message) {
					m.Content = "updated"
				})
			}
		})
	}
}

func BenchmarkStoreList(b *testing.B) {
	for _, n := range []int{10, 50, 100} {
		b.Run(fmt.Sprintf("chats=%d", n), func(b *testing.B) {
			s, err := NewStore(b.TempDir())
			if err != nil {
				b.Fatalf("NewStore: %v", err)
			}
			ctx := context.Background()
			for i := range n {
				_ = s.Mutate(ctx, api.ChatID(fmt.Sprintf("chat-%03d", i)), func(c *api.Chat, _ bool) bool {
					c.Name = fmt.Sprintf("Chat %d", i)
					return true
				})
			}
			b.ResetTimer()
			for range b.N {
				_ = s.List(ctx)
			}
		})
	}
}

func BenchmarkListArchived(b *testing.B) {
	for _, n := range []int{10, 50, 100} {
		b.Run(fmt.Sprintf("archived=%d", n), func(b *testing.B) {
			s, err := NewStore(b.TempDir())
			if err != nil {
				b.Fatalf("NewStore: %v", err)
			}
			ctx := context.Background()
			// Create and archive n chats.
			for i := range n {
				id := fmt.Sprintf("chat-%03d", i)
				_ = s.Mutate(ctx, api.ChatID(id), func(c *api.Chat, _ bool) bool {
					c.Name = fmt.Sprintf("Archived %d", i)
					return true
				})
				if err := s.Archive(ctx, api.ChatID(id)); err != nil {
					b.Fatalf("Archive: %v", err)
				}
			}
			b.ResetTimer()
			b.ReportAllocs()
			for range b.N {
				_ = s.ListArchived(ctx)
			}
		})
	}
}

func FuzzChatIDPattern(f *testing.F) {
	// Seed corpus: the 16 cases from TestChatIDPattern.
	valid := []string{
		"abc", "ABC", "01HXYZ",
		"550e8400-e29b-41d4-a716-446655440000",
		"chat-1716000000000", "a_b", "a-b",
		strings.Repeat("x", 128),
	}
	invalid := []string{
		"", "a/b", "..", "a.b", "a b", "a\x00b", "a\nb",
		strings.Repeat("x", 129),
	}
	for _, s := range valid {
		f.Add(s)
	}
	for _, s := range invalid {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, id string) {
		got := chatIDPattern(api.ChatID(id))

		// Tautology: chatIDPattern delegates to api.ValidChatID.
		if got != api.ValidChatID(id) {
			t.Errorf("chatIDPattern(%q) = %v, api.ValidChatID = %v", id, got, !got)
		}

		if got {
			// Invariant: only [A-Za-z0-9_-] and len in [1,128].
			if len(id) == 0 || len(id) > 128 {
				t.Errorf("chatIDPattern accepted id with len %d", len(id))
			}
			for _, r := range id {
				ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
					(r >= '0' && r <= '9') || r == '-' || r == '_'
				if !ok {
					t.Errorf("chatIDPattern accepted id with char %q", string(r))
				}
			}

			// Path-traversal safety: joining with a dir must not escape.
			dir := "/safe"
			joined := filepath.Join(dir, id+".json")
			if !strings.HasPrefix(joined, dir+"/") {
				t.Errorf("chatIDPattern(%q) escapes dir: joined=%q", id, joined)
			}
		}
	})
}

// --- tarch-b10-c7-p1: FuzzParallelRead ---

func FuzzParallelRead(f *testing.F) {
	f.Add([]byte(`{"id":"a","name":"x","messages":[]}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{"id":"b","messages":[{"id":"m1","role":"user","content":"hi"}]}`))
	f.Add([]byte(`not json at all`))
	f.Add([]byte(``))

	f.Fuzz(func(t *testing.T, data []byte) {
		dir := t.TempDir()
		s, err := NewStore(dir)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		// Write fuzz payload as a chat file with a valid id.
		if err := os.WriteFile(filepath.Join(s.dir, "fuzz1.json"), data, 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		// List must never panic regardless of file content.
		headers := s.List(context.Background())
		for _, h := range headers {
			if h.ID != "" && h.MessageCount > 0 {
				// If a header has a non-empty ID and messages, the JSON
				// decoded successfully — basic consistency check.
				if h.MessageCount < 0 {
					t.Errorf("negative MessageCount: %d", h.MessageCount)
				}
			}
		}
	})
}

// --- tarch-b10-c7-p6: BenchmarkStoreMutate_ArchiveRestore ---

func BenchmarkStoreMutate_ArchiveRestore(b *testing.B) {
	seedChat := func(s *Store) {
		_ = s.Mutate(context.Background(), "bench", func(c *api.Chat, _ bool) bool {
			c.Name = "bench"
			c.Messages = make([]api.Message, 50)
			for i := range 50 {
				c.Messages[i] = api.Message{
					ID: fmt.Sprintf("m%d", i), Role: api.RoleUser,
					Content: "benchmark message content that is reasonably sized for archive throughput testing",
				}
			}
			return true
		})
	}

	b.Run("Archive", func(b *testing.B) {
		for range b.N {
			b.StopTimer()
			s, err := NewStore(b.TempDir())
			if err != nil {
				b.Fatal(err)
			}
			seedChat(s)
			b.StartTimer()
			_ = s.Archive(context.Background(), "bench")
		}
	})

	b.Run("RestoreArchived", func(b *testing.B) {
		for range b.N {
			b.StopTimer()
			s, err := NewStore(b.TempDir())
			if err != nil {
				b.Fatal(err)
			}
			seedChat(s)
			_ = s.Archive(context.Background(), "bench")
			b.StartTimer()
			_ = s.RestoreArchived(context.Background(), "bench")
		}
	})

	b.Run("ArchiveRestoreCycle", func(b *testing.B) {
		s, err := NewStore(b.TempDir())
		if err != nil {
			b.Fatal(err)
		}
		seedChat(s)
		b.ResetTimer()
		for range b.N {
			_ = s.Archive(context.Background(), "bench")
			// Clear tombstone so RestoreArchived can proceed.
			s.tombMu.Lock()
			delete(s.tombstone, "bench")
			s.tombMu.Unlock()
			_ = s.RestoreArchived(context.Background(), "bench")
		}
	})
}

// --- tarch-b7-c7-p2: BenchmarkChatStore_ParallelRead ---

func BenchmarkChatStore_ParallelRead(b *testing.B) {
	const numChats = 10
	setup := func(b *testing.B) *Store {
		b.Helper()
		s, err := NewStore(b.TempDir())
		if err != nil {
			b.Fatal(err)
		}
		for i := range numChats {
			_ = s.Mutate(context.Background(), api.ChatID(fmt.Sprintf("chat-%03d", i)), func(c *api.Chat, _ bool) bool {
				c.Name = fmt.Sprintf("Chat %d", i)
				c.Messages = []api.Message{{ID: "m1", Role: api.RoleUser, Content: "hello"}}
				return true
			})
		}
		return s
	}

	b.Run("SameChatContention", func(b *testing.B) {
		s := setup(b)
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				s.Get(context.Background(), "chat-000")
			}
		})
	})

	b.Run("DifferentChatParallel", func(b *testing.B) {
		s := setup(b)
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				s.Get(context.Background(), api.ChatID(fmt.Sprintf("chat-%03d", i%numChats)))
				i++
			}
		})
	})
}

// ---------------------------------------------------------------------------
// Folded from the retired gremlins_kill_vibekit_u22 micro-file. Each test
// pins a boundary / log / branch invariant named in its function name;
// mutant-operator IDs and source line numbers were stripped.
// ---------------------------------------------------------------------------

// mustCreateChat creates an (empty) chat via the real Mutate create path so
// existence checks downstream pass.
func mustCreateChat(t *testing.T, s *Store, id api.ChatID) {
	t.Helper()
	if err := s.Mutate(context.Background(), id, func(_ *api.Chat, _ bool) bool { return true }); err != nil {
		t.Fatalf("create chat %q: %v", id, err)
	}
}

// truncToSize creates a (sparse) file of exactly size bytes at path.
func truncToSize(t *testing.T, path string, size int64) {
	t.Helper()
	f, err := os.Create(path)
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

// logRecord is one captured slog record (its message and attributes).
type logRecord struct {
	attrs map[string]slog.Value
	msg   string
}

// capHandler is a slog.Handler that records every log record for assertion.
type capHandler struct {
	mu   *sync.Mutex
	recs *[]logRecord
}

func (h capHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h capHandler) Handle(_ context.Context, r slog.Record) error {
	rec := logRecord{msg: r.Message, attrs: make(map[string]slog.Value)}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = a.Value
		return true
	})
	h.mu.Lock()
	*h.recs = append(*h.recs, rec)
	h.mu.Unlock()
	return nil
}

func (h capHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h capHandler) WithGroup(string) slog.Handler      { return h }

// captureChatLogs redirects slog.Default for the test and returns a snapshot
// accessor. Not parallel-safe; callers must not call t.Parallel().
func captureChatLogs(t *testing.T) func() []logRecord {
	t.Helper()
	var mu sync.Mutex
	recs := &[]logRecord{}
	prev := slog.Default()
	slog.SetDefault(slog.New(capHandler{mu: &mu, recs: recs}))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return func() []logRecord {
		mu.Lock()
		defer mu.Unlock()
		out := make([]logRecord, len(*recs))
		copy(out, *recs)
		return out
	}
}

func hasLogMsg(recs []logRecord, msg string) bool {
	for _, r := range recs {
		if r.msg == msg {
			return true
		}
	}
	return false
}

func findLog(recs []logRecord, msg string) (logRecord, bool) {
	for _, r := range recs {
		if r.msg == msg {
			return r, true
		}
	}
	return logRecord{}, false
}

// TestStoreError_Error pins the Error() string for every Kind, with and
// without a Detail, so a mutated branch or a swapped detail-conditional
// surfaces. The TooLarge and IDInUse strings are user-facing (writeChatErr
// surfaces ce.Error() verbatim for 413 / 409).
func TestStoreError_Error(t *testing.T) {
	cases := []struct {
		err  *StoreError
		name string
		want string
	}{
		{&StoreError{Kind: ErrKindNotFound, Detail: "c1"}, "not found with detail", "chat not found: c1"},
		{&StoreError{Kind: ErrKindNotFound}, "not found no detail", "chat not found"},
		{&StoreError{Kind: ErrKindTombstoned, Detail: "c1"}, "tombstoned with detail", "chat recently deleted: c1"},
		{&StoreError{Kind: ErrKindTombstoned}, "tombstoned no detail", "chat recently deleted"},
		{&StoreError{Kind: ErrKindTooLarge, Detail: "5 bytes"}, "too large with detail", "plan draft too large: 5 bytes"},
		{&StoreError{Kind: ErrKindTooLarge}, "too large no detail", "plan draft too large"},
		{&StoreError{Kind: ErrKindIDInUse, Detail: "c1"}, "id in use with detail", "chat id in use: c1"},
		{&StoreError{Kind: ErrKindIDInUse}, "id in use no detail", "chat id in use"},
		{&StoreError{Kind: ErrorKind(0)}, "unknown kind falls through", "chat store error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Errorf("StoreError{Kind:%d, Detail:%q}.Error() = %q, want %q",
					tc.err.Kind, tc.err.Detail, got, tc.want)
			}
		})
	}
}

// TestReadCappedFile_ExactMaxBoundary: a file of exactly maxChatFileBytes
// reads back in full with no error (the cap is `size > max`, not `>=`).
func TestReadCappedFile_ExactMaxBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exactmax.json")
	truncToSize(t, path, maxChatFileBytes)

	data, err := readCappedFile(path, "chat exactmax")
	if err != nil {
		t.Fatalf("readCappedFile(exactly maxChatFileBytes) error = %v, want nil", err)
	}
	if int64(len(data)) != maxChatFileBytes {
		t.Errorf("readCappedFile len = %d, want %d", len(data), int64(maxChatFileBytes))
	}
}

// TestGetPlanDraft_ExactMaxBoundary: a draft of exactly maxPlanDraftBytes
// reads back in full with no error (the cap is `size > max`, not `>=`).
func TestGetPlanDraft_ExactMaxBoundary(t *testing.T) {
	s, _ := newTestStore(t)
	id := api.ChatID("getmax")
	draftPath := filepath.Join(s.Dir(), string(id)+planDraftSuffix)
	truncToSize(t, draftPath, maxPlanDraftBytes)

	got, err := s.GetPlanDraft(context.Background(), id)
	if err != nil {
		t.Fatalf("GetPlanDraft(exactly maxPlanDraftBytes) error = %v, want nil", err)
	}
	if len(got) != maxPlanDraftBytes {
		t.Errorf("GetPlanDraft len = %d, want %d", len(got), maxPlanDraftBytes)
	}
}

// TestSetPlanDraft_ExactMaxAccepted: content of exactly maxPlanDraftBytes
// must be accepted (the cap is `len > max`, not `>=`).
func TestSetPlanDraft_ExactMaxAccepted(t *testing.T) {
	s, _ := newTestStore(t)
	id := api.ChatID("setmax")
	mustCreateChat(t, s, id)

	content := strings.Repeat("a", maxPlanDraftBytes)
	if err := s.SetPlanDraft(context.Background(), id, content); err != nil {
		t.Fatalf("SetPlanDraft(len==maxPlanDraftBytes) error = %v, want nil (boundary must be accepted)", err)
	}
}

// TestDeletePlanDraft_PropagatesNonEmptyDirRemoveError: a non-ENOENT
// os.Remove error must propagate. A non-empty directory at the draft path
// makes os.Remove fail with ENOTEMPTY — a root-safe injection (unlike the
// EACCES variant in TestDeletePlanDraft_SurfacesNonENOENTRemoveError, which
// skips under root).
func TestDeletePlanDraft_PropagatesNonEmptyDirRemoveError(t *testing.T) {
	s, _ := newTestStore(t)
	id := api.ChatID("deldir")
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

// TestHandleOne_LimitUpperBoundaryHonored: limit=500 must be honored (the
// clamp is `n <= 500`); a 60-message chat returns all 60, not the default 50.
func TestHandleOne_LimitUpperBoundaryHonored(t *testing.T) {
	s, _ := newTestStore(t)
	id := api.ChatID("limitmax")
	if err := s.Mutate(context.Background(), id, func(c *api.Chat, _ bool) bool {
		for i := range 60 {
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

// TestPutPlanDraft_LimitBodyEnvelopeBoundary: a body that fits the
// maxPlanDraftBytes+4096 LimitBody cap (and whose content is under the
// SetPlanDraft cap) must save and return 200 — pins the +4096 JSON-envelope
// allowance.
func TestPutPlanDraft_LimitBodyEnvelopeBoundary(t *testing.T) {
	s, _ := newTestStore(t)
	id := api.ChatID("putenv")
	mustCreateChat(t, s, id)

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

// TestPutPlanDraft_LogsExactBodyLimit: an oversized body trips the
// "body too large" warn whose logged "limit" attribute must be exactly
// maxPlanDraftBytes+4096 (266240), pinning the +4096 in the log call.
func TestPutPlanDraft_LogsExactBodyLimit(t *testing.T) {
	const wantLimit = int64(266240) // 256*1024 + 4096

	snap := captureChatLogs(t)
	s, _ := newTestStore(t)
	id := api.ChatID("putlog")

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
	rec, ok := findLog(snap(), "chat plan_draft: body too large")
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

// TestNewStore_NoChmodWarnOnWritableDir: on a writable dir chmod succeeds,
// so NewStore must NOT log "chat store: chmod" (that log is the chmod-error
// branch).
func TestNewStore_NoChmodWarnOnWritableDir(t *testing.T) {
	snap := captureChatLogs(t)
	if _, err := NewStore(t.TempDir()); err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if hasLogMsg(snap(), "chat store: chmod") {
		t.Error(`NewStore logged "chat store: chmod" on a writable dir; the chmod-error branch is inverted`)
	}
}

// TestDelete_NoDraftWarnOnCleanRemoval: when the draft removal succeeds,
// Delete must NOT log "chat delete: plan-draft removal failed" (that log is
// the removal-error branch).
func TestDelete_NoDraftWarnOnCleanRemoval(t *testing.T) {
	s, _ := newTestStore(t)
	id := api.ChatID("delclean")
	mustCreateChat(t, s, id)
	draftPath := filepath.Join(s.Dir(), string(id)+planDraftSuffix)
	if err := os.WriteFile(draftPath, []byte("draft body"), 0o600); err != nil {
		t.Fatalf("write draft: %v", err)
	}

	snap := captureChatLogs(t)
	if err := s.Delete(context.Background(), id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if hasLogMsg(snap(), "chat delete: plan-draft removal failed") {
		t.Error(`Delete logged "chat delete: plan-draft removal failed" on a clean draft removal; the error branch is inverted`)
	}
}

// TestHandleArchivedChats_ListIncludesArchived: with one archived chat the
// GET handler returns a non-nil list passed through verbatim (pins the
// `headers == nil` empty-slice guard against inversion).
func TestHandleArchivedChats_ListIncludesArchived(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	id := api.ChatID("archlist")
	mustCreateChat(t, s, id)
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

// TestMutate_RejectsInvalidUTF8 pins the write-path UTF-8 guard: content
// that cannot round-trip through the JSON storage format (invalid UTF-8 in
// the chat name or any message body) must abort the mutation with
// errInvalidUTF8 before anything is persisted or broadcast. Covers the
// validateChatUTF8 helper through the public Mutate API.
func TestMutate_RejectsInvalidUTF8(t *testing.T) {
	cases := []struct {
		mutate func(c *api.Chat)
		name   string
	}{
		{
			name:   "invalid utf-8 in name",
			mutate: func(c *api.Chat) { c.Name = "bad\xff\xfename" },
		},
		{
			name: "invalid utf-8 in message content",
			mutate: func(c *api.Chat) {
				c.Messages = append(c.Messages, api.Message{ID: "m1", Role: api.RoleUser, Content: "ok\xffbad"})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, b := newTestStore(t)
			err := s.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
				tc.mutate(c)
				return true
			})
			if !errors.Is(err, errInvalidUTF8) {
				t.Fatalf("Mutate = %v, want errInvalidUTF8", err)
			}
			if _, ok := s.Get(context.Background(), "c1"); ok {
				t.Error("invalid-UTF8 chat was persisted; the mutation must abort before save")
			}
			if n := len(b.snapshot()); n != 0 {
				t.Errorf("broadcast fired %d time(s) on a rejected mutation; want 0", n)
			}
		})
	}
}

// TestArchive_StampsArchivedAt verifies Archive records an explicit
// ArchivedAt timestamp (near "now") on the chat, so purge can age from it
// rather than the last-activity file mtime.
func TestArchive_StampsArchivedAt(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	before := time.Now().UnixMilli()
	_ = s.Mutate(ctx, "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })
	if err := s.Archive(ctx, "c1"); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	after := time.Now().UnixMilli()

	c, err := s.LoadArchived(ctx, "c1")
	if err != nil {
		t.Fatalf("LoadArchived: %v", err)
	}
	if c.ArchivedAt < before || c.ArchivedAt > after {
		t.Errorf("ArchivedAt = %d, want within [%d, %d]", c.ArchivedAt, before, after)
	}
}
