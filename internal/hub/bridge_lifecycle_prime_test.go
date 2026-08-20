package hub

// Tests for primeIfNeeded branch coverage. Three paths:
//   1. primeReasonNone → no-op (restart recovery uses session/load,
//      silent catch-up must NOT replay history).
//   2. primeReasonSwitch with non-empty history → session/prompt
//      fires with the prime text.
//   3. Empty BuildHistory → early return regardless of primeReason.

import (
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/translate"
	"github.com/cplieger/vibekit/internal/vibekit"
)

func TestPrimeIfNeeded_NoneIsNoOp(t *testing.T) {
	t.Parallel()
	h, cs, br := newTestHub()
	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.Messages = []vibekit.Message{{Role: vibekit.RoleUser, Content: "hi"}}
		return true
	})
	sb, err := h.coord.GetOrCreateBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("getOrCreateBridge: %v", err)
	}
	sb.primeReason = primeReasonNone

	br.mu.Lock()
	before := append([]string(nil), br.calls...)
	br.mu.Unlock()

	h.coord.PrimeIfNeeded(t.Context(), "c1")

	br.mu.Lock()
	after := append([]string(nil), br.calls...)
	br.mu.Unlock()
	if len(after) != len(before) {
		t.Errorf("primeIfNeeded(None) issued Calls: before=%v after=%v", before, after)
	}
}

func TestPrimeIfNeeded_SwitchSendsPromptWithHistory(t *testing.T) {
	t.Parallel()
	h, cs, br := newTestHub()
	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.Messages = []vibekit.Message{
			{Role: vibekit.RoleUser, Content: "what time is it?"},
			{Role: vibekit.RoleAssistant, Content: "it's late"},
		}
		return true
	})
	sb, err := h.coord.GetOrCreateBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("getOrCreateBridge: %v", err)
	}
	sb.primeReason = primeReasonSwitch

	h.coord.PrimeIfNeeded(t.Context(), "c1")

	br.mu.Lock()
	calls := append([]string(nil), br.calls...)
	br.mu.Unlock()

	if !slices.Contains(calls, "session/prompt") {
		t.Errorf("primeIfNeeded(Switch): session/prompt not issued; calls=%v", calls)
	}
}

func TestPrimeIfNeeded_EmptyHistoryEarlyReturn(t *testing.T) {
	t.Parallel()
	h, cs, br := newTestHub()
	// Chat with no messages → BuildHistory returns "" → primeIfNeeded
	// must return before inspecting primeReason.
	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool { c.Name = "A"; return true })
	sb, err := h.coord.GetOrCreateBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("getOrCreateBridge: %v", err)
	}
	sb.primeReason = primeReasonSwitch // would normally prime

	br.mu.Lock()
	before := len(br.calls)
	br.mu.Unlock()

	h.coord.PrimeIfNeeded(t.Context(), "c1")

	br.mu.Lock()
	after := len(br.calls)
	br.mu.Unlock()
	if after != before {
		t.Errorf("primeIfNeeded fired on empty history: before=%d after=%d", before, after)
	}
}

// ---------------------------------------------------------------------------
// The tangent's fallback: primeReasonFork is the only reason whose history
// belongs to ANOTHER chat, so it is the only one where "prime this bridge with
// its chat's transcript" is the wrong instruction.
// ---------------------------------------------------------------------------

// promptText returns the text block of the last session/prompt the fake bridge
// saw, which is the only way to tell WHICH chat's history was primed.
func promptText(t *testing.T, br *fakeBridge) string {
	t.Helper()
	p := br.paramsFor("session/prompt")
	if p == nil {
		t.Fatal("no session/prompt was issued")
	}
	blocks, ok := p["prompt"].([]map[string]any)
	if !ok {
		t.Fatalf("prompt = %T, want []map[string]any", p["prompt"])
	}
	if len(blocks) == 0 {
		t.Fatal("session/prompt carried no content blocks")
	}
	text, ok := blocks[0]["text"].(string)
	if !ok {
		t.Fatalf("prompt block text = %T, want string", blocks[0]["text"])
	}
	return text
}

// TestPrimeIfNeeded_ForkPrimesFromTheParentChat is the assertion the whole
// fallback rests on. A tangent whose session/fork was refused has NO transcript
// of its own — the parent's is the context it was opened to inherit — so reading
// the bridge's own chat would prime an empty history and return silently, and the
// agent would answer the first question blind.
func TestPrimeIfNeeded_ForkPrimesFromTheParentChat(t *testing.T) {
	t.Parallel()
	h, cs, br := newTestHub()
	_ = cs.Mutate(t.Context(), "c-parent", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "Parent"
		c.Messages = []vibekit.Message{
			{Role: vibekit.RoleUser, Content: "how does the reaper keep-list work?"},
			{Role: vibekit.RoleAssistant, Content: "it reads the whole session chain"},
		}
		return true
	})
	// The tangent itself is empty, which is exactly the state a refused fork
	// leaves it in.
	_ = cs.Mutate(t.Context(), "c-tangent", func(c *vibekit.Chat, _ bool) bool {
		c.Name = vibekit.DefaultChatName
		return true
	})
	sb, err := h.coord.GetOrCreateBridge(t.Context(), "c-tangent", "")
	if err != nil {
		t.Fatalf("getOrCreateBridge: %v", err)
	}
	sb.primeReason = primeReasonFork
	sb.primeFrom = "c-parent"

	h.coord.PrimeIfNeeded(t.Context(), "c-tangent")

	text := promptText(t, br)
	if !strings.HasPrefix(text, translate.PrimePreambleTangent) {
		t.Errorf("prime did not open with the tangent preamble; got %.80q", text)
	}
	if !strings.Contains(text, "how does the reaper keep-list work?") {
		t.Errorf("prime carried no parent history; got %.200q", text)
	}
}

// TestPrimeIfNeeded_ForkWithoutASourceReadsItsOwnChat: primeFrom empty is not a
// crash and not a read of some other chat. It falls back to the bridge's own
// chat, which for every other reason is the right answer anyway.
func TestPrimeIfNeeded_ForkWithoutASourceReadsItsOwnChat(t *testing.T) {
	t.Parallel()
	h, cs, br := newTestHub()
	_ = cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "A"
		c.Messages = []vibekit.Message{{Role: vibekit.RoleUser, Content: "its own history"}}
		return true
	})
	sb, err := h.coord.GetOrCreateBridge(t.Context(), "c1", "")
	if err != nil {
		t.Fatalf("getOrCreateBridge: %v", err)
	}
	sb.primeReason = primeReasonFork
	sb.primeFrom = ""

	h.coord.PrimeIfNeeded(t.Context(), "c1")

	if text := promptText(t, br); !strings.Contains(text, "its own history") {
		t.Errorf("prime did not read the bridge's own chat; got %.200q", text)
	}
}

// TestPrimeFromChat_IsClaimedOnce pins the note's lifetime. It is spent by the
// session it primes: a later bridge for the same chat must not re-inject a
// history that session has already read, which would put the parent's whole
// conversation into the transcript a second time.
func TestPrimeFromChat_IsClaimedOnce(t *testing.T) {
	t.Parallel()
	h, _, _ := newTestHub()
	h.coord.PrimeFromChat("c-tangent", "c-parent")

	if got := h.coord.takePrimeFrom("c-tangent"); got != "c-parent" {
		t.Fatalf("first take = %q, want c-parent", got)
	}
	if got := h.coord.takePrimeFrom("c-tangent"); got != "" {
		t.Errorf("second take = %q, want empty: the note is spent by one session", got)
	}
}

// TestPrimeFromChat_RefusesDegenerateNotes: a self-note would make a chat prime
// itself with its own transcript on its FIRST session, which is both pointless
// and a duplicate of everything the session already has.
func TestPrimeFromChat_RefusesDegenerateNotes(t *testing.T) {
	t.Parallel()
	cases := map[string][2]vibekit.ChatID{
		"self":         {"c1", "c1"},
		"empty chat":   {"", "c-parent"},
		"empty source": {"c1", ""},
	}
	for name, ids := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h, _, _ := newTestHub()
			h.coord.PrimeFromChat(ids[0], ids[1])
			if got := h.coord.takePrimeFrom(ids[0]); got != "" {
				t.Errorf("a %s note was recorded as %q", name, got)
			}
		})
	}
}

// TestSpawnBridge_ClaimsThePrimeNote closes the loop between the fork command and
// the launch: the note is claimed at SPAWN, so the reason and the source are on
// the bridge before the first prompt reaches PrimeIfNeeded.
func TestSpawnBridge_ClaimsThePrimeNote(t *testing.T) {
	t.Parallel()
	h, cs, _ := newTestHub()
	_ = cs.Mutate(t.Context(), "c-parent", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "Parent"
		c.Messages = []vibekit.Message{{Role: vibekit.RoleUser, Content: "parent history"}}
		return true
	})
	_ = cs.Mutate(t.Context(), "c-tangent", func(c *vibekit.Chat, _ bool) bool {
		c.Name = vibekit.DefaultChatName
		return true
	})
	h.coord.PrimeFromChat("c-tangent", "c-parent")

	sb, err := h.coord.GetOrCreateBridge(t.Context(), "c-tangent", "")
	if err != nil {
		t.Fatalf("getOrCreateBridge: %v", err)
	}
	if sb.primeReason != primeReasonFork {
		t.Errorf("primeReason = %q, want %q", sb.primeReason, primeReasonFork)
	}
	if sb.primeFrom != "c-parent" {
		t.Errorf("primeFrom = %q, want c-parent", sb.primeFrom)
	}
}
