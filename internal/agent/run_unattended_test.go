package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/runlease"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// TestUnattendedBudget_MatchesTheDisclaimer pins a cross-language constant.
//
// The schedule form states the budget in words ("the ask is refused after 3
// minutes"), because a user deciding whether to schedule a job needs to know what
// happens when it asks for something nobody is there to answer. There is no
// endpoint carrying the number, so the client holds its own copy —
// UNATTENDED_BUDGET_MINUTES in static-src/schedule-picker.ts — and two copies of
// one fact drift silently in exactly the direction that matters: the form would
// keep promising three minutes after the server moved to thirty.
//
// This is the whole mechanism keeping them together. Change both.
func TestUnattendedBudget_MatchesTheDisclaimer(t *testing.T) {
	t.Parallel()
	const disclaimerMinutes = 3 // UNATTENDED_BUDGET_MINUTES, schedule-picker.ts
	if unattendedApprovalBudget != disclaimerMinutes*time.Minute {
		t.Errorf("unattendedApprovalBudget = %v, but the schedule form tells the user %d minutes; "+
			"update UNATTENDED_BUDGET_MINUTES in static-src/schedule-picker.ts to match",
			unattendedApprovalBudget, disclaimerMinutes)
	}
}

// TestIsScheduledRun_ReadsTheLeasesOrigin pins the read the run_started event's
// `scheduled` flag is derived from.
//
// The flag exists because the CLIENT cannot make this distinction: a parentless
// run's lifecycle frames are workspace-global with an empty chat id, and a manual
// launch is parentless too, so watching events cannot separate the two. Only the
// launch path knows, and the lease is where it says so.
func TestIsScheduledRun_ReadsTheLeasesOrigin(t *testing.T) {
	h := &Runs{}

	if h.IsScheduled("wf_1") {
		t.Error("an unknown run reported scheduled; a manual launch must never be marked")
	}
	h.grantLease(t.Context(), "wf_manual", "publish", manualLaunch())
	if h.IsScheduled("wf_manual") {
		t.Error("a manual run reported scheduled")
	}
	h.grantLease(t.Context(), "wf_1", "publish", scheduledLaunch("sched-1", time.Time{}))
	if !h.IsScheduled("wf_1") {
		t.Error("a scheduled run did not report scheduled")
	}
	if h.IsScheduled("wf_2") {
		t.Error("the origin leaked to another run")
	}
	// A terminal run_complete releases the lease, and the flag must go with it: the
	// run is over, so a later frame naming it is not a scheduled run starting.
	h.releaseLease(t.Context(), "wf_1")
	if h.IsScheduled("wf_1") {
		t.Error("a released run still reported scheduled")
	}
	// An empty id is not a run. Answering true here would mark every frame that
	// arrived without one.
	if h.IsScheduled("") {
		t.Error("the empty workflow id reported scheduled")
	}
}

// TestUnattendedFloor_ArmsFromTheLeaseAndSurvivesARestart is the risk the durable
// lease closes, at the surface that has teeth.
//
// The mark used to be in memory, so a restart while a scheduled run was parked
// removed the deny-fast budget with no trace: the run's next permission ask at
// 03:00 waited for a human indefinitely, and under the single-run rule that parked
// the whole recipe. The floor reads the lease now, and the lease is on disk.
func TestUnattendedFloor_ArmsFromTheLeaseAndSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	store, err := runlease.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h := &Runs{leases: store}
	h.grantLease(t.Context(), "wf_1", "nightly", scheduledLaunch("sched-1", time.Time{}))

	// The restart: a brand-new store over the same directory, and a runtime that
	// launched nothing.
	reopened, err := runlease.NewStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	after := &Runs{leases: reopened}

	l, held := after.lease("wf_1")
	if !held {
		t.Fatal("the lease did not survive, so the 03:00 ask would wait for a human forever")
	}
	if !l.Unattended {
		t.Error("the unattended mark did not survive, so the floor would not arm")
	}
	if l.ScheduleID != "sched-1" {
		t.Errorf("ScheduleID = %q; the denial could not be attributed to a row", l.ScheduleID)
	}
	if !after.IsScheduled("wf_1") {
		t.Error("the origin did not survive")
	}
}

// TestUnattendedFloor_ArmsNothingForAnUnanswerableAsk pins the third clause of the
// floor's guard, which is the one protecting the process rather than the policy.
//
// The floor answers a scheduled run's permission ask after the budget by
// REQUEST ID, so an ask that carries none cannot be answered at all — there is
// nothing to route a reply to. Arming for it would dereference an id that is not
// there, taking down the whole runtime over one malformed frame from KAS. The
// ordinary handler still runs either way: dropping the frame is the permission
// path's decision, not the floor's.
func TestUnattendedFloor_ArmsNothingForAnUnanswerableAsk(t *testing.T) {
	rs := &Runs{}
	rs.grantLease(t.Context(), "wf_1", "nightly",
		scheduledLaunch("sched-1", time.Now().Add(30*time.Second)))
	if l, held := rs.lease("wf_1"); !held || !l.Unattended {
		t.Fatal("the fixture did not produce the unattended lease the floor reaches through")
	}

	inner := 0
	noteAsk := func(context.Context, vibekit.ChatID, *vibekit.RPCResponse) { inner++ }
	wrapped := rs.permissionWithUnattendedFloor(noteAsk)

	// A permission frame with no id: nothing can answer it, and nothing may try.
	wrapped(t.Context(), runChatID("wf_1"), &vibekit.RPCResponse{
		Method: vibekit.MethodRequestPermission,
		ID:     nil,
	})

	if inner != 1 {
		t.Errorf("the ordinary permission handler ran %d times, want 1: the wrapper swallowed the "+
			"frame instead of passing it on", inner)
	}
}

// TestPermissionToolName_PrefersTheMachineAuthoredName walks the precedence
// against the six shapes the backend's permission-request call sites produce.
//
// It is a precedence rather than a gate on toolId because only one of the six
// carries one: keying on that field and failing closed would blank the operator's
// only description of the other five, which is a regression from naming something.
func TestPermissionToolName_PrefersTheMachineAuthoredName(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		params string
		want   string
	}{
		"tool approval names the tool id, not the model's title": {
			params: `{"toolCall":{"title":"Run a helpful command","kind":"execute"},
				"_meta":{"kiro":{"toolId":"execute_bash","command":"ls"}}}`,
			want: "execute_bash",
		},
		"hook approval names the hook": {
			params: `{"toolCall":{"title":"pre-commit"},"_meta":{"kiro":{"hookName":"lint","command":"make lint"}}}`,
			want:   "lint",
		},
		"turn approval gets vibekit's own name": {
			// KAS titles this one the literal "Review changes", which tells an
			// operator nothing about what the run needed.
			params: `{"toolCall":{"title":"Review changes"},"_meta":{"kiro":{"type":"turn_approval","executionId":"e1"}}}`,
			want:   turnApprovalName,
		},
		"no _meta at all falls back to the title": {
			// The hook-ask, hook-confirm and safety-override frames: three of the
			// six carry no _meta, so the title stays the only thing available.
			params: `{"toolCall":{"title":"Allow this hook to run?","kind":"other"}}`,
			want:   "Allow this hook to run?",
		},
		"an untitled request falls back to the kind": {
			params: `{"toolCall":{"kind":"edit"}}`,
			want:   "edit",
		},
		"an undecodable request is nameless": {
			params: `{"toolCall":[]}`,
			want:   "",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := permissionToolName([]byte(tc.params)); got != tc.want {
				t.Errorf("permissionToolName() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPermissionToolName_DefusesTheModelsTitle is the security half.
//
// The title is composed upstream by the MODEL, so an agent that read a poisoned
// file can put a bidi override in it — and it lands unescaped in a log line and,
// worse, in the schedule row an operator reads the next morning to decide which
// permission rule to write. The sibling permission card has defused the identical
// string through this primitive all along; this path applied none of it.
func TestPermissionToolName_DefusesTheModelsTitle(t *testing.T) {
	t.Parallel()

	t.Run("a bidi override cannot reach the row", func(t *testing.T) {
		t.Parallel()
		// Renders as an innocuous find command while `rm -rf /workspace` is what
		// the run actually asked to be approved.
		const title = "Run \u202ednuof-emaN- ecapskrow/ fr- mr\u202c"
		got := permissionToolName([]byte(`{"toolCall":{"title":` + jsonString(title) + `}}`))
		for _, r := range []rune{'\u202e', '\u202c'} {
			if strings.ContainsRune(got, r) {
				t.Errorf("permissionToolName() = %q still carries U+%04X; the row can name a "+
					"different tool than the one that was refused", got, r)
			}
		}
	})

	t.Run("an unbounded title is capped", func(t *testing.T) {
		t.Parallel()
		// The preset's truncation marker rides OUTSIDE the cap, so the bound is on
		// the CONTENT and the ceiling is that plus the marker.
		const ceiling = maxToolNameBytes + len("...")
		got := permissionToolName([]byte(`{"toolCall":{"title":"` + strings.Repeat("a", 4096) + `"}}`))
		if len(got) > ceiling {
			t.Errorf("permissionToolName() returned %d bytes, want at most %d: this value is "+
				"concatenated into the schedule row and persisted on every fire",
				len(got), ceiling)
		}
		if !strings.HasSuffix(got, "...") {
			t.Errorf("a truncated name = %q, want the truncation marked so the row does not read "+
				"as a complete name", got)
		}
	})

	t.Run("a legitimate name is byte-identical", func(t *testing.T) {
		t.Parallel()
		// The sanitizer replaces rather than deletes, so the cost is paid only by
		// a name that could also be the attack.
		for _, want := range []string{"execute_bash", "fs_write", "Modifier le fichier", "ファイルを書く"} {
			got := permissionToolName([]byte(`{"toolCall":{"title":` + jsonString(want) + `}}`))
			if got != want {
				t.Errorf("permissionToolName(%q) = %q, want it unchanged", want, got)
			}
		}
	})
}

// TestOptionIDByKind_MatchesTheKindExactly pins the whole safety property of the
// widened reader.
//
// A persistable ask advertises `allow_always` and `reject_always` beside the
// one-shot pair, and selecting either makes the backend PERSIST a rule — so a
// prefix match would turn one unattended refusal into a standing deny nobody
// wrote, which is the exact mirror of the persistent-allow hazard the approve side
// has always refused.
func TestOptionIDByKind_MatchesTheKindExactly(t *testing.T) {
	t.Parallel()
	// All four kinds, as a persistable ask advertises them.
	const persistable = `{"options":[
		{"optionId":"accept","kind":"allow_once"},
		{"optionId":"always-accept","kind":"allow_always"},
		{"optionId":"reject","kind":"reject_once"},
		{"optionId":"always-reject","kind":"reject_always"}]}`

	if got := optionIDByKind([]byte(persistable), optionKindRejectOnce); got != "reject" {
		t.Errorf("optionIDByKind(reject_once) = %q, want %q", got, "reject")
	}
	if got := optionIDByKind([]byte(persistable), optionKindAllowOnce); got != "accept" {
		t.Errorf("optionIDByKind(allow_once) = %q, want %q", got, "accept")
	}
	// The advertised id is `reject`, not `reject_once`: the kind is what is
	// matched and the id is what is echoed back, and confusing the two answers
	// with a choice the request never offered.
	if got := optionIDByKind([]byte(persistable), "reject"); got != "" {
		t.Errorf("optionIDByKind(%q) = %q, want no match: the kind is matched, never the id",
			"reject", got)
	}
	// The hazard directly, with the ordering that exposes it: only the PERSISTENT
	// twin is advertised, so a prefix match would select it and turn one
	// unattended refusal into a standing deny rule. Exact match declines, and the
	// caller falls back to cancelled.
	const persistentOnly = `{"options":[{"optionId":"always-reject","kind":"reject_always"},
		{"optionId":"always-accept","kind":"allow_always"}]}`
	for _, kind := range []string{optionKindRejectOnce, optionKindAllowOnce} {
		if got := optionIDByKind([]byte(persistentOnly), kind); got != "" {
			t.Errorf("optionIDByKind(%q) = %q against a request advertising only the persistent "+
				"twins; selecting that makes the backend write a standing rule nobody asked for",
				kind, got)
		}
	}
	// A request offering only the one-shot pair yields nothing for the twins,
	// which is what keeps the fall-back reachable.
	const oneShot = `{"options":[{"optionId":"accept","kind":"allow_once"},{"optionId":"reject","kind":"reject_once"}]}`
	for _, kind := range []string{"allow_always", "reject_always"} {
		if got := optionIDByKind([]byte(oneShot), kind); got != "" {
			t.Errorf("optionIDByKind(%q) = %q on a non-persistable ask, want none", kind, got)
		}
	}
	if got := optionIDByKind([]byte(`{"options":[]}`), optionKindRejectOnce); got != "" {
		t.Errorf("optionIDByKind on an empty option list = %q, want none", got)
	}
	if got := optionIDByKind([]byte(`{"options":42}`), optionKindRejectOnce); got != "" {
		t.Errorf("optionIDByKind on undecodable params = %q, want none", got)
	}
}

// TestAnswerUnattended_DenyUsesTheAdvertisedRejectOption is the deny side of the
// same rule the approve side already followed: answer with a choice the request
// offered, and never invent one.
//
// The claim ordering is the load-bearing part of the surrounding code and this
// test rides on it: TakePendingPerm runs BEFORE the response, because the floor
// races a human who has the run's page open.
func TestAnswerUnattended_DenyUsesTheAdvertisedRejectOption(t *testing.T) {
	for name, tc := range map[string]struct {
		options     string
		wantOpt     string
		wantOutcome string
	}{
		"a reject option is selected by id": {
			options: `[{"optionId":"accept","kind":"allow_once"},
				{"optionId":"reject","kind":"reject_once"},
				{"optionId":"always-reject","kind":"reject_always"}]`,
			wantOpt:     "reject",
			wantOutcome: "selected",
		},
		"no reject option advertised falls back to cancelled": {
			options:     `[{"optionId":"accept","kind":"allow_once"}]`,
			wantOutcome: string(vibekit.StopReasonCancelled),
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, br := hubForFSTest(t, t.TempDir())
			chatID := runChatID("wf_deny")
			h.bridge.mgr.insert(chatID, &sharedBridge{bridge: br, state: bridgeIdle})

			id := int64(90210)
			h.bus.pendingPerms.Add(id, vibekit.NewEvent(vibekit.EventPermissionNeeded, chatID,
				vibekit.PermissionNeededPayload{RequestID: id}))

			h.runs.answerUnattended(chatID, id, "sched-1", "execute_bash",
				[]byte(`{"options":`+tc.options+`}`))

			select {
			case <-br.done:
			case <-time.After(2 * time.Second):
				t.Fatal("the floor answered nothing, so the ask waits for a human who is not there")
			}
			br.respMu.Lock()
			got := br.response
			br.respMu.Unlock()

			outcome, ok := got.result.(*vibekit.PermissionOutcome)
			if !ok {
				t.Fatalf("answered with %T (%v), want a permission outcome", got.result, got.result)
			}
			if outcome.Outcome.Outcome != tc.wantOutcome {
				t.Errorf("outcome = %q, want %q", outcome.Outcome.Outcome, tc.wantOutcome)
			}
			if outcome.Outcome.OptionID != tc.wantOpt {
				t.Errorf("optionId = %q, want %q", outcome.Outcome.OptionID, tc.wantOpt)
			}
			// Never the persistent twin: selecting it would make the backend write a
			// standing deny rule out of one unattended refusal.
			if outcome.Outcome.OptionID == "always-reject" {
				t.Error("the floor selected the PERSISTENT reject; one automated refusal became a " +
					"standing rule nobody wrote")
			}
		})
	}
}

// jsonString quotes s as a JSON string literal, so a test case can carry a bidi
// control without the fixture itself being unreadable.
func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}
