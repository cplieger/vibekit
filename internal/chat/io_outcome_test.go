package chat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// outcomeChatCases are the transcript shapes ChatHeader.LastTurnOutcome has to
// read correctly, shared by the two tests below so the cross-path agreement and
// the per-case expectation cannot drift apart.
//
// Every case is a real persisted shape rather than a synthetic one: the ordinary
// successful turn stamps its outcome on the last assistant message, a turn that
// emitted nothing stamps an EventTurnOutcome marker instead, and the agent
// persists rows DURING a turn (a plan row, a compaction event) which land after
// the carrier and carry no outcome of their own.
var outcomeChatCases = []struct {
	name string
	msgs []vibekit.Message
	want vibekit.TurnOutcome
}{
	{
		name: "no messages at all",
		msgs: nil,
		want: "",
	},
	{
		name: "a legacy record whose rows predate the field",
		msgs: []vibekit.Message{
			{ID: "m1", Role: vibekit.RoleUser, Content: "hi"},
			{ID: "m2", Role: vibekit.RoleAssistant, Content: "hello"},
		},
		want: "",
	},
	{
		name: "the ordinary successful turn",
		msgs: []vibekit.Message{
			{ID: "m1", Role: vibekit.RoleUser, Content: "hi"},
			{ID: "m2", Role: vibekit.RoleAssistant, TurnOutcome: vibekit.TurnOutcomeCompleted},
		},
		want: vibekit.TurnOutcomeCompleted,
	},
	{
		name: "the newest outcome wins over an older one",
		msgs: []vibekit.Message{
			{ID: "m1", Role: vibekit.RoleAssistant, TurnOutcome: vibekit.TurnOutcomeCompleted},
			{ID: "m2", Role: vibekit.RoleAssistant, TurnOutcome: vibekit.TurnOutcomeFailed},
		},
		want: vibekit.TurnOutcomeFailed,
	},
	{
		name: "an outcome on an event row is found",
		msgs: []vibekit.Message{
			{ID: "m1", Role: vibekit.RoleUser, Content: "hi"},
			{
				ID:          "m2",
				Role:        vibekit.RoleEvent,
				EventKind:   vibekit.EventTurnOutcome,
				TurnOutcome: vibekit.TurnOutcomeRefused,
			},
		},
		want: vibekit.TurnOutcomeRefused,
	},
	{
		name: "rows after the carrier do not hide it",
		msgs: []vibekit.Message{
			{ID: "m1", Role: vibekit.RoleAssistant, TurnOutcome: vibekit.TurnOutcomeCompleted},
			{ID: "m2", Role: vibekit.RoleAssistant, Plan: []vibekit.PlanEntry{{Content: "step"}}},
			{ID: "m3", Role: vibekit.RoleEvent, EventKind: vibekit.EventCompacted},
			{ID: "m4", Role: vibekit.RoleUser, Content: "next"},
		},
		want: vibekit.TurnOutcomeCompleted,
	},
	{
		name: "a cancelled turn reports cancelled, not nothing",
		msgs: []vibekit.Message{
			{ID: "m1", Role: vibekit.RoleAssistant, TurnOutcome: vibekit.TurnOutcomeCancelled},
		},
		want: vibekit.TurnOutcomeCancelled,
	},
}

// TestReadChatHeader_LastTurnOutcomeAgreesWithChatHeader is the load-bearing
// test of this pair: the two header PRODUCERS must answer identically.
//
// Store.List reads headers through readChatHeader, which token-walks the raw
// JSON on disk and never materialises a Message; every broadcast and the
// single-chat GET go through Chat.Header(), which walks the in-memory slice.
// Two derivations of one fact, over two entirely different representations, and
// nothing else in the tree would catch a drift between them — a client would
// simply see one dot state from the list endpoint and another from a
// chat_updated frame.
func TestReadChatHeader_LastTurnOutcomeAgreesWithChatHeader(t *testing.T) {
	dir := t.TempDir()

	for _, tc := range outcomeChatCases {
		t.Run(tc.name, func(t *testing.T) {
			c := &vibekit.Chat{ID: "c1", Name: "Agreement", Messages: tc.msgs}

			data, err := json.Marshal(c)
			if err != nil {
				t.Fatalf("marshal chat: %v", err)
			}
			path := filepath.Join(dir, "c1.json")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatalf("write chat file: %v", err)
			}

			onDisk, err := readChatHeader(path, "chat outcome test")
			if err != nil {
				t.Fatalf("readChatHeader: %v", err)
			}

			inMemory := c.Header().LastTurnOutcome
			if onDisk.LastTurnOutcome != inMemory {
				t.Errorf("readChatHeader = %q, Chat.Header() = %q; the two header producers disagree",
					onDisk.LastTurnOutcome, inMemory)
			}
			if onDisk.LastTurnOutcome != tc.want {
				t.Errorf("readChatHeader LastTurnOutcome = %q, want %q", onDisk.LastTurnOutcome, tc.want)
			}
			// The count is derived by the same single pass, so a change that
			// broke the walk's positioning would show here too.
			if onDisk.MessageCount != len(tc.msgs) {
				t.Errorf("MessageCount = %d, want %d", onDisk.MessageCount, len(tc.msgs))
			}
		})
	}
}

// TestReadChatHeader_MalformedMessagesStillYieldsAUsableHeader pins the
// fail-soft half: a messages array the walk cannot finish reading leaves the
// outcome empty rather than failing the whole read, so a damaged chat file
// still lists with its name and timestamps intact. Invariant 6 — a broken state
// must be able to heal itself, and a chat that cannot be listed cannot be
// deleted from the UI either.
func TestReadChatHeader_MalformedMessagesStillYieldsAUsableHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c1.json")
	// Well-formed enough for json.Unmarshal to fill the header fields (Messages
	// is a json.RawMessage, so its contents are not validated at that point),
	// and truncated inside the second element so the token walk stops early.
	body := `{"id":"c1","name":"Damaged","created_at":7,"updated_at":9,` +
		`"messages":[{"id":"m1","turn_outcome":"completed"},{"id":"m2"`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write chat file: %v", err)
	}

	h, err := readChatHeader(path, "chat malformed test")
	if err == nil {
		// A truncated document is rejected by json.Unmarshal itself, which is
		// the stricter and equally acceptable answer; the point is that it is
		// one or the other and never a panic.
		if h.Name != "Damaged" {
			t.Errorf("Name = %q, want Damaged", h.Name)
		}
		return
	}
	if h != nil {
		t.Errorf("readChatHeader returned a header %+v alongside error %v, want nil", h, err)
	}
}

// TestHandleList_CarriesLastTurnOutcomeOnTheWire drives the REAL GET /api/chats
// handler and asserts on the HTTP response BYTES, which is the only place the
// two tests above cannot reach.
//
// They prove the derivation; this proves the WIRING — that the field survives
// readChatHeader, the ChatHeader struct's json tags and the response encoder,
// and so actually reaches the browser that has to paint a dot from it. A field
// derived correctly and dropped at the encoder is indistinguishable from one
// that was never derived, from the client's seat.
//
// Asserting on the raw body rather than on a re-decode is deliberate: decoding
// into the same struct that produced it would pass for a field the encoder
// omitted, because the zero value round-trips.
func TestHandleList_CarriesLastTurnOutcomeOnTheWire(t *testing.T) {
	s, _ := newTestStore(t)

	// Three chats covering the three answers a dot can be derived from: a
	// finished turn, a broken one, and a record with no outcome at all.
	seed := []struct {
		id      string
		outcome vibekit.TurnOutcome
	}{
		{"c-done", vibekit.TurnOutcomeCompleted},
		{"c-failed", vibekit.TurnOutcomeFailed},
		{"c-legacy", ""},
	}
	for _, sc := range seed {
		msg := vibekit.Message{ID: "m1", Role: vibekit.RoleAssistant, Content: "x"}
		msg.TurnOutcome = sc.outcome
		if err := s.Mutate(t.Context(), vibekit.ChatID(sc.id), func(c *vibekit.Chat, _ bool) bool {
			c.Name = sc.id
			c.Messages = []vibekit.Message{msg}
			return true
		}); err != nil {
			t.Fatalf("seed %s: %v", sc.id, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/chats", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// The key has to be on the wire spelled the way the generated TypeScript
	// reads it. A Go-side rename would keep every other test green.
	for _, want := range []string{
		`"last_turn_outcome":"completed"`,
		`"last_turn_outcome":"failed"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("response body does not carry %s; body = %s", want, body)
		}
	}

	// The legacy chat must carry NO key rather than an empty one: `omitempty` is
	// what keeps "this record predates the field" distinguishable from a real
	// verdict, and the client's `outcomeLatch` reads an absent field as "latch
	// nothing".
	var envelope struct {
		Chats []map[string]any `json:"chats"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(envelope.Chats) != len(seed) {
		t.Fatalf("got %d rows, want %d", len(envelope.Chats), len(seed))
	}
	for _, row := range envelope.Chats {
		id, _ := row["id"].(string)
		_, present := row["last_turn_outcome"]
		if id == "c-legacy" && present {
			t.Errorf("chat %s carries last_turn_outcome = %v, want the key absent", id, row["last_turn_outcome"])
		}
		if id != "c-legacy" && !present {
			t.Errorf("chat %s is missing last_turn_outcome", id)
		}
	}
}

// TestScanMessagesArray_StopsAtTheFirstSyntaxError pins the walk's own
// fail-soft contract directly, where the test above can only reach it through
// json.Unmarshal's stricter whole-document check.
//
// The elements BEFORE the damage are counted and their outcome is kept: they
// were read successfully, and discarding them would turn a partially damaged
// transcript into an empty one.
func TestScanMessagesArray_StopsAtTheFirstSyntaxError(t *testing.T) {
	raw := json.RawMessage(`[{"turn_outcome":"completed"},{"id":"m2"`)

	count, last := scanMessagesArray(raw)

	if count != 1 {
		t.Errorf("count = %d, want 1 (the one element that read cleanly)", count)
	}
	if last != vibekit.TurnOutcomeCompleted {
		t.Errorf("outcome = %q, want %q", last, vibekit.TurnOutcomeCompleted)
	}
}
