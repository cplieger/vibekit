package chat

import (
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// FuzzBuildHistory targets the plain-text history builder used for prime
// priming. The strongest invariant is the BOUND: this output becomes a prompt,
// so unbounded growth is not a cosmetic defect but a turn that fails upstream
// with an opaque error. Content presence is therefore conditional on fitting,
// which is why these assertions key off primeHistoryCap rather than restating
// "everything appears" — an oracle that outlived its rule would report the cap
// itself as a crasher.
func FuzzBuildHistory(f *testing.F) {
	f.Add("hello", "response", "compacted", uint8(0))
	f.Add("", "", "", uint8(1))
	f.Add("multi\nline\ninput", "multi\nline\noutput", "event content", uint8(2))
	f.Add(strings.Repeat("x", 10000), "short", "", uint8(3))
	// Over the cap in one message, and over it across messages: the two trim
	// paths, seeded so `go test` exercises both without coverage-guided fuzzing.
	f.Add(strings.Repeat("y", primeHistoryCap+1), "short", "", uint8(0))
	f.Add(strings.Repeat("a", primeHistoryCap/2), strings.Repeat("b", primeHistoryCap), "", uint8(0))

	f.Fuzz(func(t *testing.T, userContent, assistantContent, eventContent string, flags uint8) {
		dir := t.TempDir()
		s, err := NewStore(dir)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}

		chatID := api.ChatID("fuzz-history-1")
		ctx := t.Context()

		// Create chat with messages of various roles.
		err = s.Mutate(ctx, chatID, func(c *api.Chat, exists bool) bool {
			c.Name = "history-test"
			c.Messages = []api.Message{
				{ID: "m1", Role: api.RoleUser, Content: userContent, Ts: 1},
				{ID: "m2", Role: api.RoleAssistant, Content: assistantContent, Ts: 2},
				{ID: "m3", Role: api.RoleEvent, EventKind: api.EventCompacted, Content: eventContent, Ts: 3},
			}
			// Optionally add unknown role based on flags.
			if flags&1 != 0 {
				c.Messages = append(c.Messages, api.Message{
					ID: "m4", Role: api.Role("unknown_role"), Content: "ignored", Ts: 4,
				})
			}
			return true
		})
		if err != nil {
			return
		}

		history := s.BuildHistory(ctx, chatID)

		// Invariant 1, the load-bearing one: the output is bounded. This is what
		// stops a long chat's prime from exceeding the model's window.
		if len(history) > primeHistoryCap {
			t.Fatalf("BuildHistory returned %d bytes, over the %d cap", len(history), primeHistoryCap)
		}

		// Invariant 2: the NEWEST message always survives in some form, because a
		// prime with no final turn cannot resume anything. The event message is
		// last here unless flags added an unknown role after it, which renders to
		// nothing and so leaves the event last among rendered messages.
		if eventContent != "" && len(eventContent) < primeHistoryCap/2 &&
			!strings.Contains(history, eventContent) {
			t.Fatalf("BuildHistory dropped the newest message")
		}

		// Invariant 3: trimming is ANNOUNCED. A silently shortened prime is
		// indistinguishable from a short conversation, so the model would answer
		// confidently about history it never received.
		full := len("User: "+userContent+"\n") + len("Assistant: "+assistantContent+"\n") +
			len("["+string(api.EventCompacted)+"] "+eventContent+"\n")
		if full > primeHistoryCap && !strings.Contains(history, "omitted") &&
			!strings.Contains(history, "...") {
			t.Fatalf("BuildHistory trimmed %d bytes to %d without saying so", full, len(history))
		}

		// Invariant 4: content that FITS is present verbatim.
		if full <= primeHistoryCap {
			if userContent != "" && !strings.Contains(history, userContent) {
				t.Fatalf("BuildHistory missing user content that fit the cap")
			}
			if assistantContent != "" && !strings.Contains(history, assistantContent) {
				t.Fatalf("BuildHistory missing assistant content that fit the cap")
			}
			if !strings.Contains(history, "User: ") || !strings.Contains(history, "Assistant: ") {
				t.Fatalf("BuildHistory missing a role prefix")
			}
		}

		// Invariant 5: unknown roles must NOT appear in output.
		if flags&1 != 0 && strings.Contains(history, "ignored") {
			t.Fatalf("BuildHistory included unknown role content")
		}
	})
}
