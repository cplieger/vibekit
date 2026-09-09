package agent

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// runOutcomePush is recordingPush plus the KIND, which is the field this feature's
// whole gate is keyed on: a run's notification has to reach the registry as
// run_outcome or its settings key governs nothing.
type runOutcomePush struct {
	sent    chan runPushSent
	subject vibekit.PushSubject
}

type runPushSent struct {
	body    string
	kind    vibekit.PushKind
	subject vibekit.PushSubject
}

func newRunOutcomePush() *runOutcomePush {
	return &runOutcomePush{sent: make(chan runPushSent, 4)}
}

func (p *runOutcomePush) RegisterRoutes(*http.ServeMux)            {}
func (p *runOutcomePush) Subscribe(vibekit.PushSubscription)       {}
func (p *runOutcomePush) Unsubscribe(string)                       {}
func (p *runOutcomePush) HasSubscribers() bool                     { return true }
func (p *runOutcomePush) SetPreferences(map[vibekit.PushKind]bool) {}
func (p *runOutcomePush) ReloadPreferences(context.Context)        {}
func (p *runOutcomePush) Close()                                   {}
func (p *runOutcomePush) Send(
	_ context.Context, _, body string, kind vibekit.PushKind, subject vibekit.PushSubject,
) {
	p.subject = subject
	select {
	case p.sent <- runPushSent{body: body, kind: kind, subject: subject}:
	default:
	}
}

// newRunPushHub is newTestHub with a push recorder that keeps the kind.
func newRunPushHub(t *testing.T) (*Runtime, *runOutcomePush) {
	t.Helper()
	cs := newFakeChatStore()
	fp := newRunOutcomePush()
	h := New(context.Background(), t.TempDir(), func() ACPBridge { return newFakeBridge() }, cs, WithPush(fp))
	cs.Bus = h
	h.mcpRegistry.SignalReady()
	t.Cleanup(func() { shutdownHub(t, h) })
	return h, fp
}

func awaitRunPush(t *testing.T, fp *runOutcomePush) runPushSent {
	t.Helper()
	select {
	case got := <-fp.sent:
		return got
	case <-time.After(2 * time.Second):
		t.Fatal("no push sent for a terminal run; a run outlives the turn that launched it," +
			" so this is the only channel that reaches a reader who is not looking at the page")
		return runPushSent{}
	}
}

// A run's outcome reaches the push service keyed on the RUN rather than on a chat,
// because a manual or scheduled run never had one. The bodies are spelled out rather
// than read back through runOutcomeBody: an expectation computed by the code under
// test passes for any mapping, and this vocabulary is a cross-language contract with
// handlers/run.ts toastCompletion.
func TestObserveComplete_PushesTheRunsOutcome(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   string
	}{
		{"completed", "Nightly review finished"},
		{"failed", "Nightly review failed"},
		{"aborted", "Nightly review was aborted"},
		{"cancelled", "Nightly review was cancelled"},
	} {
		t.Run(tc.status, func(t *testing.T) {
			h, fp := newRunPushHub(t)

			h.runs.observeComplete(t.Context(), "", runNotif(methodWFRunComplete, map[string]any{
				"workflowId": "wf_1", "workflowName": "Nightly review", "status": tc.status,
			}))

			got := awaitRunPush(t, fp)
			if got.body != tc.want {
				t.Errorf("push body = %q, want %q", got.body, tc.want)
			}
			if got.kind != vibekit.PushKindRunOutcome {
				t.Errorf("push kind = %q, want %q; the settings key governs this kind alone",
					got.kind, vibekit.PushKindRunOutcome)
			}
			if got.subject.Key != vibekit.RunSubjectPrefix+"wf_1" {
				t.Errorf("push subject key = %q, want %q; the worker routes on that prefix",
					got.subject.Key, vibekit.RunSubjectPrefix+"wf_1")
			}
			if got.subject.ChatID != "" {
				t.Errorf("push subject carries chat id %q; a run's notification is about the RUN",
					got.subject.ChatID)
			}
		})
	}
}

// The Terminal() gate: KAS reports an onMaxIterations policy pause through this same
// frame, and that run is still this process's to resume — notifying "finished" for it
// would be false, and the resume would notify again when it really ends.
func TestObserveComplete_PushesNothingForANonTerminalRun(t *testing.T) {
	h, fp := newRunPushHub(t)

	h.runs.observeComplete(t.Context(), "", runNotif(methodWFRunComplete, map[string]any{
		"workflowId": "wf_1", "workflowName": "Nightly review", "status": "paused",
	}))

	select {
	case got := <-fp.sent:
		t.Errorf("a paused run pushed %q; the run has not ended", got.body)
	case <-time.After(200 * time.Millisecond):
	}
}

// THE REAL SHAPE OF EVERY run_complete: the frame's top-level workflowName is empty
// (KAS carries it at finalState.workflowName, which lifecycleFrame deliberately does
// not decode), so the label comes from the lease the run was granted at launch. This
// is the whole reason the label resolver exists.
func TestObserveComplete_LabelFallsBackToTheLeasesRecipe(t *testing.T) {
	h, fp := newRunPushHub(t)
	ctx := t.Context()
	h.runs.grantLease(ctx, "wf_1", "app-review", manualLaunch())

	h.runs.observeComplete(ctx, "", runNotif(methodWFRunComplete, map[string]any{
		"workflowId": "wf_1", "status": "completed",
	}))

	if got := awaitRunPush(t, fp); got.body != "app-review finished" {
		t.Errorf("push body = %q, want %q; the frame carries no name, so the lease is the source",
			got.body, "app-review finished")
	}
}

// A run vibekit did not put on the wire (a TUI launch, or a lease already released)
// has neither a frame name nor a recipe, and the floor is what keeps the notification
// from reading as a bare verb.
func TestRunOutcomeBody_FloorsTheLabel(t *testing.T) {
	t.Parallel()
	rs := &Runs{}
	label := rs.runOutcomeLabel(lifecycleFrame{WorkflowID: "wf_1", Status: vibekit.RunStatusFailed})
	if got := runOutcomeBody(vibekit.RunStatusFailed, label); got != "Workflow run failed" {
		t.Errorf("body with no name anywhere = %q, want %q", got, "Workflow run failed")
	}
}
