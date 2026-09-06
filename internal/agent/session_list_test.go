package agent

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/cplieger/vibekit/internal/testsupport"
	"github.com/cplieger/vibekit/internal/vibekit"
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

// rowCreated builds a row whose creation instant matters: the picker's tie-break
// reads it, and `row` above leaves it absent the way a withheld field does.
func rowCreated(id, title, updated, created string) kasSessionRow {
	r := row(id, title, updated, false)
	r.Meta.Kiro.CreatedAt = created
	return r
}

// ownedBy seeds a chat store where each chat id owns its listed session ids, in
// order, so the last one is the chat's current session. Every test below needs
// this now: the picker offers TAB CONVERSATIONS, so a row with no owning chat is
// not a row at all and a fixture of bare session ids would assert nothing.
func ownedBy(t *testing.T, owners map[string][]string) *Runtime {
	t.Helper()
	store := testsupport.NewInMemoryChatStore()
	for chatID, sessions := range owners {
		if err := store.Mutate(t.Context(), vibekit.ChatID(chatID), func(c *vibekit.Chat, _ bool) bool {
			c.Name = chatID
			for _, sid := range sessions {
				c.RecordSession(sid)
			}
			return true
		}); err != nil {
			t.Fatalf("seed chat %s: %v", chatID, err)
		}
	}
	// runs is populated because the run projection reads the bounds state for a
	// run's end reason; a nil collaborator is a nil receiver at the first lookup.
	return &Runtime{chatStore: store, runs: &Runs{}}
}

// TestToResumable_ExcludesWorkflowSessions pins the discriminator that makes a
// KAS-sourced picker usable: measured on 2.16.0, 93 of 399 sessions carried
// _meta.kiro.workflow, so an unfiltered picker buries the conversations in run
// machinery. That field is the discriminator — `createdReason` is null on every
// row.
func TestToResumable_ExcludesWorkflowSessions(t *testing.T) {
	// The step session is CLAIMED here on purpose: a workflow step runs on a
	// chat-owned session when an agent launched the run, so the workflow marker
	// has to win over ownership rather than the other way round.
	h := ownedBy(t, map[string][]string{
		"c1": {"sess_a"}, "c2": {"sess_b"}, "c3": {"sess_wf"},
	})
	got := toResumable(h.claimedSessions(t.Context()), []kasSessionRow{
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
	got := toResumable(h.claimedSessions(t.Context()), []kasSessionRow{
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

// TestToResumable_MarksSessionsAChatAlreadyOwns keys the claim on the whole
// session CHAIN, not the current id: RecordSession retires the old id into
// PriorACPSessionIDs, so keying on ACPSessionID alone offers a chat its own
// retired sessions back as separate resumable conversations. However many a chat
// has held, it is one conversation and earns one row.
func TestToResumable_OffersOneRowPerOwningChat(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	ctx := t.Context()
	if err := store.Mutate(ctx, "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "Owned"
		c.RecordSession("sess_retired")
		c.RecordSession("sess_current")
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	h := &Runtime{chatStore: store, runs: &Runs{}}
	got := toResumable(h.claimedSessions(ctx), []kasSessionRow{
		row("sess_current", "current", "2026-08-02T12:00:00.000Z", false),
		row("sess_retired", "retired", "2026-08-02T11:00:00.000Z", false),
		row("sess_orphan", "never seen by vibekit", "2026-08-02T10:00:00.000Z", false),
	})

	byID := map[string]vibekit.ResumableSession{}
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

// Two members of one chain can tie on UpdatedAt (parseKASTime sinks an absent or
// unparseable timestamp to 0), and the surviving row is what the page shows. The
// tie breaks towards the later-CREATED session: a chain is produced by retiring a
// session for a fresh one, so the newer one is the chat's live member.
func TestToResumable_TiedRowsKeepTheLaterCreatedSession(t *testing.T) {
	h := ownedBy(t, map[string][]string{"c1": {"sess_retired", "sess_live"}})
	got := toResumable(h.claimedSessions(t.Context()), []kasSessionRow{
		// Listed first AND created first, so nothing but creation separates the two.
		rowCreated("sess_retired", "retired", "2026-08-02T12:00:00.000Z", "2026-08-02T09:00:00.000Z"),
		rowCreated("sess_live", "live", "2026-08-02T12:00:00.000Z", "2026-08-02T11:00:00.000Z"),
	})

	if len(got) != 1 {
		t.Fatalf("rows = %d, want 1 (one row per chat): %+v", len(got), got)
	}
	if got[0].SessionID != "sess_live" {
		t.Errorf("survivor of an UpdatedAt tie = %q (title %q), want sess_live (the later-created member)",
			got[0].SessionID, got[0].Title)
	}
}

// With nothing but their ids to separate two rows, the same row must survive
// whichever order KAS listed them in — while the outcome followed arrival order,
// one chat's row changed title and timestamp between two polls. Neither row
// carries a createdAt, the state a withheld field leaves them in, so the session
// id is the only key left.
func TestToResumable_TieBreakIgnoresArrivalOrder(t *testing.T) {
	h := ownedBy(t, map[string][]string{"c1": {"sess_a", "sess_b"}})
	a := row("sess_a", "listed first", "2026-08-02T12:00:00.000Z", false)
	b := row("sess_b", "listed second", "2026-08-02T12:00:00.000Z", false)

	forward := toResumable(h.claimedSessions(t.Context()), []kasSessionRow{a, b})
	reversed := toResumable(h.claimedSessions(t.Context()), []kasSessionRow{b, a})

	if len(forward) != 1 || len(reversed) != 1 {
		t.Fatalf("rows = %d listed a-then-b and %d listed b-then-a, want 1 each: %+v / %+v",
			len(forward), len(reversed), forward, reversed)
	}
	if forward[0].SessionID != "sess_b" {
		t.Errorf("survivor listed a-then-b = %q, want sess_b", forward[0].SessionID)
	}
	if reversed[0].SessionID != "sess_b" {
		t.Errorf("survivor listed b-then-a = %q, want sess_b (the survivor must not depend on the order KAS listed the pair)",
			reversed[0].SessionID)
	}
}

// The population is TAB CONVERSATIONS, and ownership is the only test that can
// separate them: session/list carries NO message count, and a utility turn (a
// commit message, an error explanation) gives its session real messages and a
// derived title, so an unclaimed row can look exactly like a conversation.
// `createdAt` and `lastModifiedAt` differ by ~30ms even on an unused session, so
// equality does not separate them either. Untreated, the utility bridge's own
// session sorted to the top of the page and adopting it left a blank chat behind.
func TestToResumable_ExcludesEverySessionNoChatOwns(t *testing.T) {
	h := ownedBy(t, map[string][]string{"c1": {"sess_real"}})
	got := toResumable(h.claimedSessions(t.Context()), []kasSessionRow{
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

// TestWorkflowRunAttribution attributes a run through the launching session's
// CHAIN, not its current id: a chat that has since changed session leaves the
// recorded `parentSessionId` retired, so matching on ACPSessionID alone reads the
// run as parentless and no tab can say which conversation started it.
func TestWorkflowRunAttribution(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	ctx := t.Context()
	if err := store.Mutate(ctx, "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "Launcher"
		c.RecordSession("sess_launched_from") // retired below
		c.RecordSession("sess_now")
		return true
	}); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	h := &Runtime{chatStore: store, runs: &Runs{}}
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

// TestStepSessionsAreNotRuns keeps step sessions out of the run list: measured,
// 93 workflow-tagged rows spanned only 6 runs (one loop contributed 76), so
// presenting them as runs puts dozens of entries in the history for one run —
// and a step's status is idle regardless of the run's outcome.
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

	got := toResumable(h.claimedSessions(t.Context()), rows)
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

// A run a CHAT launched is that conversation's work and renders in its
// transcript, so listing it puts a second door on something the user reaches by
// opening the chat and buries the runs they can act on. A manual or scheduled run
// must survive: nothing pushes on a finished run, so this page is the only place
// its outcome is ever read.
func TestToWorkflowRuns_KeepsOnlyParentlessRuns(t *testing.T) {
	h := ownedBy(t, map[string][]string{"c1": {"sess_retired", "sess_now"}})
	got := h.runs.toWire(h.claimedSessions(t.Context()), []kasWorkflowRun{
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

// TestHandleSessionList_SaysWhichListFailed pins the endpoint's honesty about a
// degraded read: 200 with `[]` for both a failed read and a workspace with
// nothing to resume left the picker unable to tell a first boot from a dead
// bridge. The two lists are asserted INDEPENDENTLY because they are separate
// verbs on one bridge: one failing must neither blank nor discredit the other.
func TestHandleSessionList_SaysWhichListFailed(t *testing.T) {
	cases := map[string]struct {
		arm          func(*fakeBridge)
		wantSessions vibekit.ReadState
		wantRuns     vibekit.ReadState
	}{
		"both reads land": {
			arm: func(br *fakeBridge) {
				br.callResults = map[string]json.RawMessage{
					vibekit.MethodSessionList: json.RawMessage(`{"sessions":[]}`),
					methodKiroWorkflowList:    json.RawMessage(`{"runs":[]}`),
				}
			},
			wantSessions: vibekit.ReadReady,
			wantRuns:     vibekit.ReadReady,
		},
		"the session read fails": {
			arm: func(br *fakeBridge) {
				br.callResults = map[string]json.RawMessage{
					methodKiroWorkflowList: json.RawMessage(`{"runs":[]}`),
				}
				br.callErrs = map[string]error{vibekit.MethodSessionList: errors.New("kas gone")}
			},
			wantSessions: vibekit.ReadUnavailable,
			wantRuns:     vibekit.ReadReady,
		},
		"the run read fails": {
			arm: func(br *fakeBridge) {
				br.callResults = map[string]json.RawMessage{
					vibekit.MethodSessionList: json.RawMessage(`{"sessions":[]}`),
				}
				br.callErrs = map[string]error{methodKiroWorkflowList: errors.New("kas gone")}
			},
			wantSessions: vibekit.ReadReady,
			wantRuns:     vibekit.ReadUnavailable,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_ = captureLogs(t)
			h, _, br := newTestHub()
			tc.arm(br)

			req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
			rec := httptest.NewRecorder()
			h.handleSessionList(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: a picker is an affordance, so a failed read "+
					"must not break the view", rec.Code)
			}
			var got vibekit.SessionListResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode reply %q: %v", rec.Body.String(), err)
			}
			if got.SessionsState != tc.wantSessions || got.RunsState != tc.wantRuns {
				t.Errorf("states = %q/%q, want %q/%q", got.SessionsState, got.RunsState,
					tc.wantSessions, tc.wantRuns)
			}
		})
	}
}
