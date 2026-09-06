package chat

// The in-flight reply reaches the chat file only at turn end, so a turn in flight has NO
// carrier in either response, and a reader treating that silence as evidence derives a
// TERMINAL `unknown`. Both handlers must read the same injected predicate instead.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// seedMidTurn writes the record a turn in flight leaves on disk: the user's prompt and
// NOTHING ELSE, which is the carrier-less input the derivation answers `unknown` for.
// Seeding an assistant message reads `completed`, and no assertion below would mean anything.
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

// getTurns returns the NEWEST turn's row.
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
	// BOTH directions: the key must be on the wire under the spelling the client reads,
	// carrying the predicate's answer rather than a constant.
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

// A Store constructed with no predicate must not deref nil, and false is the safe answer:
// a client told no turn is open derives the outcome the record supports.
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

// The turn index is the third surface: with a literal for its liveness input it can only
// ever report a carrier-less newest turn as `unknown`.
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

// The other direction, which keeps the liveness fix from erasing this one: after a restart
// mid-turn no turn is open, so the newest turn is genuinely one nothing closed.
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
