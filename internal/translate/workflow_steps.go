package translate

// The step-session registry, and the classifier that needs it. A workflow step runs as its own
// ACP session on the launching chat's process. A `session/update` self-describes via
// `params.update._meta.kiro.workflow` (under `update`, not `params`); a host request or `_kiro/*`
// notification carries no marker, so there the only handle is the session id and `node_start` is
// the one frame that announces it — which is the case this registry serves. Nothing is persisted:
// the run's own `inspect` state carries `sessionId` on every node as the durable copy.

import (
	"encoding/json"
	"sync"

	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/vibekit/internal/workflow"
)

// FrameOwner is who a frame on a chat's connection belongs to.
type FrameOwner uint8

// The three owners. A frame is the chat's own unless its session id says otherwise, and a
// differing id is a run step when the registry or the frame's metadata says so, else a subagent.
const (
	// OwnerChat is the launching chat itself: no session id, or the chat's own.
	OwnerChat FrameOwner = iota
	// OwnerSubagent is a session id that differs and is not a known run step. Retained rather
	// than assumed away: v3 attributes subagents by `agentSubtaskId` on the parent session.
	OwnerSubagent
	// OwnerStep is a workflow step session.
	OwnerStep
)

// FrameAttribution is who a `session/update` frame belongs to, in the form a per-kind handler
// can act on. Three owners rather than the two a bare `subSessionID string` could express: that
// gave the chat and a run step the same empty value, so `HandleSessionInfoUpdate` counted every
// step's `turn_completion` as one of the launching chat's own turns.
type FrameAttribution struct {
	// SubSessionID is the frame's session id when a SUBAGENT owns it, empty for the chat and
	// for a run step. Handlers stamp it onto a tool call or an ask, so a non-empty value means
	// "attribute this to a subagent" and nothing else; do not repurpose it as "not the chat".
	SubSessionID string
	// Step reports that a workflow STEP session owns the frame, for handlers that must not
	// apply a step's frame to the chat's own accounting. A step's DISPLAY attribution rides
	// its blocks instead (ACPWorkflowMeta.SubtaskID).
	Step bool
}

// Attribute classifies a frame and returns what its handlers need: the one derivation point for
// a `session/update`, so a step cannot classify differently depending on which handler reads it.
// `workflowMarked` is the frame's own answer when it has one, which is how a CONTENT frame
// classifies correctly even with the step registry cold after a restart.
func (t *Translator) Attribute(chatID vibekit.ChatID, sessionID string, workflowMarked bool) FrameAttribution {
	switch t.ClassifyFrame(chatID, sessionID, workflowMarked) {
	case OwnerSubagent:
		return FrameAttribution{SubSessionID: sessionID}
	case OwnerStep:
		return FrameAttribution{Step: true}
	case OwnerChat:
		return FrameAttribution{}
	}
	return FrameAttribution{}
}

// foldSource is the turn source a fold site states when it finds no open turn. A step's frames
// fold onto the launching chat, so a turn opened for one is the RUN's rather than the chat's —
// a distinction the client needs, because the attribution gate drops a step's own turn_end, so
// what closes such a turn is the run's terminal transition on the run surface.
func foldSource(step bool) vibekit.TurnOpenSource {
	if step {
		return vibekit.TurnSourceWorkflowStep
	}
	return vibekit.TurnSourceWireTurnStart
}

// stepRegistry maps a step's ACP session id to the run and node it belongs to, and counts each
// step instance's tool calls for the turn cap. Written and read from the bridge-forward
// goroutine, but a runtime has many chats and one Translator, so the mutex is real contention
// protection.
type stepRegistry struct {
	byID  map[string]StepRef
	byRun map[string]map[string]struct{}
	// turns counts tool calls per step INSTANCE. Keyed by node PATH rather than node id so a
	// repeat's second iteration does not inherit the first's count, and by workflow id so two
	// concurrent runs sharing a path do not share a counter — which could cancel a run neither
	// step had exhausted. Bounded by forgetRun, like the session map beside it.
	turns   map[stepTurnKey]int
	turnRun map[string]map[stepTurnKey]struct{}
	// runTools holds the in-flight tool calls of a parentless run's steps, keyed by run then
	// tool-call id. A `tool_call_update` must fold into its `tool_call` and a run has no
	// buffer to fold into, having neither a message nor a chat. Bounded by forgetRun on the
	// same terminal `run_complete` KAS's own notification bridge unsubscribes on.
	runTools map[string]map[string]runToolEntry
	mu       sync.RWMutex
}

// runToolEntry is one accumulated tool call plus the step row it belongs to. The path is stored
// rather than re-derived because an update frame need not carry the workflow meta the create
// carried, so the address must come from what the create recorded.
type runToolEntry struct {
	nodePath string
	call     vibekit.ToolCall
}

// stepTurnKey is the enforcement identity of a step instance: which run, and which step within
// it. A struct key rather than a joined string, because a node path comes from a workflow file
// and cannot be forged by a separator inside one of its parts. Kept separate from the DISPLAY
// grouping id even though both carry the same facts: that one must stay human-shaped, this one
// only has to be unique.
type stepTurnKey struct {
	workflowID string
	stepKey    string
}

// StepRef names the run and node a step session is executing.
type StepRef struct {
	WorkflowID string
	NodeID     string
}

func newStepRegistry() *stepRegistry {
	return &stepRegistry{
		byID:     make(map[string]StepRef),
		byRun:    make(map[string]map[string]struct{}),
		turns:    make(map[stepTurnKey]int),
		turnRun:  make(map[string]map[stepTurnKey]struct{}),
		runTools: make(map[string]map[string]runToolEntry),
	}
}

// recordRunTool stores a run step's tool call, replacing any earlier state for the same id.
func (s *stepRegistry) recordRunTool(workflowID, nodePath string, call *vibekit.ToolCall) {
	if workflowID == "" || call.ID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	byID := s.runTools[workflowID]
	if byID == nil {
		byID = make(map[string]runToolEntry)
		s.runTools[workflowID] = byID
	}
	byID[call.ID] = runToolEntry{call: *call, nodePath: nodePath}
}

// runTool returns a COPY of a run step's accumulated tool call and the step row it belongs to.
// A copy because the caller folds an update into it and writes it back, so handing out the
// stored value would mutate the map from outside its own lock.
func (s *stepRegistry) runTool(workflowID, toolCallID string) (vibekit.ToolCall, string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.runTools[workflowID][toolCallID]
	if !ok {
		return vibekit.ToolCall{}, "", false
	}
	return entry.call, entry.nodePath, true
}

// countTurn records one tool call for a step instance and returns the new count. Returns 0 for
// an unusable key, so a caller cannot mistake a missing identity for a real step's first turn.
func (s *stepRegistry) countTurn(workflowID, stepKey string) int {
	if workflowID == "" || stepKey == "" {
		return 0
	}
	key := stepTurnKey{workflowID: workflowID, stepKey: stepKey}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turns[key]++
	keys, ok := s.turnRun[workflowID]
	if !ok {
		keys = make(map[stepTurnKey]struct{})
		s.turnRun[workflowID] = keys
	}
	keys[key] = struct{}{}
	return s.turns[key]
}

// record notes that sessionID is executing node nodeID of run workflowID.
func (s *stepRegistry) record(sessionID, workflowID, nodeID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[sessionID] = StepRef{WorkflowID: workflowID, NodeID: nodeID}
	ids, ok := s.byRun[workflowID]
	if !ok {
		ids = make(map[string]struct{})
		s.byRun[workflowID] = ids
	}
	ids[sessionID] = struct{}{}
}

// lookup resolves a session id to its step, if it is one.
func (s *stepRegistry) lookup(sessionID string) (StepRef, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ref, ok := s.byID[sessionID]
	return ref, ok
}

// refFor resolves a frame's session id to its run and node, when it is a step's. The empty
// StepRef for everything else lets an ask handler stamp unconditionally: omitempty keeps two
// empty strings off the wire, so the miss is not an error.
func (s *stepRegistry) refFor(sessionID string) StepRef {
	if sessionID == "" {
		return StepRef{}
	}
	ref, _ := s.lookup(sessionID)
	return ref
}

// forgetRun drops every step session and turn count of a terminated run, bounding growth that
// would otherwise accumulate one entry per step forever. The hook is a TERMINAL `run_complete`,
// gated by the caller — see ForgetRunSteps.
//
// The status test is the whole correctness of the bound: KAS reports a step parked on a question
// through this same frame with `status: paused`, so wiping on every `run_complete` empties the
// registry MID-RUN and the resumed run's next ask resolves no run id at all — invisible to every
// run-scoped surface while still lighting the launching chat's tab dot.
func (s *stepRegistry) forgetRun(workflowID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.byRun[workflowID] {
		delete(s.byID, id)
	}
	delete(s.byRun, workflowID)
	for key := range s.turnRun[workflowID] {
		delete(s.turns, key)
	}
	delete(s.turnRun, workflowID)
	delete(s.runTools, workflowID)
}

// ForgetRunSteps drops the step sessions, turn counts and in-flight step tool calls of a run
// that has ENDED. Exported rather than driven off the `run_complete` frame this translator
// already handles, because the gate is a status test owned by `internal/agent`: the branch that
// already makes this decision for `forgetBounds` makes it here too, rather than a second copy of
// the predicate that is a second place to get `paused` wrong. No empty-id guard is needed —
// both `record` doors refuse one, so an empty argument names an empty bucket.
func (t *Translator) ForgetRunSteps(workflowID string) {
	t.steps.forgetRun(workflowID)
}

// RecordStepSession notes a step session learned from an `inspect` read rather than a
// `node_start` frame. That is the recovery path: after a restart the registry is empty and a
// resumed run's step frames carry session ids nothing announced in this process.
func (t *Translator) RecordStepSession(sessionID, workflowID, nodeID string) {
	if sessionID == "" || workflowID == "" {
		return
	}
	t.steps.record(sessionID, workflowID, nodeID)
}

// RecordRunSteps seeds the registry from a raw `_kiro/workflow/inspect` reply, on every run read,
// because that read is the only other moment the durable step→session mapping is in hand: a
// restart empties the registry while the run carries on, and a resumed run's frames would then be
// classified as a subagent's. Best-effort by design — the run endpoint passes the same bytes
// through and is useful either way, so a decode failure must not fail a read, and the cost of
// missing it is one run's frames misclassified until the next read.
func (t *Translator) RecordRunSteps(raw json.RawMessage) {
	var res workflow.InspectResult
	if json.Unmarshal(raw, &res) != nil || res.State == nil {
		return
	}
	for _, st := range workflow.StepSessions(res.State) {
		t.RecordStepSession(st.SessionID, res.State.WorkflowID, st.NodeID)
	}
}

// ClassifyFrame decides who a frame belongs to, from the chat it arrived on and the session id it
// carries; the single classifier both derivation sites use. `workflowMarked` is the frame's own
// answer when it has one, which classifies a `session/update` correctly even with a cold registry.
func (t *Translator) ClassifyFrame(chatID vibekit.ChatID, sessionID string, workflowMarked bool) FrameOwner {
	parent := t.sessions.ParentACPSession(chatID)
	if sessionID == "" || parent == "" || sessionID == parent {
		return OwnerChat
	}
	if workflowMarked {
		return OwnerStep
	}
	if _, ok := t.steps.lookup(sessionID); ok {
		return OwnerStep
	}
	return OwnerSubagent
}

// foreignSession reports whether a frame belongs to something OTHER than the chat it arrived on,
// subagent or workflow step alike. Distinct from deriveSubSession, and both exist because the
// questions differ: code_references, governance and safety DROP a non-chat frame for dedup (KAS
// fans one account-global payload out to every live session), while elicitation, permission and
// user_input emit either way and only need to know whether to LABEL the ask as a subagent's —
// where a step must answer no, or its ask names a subagent that does not exist.
func (t *Translator) foreignSession(chatID vibekit.ChatID, sessionID string) bool {
	return t.ClassifyFrame(chatID, sessionID, false) != OwnerChat
}

// StepTurnCap is how many tool calls one step instance may make before the host is told it
// breached, exported so the host's own test pins the number the counter enforces. Counted on the
// new `tool_call` frame only, never an update: pending → in_progress → completed would otherwise
// count three times and cap a step at 67 real calls.
const StepTurnCap = 200

// countStepTurn tallies one of a step's tool calls and reports a breach exactly once per step
// instance. `== StepTurnCap` rather than `>=`, because the count keeps rising while the run's
// cancel travels to a node boundary and `>=` would report every frame after the breach.
func (t *Translator) countStepTurn(wf *ACPWorkflowMeta, stepKey string) {
	if wf == nil {
		return
	}
	if turns := t.steps.countTurn(wf.WorkflowID, stepKey); turns == StepTurnCap {
		t.runBounds.StepTurnCapExceeded(wf.WorkflowID, wf.NodeID, turns)
	}
}

// reportRunProgress tells the host a run's step did something observable, one of the two signals
// that roll its idle window forward (node_complete, the other, is wrapped host-side). A tool call
// STARTING is the signal rather than one completing, because a step wedged inside a long call is
// still a run making progress: the call landing is evidence KAS is producing frames at all, which
// is all the window asks. The nil-meta guard is countStepTurn's — a frame with no workflow block
// belongs to no run.
func (t *Translator) reportRunProgress(wf *ACPWorkflowMeta) {
	if wf == nil || wf.WorkflowID == "" {
		return
	}
	t.runBounds.RunMadeProgress(wf.WorkflowID)
}
