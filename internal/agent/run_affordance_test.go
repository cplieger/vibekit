package agent

// Tests for the one run-control table, over (status × parent × hosted).

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// allRunStatuses is KAS's WorkflowStatusSchema, exhaustively, so a case can be
// total over the vocabulary rather than over the subset the table lists.
var allRunStatuses = []string{"running", "paused", "completed", "failed", "aborted"}

// TestAffordance_VerbsByStatus pins which verbs each status accepts on a run this
// process hosts. The table mirrors what KAS accepts: `_kiro/workflow/retry` throws
// for any non-terminal status, `pause` means nothing once a run has stopped.
// Cancel is offered on both live statuses and neither terminal one, so every live
// run has a way out and a finished one offers no stop that would do nothing.
func TestAffordance_VerbsByStatus(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   []string
	}{
		{"running", []string{verbPause, verbCancel}},
		{runStatusPaused, []string{verbResume, verbCancel}},
		{"completed", []string{}},
		{"failed", []string{verbRetry}},
		{"aborted", []string{verbRetry}},
	} {
		t.Run(tc.status, func(t *testing.T) {
			got := affordanceOf(runFacts{status: tc.status, hosted: true})
			if !slices.Equal(got.Verbs, tc.want) {
				t.Errorf("affordanceOf(%q, hosted) verbs = %v, want %v", tc.status, got.Verbs, tc.want)
			}
			if len(got.Refused) != 0 {
				t.Errorf("affordanceOf(%q, hosted) refused %v; a hosted run's status is the only gate",
					tc.status, got.Refused)
			}
		})
	}
}

// TestAffordance_ARetryableRunOffersRetryWhoeverLaunchedIt pins that a failed or
// aborted run offers retry whatever parents it. kiro-cli's restore pass considers
// only a `running` or `paused` run, so nothing else recovers an aborted
// chat-parented run: withholding retry leaves it unreachable in both products.
func TestAffordance_ARetryableRunOffersRetryWhoeverLaunchedIt(t *testing.T) {
	for _, status := range []string{"failed", "aborted"} {
		for name, f := range map[string]runFacts{
			"parentless, nothing hosting it":    {status: status},
			"chat-parented, chat closed":        {status: status, parentChat: "c1", parentName: "Nightly"},
			"chat-parented, chat's bridge live": {status: status, parentChat: "c1", parentName: "Nightly", hosted: true},
		} {
			t.Run(status+": "+name, func(t *testing.T) {
				got := affordanceOf(f)
				if !got.permits(verbRetry) {
					t.Errorf("a %s run does not offer retry (%v); nothing else recovers this run, "+
						"so withholding it leaves the run unreachable", status, got.Verbs)
				}
				if sentence := got.refusal(verbRetry); sentence != "" {
					t.Errorf("retry carries a refusal %q while also being offered", sentence)
				}
			})
		}
	}
}

// TestAffordance_HostedOnlyVerbsAreWithheldWithAReason: pause and resume reach a
// run only through the process holding its registry entry — KAS's pause throws for
// a run absent from the live registry, and resume EXECUTES, so the text-only
// utility bridge would grind the run through its steps with no tools.
func TestAffordance_HostedOnlyVerbsAreWithheldWithAReason(t *testing.T) {
	for _, tc := range []struct {
		status string
		verb   string
	}{
		{"running", verbPause},
		{runStatusPaused, verbResume},
	} {
		t.Run(tc.status+"/"+tc.verb, func(t *testing.T) {
			got := affordanceOf(runFacts{status: tc.status})
			if got.permits(tc.verb) {
				t.Errorf("%s is offered on a run nothing hosts; its only outcome is a refusal", tc.verb)
			}
			if got.refusal(tc.verb) == "" {
				t.Errorf("%s was withheld with no sentence; an empty control row tells a reader "+
					"nothing about why the run cannot be driven", tc.verb)
			}
			// Cancel reaches a run through any connection, so a live run whose engine
			// is gone still has a way out.
			if !got.permits(verbCancel) {
				t.Errorf("cancel was withheld from a live run (%v), leaving it unstoppable", got.Verbs)
			}
		})
	}
}

// TestAffordance_ARefusalNamesTheChatToOpen: opening the chat respawns the bridge
// its parent session lives on, so the refusal must say which chat to open.
func TestAffordance_ARefusalNamesTheChatToOpen(t *testing.T) {
	got := affordanceOf(runFacts{status: "running", parentChat: "c-abc", parentName: "Nightly publish"})
	sentence := got.refusal(verbPause)
	if !strings.Contains(sentence, "Nightly publish") {
		t.Errorf("the refusal = %q, want it to name the chat to open", sentence)
	}

	// A never-named chat carries an empty Name, and `open ""` is not a remedy
	// anybody can follow, so the id is the fallback.
	unnamed := affordanceOf(runFacts{status: "running", parentChat: "c-abc"}).refusal(verbPause)
	if !strings.Contains(unnamed, "c-abc") {
		t.Errorf("the refusal for an unnamed chat = %q, want it to name the chat's id", unnamed)
	}

	// A parentless run has no chat to open, so its sentence must not invent one.
	parentless := affordanceOf(runFacts{status: "running"}).refusal(verbPause)
	if strings.Contains(parentless, "chat") && !strings.Contains(parentless, "Cancel") {
		t.Errorf("a parentless run's refusal = %q, want it to name no chat and to name the "+
			"verb that still works", parentless)
	}
}

// TestAffordance_AnUnknownStatusDegradesToReadOnly: a future KAS status must
// produce a read-only view rather than a wrong control, and no refusal either —
// there is no sentence to offer about a state this build cannot interpret.
func TestAffordance_AnUnknownStatusDegradesToReadOnly(t *testing.T) {
	for _, status := range []string{"", "cancelled", "some_future_status"} {
		t.Run("status="+status, func(t *testing.T) {
			got := affordanceOf(runFacts{status: status, hosted: true})
			if len(got.Verbs) != 0 || len(got.Refused) != 0 {
				t.Errorf("affordanceOf(%q) = %+v, want nothing offered and nothing refused", status, got)
			}
		})
	}
}

// TestAffordance_PauseAndResumeAreNeverOfferedTogether: they are opposites, so a
// row carrying both states two contradictory things about the run.
func TestAffordance_PauseAndResumeAreNeverOfferedTogether(t *testing.T) {
	for _, status := range allRunStatuses {
		for _, hosted := range []bool{true, false} {
			got := affordanceOf(runFacts{status: status, hosted: hosted})
			if got.permits(verbPause) && got.permits(verbResume) {
				t.Errorf("status %q (hosted=%v) offers both pause and resume: %v", status, hosted, got.Verbs)
			}
		}
	}
}

// TestAffordance_EveryOfferedVerbHasARoute guards the seam between the table and
// the routes: a verb offered with nothing behind it answers 200 and does nothing.
// Two names are deliberately not gated runVerbs — retry has its own handler because
// its reply carries the outcome, and cancel doubles as the tab-close gesture, so
// gating it would turn closing a tab whose run just finished into an error toast.
func TestAffordance_EveryOfferedVerbHasARoute(t *testing.T) {
	routes := map[string]runVerb{
		runVerbCancel.name: runVerbCancel,
		runVerbPause.name:  runVerbPause,
		runVerbResume.name: runVerbResume,
	}
	for _, verbs := range runStatusVerbs {
		for _, verb := range verbs {
			if verb == verbRetry {
				continue
			}
			v, ok := routes[verb]
			if !ok {
				t.Errorf("the table offers %q and no route issues it", verb)
				continue
			}
			if v.issue == nil {
				t.Errorf("run verb %q has no issuer: the route would answer ok without calling KAS", verb)
			}
			// A verb whose absence the table can EXPLAIN must have its route consult
			// it, or the sentence contradicts what the server accepts.
			if !v.gated && refusableVerb(verb) {
				t.Errorf("run verb %q can be refused by the table but its route does not consult "+
					"it, so the sentence would contradict what the server accepts", verb)
			}
		}
	}
}

// refusableVerb reports whether any (status × parent × hosted) combination has the
// table withhold this verb with a sentence. Derived, not listed, so a verb that
// becomes refusable later cannot slip past the assertion above.
func refusableVerb(verb string) bool {
	for _, status := range allRunStatuses {
		for _, hosted := range []bool{true, false} {
			if affordanceOf(runFacts{status: status, hosted: hosted}).refusal(verb) != "" {
				return true
			}
		}
	}
	return false
}

// TestChatForSession_ResolvesARunsParentWithoutALiveBridge: hostBridgeChat answers
// only for a chat whose bridge is LIVE, which is the wrong question for a refusal —
// a closed chat is when the reader most needs to be told which one to open.
func TestChatForSession_ResolvesARunsParentWithoutALiveBridge(t *testing.T) {
	seed := func(t *testing.T, sessions ...string) *Runtime {
		t.Helper()
		h, cs, _ := newTestHub()
		if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
			c.Name = "Nightly publish"
			for _, s := range sessions {
				c.RecordSession(s)
			}
			return true
		}); err != nil {
			t.Fatalf("Setup: seeding the chat: %s", err)
		}
		return h
	}

	t.Run("the chat's current session, with no bridge open", func(t *testing.T) {
		h := seed(t, "sess_owned")
		id, name := h.runs.chatForSession(t.Context(), "sess_owned")
		if id != "c1" || name != "Nightly publish" {
			t.Errorf("chatForSession = (%q, %q), want (c1, Nightly publish)", id, name)
		}
	})

	// A chat changes session on a failed load, a model-switch fallback and empty-turn
	// recovery, so a run launched before that is parented on a RETIRED id and
	// matching only the current one would report it as parentless.
	t.Run("a RETIRED session in the chain still resolves", func(t *testing.T) {
		h := seed(t, "sess_old", "sess_current")
		if id, _ := h.runs.chatForSession(t.Context(), "sess_old"); id != "c1" {
			t.Errorf("chatForSession(a retired session) = %q, want c1", id)
		}
	})

	t.Run("a parentless run and a stranger session resolve to nothing", func(t *testing.T) {
		h := seed(t, "sess_owned")
		for _, session := range []string{"", "sess_stranger"} {
			if id, name := h.runs.chatForSession(t.Context(), session); id != "" || name != "" {
				t.Errorf("chatForSession(%q) = (%q, %q), want empty", session, id, name)
			}
		}
	})
}

// TestAffordance_ChatParentedRunIsHostedByItsChatsBridge: a run whose launching
// chat is open IS hosted, even with nothing registered under its own synthetic
// `run:<id>` key.
func TestAffordance_ChatParentedRunIsHostedByItsChatsBridge(t *testing.T) {
	h, cs, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroWorkflowList: kasRuns(t, map[string]any{
			"workflowId": "wf_1", "status": "running", "parentSessionId": "sess_owned",
		}),
	}
	if err := cs.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "Nightly publish"
		c.RecordSession("sess_owned")
		return true
	}); err != nil {
		t.Fatalf("Setup: seeding the chat: %s", err)
	}

	closed := h.runs.affordance(t.Context(), "wf_1", "running")
	if closed.permits(verbPause) {
		t.Error("pause offered while the launching chat has no bridge; nothing holds the run")
	}
	if !strings.Contains(closed.refusal(verbPause), "Nightly publish") {
		t.Errorf("the refusal = %q, want it to name the launching chat", closed.refusal(verbPause))
	}

	if _, err := h.coord.OpenBridge(t.Context(), "c1", ""); err != nil {
		t.Fatalf("Setup: opening the chat's bridge: %s", err)
	}
	open := h.runs.affordance(t.Context(), "wf_1", "running")
	if !open.permits(verbPause) {
		t.Errorf("pause withheld from a run whose launching chat is live (%v, %v); that process "+
			"holds the run's registry entry", open.Verbs, open.Refused)
	}
}
