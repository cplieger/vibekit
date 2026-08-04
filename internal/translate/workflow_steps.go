package translate

// The step-session registry, and the classifier that needs it.
//
// THE PROBLEM. A workflow step runs as its own real ACP session, on the
// launching chat's process (KAS parents a run on the calling chat's session). So
// a step's frames arrive on that chat's bridge carrying a session id that is
// neither the chat's nor a subagent's — and every site that reads a differing
// session id was written when a subagent was the only thing it could mean:
//
//   - three callers (code_references, governance, safety) `return` on a
//     non-empty result, so a step frame is silently DROPPED
//   - three more (elicitation, permission_handler, user_input) stamp
//     `SubSessionID`, so a step's ask is emitted as a subagent's ask
//
// `deriveSubSession`'s own comment called the mechanism "inert but harmless"
// because on v3 a subagent rides the parent's session id and attributes via
// `_meta.kiro.agentSubtaskId`. That was true until a run existed. It is now
// neither inert nor harmless, and the fix is a distinction rather than a
// deletion: the frame belongs to the chat, to a subagent, or to a run step.
//
// TWO IDENTIFIERS, because KAS exposes the linkage two different ways and
// neither covers both frame classes:
//
//  1. A `session/update` frame is SELF-DESCRIBING: `params.update._meta.kiro.workflow`
//     = {workflowId, workflowName, nodeId, nodePath[], type} on every step frame
//     (probe 17; note the path is under `update`, not `params`). Tool frames add
//     `agentSubtaskId` and `toolOrigin`.
//  2. A host REQUEST (`session/request_permission`) and the `_kiro/*`
//     notifications carry no such marker — their params are the method's own
//     shape. For those the only handle is the session id itself, and the one
//     frame that ever announces a step's session id is `node_start`.
//
// So the registry exists to serve (2), and it is fed by the wire rather than
// reconstructed: `node_start` is emitted immediately after KAS appends the step
// to its session tracker and BEFORE the step's turn begins, so a step's session
// is always known by the time any of its frames arrive. Nothing is persisted —
// a restart loses the map, and the run's own `inspect` state carries `sessionId`
// on every node, which is the durable copy.

import (
	"encoding/json"
	"sync"

	"github.com/cplieger/vibekit/internal/api"
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

// stepRegistry maps a step's ACP session id to the run and node it belongs to.
//
// Written from the bridge-forward goroutine (one per chat) and read from the
// same, but a hub has many chats and one Translator, so the mutex is real
// contention protection rather than ceremony.
type stepRegistry struct {
	byID  map[string]StepRef
	byRun map[string]map[string]struct{}
	mu    sync.RWMutex
}

// StepRef names the run and node a step session is executing.
type StepRef struct {
	WorkflowID string
	NodeID     string
}

func newStepRegistry() *stepRegistry {
	return &stepRegistry{
		byID:  make(map[string]StepRef),
		byRun: make(map[string]map[string]struct{}),
	}
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

// forgetRun drops every step session of a terminated run.
//
// Bounded growth is the point: a long-lived container running many workflows
// would otherwise accumulate one entry per step forever. `run_complete` is the
// hook because KAS's own notification bridge unsubscribes on the same frame, so
// no later frame for that run can arrive.
func (s *stepRegistry) forgetRun(workflowID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.byRun[workflowID] {
		delete(s.byID, id)
	}
	delete(s.byRun, workflowID)
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
func (t *Translator) ClassifyFrame(chatID api.ChatID, sessionID string, workflowMarked bool) FrameOwner {
	parent := t.deps.ParentACPSession(chatID)
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
func (t *Translator) foreignSession(chatID api.ChatID, sessionID string) bool {
	return t.ClassifyFrame(chatID, sessionID, false) != OwnerChat
}

// stepRef resolves a frame's session id to its run and node, when it is a
// step's. The empty StepRef for everything else lets an ask handler stamp
// unconditionally: a non-step ask stamps two empty strings, which omitempty
// then keeps off the wire.
func (t *Translator) stepRef(sessionID string) StepRef {
	if sessionID == "" {
		return StepRef{}
	}
	ref, _ := t.steps.lookup(sessionID)
	return ref
}
