package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// fakeBroadcaster captures broadcasts for assertions. Access is guarded
// by mu so concurrent appends from parallel AppendMessage goroutines
// don't race the slice header.
type fakeBroadcaster struct {
	events []vibekit.ServerEvent
	count  atomic.Int32
	mu     sync.Mutex
}

var _ broadcaster = (*fakeBroadcaster)(nil)

func (f *fakeBroadcaster) Broadcast(_ context.Context, e vibekit.ServerEvent) {
	f.mu.Lock()
	f.events = append(f.events, e)
	f.mu.Unlock()
	f.count.Add(1)
}

// snapshot returns a copy of the captured events under the mutex.
// Tests should use this instead of reading f.events directly so a
// future asynchronous broadcaster doesn't race a bare slice read.
func (f *fakeBroadcaster) snapshot() []vibekit.ServerEvent {
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
	WithBroadcaster(b)(s)
	return s, b
}

// ageChat backdates a chat so it is eligible for purge, writing BOTH its
// UpdatedAt and its mtime. Purge ages from the chat's own UpdatedAt — its last
// activity — so aging the mtime alone does not make an entry purgeable; mtime is
// written too because it is the fallback for a chat that cannot be read.
func ageChat(t *testing.T, s *Store, id string, ago time.Duration) {
	t.Helper()
	path := filepath.Join(s.dir, id+chatFileSuffix)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read chat %s: %v", id, err)
	}
	var c vibekit.Chat
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("unmarshal chat %s: %v", id, err)
	}
	old := time.Now().Add(-ago)
	c.UpdatedAt = old.UnixMilli()
	out, err := json.MarshalIndent(&c, "", "  ")
	if err != nil {
		t.Fatalf("marshal chat %s: %v", id, err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatalf("write chat %s: %v", id, err)
	}
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("chtimes chat %s: %v", id, err)
	}
}

// badChatIDs is the canonical set of invalid chat identifiers used
// across all RejectsBadChatID / InvalidChatIDRejected tests. Adding a
// new invalid pattern here automatically covers every method.
var badChatIDs = []vibekit.ChatID{"", "a/b", "..", "a\x00b", "a b", vibekit.ChatID(strings.Repeat("x", 200))}

// assertRejectsBadChatIDs iterates badChatIDs and asserts that fn
// returns a non-nil error for each. Use in table-driven subtests to
// eliminate duplicated bad-id slices across store method tests.
func assertRejectsBadChatIDs(t *testing.T, fn func(id vibekit.ChatID) error) {
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
	err := s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, exists bool) bool {
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
	got, ok := s.Get(t.Context(), "c1")
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
	_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })
	// Second Mutate should broadcast chat_updated.
	_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, exists bool) bool {
		if !exists {
			t.Error("exists = false on existing chat")
		}
		c.Name = "B"
		return true
	})
	got, _ := s.Get(t.Context(), "c1")
	if got.Name != "B" {
		t.Errorf("name = %q", got.Name)
	}
	if len(b.events) != 2 || b.events[1].Type != "chat_updated" {
		t.Errorf("second event should be chat_updated: %+v", b.events)
	}
}

func TestMutate_AbortDoesNotBroadcast(t *testing.T) {
	s, b := newTestStore(t)
	err := s.Mutate(t.Context(), "c1", func(*vibekit.Chat, bool) bool { return false })
	if err != nil {
		t.Fatalf("Mutate error = %v", err)
	}
	if _, ok := s.Get(t.Context(), "c1"); ok {
		t.Error("chat created despite abort")
	}
	if len(b.events) != 0 {
		t.Errorf("broadcast despite abort: %+v", b.events)
	}
}

func TestMutate_RejectsBadChatID(t *testing.T) {
	s, _ := newTestStore(t)
	assertRejectsBadChatIDs(t, func(id vibekit.ChatID) error {
		return s.Mutate(t.Context(), id, func(*vibekit.Chat, bool) bool { return true })
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
func BroadcasterContractTest(t *testing.T, newBroadcaster func() broadcaster) {
	t.Helper()

	t.Run("ConcurrentBroadcastsDoNotBlock", func(t *testing.T) {
		b := newBroadcaster()
		const N = 100
		done := make(chan struct{})
		go func() {
			var wg sync.WaitGroup
			for i := range N {
				wg.Go(func() {
					b.Broadcast(t.Context(), vibekit.ServerEvent{
						Type:   "test_event",
						ChatID: vibekit.ChatID(fmt.Sprintf("c%d", i)),
					})
				})
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
			b.Broadcast(t.Context(), vibekit.ServerEvent{
				Type:   "order_test",
				ChatID: vibekit.ChatID(fmt.Sprintf("%d", i)),
			})
		}
		// Verify ordering via the concrete type's snapshot if available.
		type snapshotter interface {
			snapshot() []vibekit.ServerEvent
		}
		if s, ok := b.(snapshotter); ok {
			evs := s.snapshot()
			if len(evs) != N {
				t.Fatalf("len(events) = %d, want %d", len(evs), N)
			}
			for i, e := range evs {
				want := vibekit.ChatID(fmt.Sprintf("%d", i))
				if e.ChatID != want {
					t.Errorf("event[%d].ChatID = %q, want %q (order violated)", i, e.ChatID, want)
					break
				}
			}
		}
	})
}

func TestFakeBroadcaster_ContractCompliance(t *testing.T) {
	BroadcasterContractTest(t, func() broadcaster {
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
		if !chatIDPattern(vibekit.ChatID(id)) {
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
		if chatIDPattern(vibekit.ChatID(id)) {
			t.Errorf("chatIDPattern(%q) = true, want false", id)
		}
	}
}

func TestGet_MissingChat(t *testing.T) {
	s, _ := newTestStore(t)
	if _, ok := s.Get(t.Context(), "nonexistent"); ok {
		t.Error("Get returned true for missing chat")
	}
}

// --- List ---

// TestList_SortsByUpdatedAtDesc runs in a synctest bubble, which is what lets it
// assert the gap between the two timestamps EXACTLY rather than nudge a real
// clock and hope.
//
// The two `time.Sleep(2 * time.Millisecond)` calls this replaces were class (b)
// — advancing a real clock the test could not fake — and their comment said so:
// "nudge the wall clock so the UpdatedAt ms timestamps don't collide on fast
// machines". Inside the bubble the clock is synthetic, so the nudge is exact and
// the ordering is deterministic by construction instead of by resolution.
//
// Measured: atomicfile.WriteFile's real filesystem work (mkdir walk, fsync,
// rename, parent fsync) is fine in here. That is the documented boundary —
// TRANSIENT file I/O reaches a durably-blocked state afterwards, so the clock
// still advances; only a goroutine parked indefinitely on an external FD defeats
// a bubble.
func TestList_SortsByUpdatedAtDesc(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _ := newTestStore(t)
		_ = s.Mutate(t.Context(), "a", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })
		synctest.Sleep(2 * time.Millisecond)
		_ = s.Mutate(t.Context(), "b", func(c *vibekit.Chat, _ bool) bool { c.Name = "B"; return true })
		synctest.Sleep(2 * time.Millisecond)
		_ = s.Mutate(t.Context(), "a", func(c *vibekit.Chat, _ bool) bool { return true }) // bump updated_at
		headers := s.List(t.Context())
		// Fatal: every assertion below indexes headers.
		if len(headers) != 2 {
			t.Fatalf("len = %d, want 2", len(headers))
		}
		if headers[0].ID != "a" {
			t.Errorf("first = %q, want a (most recently updated)", headers[0].ID)
		}
		// Exactly 4ms of synthetic time separates a's second mutation from b's
		// only one. On a real clock this could only ever be asserted as `> 0`,
		// which a save that stamped the wrong field, or stamped once and reused
		// the value, would satisfy.
		if gap := headers[0].UpdatedAt - headers[1].UpdatedAt; gap != 2 {
			t.Errorf("UpdatedAt gap = %dms, want exactly 2 (b at +2ms, a re-stamped at +4ms)", gap)
		}
	})
}

func TestList_IgnoresNonChatFiles(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(t.Context(), "a", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })
	// Non-.json file → skipped by the suffix filter.
	if err := os.WriteFile(filepath.Join(s.dir, "random.txt"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Valid .json suffix but invalid chat id (contains a '.') → skipped
	// by the chatIDPattern filter in List.
	if err := os.WriteFile(filepath.Join(s.dir, "bad.id.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	headers := s.List(t.Context())
	if len(headers) != 1 || headers[0].ID != "a" {
		t.Errorf("headers = %+v", headers)
	}
}

func TestList_SkipsMalformedChatFile(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(t.Context(), "good", func(c *vibekit.Chat, _ bool) bool { c.Name = "ok"; return true })
	// Drop a file that matches chatIDPattern but isn't valid JSON.
	// List must log and skip it, not panic or return a zero-value
	// header that confuses clients.
	badPath := filepath.Join(s.dir, "bad.json")
	if err := os.WriteFile(badPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	headers := s.List(t.Context())
	if len(headers) != 1 || headers[0].ID != "good" {
		t.Errorf("List() = %+v, want only the good chat", headers)
	}
}

// --- AppendMessage ---

func TestAppendMessage_AddsAndBroadcasts(t *testing.T) {
	s, b := newTestStore(t)
	_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })
	b.reset()

	msg := &vibekit.Message{ID: "m1", Role: vibekit.RoleUser, Content: "hi"}
	if err := s.AppendMessage(t.Context(), "c1", msg); err != nil {
		t.Fatalf("AppendMessage error = %v", err)
	}

	got, _ := s.Get(t.Context(), "c1")
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
	err := s.AppendMessage(t.Context(), "nonexistent", &vibekit.Message{ID: "m1", Role: vibekit.RoleUser})
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
	_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = s.AppendMessage(t.Context(), "c1", &vibekit.Message{ID: "m1", Role: vibekit.RoleAssistant, Content: "old"})
	b.reset()

	err := s.UpdateMessage(t.Context(), "c1", "m1", func(m *vibekit.Message) { m.Content = "new" })
	if err != nil {
		t.Fatalf("UpdateMessage error = %v", err)
	}
	got, _ := s.Get(t.Context(), "c1")
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
	_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })
	b.reset()

	_ = s.UpdateMessage(t.Context(), "c1", "nonexistent", func(*vibekit.Message) {})
	if evs := b.snapshot(); len(evs) != 0 {
		t.Errorf("events: %+v", evs)
	}
}

// --- Delete ---

func TestDelete_RemovesFileAndBroadcasts(t *testing.T) {
	s, b := newTestStore(t)
	_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })
	b.reset()

	if err := s.Delete(t.Context(), "c1"); err != nil {
		t.Fatalf("Delete error = %v", err)
	}
	if _, ok := s.Get(t.Context(), "c1"); ok {
		t.Error("chat still exists after delete")
	}
	if len(b.events) != 1 || b.events[0].Type != "chat_deleted" {
		t.Errorf("events: %+v", b.events)
	}
}

func TestDelete_MissingChatIsNoOp(t *testing.T) {
	s, b := newTestStore(t)
	if err := s.Delete(t.Context(), "nonexistent"); err != nil {
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
	_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = s.Delete(t.Context(), "c1")
	if !s.isTombstoned("c1") {
		t.Error("tombstone not set after Delete")
	}
}

func TestMutate_RefusesToCreateTombstonedChat(t *testing.T) {
	s, b := newTestStore(t)
	_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = s.Delete(t.Context(), "c1")
	b.reset()

	// Simulate a late handler racing the delete — it tries to Mutate
	// the just-deleted id. The call must return nil (no error, no
	// resurrection) so the caller treats it as a benign no-op.
	err := s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, exists bool) bool {
		c.Name = "resurrected"
		c.Messages = append(c.Messages, vibekit.Message{Role: vibekit.RoleUser, Content: "ghost"})
		return true
	})
	if err != nil {
		t.Fatalf("Mutate on tombstoned chat returned error: %v", err)
	}
	if _, ok := s.Get(t.Context(), "c1"); ok {
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
	_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = s.Delete(t.Context(), "c1")
	// Tombstone is now live for c1. Re-Create via a different id
	// shouldn't be affected.
	err := s.Mutate(t.Context(), "c2", func(c *vibekit.Chat, _ bool) bool { c.Name = "B"; return true })
	if err != nil {
		t.Fatalf("unrelated chat blocked by unrelated tombstone: %v", err)
	}
	if _, ok := s.Get(t.Context(), "c2"); !ok {
		t.Error("c2 was not created")
	}
}

func TestAppendMessage_OnTombstonedChatIsNoOp(t *testing.T) {
	s, b := newTestStore(t)
	_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = s.Delete(t.Context(), "c1")
	b.reset()

	// Mutate's tombstone check runs before the mutator when the chat
	// doesn't exist, so AppendMessage's `if !exists { return false }`
	// early-return never fires here — the tombstone guard short-circuits
	// first. This test pins that path: after Delete, AppendMessage must
	// produce no events and not resurrect the chat even though the
	// mutator would have happily returned false anyway.
	err := s.AppendMessage(t.Context(), "c1", &vibekit.Message{Role: vibekit.RoleUser, Content: "ghost"})
	if err != nil {
		t.Fatalf("AppendMessage error: %v", err)
	}
	if _, ok := s.Get(t.Context(), "c1"); ok {
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
	_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Messages = []vibekit.Message{
			{Role: vibekit.RoleUser, Content: "hello"},
			{
				Role:    vibekit.RoleAssistant,
				Content: "thinking",
				ToolCalls: []vibekit.ToolCall{
					{Title: "grep", Status: vibekit.ToolCompleted},
					{Title: "edit", Status: vibekit.ToolFailed},
				},
			},
			{Role: vibekit.RoleEvent, EventKind: vibekit.EventCancelled, Content: "user aborted"},
			{Role: vibekit.RoleEvent, EventKind: vibekit.EventCompacted, Content: "summary text"},
		}
		return true
	})
	got := s.BuildHistory(t.Context(), "c1")
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
	_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })
	if got := s.BuildHistory(t.Context(), "c1"); got != "" {
		t.Errorf("BuildHistory on empty-message chat = %q, want empty", got)
	}
}

func TestBuildHistory_EmptyForMissingChat(t *testing.T) {
	s, _ := newTestStore(t)
	if s.BuildHistory(t.Context(), "nonexistent") != "" {
		t.Error("expected empty string")
	}
}

// --- HTTP endpoints ---

func TestHandleList_ReturnsHeaders(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "One"
		c.Messages = []vibekit.Message{{ID: "m1", Role: vibekit.RoleUser, Content: "x"}}
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
	_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "One"
		c.Messages = []vibekit.Message{
			{ID: "m1", Role: vibekit.RoleUser, Content: "a", Ts: 100},
			{ID: "m2", Role: vibekit.RoleAssistant, Content: "b", Ts: 200},
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

// TestHandleOne_PaginationSurvivesUnorderedTimestamps pins the two states that
// made the previous `?before=<ts>` cursor lose a page, because both are reachable
// on the real wire and neither is exotic.
//
// The cursor resolved a millisecond timestamp with sort.Search, which needs
// Message.Ts to be non-decreasing across the slice. Nothing makes that true:
// render order is array position, and translate.newEventMessage stamps Ts at
// CONSTRUCTION, outside the per-chat lock AppendMessage takes, so two writers can
// stamp in one order and append in the other. Separately,
// projection.applySummary gives a compaction event its predecessor's exact Ts on
// purpose, so a tie group always exists after a replayed compaction.
//
// Both cases assert the same property: paging back from the newest window returns
// the messages immediately before it, in array order, with none skipped. Run
// against the old cursor, the tie case returns "a" only (the whole tie group is
// excluded at once) and the inverted case returns an arbitrary window.
func TestHandleOne_PaginationSurvivesUnorderedTimestamps(t *testing.T) {
	cases := []struct {
		name     string
		msgs     []vibekit.Message
		beforeID string
		wantIDs  []string
	}{
		{
			// The cursor message SHARES its millisecond with the two before it,
			// which is the state applySummary guarantees after a replayed
			// compaction. A timestamp search lands on the FIRST message at that
			// value, so it excludes b and c along with d and answers [a].
			name: "a tie group holding the cursor is paged rather than skipped",
			msgs: []vibekit.Message{
				{ID: "a", Role: vibekit.RoleUser, Ts: 100},
				{ID: "b", Role: vibekit.RoleUser, Ts: 120},
				{ID: "c", Role: vibekit.RoleEvent, Ts: 120},
				{ID: "d", Role: vibekit.RoleAssistant, Ts: 120},
				{ID: "e", Role: vibekit.RoleUser, Ts: 130},
			},
			beforeID: "d",
			wantIDs:  []string{"a", "b", "c"},
		},
		{
			// Two writers stamped 120 and 119 and appended in the other order, so
			// the slice is not non-decreasing and a binary search over it is
			// undefined. Here it answers [a], dropping b.
			name: "an inverted cursor is paged in array order",
			msgs: []vibekit.Message{
				{ID: "a", Role: vibekit.RoleUser, Ts: 100},
				{ID: "b", Role: vibekit.RoleUser, Ts: 120},
				{ID: "c", Role: vibekit.RoleEvent, Ts: 119},
				{ID: "d", Role: vibekit.RoleUser, Ts: 130},
			},
			beforeID: "c",
			wantIDs:  []string{"a", "b"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestStore(t)
			_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
				c.Name = "A"
				c.Messages = tc.msgs
				return true
			})
			req := httptest.NewRequest(http.MethodGet,
				"/api/chats/c1?before_id="+tc.beforeID, nil)
			rec := httptest.NewRecorder()
			NewRouter(s).handleOne(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
			}
			var got struct {
				Messages []vibekit.Message `json:"messages"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			ids := make([]string, 0, len(got.Messages))
			for i := range got.Messages {
				ids = append(ids, got.Messages[i].ID)
			}
			if !slices.Equal(ids, tc.wantIDs) {
				t.Errorf("page = %v, want %v", ids, tc.wantIDs)
			}
		})
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
		{"before_id_is_exclusive_of_the_named_message", "?before_id=d", []string{"a", "b", "c"}, false},
		{"before_id_unknown_returns_the_newest_window", "?before_id=nope", []string{"a", "b", "c", "d", "e"}, false},
		{"before_id_oldest_returns_none", "?before_id=a", []string{}, false},
		{"limit_and_before_id_combined", "?before_id=e&limit=2", []string{"c", "d"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestStore(t)
			_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
				c.Name = "A"
				c.Messages = []vibekit.Message{
					{ID: "a", Role: vibekit.RoleUser, Content: "1", Ts: 100},
					{ID: "b", Role: vibekit.RoleUser, Content: "2", Ts: 110},
					{ID: "c", Role: vibekit.RoleUser, Content: "3", Ts: 120},
					{ID: "d", Role: vibekit.RoleUser, Content: "4", Ts: 130},
					{ID: "e", Role: vibekit.RoleUser, Content: "5", Ts: 140},
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
				Messages []vibekit.Message `json:"messages"`
				HasMore  bool              `json:"has_more"`
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
	// export and archive are valid sub-resources.
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
	_ = s1.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "Saved"
		c.ACPSessionID = "acp-1"
		c.Messages = []vibekit.Message{{ID: "m1", Role: vibekit.RoleUser, Content: "hi"}}
		return true
	})

	// Reopen.
	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore reopen: %v", err)
	}
	got, ok := s2.Get(t.Context(), "c1")
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
	_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })
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
	_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.Messages = []vibekit.Message{{ID: "m1", Role: vibekit.RoleUser, Content: "x", Ts: 100}}
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
		{"before_id_unknown", "?before_id=nope"},
		{"before_id_empty", "?before_id="},
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
	_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

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
	_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	const N = 50
	var wg sync.WaitGroup
	for i := range N {
		wg.Go(func() {
			_ = s.AppendMessage(t.Context(), "c1", &vibekit.Message{
				ID: fmt.Sprintf("m%d", i), Role: vibekit.RoleUser, Content: "x",
			})
		})
	}
	wg.Wait()
	got, _ := s.Get(t.Context(), "c1")
	if len(got.Messages) != N {
		t.Errorf("concurrent appends: len = %d, want %d (per-chat mutex should serialize)", len(got.Messages), N)
	}
}

func TestMutate_DifferentChatsAreIndependent(t *testing.T) {
	// Two chats should not block each other. We assert completion of
	// N mutations on each chat runs to success with no deadlock.
	s, _ := newTestStore(t)
	_ = s.Mutate(t.Context(), "a", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = s.Mutate(t.Context(), "b", func(c *vibekit.Chat, _ bool) bool { c.Name = "B"; return true })

	const N = 20
	var wg sync.WaitGroup
	for i := range N {
		wg.Go(func() {
			_ = s.AppendMessage(t.Context(), "a", &vibekit.Message{ID: fmt.Sprintf("a%d", i), Role: vibekit.RoleUser, Content: "x"})
		})
		wg.Go(func() {
			_ = s.AppendMessage(t.Context(), "b", &vibekit.Message{ID: fmt.Sprintf("b%d", i), Role: vibekit.RoleUser, Content: "x"})
		})
	}
	wg.Wait()
	a, _ := s.Get(t.Context(), "a")
	b, _ := s.Get(t.Context(), "b")
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
	_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })
	_ = s.Delete(t.Context(), "c1")

	// Age the tombstone past its TTL.
	s.tombMu.Lock()
	s.tombstone["c1"] = time.Now().Add(-2 * tombstoneTTL)
	s.tombMu.Unlock()

	err := s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, exists bool) bool {
		if exists {
			t.Error("exists = true after expired tombstone")
		}
		c.Name = "reborn"
		return true
	})
	if err != nil {
		t.Fatalf("Mutate after tombstone expiry: %v", err)
	}
	got, ok := s.Get(t.Context(), "c1")
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
		if _, ok := s.tombstone[vibekit.ChatID(id)]; ok {
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
	_ = s.Delete(t.Context(), "never-existed")
	if s.isTombstoned("never-existed") {
		t.Error("phantom delete tombstoned a chat that never existed")
	}
	// Creating a new chat with that id must succeed.
	err := s.Mutate(t.Context(), "never-existed", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })
	if err != nil {
		t.Fatalf("Mutate after phantom delete: %v", err)
	}
	if _, ok := s.Get(t.Context(), "never-existed"); !ok {
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
	if _, ok := s.Get(t.Context(), "big"); ok {
		t.Error("Get on oversize chat file returned ok=true, want false")
	}
}

func TestList_SkipsOversizeChatFile(t *testing.T) {
	// The same guardrail must keep one oversize file from erasing the
	// sidebar for every other chat. List should log-and-skip, not fail.
	s, _ := newTestStore(t)
	_ = s.Mutate(t.Context(), "good", func(c *vibekit.Chat, _ bool) bool { c.Name = "ok"; return true })
	path := filepath.Join(s.dir, "big.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = f.Close()
	if err := os.Truncate(path, int64(maxChatFileBytes)+1); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	headers := s.List(t.Context())
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
	err := s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
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
	err := s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
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
	if _, ok := s.Get(t.Context(), "c1"); ok {
		t.Error("c1 was written despite mutator reassigning id")
	}
	if _, ok := s.Get(t.Context(), "c2"); ok {
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
	err := s.UpdateMessage(t.Context(), "never-existed", "m1", func(m *vibekit.Message) {
		m.Content = "ghost"
	})
	if err != nil {
		t.Errorf("UpdateMessage on missing chat = %v, want nil", err)
	}
	if _, ok := s.Get(t.Context(), "never-existed"); ok {
		t.Error("UpdateMessage resurrected a never-existed chat")
	}
	if evs := b.snapshot(); len(evs) != 0 {
		t.Errorf("UpdateMessage on missing chat broadcast events: %+v", evs)
	}
}

// --- handleOne pre-validation of chat id ---

func TestHandleOne_RejectsInvalidChatID(t *testing.T) {
	// The chat id pre-validation ensures malformed ids (bot probes,
	// typos) return 400 without emitting an slog.Error from load's
	// pathFor rejection. We stick to ids that httptest.NewRequest accepts literally —
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
	assertRejectsBadChatIDs(t, func(id vibekit.ChatID) error {
		return s.Delete(t.Context(), id)
	})
	// Defensive assertion: rejected ids never emit chat_deleted.
	// A broken refactor that moves pathFor after os.Remove would
	// pass the error tests but still broadcast — catch it here.
	if evs := b.snapshot(); len(evs) != 0 {
		t.Errorf("invalid chat id deletes broadcast events: %+v", evs)
	}
}

func TestDelete_SurfacesNonENOENTChatRemoveError(t *testing.T) {
	skipIfRoot(t)
	// Delete must return the rmErr
	// verbatim when it's neither ENOENT nor nil so upstream handlers
	// can distinguish "chat gone" (no-op) from "filesystem broken"
	// (surface to operator).
	s, _ := newTestStore(t)
	_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })
	if err := os.Chmod(s.dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(s.dir, 0o700) })

	err := s.Delete(t.Context(), "c1")
	if err == nil {
		t.Fatal("Delete on readonly parent dir = nil error, want EACCES")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, unexpectedly ENOENT (file should still exist)", err)
	}
}

// --- handleExport ---
// Ported from main's pre-rewrite store_test.go: the export handler and
// its filename sanitiser survived the conversational-surface rewrite
// unchanged, so their handler-level battery comes along.

func TestHandleExport_JSONFormatReturnsChatJSON(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "Named Chat"
		c.Messages = []vibekit.Message{
			{ID: "m1", Role: vibekit.RoleUser, Content: "hi"},
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
	_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "Named Chat"
		c.Messages = []vibekit.Message{
			{ID: "m1", Role: vibekit.RoleUser, Content: "hi"},
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
	_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })

	req := httptest.NewRequest(http.MethodGet, "/api/chats/c1/export?format=xml", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400 for unsupported format", rec.Code)
	}
}

func TestHandleExport_FallsBackToChatIDWhenNameEmpty(t *testing.T) {
	s, _ := newTestStore(t)
	_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { return true })

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
	_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })
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
	_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
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
		t.Errorf("len(%q) = %d runes, want stem capped at 80", got, len([]rune(got)))
	}
}

// --- UTF-8 write gate ---

// TestMutate_RejectsInvalidUTF8 pins the store's write-side UTF-8 gate:
// a mutation producing invalid UTF-8 anywhere in the chat must abort
// before save (json.Marshal would otherwise silently corrupt it to
// U+FFFD) and must not broadcast.
func TestMutate_RejectsInvalidUTF8(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(c *vibekit.Chat)
	}{
		{
			name:   "invalid utf-8 in name",
			mutate: func(c *vibekit.Chat) { c.Name = "bad\xff\xfename" },
		},
		{
			name: "invalid utf-8 in message content",
			mutate: func(c *vibekit.Chat) {
				c.Messages = append(c.Messages, vibekit.Message{ID: "m1", Role: vibekit.RoleUser, Content: "ok\xffbad"})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, b := newTestStore(t)
			err := s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
				tc.mutate(c)
				return true
			})
			if !errors.Is(err, errInvalidUTF8) {
				t.Fatalf("Mutate = %v, want errInvalidUTF8", err)
			}
			if _, ok := s.Get(t.Context(), "c1"); ok {
				t.Error("invalid-UTF8 chat was persisted; the mutation must abort before save")
			}
			if n := len(b.snapshot()); n != 0 {
				t.Errorf("broadcast fired %d time(s) on a rejected mutation; want 0", n)
			}
		})
	}
}

// TestBuildHistory_TrimsOldestFirst pins the priming budget's three rules, which
// are behavioural rather than incidental: the output is bounded, the NEWEST
// messages are the ones kept, and the model is told what it lost.
//
// The unit matters as much as the number. Trimming drops whole messages because
// half a sentence with no marker is worse than a shorter history: the model
// cannot tell a truncated turn from a terse one.
func TestBuildHistory_TrimsOldestFirst(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := t.Context()
	chatID := vibekit.ChatID("prime-trim")

	// Twelve messages of ~8 KiB each: comfortably over the 64 KiB cap, so the
	// oldest must go.
	const each = 8 << 10
	if err := s.Mutate(ctx, chatID, func(c *vibekit.Chat, _ bool) bool {
		for i := range 12 {
			c.Messages = append(c.Messages, vibekit.Message{
				ID:      fmt.Sprintf("m%02d", i),
				Role:    vibekit.RoleUser,
				Content: fmt.Sprintf("MARK%02d ", i) + strings.Repeat("x", each),
				Ts:      int64(i + 1),
			})
		}
		return true
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}

	h := s.BuildHistory(ctx, chatID)

	if len(h) > primeHistoryCap {
		t.Errorf("history is %d bytes, over the %d cap", len(h), primeHistoryCap)
	}
	if !strings.Contains(h, "MARK11") {
		t.Error("newest message was dropped; a prime without the last turn cannot resume")
	}
	if strings.Contains(h, "MARK00") {
		t.Error("oldest message survived a trim that should have dropped it first")
	}
	if !strings.Contains(h, "omitted") {
		t.Error("history was trimmed without telling the model")
	}
}

// TestBuildHistory_KeepsTheLastMessageEvenOversize covers the one case where a
// single message exceeds the whole budget. Dropping it would return a prime with
// no final turn, which is useless for resuming, so it is truncated with a marker
// instead.
func TestBuildHistory_KeepsTheLastMessageEvenOversize(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := t.Context()
	chatID := vibekit.ChatID("prime-oversize")

	if err := s.Mutate(ctx, chatID, func(c *vibekit.Chat, _ bool) bool {
		c.Messages = []vibekit.Message{
			{ID: "old", Role: vibekit.RoleUser, Content: "OLDMARK", Ts: 1},
			{ID: "big", Role: vibekit.RoleUser, Content: strings.Repeat("z", primeHistoryCap*2), Ts: 2},
		}
		return true
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}

	h := s.BuildHistory(ctx, chatID)

	if len(h) > primeHistoryCap {
		t.Errorf("history is %d bytes, over the %d cap", len(h), primeHistoryCap)
	}
	if !strings.Contains(h, "User: ") {
		t.Error("the oversize message was dropped entirely rather than truncated")
	}
	if strings.Contains(h, "OLDMARK") {
		t.Error("an older message was kept while the newest had to be truncated")
	}
}
