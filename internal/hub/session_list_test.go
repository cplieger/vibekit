package hub

import (
	"context"
	"encoding/json"
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
	h := &Hub{chatStore: testsupport.NewInMemoryChatStore()}
	got := h.toResumable(h.claimedSessions(context.Background()), []kasSessionRow{
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
	h := &Hub{chatStore: testsupport.NewInMemoryChatStore()}
	got := h.toResumable(h.claimedSessions(context.Background()), []kasSessionRow{
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
func TestToResumable_MarksSessionsAChatAlreadyOwns(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	ctx := context.Background()
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
	if byID["sess_current"].ChatID != "c1" {
		t.Errorf("current session chat_id = %q, want c1", byID["sess_current"].ChatID)
	}
	if byID["sess_retired"].ChatID != "c1" {
		t.Errorf("RETIRED session chat_id = %q, want c1 — the claim must cover the whole chain",
			byID["sess_retired"].ChatID)
	}
	if byID["sess_orphan"].ChatID != "" {
		t.Errorf("orphan session chat_id = %q, want empty", byID["sess_orphan"].ChatID)
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
	ctx := context.Background()
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
	h := &Hub{chatStore: testsupport.NewInMemoryChatStore()}
	rows := make([]kasSessionRow, 0, 77)
	rows = append(rows, row("sess_chat", "A real conversation", "2026-08-01T03:00:00.000Z", false))
	// One run's loop, as measured.
	for i := range 76 {
		r := row("sess_step_"+string(rune('a'+i%26))+string(rune('a'+i/26)),
			"p24-step-parked · tick", "2026-08-01T03:37:00.000Z", true)
		rows = append(rows, r)
	}

	got := h.toResumable(h.claimedSessions(context.Background()), rows)
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1: 76 step sessions leaked into the chat list", len(got))
	}
	if got[0].SessionID != "sess_chat" {
		t.Errorf("surviving entry is %q, want the chat", got[0].SessionID)
	}
}
