package agent

// The pending-run-ask registry, and the two doors a step's question arrives through.
//
// IT IS NOT pendingPermsTracker and the two must not be merged: that tracker holds an open
// JSON-RPC request keyed by (chat, id) which cannot outlive its bridge, while a run ask has
// no request id, blocks nothing upstream, and is DURABLE across a bridge death and a
// container restart — hence a different identity, a different clearing rule, and a reconcile
// against KAS's own state. GROWTH IS BOUNDED BY THE RUN, not by an expiry: every clear is
// idempotent, so the answer and lifecycle paths both run for one ask independently.

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"

	"github.com/cplieger/keyenc"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// scrubLog strips the CR and LF a wire-sourced value could carry to forge extra
// log lines (CWE-117). A real workflow id, node id or pause reason never contains
// them, so it is a no-op on honest input and a barrier on hostile input.
func scrubLog(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\n", ""), "\r", "")
}

// runAskKey is a pending ask's identity: the run it parks, plus the ask within it. THE
// PAIR, never the ask id alone — a synthesised id derives from a node path, which two
// concurrent runs of one recipe share, so an id-only key would let one run's reconcile
// overwrite the other's ask.
type runAskKey struct {
	workflowID string
	askID      string
}

// runAsk is one unanswered ask plus the surface it was keyed to. The chat id is STORED
// rather than re-derived: it is the client's queue key and not a property of the run, so
// only the door the frame arrived through knows it.
//
// Shared out by pointer safely rather than merely cheaply: an entry is immutable after
// Add, and every method that hands one back has already DELETED it, so the registry and
// its caller never hold the same ask at once.
type runAsk struct {
	chatID  vibekit.ChatID
	payload vibekit.RunInputNeededPayload
}

// event renders an ask as the frame a client consumes, so the live broadcast and
// the connect-time replay cannot disagree about its shape.
func (a *runAsk) event() vibekit.ServerEvent {
	return vibekit.NewEvent(vibekit.EventRunInputNeeded, a.chatID, a.payload)
}

// pendingRunAsks holds the asks nobody has answered.
//
// Its own mutex: it is read from every SSE connect and written from every bridge's
// forward goroutine, so contending on the run surface's lock would put an unrelated
// launch behind a connect. ITS ZERO VALUE IS USABLE — the map is created on first write
// — so it is a value field on Runs and a bare `&Runs{}` still works. It holds a mutex,
// so it travels by pointer only. `answering` counts the answers in flight per run, the
// second half of "does this run have an open question"; see beginAnswer.
type pendingRunAsks struct {
	asks      map[runAskKey]*runAsk
	answering map[string]int
	mu        sync.Mutex
}

// ensure creates the map on first write. Callers hold the lock.
func (r *pendingRunAsks) ensure() {
	if r.asks == nil {
		r.asks = make(map[runAskKey]*runAsk)
	}
}

// Add records an ask and reports whether it is NEW, which is what makes a redelivered
// frame free — KAS's notification bridge can replay one. The stored entry is left alone
// on a repeat, so the ORIGINAL `asked_at` survives.
func (r *pendingRunAsks) Add(a *runAsk) bool {
	if a.payload.WorkflowID == "" || a.payload.AskID == "" {
		return false
	}
	k := runAskKey{workflowID: a.payload.WorkflowID, askID: a.payload.AskID}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensure()
	if _, dup := r.asks[k]; dup {
		return false
	}
	r.asks[k] = a
	return true
}

// TakeIfPresent claims one ask: it deletes the entry and returns it, reporting false
// when something else got there first.
//
// The lock spans BOTH the lookup and the delete, which is the whole point: several
// surfaces are offered one ask and KAS accepts exactly one answer, so a loser's
// `session/prompt` falls through to an ORDINARY prompt on the step's session — a message
// injected into a step nobody asked to steer.
func (r *pendingRunAsks) TakeIfPresent(workflowID, askID string) (*runAsk, bool) {
	k := runAskKey{workflowID: workflowID, askID: askID}
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.asks[k]
	if !ok {
		return nil, false
	}
	delete(r.asks, k)
	return a, true
}

// Restore puts a claimed ask back, reporting whether it went in. Required rather than
// tidy: the answer path claims BEFORE it sends, so a transport failure would otherwise
// leave the run parked with its card gone from every surface. Callers go through
// (*Runs).restoreAsk, because an entry with no frame behind it is visible to nobody
// until the next SSE connect.
func (r *pendingRunAsks) Restore(a *runAsk) bool {
	return r.Add(a)
}

// HasRun reports whether a run's question is already accounted for: an ask nobody has
// answered, OR an answer in flight for one just claimed. BOTH arms, because in the window
// between the claim and the send the registry holds nothing while `inspect` still reports
// a need_input pause, so an entries-only read lets a concurrent refetch mint a text-less
// TWIN the following settle cannot retire.
func (r *pendingRunAsks) HasRun(workflowID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.answering[workflowID] > 0 {
		return true
	}
	for k := range r.asks {
		if k.workflowID == workflowID {
			return true
		}
	}
	return false
}

// beginAnswer opens an answer window for a run; endAnswer closes it. PAIRED by the
// CALLER: AnswerInput opens it BEFORE it claims and defers the close, or the gap HasRun
// covers is left one statement wide. A COUNT, not a flag: two parked steps of one run can
// be answered at once, and a flag would let the first close the second's window.
func (r *pendingRunAsks) beginAnswer(workflowID string) {
	if workflowID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.answering == nil {
		r.answering = make(map[string]int)
	}
	r.answering[workflowID]++
}

// endAnswer closes one answer window, dropping the key at zero so the map does
// not grow by one entry per run this process ever answered.
func (r *pendingRunAsks) endAnswer(workflowID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.answering[workflowID] > 1 {
		r.answering[workflowID]--
		return
	}
	delete(r.answering, workflowID)
}

// TakeRun claims every ask of a run whose wait is over and RETURNS them, so the caller
// can tell the surfaces still showing them: dropping an entry changes nothing anybody can
// see, and the head of a per-chat queue is the only entry rendered, so a stale run card
// also hides every card queued behind it for that chat.
func (r *pendingRunAsks) TakeRun(workflowID string) []*runAsk {
	if workflowID == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*runAsk
	for k, a := range r.asks {
		if k.workflowID == workflowID {
			out = append(out, a)
			delete(r.asks, k)
		}
	}
	return out
}

// TakeNode claims every ask naming one node and returns them. NODE-scoped rather than
// run-scoped, which is what makes it safe on a `node_complete`: a parallel branch's node
// can finish while a sibling's step is still parked. An ask with an EMPTY node id cannot
// be matched and is left for the terminal clear.
func (r *pendingRunAsks) TakeNode(workflowID, nodeID string) []*runAsk {
	if workflowID == "" || nodeID == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*runAsk
	for k, a := range r.asks {
		if k.workflowID == workflowID && a.payload.NodeID == nodeID {
			out = append(out, a)
			delete(r.asks, k)
		}
	}
	return out
}

// ClearChat drops every ask keyed to a chat that has gone away. A chat's delete also
// cancels the runs its sessions launched, so an ask keyed here is answerable by nobody
// and replaying it would show a card for a conversation that no longer exists.
func (r *pendingRunAsks) ClearChat(chatID vibekit.ChatID) {
	if chatID == "" {
		return
	}
	r.mu.Lock()
	for k, a := range r.asks {
		if a.chatID == chatID {
			delete(r.asks, k)
		}
	}
	r.mu.Unlock()
}

// List snapshots every unanswered ask, optionally filtered to one surface. EVERY entry is
// replayed however old: a parked run has no deadline, so an ask a client saw an hour ago
// is still all that stands between that run and its next step. The filter matches the SSE
// subscriber's topic, and a `run:<id>` key is deliberately not a chat.
func (r *pendingRunAsks) List(chatFilter vibekit.ChatID) []vibekit.ServerEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]vibekit.ServerEvent, 0, len(r.asks))
	for _, a := range r.asks {
		if chatFilter != "" && a.chatID != "" && a.chatID != chatFilter {
			continue
		}
		out = append(out, a.event())
	}
	return out
}

// --- The two doors ---

// handleSessionNotify records a step's question and broadcasts it.
//
// RECORD BEFORE BROADCAST: a client that opens its stream between the two gets the ask
// from the replay rather than waiting for a frame that will never re-fire. chatID is the
// ask's QUEUE KEY and comes from the door, never from the payload — whose `sessionId`
// names a KAS session, which the client indexes nothing by.
func (rs *Runs) handleSessionNotify(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	p, ok := rs.translate.SessionNotifyAsk(msg)
	if !ok {
		return
	}
	a := &runAsk{chatID: chatID, payload: p}
	if !rs.asks.Add(a) {
		slog.Debug("run ask: already recorded, not re-broadcast",
			"workflow_id", scrubLog(p.WorkflowID), "ask_id", scrubLog(p.AskID))
		return
	}
	slog.Info("a workflow step is waiting for an answer",
		"workflow_id", scrubLog(p.WorkflowID), "node_id", scrubLog(p.NodeID),
		"agent", scrubLog(p.AgentName), "chat_id", chatID)
	rs.bus.Broadcast(ctx, a.event())
}

// settleAskForNode retires the asks a node was holding and tells every surface so. The
// announcement is not optional: `run_input_settled` is what takes those cards down.
//
// `by` is the CALLER's, because only one of the two doors is an answer. Continue-without-
// answering did settle it (SettledByUser); a node completing did not, so that is
// SettledByMoot — the question stopped being answerable rather than being decided.
func (rs *Runs) settleAskForNode(
	ctx context.Context, workflowID, nodeID string, by vibekit.SettledBy,
) {
	for _, a := range rs.asks.TakeNode(workflowID, nodeID) {
		slog.Info("a parked step moved on, so its question is retired",
			"workflow_id", scrubLog(workflowID), "node_id", scrubLog(nodeID),
			"ask_id", scrubLog(a.payload.AskID), "settled_by", string(by))
		rs.announceSettled(ctx, a, by)
	}
}

// settleAsksForRun retires every ask a run still held and tells every surface so.
// SettledByMoot always: the run ending is not an answer, and the answer path settles the
// entry it took, so anything left here is a question nobody replied to.
func (rs *Runs) settleAsksForRun(ctx context.Context, workflowID string) {
	for _, a := range rs.asks.TakeRun(workflowID) {
		slog.Info("a run ended still holding a question, so the card is retired",
			"workflow_id", scrubLog(workflowID), "node_id", scrubLog(a.payload.NodeID),
			"ask_id", scrubLog(a.payload.AskID))
		rs.announceSettled(ctx, a, vibekit.SettledByMoot)
	}
}

// restoreAsk puts a claimed ask back AND re-offers it. BOTH halves, because the click
// spliced the dock entry before the dispatch: an entry back in the registry with no frame
// behind it is invisible until the next SSE connect. Re-broadcast rather than a settle —
// the question is still open, and the dock de-duplicates by (kind, askID).
func (rs *Runs) restoreAsk(ctx context.Context, a *runAsk) {
	if !rs.asks.Restore(a) {
		return
	}
	slog.Info("an answer did not reach the step, so its question is offered again",
		"workflow_id", scrubLog(a.payload.WorkflowID), "ask_id", scrubLog(a.payload.AskID))
	rs.bus.Broadcast(ctx, a.event())
}

// announceSettled publishes one ask's settlement on the surface it was keyed to — the
// ASK's own chat id rather than the run's, because a client filtering its stream to one
// chat has to receive the retirement of the card it was shown.
func (rs *Runs) announceSettled(ctx context.Context, a *runAsk, by vibekit.SettledBy) {
	rs.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventRunInputSettled, a.chatID,
		vibekit.RunInputSettledPayload{
			WorkflowID: a.payload.WorkflowID,
			AskID:      a.payload.AskID,
			SettledBy:  by,
		}))
}

// --- The restart reconcile ---

// The two pauseReason literals KAS writes for a step waiting on a person: `send_message`'s
// own park, and the re-park a plain Resume produces (resume clears state.pauseReason
// without clearing the node's completionSignal, so the next executeStep parks again on a
// sentence naming the node). Literals, because several sites write a pauseReason and only
// these two mean "a person owes an answer".
const (
	needInputPauseReason  = "Step requested user input via send_message."
	reparkPausePrefix     = "Step '"
	reparkPauseSuffix     = "' is waiting for user input."
	waitingForNextMessage = "' is waiting for the next user message."
)

// needInputSignal is KAS's node-level completionSignal for a step waiting on a person, and
// the only thing a parallel branch's park can be recognised by; see needInputParked.
const needInputSignal = "need_input"

// needInputPause reports whether a pause reason means a step is waiting on a person. The
// re-park sentence is matched by its two ends, because KAS interpolates the node id into
// it. It cannot see a park inside a PARALLEL BRANCH and must not be widened to the wrapper
// sentence KAS composes there — needInputParked is that arm.
func needInputPause(reason string) bool {
	if reason == needInputPauseReason {
		return true
	}
	if !strings.HasPrefix(reason, reparkPausePrefix) {
		return false
	}
	return strings.HasSuffix(reason, reparkPauseSuffix) ||
		strings.HasSuffix(reason, waitingForNextMessage)
}

// askInspect is the reconcile's own minimal decode of an `inspect` reply, deliberately not
// a share of internal/workflow's State: the read endpoint passes the whole tree through, so
// a field decoded there becomes a second definition that can only drift. Pointers ahead of
// the strings for govet's fieldalignment; the JSON tags carry the wire names.
type askInspect struct {
	State *struct {
		PauseDetail *askPauseDetail `json:"pauseDetail"`
		Root        *askNode        `json:"root"`
		Status      string          `json:"status"`
		PauseReason string          `json:"pauseReason"`
	} `json:"state"`
}

// askPauseDetail is the ONE field this reconcile reads off `state.pauseDetail`: when the
// step started waiting. Deliberately not the package's own `pauseDetail`, which the resume
// and cancel PREDICATES decode — every field there is another way a wire change can reach
// the heal, and `occurredAt` has no predicate reading it. Do not fold the two together.
type askPauseDetail struct {
	OccurredAt string `json:"occurredAt"`
}

// askNode is one node of the state tree, decoded to what naming a step needs.
// CompletionSignal is the pause's machine-readable half, per-NODE, so it survives a
// parallel branch where the run-level pauseReason does not. Type has ONE reader,
// statusUpdateTarget, because KAS considers `type: "step"` nodes alone.
type askNode struct {
	NodeID           string    `json:"nodeId"`
	Type             string    `json:"type"`
	Status           string    `json:"status"`
	SessionID        string    `json:"sessionId"`
	AgentName        string    `json:"agentName"`
	CompletionSignal string    `json:"completionSignal"`
	Children         []askNode `json:"children"`
}

// reconcileNeedInput mints an ask for a run parked on a person with nothing in the registry
// to answer with. The registry is IN MEMORY while the run is not, so a restart would leave
// the run parked forever with cancelling as the only recourse; not only a restart, which is
// why askChatID resolves the surface rather than assuming it.
//
// IDEMPOTENT twice over, since it runs on every read: the ask id derives from the paused
// leaf's node PATH, and the pass is skipped while HasRun answers true. From the READ path
// rather than a sweep, or a run nobody has opened broadcasts a card nobody is watching.
func (rs *Runs) reconcileNeedInput(ctx context.Context, workflowID string, raw json.RawMessage) {
	var res askInspect
	if json.Unmarshal(raw, &res) != nil || res.State == nil {
		return
	}
	if res.State.Status != runStatusPaused {
		return
	}
	// The signal arm leads: it reaches a park inside a parallel branch, whose reason the
	// run never keeps, and it NAMES the step, so it wins over pausedLeaf's first match.
	leaf, path := needInputParked(res.State.Root, nil)
	if leaf == nil {
		if !needInputPause(res.State.PauseReason) {
			return
		}
		leaf, path = pausedLeaf(res.State.Root, nil)
	}
	if leaf == nil {
		return
	}
	if rs.asks.HasRun(workflowID) {
		return
	}
	askedAt := ""
	if res.State.PauseDetail != nil {
		askedAt = res.State.PauseDetail.OccurredAt
	}
	a := &runAsk{
		chatID: rs.askChatID(ctx, workflowID),
		payload: vibekit.RunInputNeededPayload{
			WorkflowID:    workflowID,
			AskID:         keyenc.Join("reconciled", keyenc.Join(path...)),
			NodeID:        leaf.NodeID,
			StepSessionID: leaf.SessionID,
			AgentName:     leaf.AgentName,
			// Empty: inventing one would put words in the step's mouth.
			Question: "",
			AskedAt:  askedAt,
		},
	}
	if !rs.asks.Add(a) {
		return
	}
	slog.Info("a parked run had no ask on this server, so one was reconstructed from its state",
		"workflow_id", scrubLog(workflowID), "node_id", scrubLog(leaf.NodeID), "chat_id", a.chatID,
		"pause_reason", scrubLog(res.State.PauseReason))
	rs.bus.Broadcast(ctx, a.event())
}

// askChatID resolves the surface a synthesised ask is keyed to: the LAUNCHING CHAT when a
// live bridge still hosts the run, `run:<workflowId>` when none does. The chat is preferred
// because keying there reaches both docks while `run:` reaches one — the composer's matcher
// is the chat id alone. The fallback is also already the right key for a parentless run.
func (rs *Runs) askChatID(ctx context.Context, workflowID string) vibekit.ChatID {
	if chatID, sb := rs.hostBridgeChat(ctx, workflowID); sb != nil && chatID != "" {
		return chatID
	}
	return runChatID(workflowID)
}

// needInputParked finds a PAUSED node whose own completion signal says it is waiting on a
// person, with its node path. The arm that reaches a park inside a PARALLEL BRANCH, which no
// pause reason can: executeParallel copies the run STATE and not the node records, so the
// signal survives where the reason does not. It also names the right step where pausedLeaf's
// first depth-first match cannot.
func needInputParked(n *askNode, trail []string) (leaf *askNode, path []string) {
	if n == nil {
		return nil, nil
	}
	here := append(append([]string{}, trail...), n.NodeID)
	if n.Status == "paused" && n.CompletionSignal == needInputSignal {
		return n, here
	}
	for i := range n.Children {
		if leaf, path := needInputParked(&n.Children[i], here); leaf != nil {
			return leaf, path
		}
	}
	return nil, nil
}

// pausedLeaf finds the paused LEAF a run is waiting at, with its node path. The leaf rather
// than the root's own status, because the step holding the question is somewhere below it
// and its session id is the answer address. Depth-first, first match wins.
func pausedLeaf(n *askNode, trail []string) (leaf *askNode, path []string) {
	if n == nil {
		return nil, nil
	}
	here := append(append([]string{}, trail...), n.NodeID)
	if len(n.Children) == 0 {
		if n.Status == "paused" {
			return n, here
		}
		return nil, nil
	}
	for i := range n.Children {
		if leaf, path := pausedLeaf(&n.Children[i], here); leaf != nil {
			return leaf, path
		}
	}
	return nil, nil
}
