package hub

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/testsupport"
)

// row builds a session/list row in the measured wire shape.
func row(id, title, updated string, workflow bool) kasSessionRow {
	var r kasSessionRow
	r.SessionID = id
	r.Title = title
	r.UpdatedAt = updated
	r.Meta.Kiro.Status = "idle"
	if workflow {
		r.Meta.Kiro.Workflow = json.RawMessage(`{"workflowId":"wf-1"}`)
	}
	return r
}

// ownedBy seeds a chat store where each chat id owns its listed session ids, in
// order, so the last one is the chat's current session. Every test below needs
// this now: the picker offers TAB CONVERSATIONS, so a row with no owning chat is
// not a row at all and a fixture of bare session ids would assert nothing.
func ownedBy(t *testing.T, owners map[string][]string) *Hub {
	t.Helper()
	store := testsupport.NewInMemoryChatStore()
	for chatID, sessions := range owners {
		if err := store.Mutate(t.Context(), api.ChatID(chatID), func(c *api.Chat, _ bool) bool {
			c.Name = chatID
			for _, sid := range sessions {
				c.RecordSession(sid)
			}
			return true
		}); err != nil {
			t.Fatalf("seed chat %s: %v", chatID, err)
		}
	}
	return &Hub{chatStore: store}
}

// TestToResumable_ExcludesWorkflowSessions pins the discriminator that makes a
// KAS-sourced picker usable at all.
//
// Measured on 2.16.0: 399 sessions on one box, 93 of them carrying
// _meta.kiro.workflow (co-occurring exactly with modelId and shellType). A
// workspace that runs workflows is therefore mostly run machinery, and an
// unfiltered picker buries the user's conversations in it.
//
// This is also the correction to the design doc, which declined session/list
// partly on "createdReason is null on every row, so an inventory cannot tell a
// chat from a workflow step". createdReason IS still null on all 399 — the
// discriminator is just a different field.
func TestToResumable_ExcludesWorkflowSessions(t *testing.T) {
	// The step session is CLAIMED here on purpose: a workflow step runs on a
	// chat-owned session when an agent launched the run, so the workflow marker
	// has to win over ownership rather than the other way round.
	h := ownedBy(t, map[string][]string{
		"c1": {"sess_a"}, "c2": {"sess_b"}, "c3": {"sess_wf"},
	})
	got := h.toResumable(h.claimedSessions(t.Context()), []kasSessionRow{
		row("sess_a", "A real conversation", "2026-08-02T10:00:00.000Z", false),
		row("sess_wf", "workflow step 3", "2026-08-02T11:00:00.000Z", true),
		row("sess_b", "Another conversation", "2026-08-02T12:00:00.000Z", false),
	})

	if len(got) != 2 {
		var ids []string
		for i := range got {
			ids = append(ids, got[i].SessionID)
		}
		t.Fatalf("got %d sessions %v, want 2 (the workflow step excluded)", len(got), ids)
	}
	for i := range got {
		if got[i].SessionID == "sess_wf" {
			t.Error("a workflow-step session reached the chat picker")
		}
	}
}

// TestToResumable_NewestFirst pins the ordering and the RFC3339 → epoch-millis
// conversion. KAS reports timestamps as strings, so an unconverted sort would
// order them lexically — which happens to work for same-format dates and would
// hide the bug until a timezone or precision difference appeared.
func TestToResumable_NewestFirst(t *testing.T) {
	h := ownedBy(t, map[string][]string{"c1": {"old"}, "c2": {"new"}, "c3": {"mid"}})
	got := h.toResumable(h.claimedSessions(t.Context()), []kasSessionRow{
		row("old", "older", "2026-08-01T10:00:00.000Z", false),
		row("new", "newer", "2026-08-02T10:00:00.000Z", false),
		row("mid", "middle", "2026-08-01T22:00:00.000Z", false),
	})

	want := []string{"new", "mid", "old"}
	for i := range want {
		if got[i].SessionID != want[i] {
			t.Errorf("position %d = %q, want %q (newest first)", i, got[i].SessionID, want[i])
		}
	}
	if got[0].UpdatedAt == 0 {
		t.Error("updated_at is 0: the RFC3339 timestamp was not parsed")
	}
}

// TestToResumable_MarksSessionsAChatAlreadyOwns pins that the picker knows
// which sessions are not "previous" at all.
//
// The claim is keyed on the whole session CHAIN, not the current id. A chat
// routinely changes session — a failed session/load, a model-switch fallback,
// empty-turn recovery all call RecordSession, retiring the old id into
// PriorACPSessionIDs. Keying on ACPSessionID alone would offer a chat's own
// retired sessions back to the user as separate resumable conversations.
//
// The chain claim and the collapse are ONE property from the reader's side:
// however many sessions a chat has held, it is one conversation and earns one
// row. Asserted here as an absence, because that is what the History page
// showed when it was wrong — the same chat listed twice at two different times,
// which every `/goal` launch produced while a goal turn was being misread as an
// empty turn.
func TestToResumable_OffersOneRowPerOwningChat(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	ctx := t.Context()
	if err := store.Mutate(ctx, "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "Owned"
		c.RecordSession("sess_retired")
		c.RecordSession("sess_current")
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	h := &Hub{chatStore: store}
	got := h.toResumable(h.claimedSessions(ctx), []kasSessionRow{
		row("sess_current", "current", "2026-08-02T12:00:00.000Z", false),
		row("sess_retired", "retired", "2026-08-02T11:00:00.000Z", false),
		row("sess_orphan", "never seen by vibekit", "2026-08-02T10:00:00.000Z", false),
	})

	byID := map[string]api.ResumableSession{}
	for i := range got {
		byID[got[i].SessionID] = got[i]
	}
	if len(got) != 1 {
		t.Fatalf("rows = %d, want 1 (one per chat; the orphan is not a tab conversation): %+v",
			len(got), got)
	}
	// The NEWEST member survives, because UpdatedAt is what the row displays and
	// what the list sorts on, and it is the chat's live session in every case
	// that produces a chain.
	if byID["sess_current"].ChatID != "c1" {
		t.Errorf("current session chat_id = %q, want c1", byID["sess_current"].ChatID)
	}
	if _, present := byID["sess_retired"]; present {
		t.Error("the retired session earned its own row: one chat must offer one row")
	}
	if _, present := byID["sess_orphan"]; present {
		t.Error("a session no chat owns reached the picker")
	}
}

// The population is TAB CONVERSATIONS, and this is the rule that makes the
// History page's row set MECE. Everything a user met that was not a conversation
// arrived through the unclaimed branch:
//
//   - vibekit's own UTILITY-bridge session. Its `session/new` is an ordinary
//     session in the workspace cwd, so it sat at the TOP of the page (newest
//     updatedAt) reading "New Session", permanently — a live bridge's session is
//     exempt from the orphan sweep too. Clicking it adopted vibekit's machinery as
//     a chat, whose replay has no user turn, so the page rendered blank and an
//     empty chat was left behind. Measured on the live volume: 3 of 10 workspace
//     sessions were this.
//   - a session from a `kiro-cli` run inside the container.
//
// Ownership is the only test that separates them, and it has to be: session/list
// carries NO message count (KAS's SessionSummary has none), so emptiness is not on
// the wire, and a utility turn — a commit message, an error explanation — gives its
// session real messages and a derived title, so such a row can look exactly like a
// conversation. `createdAt` and `lastModifiedAt` differ by ~30ms even on a session
// that never ran a turn, so an equality test does not separate them either.
func TestToResumable_ExcludesEverySessionNoChatOwns(t *testing.T) {
	h := ownedBy(t, map[string][]string{"c1": {"sess_real"}})
	got := h.toResumable(h.claimedSessions(t.Context()), []kasSessionRow{
		// Newest, so without the rule it sorts first — which is where the user met it.
		row("sess_utility_live", "New Session", "2026-08-02T13:00:00.000Z", false),
		row("sess_real", "A real conversation", "2026-08-02T12:00:00.000Z", false),
		// A retired utility session keeps the title of whatever turn it ran.
		row("sess_utility_retired", "Write a commit message", "2026-08-02T11:00:00.000Z", false),
		row("sess_tui", "Started from the terminal", "2026-08-02T10:00:00.000Z", false),
	})

	if len(got) != 1 {
		var ids []string
		for i := range got {
			ids = append(ids, got[i].SessionID)
		}
		t.Fatalf("got %d sessions %v, want 1 (only the chat-owned one)", len(got), ids)
	}
	if got[0].SessionID != "sess_real" || got[0].ChatID != "c1" {
		t.Errorf("surviving row = %+v, want sess_real owned by c1", got[0])
	}
}

// TestParseKASTime covers the conversion's failure mode: a bad value must sink
// in the sort rather than float to "now".
func TestParseKASTime(t *testing.T) {
	cases := map[string]int64{
		"2026-08-02T13:32:21.286Z": 1785677541286,
		"":                         0,
		"not a timestamp":          0,
		"2026-08-02":               0,
	}
	for in, want := range cases {
		if got := parseKASTime(in); got != want {
			t.Errorf("parseKASTime(%q) = %d, want %d", in, got, want)
		}
	}
}

// TestWorkflowRunAttribution pins that a run is attributed to the chat that
// launched it through the launching session's CHAIN, not just its current id.
//
// A run records the `parentSessionId` it was launched from. If that chat has
// since changed session (failed load, model-switch fallback, empty-turn
// recovery), the recorded parent is now a RETIRED id — so matching on
// ACPSessionID alone leaves the run looking parentless and the review tab
// cannot say which conversation started it.
func TestWorkflowRunAttribution(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	ctx := t.Context()
	if err := store.Mutate(ctx, "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "Launcher"
		c.RecordSession("sess_launched_from") // retired below
		c.RecordSession("sess_now")
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	h := &Hub{chatStore: store}
	claimed := h.claimedSessions(ctx)

	if got := claimed["sess_launched_from"]; got != "c1" {
		t.Errorf("retired launching session resolved to %q, want c1", got)
	}
	if got := claimed["sess_now"]; got != "c1" {
		t.Errorf("current session resolved to %q, want c1", got)
	}
	if got := claimed["sess_unknown"]; got != "" {
		t.Errorf("unknown session resolved to %q, want empty", got)
	}
}

// TestStepSessionsAreNotRuns is a regression guard on the distinction that
// makes the history list usable.
//
// Measured: one workspace's session/list carried 93 workflow-tagged rows, all
// `type:"step"`, spanning only 6 runs — a single loop contributed 76 of them
// (`p24-step-parked · tick #17`, `#16`, …). The run inventory for the same
// workspace is 4 rows from _kiro/workflow/list. So step sessions must never be
// presented as runs: it would put dozens of entries in the history for one
// run, and their status is idle regardless of the run's outcome.
func TestStepSessionsAreNotRuns(t *testing.T) {
	h := ownedBy(t, map[string][]string{"c1": {"sess_chat"}})
	rows := make([]kasSessionRow, 0, 77)
	rows = append(rows, row("sess_chat", "A real conversation", "2026-08-01T03:00:00.000Z", false))
	// One run's loop, as measured.
	for i := range 76 {
		r := row("sess_step_"+string(rune('a'+i%26))+string(rune('a'+i/26)),
			"p24-step-parked · tick", "2026-08-01T03:37:00.000Z", true)
		rows = append(rows, r)
	}

	got := h.toResumable(h.claimedSessions(t.Context()), rows)
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1: 76 step sessions leaked into the chat list", len(got))
	}
	if got[0].SessionID != "sess_chat" {
		t.Errorf("surviving entry is %q, want the chat", got[0].SessionID)
	}
}

// wfRun builds a _kiro/workflow/list row.
func wfRun(id, name, status, parentSession, updated string) kasWorkflowRun {
	return kasWorkflowRun{
		WorkflowID:      id,
		Name:            name,
		Status:          status,
		ParentSessionID: parentSession,
		UpdatedAt:       updated,
	}
}

// A run a CHAT launched is that conversation's work, not a history entry of its
// own: it renders in the chat's transcript, its outcome is the agent's to handle,
// and its recovery is the agent's job. Listing it put a second door on something
// the user reaches by opening the chat, and buried the runs a user can act on —
// all six runs in the live workspace were agent-launched `/goal` runs off three
// chats, so the page was six duplicates and nothing else.
//
// A manual or scheduled run is the opposite case and must survive: nothing pushes
// on a finished run, so this page is the only place its outcome is ever read.
func TestToWorkflowRuns_KeepsOnlyParentlessRuns(t *testing.T) {
	h := ownedBy(t, map[string][]string{"c1": {"sess_retired", "sess_now"}})
	got := h.toWorkflowRuns(h.claimedSessions(t.Context()), []kasWorkflowRun{
		wfRun("wf_manual", "nightly", "completed", "", "2026-08-02T12:00:00.000Z"),
		wfRun("wf_agent", "goal", "completed", "sess_now", "2026-08-02T11:00:00.000Z"),
		// Launched from a session the chat has since RETIRED. The chain claim is
		// what catches this one; matching the current id alone would read it as
		// parentless and list it.
		wfRun("wf_agent_retired", "goal", "aborted", "sess_retired", "2026-08-02T10:00:00.000Z"),
		wfRun("wf_scheduled", "backup", "failed", "", "2026-08-02T09:00:00.000Z"),
	})

	var ids []string
	for i := range got {
		ids = append(ids, got[i].WorkflowID)
	}
	want := []string{"wf_manual", "wf_scheduled"}
	if !slices.Equal(ids, want) {
		t.Fatalf("runs = %v, want %v (parentless only, newest first)", ids, want)
	}
	for i := range got {
		if got[i].ParentChatID != "" {
			t.Errorf("%s carries parent_chat_id %q: every surviving run is parentless",
				got[i].WorkflowID, got[i].ParentChatID)
		}
	}
}
