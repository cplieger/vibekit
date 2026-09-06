package chat

// Does the HTTP surface STATE whether a turn is open, or leave the client to guess?
//
// The in-flight reply lives in the agent's in-memory buffer and is appended to the
// chat file once, at turn end, so a turn in flight has NO carrier in either of these
// responses. A reader that treats that silence as evidence derives `unknown` —
// "nothing closed this turn" — for a turn that is running: a TERMINAL verdict during
// the one window in which nothing can know one.
//
// Both handlers read ONE injected predicate. `handleTurns` used to pass a hardcoded
// `false`, which is the same defect on a third surface: a mid-turn fetch reported the
// in-flight turn as `unknown`, so the timeline rail painted a neutral marker labelled
// "This turn's end could not be read" for a turn that was still going.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// seedMidTurn writes the record a turn in flight actually leaves on disk: the user's
// prompt and NOTHING ELSE. The reply is accumulating in the agent's in-memory buffer
// and reaches the chat file once, at turn end, so there is no assistant message and no
// carrier — which is exactly the input `deriveTurnOutcome`'s `!sawAssistant` arm
// answers `unknown` for. Seeding an assistant message instead would read `completed`
// (the legacy-transcript case) and neither assertion below would mean anything.
func seedMidTurn(t *testing.T, s *Store, id vibekit.ChatID) {
	t.Helper()
	if err := s.Mutate(t.Context(), id, func(c *vibekit.Chat, _ bool) bool {
		c.Name = string(id)
		c.Messages = []vibekit.Message{
			{ID: "u1", Role: vibekit.RoleUser, Content: "do the thing", Ts: 1},
		}
		return true
	}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

// getChat drives the paginated single-chat GET and decodes its envelope.
func getChat(t *testing.T, s *Store, id vibekit.ChatID) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/chats/"+string(id), nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	var envelope map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v; body = %s", err, rec.Body.String())
	}
	return envelope
}

// getTurns drives the session-wide turn index and returns the newest turn's row.
func getTurns(t *testing.T, s *Store, id vibekit.ChatID) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/chats/"+string(id)+"/turns", nil)
	rec := httptest.NewRecorder()
	NewRouter(s).handleOne(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Turns []map[string]any `json:"turns"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v; body = %s", err, rec.Body.String())
	}
	if len(envelope.Turns) == 0 {
		t.Fatalf("no turns in %s", rec.Body.String())
	}
	return envelope.Turns[len(envelope.Turns)-1]
}

func TestChatGet_ReportsTurnOpenFromTheInjectedPredicate(t *testing.T) {
	// BOTH directions from one fixture: the key has to be on the wire spelled the
	// way `decodeChatGetResponseLocal` reads it, and it has to carry the predicate's
	// answer rather than a constant.
	for _, open := range []bool{true, false} {
		s, err := NewStore(t.TempDir(), WithTurnOpen(func(vibekit.ChatID) bool { return open }))
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		seedMidTurn(t, s, "c1")

		got := getChat(t, s, "c1")
		if got["turn_open"] != open {
			t.Errorf("turn_open = %v, want %v; the transcript response is the only place "+
				"the client can learn this without a second round trip", got["turn_open"], open)
		}
	}
}

// TestChatGet_ReportsTurnClosedWithNoPredicateInjected is the nil-tolerance half.
// Every existing chat test constructs a Store with no predicate, and a nil deref
// there would be a boot-path panic; false is also the safe answer, because a client
// told "no turn is open" derives the outcome the record supports.
func TestChatGet_ReportsTurnClosedWithNoPredicateInjected(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	seedMidTurn(t, s, "c1")

	got := getChat(t, s, "c1")
	if got["turn_open"] != false {
		t.Errorf("turn_open = %v, want false with no predicate injected", got["turn_open"])
	}
}

// TestTurnsIndex_MarksTheNewestTurnRunningWhileOpen is the THIRD surface, and the
// direction the hardcoded `false` got wrong. `projectTurnSummaries`' liveness input
// used to be a literal, so the rail's own fetch could only ever report a carrier-less
// newest turn as `unknown`.
func TestTurnsIndex_MarksTheNewestTurnRunningWhileOpen(t *testing.T) {
	s, err := NewStore(t.TempDir(), WithTurnOpen(func(vibekit.ChatID) bool { return true }))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	seedMidTurn(t, s, "c1")

	if got := getTurns(t, s, "c1")["outcome"]; got != string(vibekit.TurnOutcomeRunning) {
		t.Errorf("newest turn outcome = %v, want %q; a running turn marked otherwise paints "+
			"a settled mark on the rail for work that has not finished",
			got, vibekit.TurnOutcomeRunning)
	}
}

// TestTurnsIndex_MarksTheNewestTurnUnknownWhenNoTurnIsOpen is the other direction,
// and it is what keeps the previous item's fix from erasing this one: a reload after a
// server restart mid-turn reports no turn open, because the process died — so the
// newest turn is genuinely one nothing closed, and `unknown` is the honest answer.
func TestTurnsIndex_MarksTheNewestTurnUnknownWhenNoTurnIsOpen(t *testing.T) {
	s, err := NewStore(t.TempDir(), WithTurnOpen(func(vibekit.ChatID) bool { return false }))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	seedMidTurn(t, s, "c1")

	if got := getTurns(t, s, "c1")["outcome"]; got != string(vibekit.TurnOutcomeUnknown) {
		t.Errorf("newest turn outcome = %v, want %q; a turn nothing closed must keep its "+
			"neutral mark rather than being hidden by the liveness fix",
			got, vibekit.TurnOutcomeUnknown)
	}
}
