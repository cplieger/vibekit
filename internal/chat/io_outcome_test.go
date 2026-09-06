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
// read, shared by the two tests below so the cross-path agreement and the
// per-case expectation cannot drift apart. Each is a real persisted shape,
// including rows the agent writes DURING a turn, which land after the carrier and
// carry no outcome of their own.
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

// TestReadChatHeader_LastTurnOutcomeAgreesWithChatHeader holds the two header
// PRODUCERS to identical answers: the list path token-walks the raw JSON without
// materialising a Message, while broadcasts and the single-chat GET walk the
// in-memory slice. Nothing else would catch a drift, which reaches a client as one
// dot state from the list and another from a chat_updated frame.
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

// TestReadChatHeader_MalformedMessagesStillYieldsAUsableHeader pins the fail-soft
// half: an unreadable messages array leaves the outcome empty rather than failing
// the read, so a damaged chat still lists with its name and timestamps —
// invariant 6, and a chat that cannot be listed cannot be deleted from the UI.
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

// TestHandleList_CarriesLastTurnOutcomeOnTheWire drives the REAL handler and
// asserts on the response BYTES: the tests above prove the derivation, and a field
// dropped at the json tags or the encoder is indistinguishable from one never
// derived, from the client's seat.
//
// Raw body rather than a re-decode, because decoding into the same struct that
// produced it passes for an omitted field — the zero value round-trips.
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

// TestScanMessagesArray_StopsAtTheFirstSyntaxError pins the walk's fail-soft
// contract directly, where the test above reaches it only through
// json.Unmarshal's stricter whole-document check: elements BEFORE the damage are
// counted and keep their outcome, or a partial transcript becomes an empty one.
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
