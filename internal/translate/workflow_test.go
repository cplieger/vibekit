package translate

// Tests for the workflow-run channel and the step-frame classification it makes
// possible.
//
// Every case here pins a defect a run creates on code that predates runs. Before
// a workflow existed, a session id differing from the chat's could only mean a
// subagent, and six sites read it that way — three by DROPPING the frame and
// three by labelling it a subagent's. A step is neither, and the whole point of
// the classifier is that the two questions those six sites ask are different
// questions with different right answers for a step.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cplieger/slogx/capture"
	"github.com/cplieger/vibekit/internal/api"
)

const (
	testChat   = api.ChatID("chat-1")
	testParent = "sess_parent"
	testStep   = "sess_step"
	testSub    = "sess_subagent"
)

// notif builds an A→C notification frame with the given params.
func notif(method string, params map[string]any) *api.RPCResponse {
	raw, err := json.Marshal(params)
	if err != nil {
		panic(err)
	}
	return &api.RPCResponse{Method: method, Params: raw}
}

// capturing returns deps that append every broadcast event to the slice.
func capturing(events *[]api.ServerEvent) *baseDeps {
	d := newBaseDeps()
	d.parent = testParent
	d.onBroadcast = func(_ context.Context, evt api.ServerEvent) {
		*events = append(*events, evt)
	}
	return d
}

func TestClassifyFrame(t *testing.T) {
	t.Parallel()
	var events []api.ServerEvent
	tr := New(capturing(&events))
	tr.RecordStepSession(testStep, "wf_1", "s1")

	cases := []struct {
		name    string
		session string
		marked  bool
		want    FrameOwner
	}{
		{"no session id is the chat", "", false, OwnerChat},
		{"the chat's own session", testParent, false, OwnerChat},
		{"a registered step session", testStep, false, OwnerStep},
		// The recovery path: after a restart the registry is cold, so a resumed
		// run's frames carry session ids nothing announced in this process. The
		// frame's own _meta.kiro.workflow is what still classifies them.
		{"an unregistered session the FRAME marks as a step", "sess_unknown", true, OwnerStep},
		{"an unregistered, unmarked session is a subagent", testSub, false, OwnerSubagent},
		// A marked frame on the chat's OWN session stays the chat's: the session
		// id is the discriminator, and a marker cannot promote the parent.
		{"the marker does not override the chat's own session", testParent, true, OwnerChat},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := tr.ClassifyFrame(testChat, c.session, c.marked); got != c.want {
				t.Errorf("ClassifyFrame(%q, marked=%v) = %d, want %d", c.session, c.marked, got, c.want)
			}
		})
	}
}

// TestDeriveSubSession_StepIsNotASubagent pins the narrower of the two questions.
// A step must answer "" here, or its permission ask is emitted carrying a
// SubSessionID that names a subagent which does not exist.
func TestDeriveSubSession_StepIsNotASubagent(t *testing.T) {
	t.Parallel()
	var events []api.ServerEvent
	tr := New(capturing(&events))
	tr.RecordStepSession(testStep, "wf_1", "s1")

	if got := tr.deriveSubSession(testChat, testStep); got != "" {
		t.Errorf("deriveSubSession(step) = %q, want \"\" (a step is not a subagent)", got)
	}
	if got := tr.deriveSubSession(testChat, testSub); got != testSub {
		t.Errorf("deriveSubSession(subagent) = %q, want %q", got, testSub)
	}
	if got := tr.deriveSubSession(testChat, testParent); got != "" {
		t.Errorf("deriveSubSession(parent) = %q, want \"\"", got)
	}
}

// TestForeignSession_DropsBothNonChatOwners pins the wider question. The three
// dedup guards must drop a step's copy as well as a subagent's, because KAS fans
// the identical payload out to every live session and emitting both renders it
// twice.
func TestForeignSession_DropsBothNonChatOwners(t *testing.T) {
	t.Parallel()
	var events []api.ServerEvent
	tr := New(capturing(&events))
	tr.RecordStepSession(testStep, "wf_1", "s1")

	for _, c := range []struct {
		name    string
		session string
		want    bool
	}{
		{"chat", testParent, false},
		{"unknown session id", "", false},
		{"step", testStep, true},
		{"subagent", testSub, true},
	} {
		if got := tr.foreignSession(testChat, c.session); got != c.want {
			t.Errorf("foreignSession(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestRunNotifications_NineBecomeThree pins the whole translation table: the two
// ends of a run are their own events and the seven kinds between them are one
// invalidation carrying the kind.
func TestRunNotifications_NineBecomeThree(t *testing.T) {
	t.Parallel()
	cases := []struct {
		method   string
		params   map[string]any
		wantType api.EventType
		wantKind api.RunProgressKind
	}{
		{"run_start", map[string]any{"workflowId": "wf_1", "workflowName": "publish"}, api.EventRunStarted, ""},
		{"run_complete", map[string]any{"workflowId": "wf_1", "status": "completed"}, api.EventRunFinished, ""},
		{"node_start", map[string]any{"workflowId": "wf_1", "nodeId": "a"}, api.EventRunProgress, api.RunProgressNodeStart},
		{"node_complete", map[string]any{"workflowId": "wf_1", "nodeId": "a"}, api.EventRunProgress, api.RunProgressNodeComplete},
		{"node_paused", map[string]any{"workflowId": "wf_1", "nodeId": "a"}, api.EventRunProgress, api.RunProgressNodePaused},
		{"paused", map[string]any{"workflowId": "wf_1"}, api.EventRunProgress, api.RunProgressPaused},
		{"watch_poll", map[string]any{"workflowId": "wf_1", "nodeId": "w"}, api.EventRunProgress, api.RunProgressWatchPoll},
		{"steps_queued", map[string]any{"workflowId": "wf_1"}, api.EventRunProgress, api.RunProgressStepsQueued},
		// loop_iteration names its node in `loopId`, not `nodeId`.
		{"loop_iteration", map[string]any{"workflowId": "wf_1", "loopId": "loop"}, api.EventRunProgress, api.RunProgressLoopIteration},
	}
	for _, c := range cases {
		t.Run(c.method, func(t *testing.T) {
			t.Parallel()
			var events []api.ServerEvent
			tr := New(capturing(&events))
			msg := notif("_kiro/workflow/"+c.method, c.params)
			switch c.method {
			case "run_start":
				tr.HandleRunStart(t.Context(), testChat, msg)
			case "run_complete":
				tr.HandleRunComplete(t.Context(), testChat, msg)
			default:
				tr.RunProgressHandler(c.wantKind)(t.Context(), testChat, msg)
			}
			if len(events) != 1 {
				t.Fatalf("%s: got %d events, want 1", c.method, len(events))
			}
			if events[0].Type != c.wantType {
				t.Errorf("%s: event type = %q, want %q", c.method, events[0].Type, c.wantType)
			}
			// Every run event rides the LAUNCHING CHAT's topic: KAS parents a run
			// on the calling chat's session, so the frame arrives on that chat's
			// bridge and no session→chat resolution is needed.
			if events[0].ChatID != testChat {
				t.Errorf("%s: chat_id = %q, want %q", c.method, events[0].ChatID, testChat)
			}
			if c.wantKind == "" {
				return
			}
			p, ok := events[0].Payload.(api.RunProgressPayload)
			if !ok {
				t.Fatalf("%s: payload type %T, want RunProgressPayload", c.method, events[0].Payload)
			}
			if p.Kind != c.wantKind {
				t.Errorf("%s: kind = %q, want %q", c.method, p.Kind, c.wantKind)
			}
			if c.method == "loop_iteration" && p.NodeID != "loop" {
				t.Errorf("loop_iteration: node_id = %q, want the loopId %q", p.NodeID, "loop")
			}
		})
	}
}

// TestRunStart_CarriesTheName pins that a client which has never fetched this run
// still has something to label the row with.
func TestRunStart_CarriesTheName(t *testing.T) {
	t.Parallel()
	var events []api.ServerEvent
	tr := New(capturing(&events))
	tr.HandleRunStart(t.Context(), testChat,
		notif("_kiro/workflow/run_start", map[string]any{"workflowId": "wf_1", "workflowName": "publish-pr"}))
	p, ok := events[0].Payload.(api.RunStartedPayload)
	if !ok {
		t.Fatalf("payload type %T", events[0].Payload)
	}
	if p.WorkflowID != "wf_1" || p.Name != "publish-pr" {
		t.Errorf("payload = %+v, want {wf_1 publish-pr}", p)
	}
}

// TestRunNotifications_IgnoreFramesWithNoWorkflowID pins that a malformed frame
// emits nothing rather than an event naming the empty run.
func TestRunNotifications_IgnoreFramesWithNoWorkflowID(t *testing.T) {
	t.Parallel()
	var events []api.ServerEvent
	tr := New(capturing(&events))
	ctx := t.Context()
	tr.HandleRunStart(ctx, testChat, notif("_kiro/workflow/run_start", map[string]any{}))
	tr.HandleRunComplete(ctx, testChat, notif("_kiro/workflow/run_complete", map[string]any{}))
	tr.RunProgressHandler(api.RunProgressNodeStart)(ctx, testChat,
		notif("_kiro/workflow/node_start", map[string]any{"nodeId": "a"}))
	if len(events) != 0 {
		t.Errorf("got %d events for frames with no workflow id, want 0", len(events))
	}
}

// TestNodeStart_RecordsTheStepSession pins the one side effect the notification
// layer has, and it is what makes a step's later frames classifiable: node_start
// is the ONLY frame that announces a step's session id.
func TestNodeStart_RecordsTheStepSession(t *testing.T) {
	t.Parallel()
	var events []api.ServerEvent
	tr := New(capturing(&events))

	if _, ok := tr.steps.lookup("sess_new"); ok {
		t.Fatal("registry is not empty before node_start")
	}
	tr.RunProgressHandler(api.RunProgressNodeStart)(t.Context(), testChat,
		notif("_kiro/workflow/node_start", map[string]any{
			"workflowId": "wf_1", "nodeId": "build", "sessionId": "sess_new",
		}))
	ref, ok := tr.steps.lookup("sess_new")
	if !ok {
		t.Fatal("node_start did not record the step session")
	}
	if ref.WorkflowID != "wf_1" || ref.NodeID != "build" {
		t.Errorf("StepOf = %+v, want {wf_1 build}", ref)
	}
	// A node_start without a sessionId (the continuation/resume path) records
	// nothing rather than an entry keyed on "".
	tr.RunProgressHandler(api.RunProgressNodeStart)(t.Context(), testChat,
		notif("_kiro/workflow/node_start", map[string]any{"workflowId": "wf_1", "nodeId": "next"}))
	if _, ok := tr.steps.lookup(""); ok {
		t.Error("a node_start with no sessionId recorded an empty-keyed entry")
	}
}

// TestRunComplete_ForgetsTheRunsStepSessions pins the registry's bound. A
// long-lived container running many workflows would otherwise hold one entry per
// step forever; run_complete is the right hook because KAS's own notification
// bridge unsubscribes on the same frame, so no later frame for that run arrives.
func TestRunComplete_ForgetsTheRunsStepSessions(t *testing.T) {
	t.Parallel()
	var events []api.ServerEvent
	tr := New(capturing(&events))
	tr.RecordStepSession("sess_a", "wf_1", "a")
	tr.RecordStepSession("sess_b", "wf_1", "b")
	tr.RecordStepSession("sess_c", "wf_2", "c")

	tr.HandleRunComplete(t.Context(), testChat,
		notif("_kiro/workflow/run_complete", map[string]any{"workflowId": "wf_1", "status": "completed"}))

	for _, id := range []string{"sess_a", "sess_b"} {
		if _, ok := tr.steps.lookup(id); ok {
			t.Errorf("%s survived its run's completion", id)
		}
	}
	if _, ok := tr.steps.lookup("sess_c"); !ok {
		t.Error("another run's step session was forgotten too")
	}
}

func TestRecordStepSession_IgnoresIncompleteRefs(t *testing.T) {
	t.Parallel()
	var events []api.ServerEvent
	tr := New(capturing(&events))
	tr.RecordStepSession("", "wf_1", "a")
	tr.RecordStepSession("sess_x", "", "a")
	if _, ok := tr.steps.lookup("sess_x"); ok {
		t.Error("recorded a step with no workflow id")
	}
}

func TestStepOf_EmptySessionIsNeverAStep(t *testing.T) {
	t.Parallel()
	var events []api.ServerEvent
	tr := New(capturing(&events))
	if _, ok := tr.steps.lookup(""); ok {
		t.Error("StepOf(\"\") reported a step")
	}
}

// TestWorkflowMeta_SubtaskID pins the per-block attribution key.
func TestWorkflowMeta_SubtaskID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		meta *ACPWorkflowMeta
		want string
	}{
		{"nil", nil, ""},
		{"no workflow id", &ACPWorkflowMeta{NodeID: "a"}, ""},
		{
			"nodePath is the key, because it is instance-unique",
			&ACPWorkflowMeta{WorkflowID: "wf_1", NodeID: "wait", NodePath: []string{"wf_1", "loop", "iter-0", "wait"}},
			"wf:wf_1/loop/iter-0/wait",
		},
		{
			// Two iterations of ONE step share a nodeId and must not share a
			// block; nodePath is what separates them.
			"a second iteration is a different key",
			&ACPWorkflowMeta{WorkflowID: "wf_1", NodeID: "wait", NodePath: []string{"wf_1", "loop", "iter-1", "wait"}},
			"wf:wf_1/loop/iter-1/wait",
		},
		{
			"falls back to workflow + node when nodePath is absent",
			&ACPWorkflowMeta{WorkflowID: "wf_1", NodeID: "build"},
			"wf:wf_1/build",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := c.meta.SubtaskID(); got != c.want {
				t.Errorf("SubtaskID() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestStepChunk_OpensItsOwnBlock pins the defect that made a step's prose
// indistinguishable from the launching agent's own.
//
// The mechanism: the chunk handlers append through Buffer.AppendTextDelta(text,
// subtask), which EXTENDS the trailing block when kind and subtask both match. A
// step's text frame carries an empty agentSubtaskId (KAS stamps that only on tool
// frames), so empty matched empty and the step's words landed inside the parent's
// paragraph — reproducing exactly the context confusion workflows exist to fix.
func TestStepChunk_OpensItsOwnBlock(t *testing.T) {
	deps, events := newEventCaptureDeps()
	tr := New(deps, withIDGenerator(func() string { return "m1" }))
	ctx := t.Context()
	buf := deps.bufStore.GetOrInit(testChat)

	chunk := func(text string, wf map[string]any) json.RawMessage {
		frame := map[string]any{"content": map[string]any{"type": "text", "text": text}}
		if wf != nil {
			frame["_meta"] = map[string]any{"kiro": map[string]any{"workflow": wf}}
		}
		raw, err := json.Marshal(frame)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	tr.HandleAssistantChunk(ctx, testChat, chunk("parent says ", nil), false)
	tr.HandleAssistantChunk(ctx, testChat, chunk("step says", map[string]any{
		"workflowId": "wf_1", "nodeId": "build", "nodePath": []string{"wf_1", "build"},
	}), false)
	tr.HandleAssistantChunk(ctx, testChat, chunk(" more", map[string]any{
		"workflowId": "wf_1", "nodeId": "build", "nodePath": []string{"wf_1", "build"},
	}), false)

	if len(buf.Blocks) != 2 {
		t.Fatalf("got %d blocks, want 2 (the parent's and the step's); blocks = %+v", len(buf.Blocks), buf.Blocks)
	}
	if buf.Blocks[0].Text != "parent says " || buf.Blocks[0].AgentSubtaskID != "" {
		t.Errorf("block 0 = %+v, want the parent's unattributed text", buf.Blocks[0])
	}
	// The step's two chunks share a key, so they extend ONE block rather than
	// opening one each — the same run-extension rule, now keyed correctly.
	if buf.Blocks[1].Text != "step says more" {
		t.Errorf("block 1 text = %q, want the step's two chunks joined", buf.Blocks[1].Text)
	}
	if buf.Blocks[1].AgentSubtaskID != "wf:wf_1/build" {
		t.Errorf("block 1 subtask = %q, want %q", buf.Blocks[1].AgentSubtaskID, "wf:wf_1/build")
	}
	// The attribution also travels on the wire, so a live renderer groups the
	// step's deltas without waiting for the turn to persist.
	last := (*events)[len(*events)-1]
	p, ok := last.Payload.(api.MessageChunkPayload)
	if !ok {
		t.Fatalf("last event payload %T, want MessageChunkPayload", last.Payload)
	}
	if p.AgentSubtaskID != "wf:wf_1/build" {
		t.Errorf("chunk payload subtask = %q, want the step's key", p.AgentSubtaskID)
	}
}

// TestStepChunk_TwoIterationsDoNotShareABlock pins why nodePath rather than
// nodeId is the key: a repeat's iterations reuse the node id.
func TestStepChunk_TwoIterationsDoNotShareABlock(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	tr := New(deps, withIDGenerator(func() string { return "m1" }))
	ctx := t.Context()
	buf := deps.bufStore.GetOrInit(testChat)

	for _, iter := range []string{"iter-0", "iter-1"} {
		raw, err := json.Marshal(map[string]any{
			"content": map[string]any{"type": "text", "text": "ran " + iter},
			"_meta": map[string]any{"kiro": map[string]any{"workflow": map[string]any{
				"workflowId": "wf_1", "nodeId": "step", "nodePath": []string{"wf_1", "loop", iter, "step"},
			}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		tr.HandleAssistantChunk(ctx, testChat, raw, false)
	}
	if len(buf.Blocks) != 2 {
		t.Fatalf("got %d blocks, want one per iteration; blocks = %+v", len(buf.Blocks), buf.Blocks)
	}
}

// usageStore captures the chat-usage write persistTurnSummary makes.
type usageStore struct {
	recStore
	chat api.Chat
}

func (s *usageStore) Mutate(_ context.Context, _ api.ChatID, fn func(*api.Chat, bool) bool) error {
	s.mutateCalls++
	fn(&s.chat, true)
	return nil
}

// TestSessionInfoUpdate_StepMeteringCountsCreditsOnly pins the scoped allowance.
//
// The blanket `subSessionID != ""` gate discarded a step's turn_completion, which
// is the ONLY record of what that step spent — so a run of twenty steps reported
// no cost at all on the chat that launched and paid for it. Letting it through
// wholesale is the opposite error: TurnCount and LastTurnMs describe the
// CONVERSATION, and twenty steps would report a four-message chat as twenty-four
// turns.
func TestSessionInfoUpdate_StepMeteringCountsCreditsOnly(t *testing.T) {
	t.Parallel()
	infoFrame := func(wf bool) json.RawMessage {
		kiro := map[string]any{
			"kind":                "turn_completion",
			"promptTurnSummaries": []map[string]any{{"unit": "credit", "usage": 0.25}},
			"elapsedTime":         1234.0,
		}
		if wf {
			kiro["workflow"] = map[string]any{"workflowId": "wf_1", "nodeId": "build"}
		}
		raw, err := json.Marshal(map[string]any{"_meta": map[string]any{"kiro": kiro}})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	for _, c := range []struct {
		name          string
		step          bool
		wantTurnCount int
		wantLastMs    float64
	}{
		{"the chat's own turn moves all three", false, 1, 1234},
		{"a step's turn moves credits only", true, 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			store := &usageStore{}
			deps := newBaseDeps()
			deps.store = store
			tr := New(deps)
			tr.HandleSessionInfoUpdate(t.Context(), testChat, infoFrame(c.step), "")

			if store.chat.Usage.Credits != 0.25 {
				t.Errorf("credits = %v, want 0.25 (real spend is the user's either way)", store.chat.Usage.Credits)
			}
			if !store.chat.Usage.HasRealData {
				t.Error("HasRealData not set, so the popup would keep showing placeholder zeros")
			}
			if store.chat.Usage.TurnCount != c.wantTurnCount {
				t.Errorf("TurnCount = %d, want %d", store.chat.Usage.TurnCount, c.wantTurnCount)
			}
			if store.chat.Usage.LastTurnMs != c.wantLastMs {
				t.Errorf("LastTurnMs = %v, want %v", store.chat.Usage.LastTurnMs, c.wantLastMs)
			}
		})
	}
}

// TestSessionInfoUpdate_StepFramesWithoutMeteringStayDropped pins that the
// allowance is narrow: a step's focus, compaction and context-usage frames must
// NOT reach the chat, or a step would rename the chat and move its context ring.
func TestSessionInfoUpdate_StepFramesWithoutMeteringStayDropped(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(map[string]any{"_meta": map[string]any{"kiro": map[string]any{
		"kind":            "context_usage",
		"usagePercentage": 42.0,
		"workflow":        map[string]any{"workflowId": "wf_1", "nodeId": "build"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	store := &usageStore{}
	deps := newBaseDeps()
	deps.store = store
	New(deps).HandleSessionInfoUpdate(t.Context(), testChat, raw, "")
	if store.mutateCalls != 0 {
		t.Errorf("a step's context_usage reached the chat (%d mutations), want 0", store.mutateCalls)
	}
}

// TestRecordRunSteps_SeedsFromAnInspectRead pins the recovery path for step
// attribution.
//
// `node_start` announces a step's session id live, but a container restart
// empties the registry while the run carries on — so reading the run is the only
// other moment the mapping is in hand, and `inspect` carries it on every node.
// The assertion is on the observable consequence: the frame now classifies as a
// step rather than as a subagent.
func TestRecordRunSteps_SeedsFromAnInspectRead(t *testing.T) {
	t.Parallel()
	var events []api.ServerEvent
	tr := New(capturing(&events))

	if got := tr.ClassifyFrame(testChat, "sess_build", false); got != OwnerSubagent {
		t.Fatalf("before the read, sess_build classified %d, want OwnerSubagent", got)
	}
	tr.RecordRunSteps(json.RawMessage(`{
	  "workflowId": "wf_1",
	  "state": {"workflowId": "wf_1", "root": {"nodeId": "wf_1", "type": "sequence", "children": [
	    {"nodeId": "build", "type": "step", "sessionId": "sess_build"},
	    {"nodeId": "test", "type": "step", "sessionId": "sess_test"},
	    {"nodeId": "later", "type": "step"}
	  ]}}
	}`))

	for _, id := range []string{"sess_build", "sess_test"} {
		if got := tr.ClassifyFrame(testChat, id, false); got != OwnerStep {
			t.Errorf("after the read, %s classified %d, want OwnerStep", id, got)
		}
	}
	ref, ok := tr.steps.lookup("sess_build")
	if !ok || ref.WorkflowID != "wf_1" || ref.NodeID != "build" {
		t.Errorf("lookup(sess_build) = %+v ok=%v, want {wf_1 build} true", ref, ok)
	}
	// A step with no session has not started; recording an empty key would make
	// every unattributed frame on this chat look like a step.
	if _, ok := tr.steps.lookup(""); ok {
		t.Error("a pending step seeded an empty-keyed entry")
	}
}

// TestRecordRunSteps_ToleratesJunk pins that seeding cannot fail a read: the run
// endpoint passes the same bytes through to the client either way.
func TestRecordRunSteps_ToleratesJunk(t *testing.T) {
	t.Parallel()
	var events []api.ServerEvent
	tr := New(capturing(&events))
	for _, raw := range []string{`{`, `null`, `[]`, `{"state":null}`, `{"state":{"root":null}}`, `"a string"`, ``} {
		tr.RecordRunSteps(json.RawMessage(raw))
	}
}

// TestStepToolCall_SharesTheStepsBlockKey pins the other half of step
// attribution: a step's TOOL frames carry KAS's own agentSubtaskId (or none),
// while its TEXT is keyed by nodePath — without the same override on the tool
// path, one step's work fragments across two delegated-work boxes.
func TestStepToolCall_SharesTheStepsBlockKey(t *testing.T) {
	deps, _ := newEventCaptureDeps()
	tr := New(deps, withIDGenerator(func() string { return "m1" }))
	ctx := t.Context()
	buf := deps.bufStore.GetOrInit(testChat)

	wf := map[string]any{"workflowId": "wf_1", "nodeId": "build", "nodePath": []string{"wf_1", "build"}}
	text, err := json.Marshal(map[string]any{
		"content": map[string]any{"type": "text", "text": "building"},
		"_meta":   map[string]any{"kiro": map[string]any{"workflow": wf}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tr.HandleAssistantChunk(ctx, testChat, text, false)

	tool, err := json.Marshal(map[string]any{
		"toolCallId": "tc-1", "title": "write file", "kind": "edit", "status": "pending",
		"_meta": map[string]any{"kiro": map[string]any{
			"workflow":       wf,
			"agentSubtaskId": "kas-own-subtask-uuid",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tr.HandleToolCall(ctx, testChat, tool, "")

	if len(buf.Blocks) != 2 {
		t.Fatalf("got %d blocks, want 2 (text + tool_use): %+v", len(buf.Blocks), buf.Blocks)
	}
	want := "wf:wf_1/build"
	if buf.Blocks[0].AgentSubtaskID != want || buf.Blocks[1].AgentSubtaskID != want {
		t.Errorf("block keys = %q / %q, want both %q (one step, one box)",
			buf.Blocks[0].AgentSubtaskID, buf.Blocks[1].AgentSubtaskID, want)
	}
	if buf.ToolCalls[0].AgentSubtaskID != want {
		t.Errorf("tool call subtask = %q, want %q", buf.ToolCalls[0].AgentSubtaskID, want)
	}
}

// TestAgentLaunchedRun_IsRecorded pins the one durable trace an agent-launched
// run gets in this tier, in both directions.
//
// The capability that makes the agent able to start a run
// (_meta.kiro.settings.workflows, internal/kascap) creates runs nobody clicked,
// and this tier has no run record, no supervisor and no host-lost detection. Two
// slog lines are the whole mechanism, so a change that silently stops emitting
// them takes the only evidence with it — which is exactly the class of loss a log
// assertion catches and nothing else does.
//
// Both directions matter equally. Logging a manual run would dilute the class the
// line exists to make greppable, and the origin test is real logic rather than a
// formality: it reads `parentSessionId`, the only origin signal on this wire.
//
// slog's default logger is process-global, so no t.Parallel here.
func TestAgentLaunchedRun_IsRecorded(t *testing.T) {
	const (
		startMsg = "agent-launched workflow run started"
		endMsg   = "agent-launched workflow run finished"
	)
	cases := []struct {
		name       string
		parent     string
		wantLogged bool
	}{
		{"agent-launched run is recorded", testParent, true},
		{"manual run is not", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := capture.Default(t)
			var events []api.ServerEvent
			tr := New(capturing(&events))
			ctx := t.Context()

			start := map[string]any{"workflowId": "wf_7", "workflowName": "publish-pr"}
			done := map[string]any{
				"workflowId": "wf_7",
				"status":     "completed",
				"finalState": map[string]any{"workflowName": "publish-pr"},
			}
			if c.parent != "" {
				start["parentSessionId"] = c.parent
				done["finalState"].(map[string]any)["parentSessionId"] = c.parent
			}
			tr.HandleRunStart(ctx, testChat, notif("_kiro/workflow/run_start", start))
			tr.HandleRunComplete(ctx, testChat, notif("_kiro/workflow/run_complete", done))

			// The events are unconditional; only the log line is gated. Asserting
			// this keeps the origin gate from being "reads run_start" by accident.
			if len(events) != 2 {
				t.Fatalf("got %d SSE events, want 2 (started + finished) regardless of origin", len(events))
			}
			if !c.wantLogged {
				if n := rec.Count("agent-launched"); n != 0 {
					t.Errorf("a parentless run produced %d agent-origin log line(s), want 0", n)
				}
				return
			}
			for _, msg := range []string{startMsg, endMsg} {
				if rec.CountExact(msg) != 1 {
					t.Errorf("got %d %q lines, want 1", rec.CountExact(msg), msg)
				}
				for key, want := range map[string]string{
					"workflow_id": "wf_7",
					"origin":      "agent",
					"recipe":      "publish-pr",
				} {
					if !rec.HasAttr(msg, key, want) {
						got, _ := rec.AttrValue(msg, key)
						t.Errorf("%q: %s = %q, want %q", msg, key, got, want)
					}
				}
			}
			// The terminal line carries the outcome, because terminal covers
			// success, failure, cancel and a policy stop.
			if !rec.HasAttr(endMsg, "status", "completed") {
				got, _ := rec.AttrValue(endMsg, "status")
				t.Errorf("%q: status = %q, want %q", endMsg, got, "completed")
			}
		})
	}
}

// TestRunComplete_ReadsTopLevelParentSessionID pins the PRIMARY origin field on
// the terminal frame.
//
// The notification bridge merges `parentSessionId` top-level into every lifecycle
// payload when the run has a parent, and upstream treats that as the primary
// source with the copy inside `finalState` as a back-compat fallback. The case
// above sends both, so it passes whichever one is decoded; this one sends ONLY the
// top-level field, which is the shape a bundle that dropped the state copy would
// produce. Without the top-level decode the terminal line disappears while the
// launch line still prints — silently, since nothing else observes it.
//
// slog's default logger is process-global, so no t.Parallel here.
func TestRunComplete_ReadsTopLevelParentSessionID(t *testing.T) {
	const endMsg = "agent-launched workflow run finished"
	rec := capture.Default(t)
	var events []api.ServerEvent
	tr := New(capturing(&events))

	tr.HandleRunComplete(t.Context(), testChat, notif("_kiro/workflow/run_complete", map[string]any{
		"workflowId":      "wf_9",
		"status":          "completed",
		"parentSessionId": testParent,
		// finalState deliberately carries NO parentSessionId: only workflowName,
		// which genuinely has no top-level counterpart on this frame.
		"finalState": map[string]any{"workflowName": "publish-pr"},
	}))

	if rec.CountExact(endMsg) != 1 {
		t.Fatalf("got %d %q lines, want 1; a top-level parentSessionId is the primary origin signal",
			rec.CountExact(endMsg), endMsg)
	}
	if !rec.HasAttr(endMsg, "recipe", "publish-pr") {
		got, _ := rec.AttrValue(endMsg, "recipe")
		t.Errorf("%q: recipe = %q, want %q (still the one field only finalState carries)", endMsg, got, "publish-pr")
	}
}
