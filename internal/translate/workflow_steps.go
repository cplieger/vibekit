package translate

// The step-session registry, and the classifier that needs it.
//
// A workflow step runs as its own ACP session on the launching chat's process,
// so a step's frames arrive on that chat's bridge carrying a session id that is
// neither the chat's nor a subagent's. A `session/update` frame self-describes
// via `params.update._meta.kiro.workflow` (note: under `update`, not `params`).
// A host request or `_kiro/*` notification carries no such marker, so the only
// handle there is the session id — and the one frame that ever announces a
// step's session id is `node_start`. The registry serves that second case and
// is fed live: `node_start` fires before the step's turn begins, so its session
// is known before any of its frames arrive. Nothing is persisted — a restart
// loses the map, and the run's own `inspect` state carries `sessionId` on every
// node as the durable copy.

import (
	"encoding/json"
	"sync"

	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/vibekit/internal/workflow"
)

// FrameOwner is who a frame on a chat's connection belongs to.
type FrameOwner uint8

// The three owners. A frame is the chat's own unless its session id says
// otherwise, and a differing id is a run step when the registry or the frame's
// own metadata says so, a subagent otherwise.
const (
	// OwnerChat is the launching chat itself: no session id, or the chat's own.
	OwnerChat FrameOwner = iota
	// OwnerSubagent is a session id that differs and is not a known run step.
	// Retained rather than assumed away: v3 attributes subagents by
	// `agentSubtaskId` on the parent session, so this is the wire-compat path.
	OwnerSubagent
	// OwnerStep is a workflow step session.
	OwnerStep
)

// FrameAttribution is who a `session/update` frame belongs to, in the form a
// per-kind handler can act on.
//
// Replaces a bare `subSessionID string` that collapsed three owners into two
// values (a subagent got its id, both the chat and a run step got ""). That
// cost a live defect: `HandleSessionInfoUpdate` derived its step flag from
// `_meta.kiro.workflow`, which a `session_info_update` never carries, so the
// flag was always false and every workflow step's `turn_completion` was
// counted as one of the launching chat's own turns.
type FrameAttribution struct {
	// SubSessionID is the frame's session id when a SUBAGENT owns it, and empty
	// for the chat and for a run step. Handlers stamp it onto a tool call or an
	// ask, so a non-empty value means "attribute this to a subagent" and nothing
	// else; do not repurpose it as "not the chat".
	SubSessionID string
	// Step reports that a workflow STEP session owns the frame. A step's own
	// display attribution rides its blocks instead (ACPWorkflowMeta.SubtaskID),
	// so this exists for handlers that must not apply a step's frame to the
	// chat's own accounting.
	Step bool
}

// Attribute classifies a frame and returns what its handlers need.
//
// The one derivation point for a `session/update` frame, so a step cannot
// classify differently depending on which handler reads it. `workflowMarked` is
// the frame's own answer when it has one — true when `_meta.kiro.workflow` is
// present, which is how a CONTENT frame classifies correctly even when the
// step registry is cold after a restart.
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

// stepRegistry maps a step's ACP session id to the run and node it belongs to,
// and counts each step instance's tool calls for the turn cap.
//
// Written from the bridge-forward goroutine (one per chat) and read from the
// same, but a runtime has many chats and one Translator, so the mutex is real
// contention protection rather than ceremony.
type stepRegistry struct {
	byID  map[string]StepRef
	byRun map[string]map[string]struct{}
	// turns counts tool calls per step INSTANCE, keyed by the run plus the
	// step's display subtask id. Keyed by node PATH rather than node id so a
	// repeat's second iteration does not inherit the first's count, and by
	// workflow id so two concurrent runs sharing a node path do not share a
	// counter (which could cancel a run neither step had exhausted). Bounded by
	// forgetRun, like the session map beside it.
	turns   map[stepTurnKey]int
	turnRun map[string]map[stepTurnKey]struct{}
	// runTools holds the in-flight tool calls of a parentless run's steps, keyed
	// by run then tool-call id. A `tool_call_update` must fold into its
	// `tool_call`, and a run has no buffer to fold into — a chat's calls live in
	// its assistant-message buffer, a run has neither a message nor a chat (see
	// run_host.go, workflow_step_content.go). Bounded by forgetRun on the same
	// terminal `run_complete` frame KAS's own notification bridge unsubscribes on.
	runTools map[string]map[string]runToolEntry
	mu       sync.RWMutex
}

// runToolEntry is one accumulated tool call plus the step row it belongs to.
//
// The path is stored rather than re-derived because an update frame is not
// guaranteed to carry the workflow meta the create carried, so the address must
// come from what the create recorded.
type runToolEntry struct {
	nodePath string
	call     vibekit.ToolCall
}

// stepTurnKey is the enforcement identity of a step instance: which run, and
// which step within it.
//
// A struct key rather than a joined string, because a node path comes from a
// workflow file and is not the program's to escape — a struct key cannot be
// forged by a separator inside one of its parts. Kept separate from the
// DISPLAY grouping id (ACPWorkflowMeta.SubtaskID) even though both now carry
// the same two facts: the display id must stay stable and human-shaped, this
// one only has to be unique.
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

// recordRunTool stores a run step's tool call, replacing any earlier state for
// the same id.
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

// runTool returns a COPY of a run step's accumulated tool call and the step row
// it belongs to.
//
// A copy because the caller folds an update into it and then writes it back, so
// handing out the stored value would mutate the map from outside its own lock.
func (s *stepRegistry) runTool(workflowID, toolCallID string) (vibekit.ToolCall, string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.runTools[workflowID][toolCallID]
	if !ok {
		return vibekit.ToolCall{}, "", false
	}
	return entry.call, entry.nodePath, true
}

// countTurn records one tool call for a step instance and returns the new count.
//
// Returns 0 for an unusable key so a caller cannot mistake a missing identity for
// the first turn of a real step.
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

// refFor resolves a frame's session id to its run and node, when it is a step's.
//
// The empty StepRef for everything else lets an ask handler stamp
// unconditionally: a non-step ask stamps two empty strings, which omitempty
// then keeps off the wire, so the miss is not an error.
func (s *stepRegistry) refFor(sessionID string) StepRef {
	if sessionID == "" {
		return StepRef{}
	}
	ref, _ := s.lookup(sessionID)
	return ref
}

// forgetRun drops every step session and turn count of a terminated run.
//
// Bounds growth that would otherwise accumulate one entry per step forever.
// `run_complete` is the hook because KAS's own notification bridge
// unsubscribes on the same frame, so no later frame for that run can arrive.
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

// RecordStepSession notes a step session learned from somewhere other than a
// `node_start` frame — specifically an `inspect` read, whose `state` carries
// `sessionId` on every node. That is the recovery path: after a restart the
// registry is empty and the step frames of a resumed run carry session ids
// nothing announced in this process.
func (t *Translator) RecordStepSession(sessionID, workflowID, nodeID string) {
	if sessionID == "" || workflowID == "" {
		return
	}
	t.steps.record(sessionID, workflowID, nodeID)
}

// RecordRunSteps seeds the registry from a raw `_kiro/workflow/inspect` reply.
//
// Called on every run read, because that read is the only other moment the
// durable step→session mapping is in hand: `node_start` announces it live, but a
// container restart empties the registry while the run carries on, so a resumed
// run's frames would arrive carrying session ids nothing in this process ever
// announced — and be classified as a subagent's.
//
// Best-effort by design: the run endpoint passes the same bytes through to the
// client and is useful whether or not this landed, so a decode failure must not
// be able to fail a read. The cost of missing it is one run's frames
// misclassified until the next read.
func (t *Translator) RecordRunSteps(raw json.RawMessage) {
	var res workflow.InspectResult
	if json.Unmarshal(raw, &res) != nil || res.State == nil {
		return
	}
	for _, st := range workflow.StepSessions(res.State) {
		t.RecordStepSession(st.SessionID, res.State.WorkflowID, st.NodeID)
	}
}

// ClassifyFrame decides who a frame belongs to, from the chat it arrived on and
// the session id it carries. The single classifier both derivation sites use.
//
// `workflowMarked` is the frame's own answer when it has one — true when
// `_meta.kiro.workflow` is present — and is what makes a `session/update` frame
// classify correctly even on the recovery path where the registry is cold.
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

// foreignSession reports whether a frame belongs to something OTHER than the
// chat it arrived on — a subagent or a workflow step, either way.
//
// The distinction from deriveSubSession matters and is the reason both exist.
// Three handlers (code_references, governance, safety) drop a non-chat frame,
// and their reason is DEDUP: KAS fans the same account-global or turn-global
// payload out to every live session, so the copy tagged with a step's or a
// subagent's session id is a duplicate of the chat's copy and emitting it twice
// would double-render it. That reason applies identically to both, so those
// three ask this question. The other three (elicitation, permission_handler,
// user_input) EMIT either way and only need to know whether to LABEL the ask as
// a subagent's — which is deriveSubSession's narrower question, and a step must
// answer no there or its ask is attributed to a subagent that does not exist.
func (t *Translator) foreignSession(chatID vibekit.ChatID, sessionID string) bool {
	return t.ClassifyFrame(chatID, sessionID, false) != OwnerChat
}

// StepTurnCap is how many tool calls one step instance may make before the host
// is told it breached. Exported so the host's own test can pin the same number
// the counter enforces.
//
// Counted on the new `tool_call` frame only, never on an update: a call that
// reports pending → in_progress → completed would otherwise count three times
// and cap a step at 67 real calls.
const StepTurnCap = 200

// countStepTurn tallies one of a step's tool calls and reports a breach exactly
// once per step instance.
//
// `== StepTurnCap` rather than `>=`: the count keeps rising while the run's
// cancel travels (KAS decides at a node boundary), and `>=` would report every
// frame after the breach instead of just the first.
func (t *Translator) countStepTurn(wf *ACPWorkflowMeta, stepKey string) {
	if wf == nil {
		return
	}
	if turns := t.steps.countTurn(wf.WorkflowID, stepKey); turns == StepTurnCap {
		t.runBounds.StepTurnCapExceeded(wf.WorkflowID, wf.NodeID, turns)
	}
}
