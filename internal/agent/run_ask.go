package agent

// The pending-run-ask registry, and the two doors a step's question arrives
// through.
//
// A workflow step calling `send_message(severity:"warning")` parks its run and
// asks a person something. That question reaches vibekit on exactly one frame
// (`_kiro/session/notify`), and this file is where it becomes an answerable ask:
// recorded so a reconnecting client gets it replayed, broadcast so a live one
// renders it, cleared by whatever ends the wait.
//
// IT IS NOT pendingPermsTracker, and the two must not be merged. That tracker
// holds REQUEST-shaped asks: an open JSON-RPC request with an int64 id, keyed by
// (chat, id), which stays open until answered and cannot survive the bridge that
// carries it — its no-TTL contract is grounded in exactly that property. A run ask
// has no request id, nothing upstream is blocked on a response, and it is DURABLE:
// the run stays parked across a bridge death and a container restart. So it needs
// the same posture (no expiry, cleared by lifecycle) with a different identity and
// a different clearing rule, plus a reconcile against KAS's own state that a
// request-shaped ask has no use for.
//
// GROWTH IS BOUNDED BY THE RUN, not by an expiry: an answer claims its entry, and
// every path that ends a run's wait drops the rest. The clears are idempotent by
// construction, which is what lets the answer path and the lifecycle path both
// run without either having to know whether the other already did.

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"

	"github.com/cplieger/keyenc"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// runAskKey is a pending ask's identity: the run it parks, plus the ask within
// that run.
//
// THE PAIR, never the ask id alone. A synthesised id is derived from a node path,
// which two concurrent runs of one recipe share, so a process-wide key on the id
// would let one run's reconcile overwrite the other's ask and an answer to either
// retire whichever survived.
type runAskKey struct {
	workflowID string
	askID      string
}

// runAsk is one unanswered ask plus the surface it was keyed to.
//
// The chat id is stored rather than re-derived because it is the CLIENT's queue
// key and it is not a property of the run: an agent-parented run's ask belongs to
// the launching chat's dock, a parentless one's to `run:<workflowId>`, and only the
// door the frame arrived through knows which.
//
// It travels by POINTER everywhere — the registry stores `*runAsk` and every
// method takes or returns one — because the payload is 7 strings and the value is
// 128 bytes, which a by-value registry copies on the record, on every claim, and
// once per entry on every map walk (the connect replay walks all of them). Sharing
// the record out is safe rather than merely cheap: an entry is immutable after
// Add, and every method that hands one back has already DELETED it, so the
// registry and its caller never hold the same ask at once.
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
// Its own mutex, like pendingPermsTracker's and for the same reason: it is read
// from every SSE connect and written from every bridge's forward goroutine, so
// contending on the run surface's own lock would put an unrelated launch behind a
// connect.
//
// ITS ZERO VALUE IS USABLE: the map is created on first write, under the lock,
// the way runBoundsState's own maps are. It is a VALUE field on Runs rather than
// a pointer for that reason — a bare `&Runs{}` is the established shape across
// the run surface's tests, and a pointer field would make every one of them wire
// a registry it does not assert on or nil-dereference on the first clear. It
// holds a mutex, so it travels by pointer only; Runs always does.
//
// `answering` counts the answers in flight per run, which is the second half of
// "does this run have an open question" — see beginAnswer.
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

// Add records an ask and reports whether it is NEW.
//
// The report is what makes a redelivered frame free: KAS's notification bridge
// can replay one, and re-broadcasting an ask a client already holds would be
// harmless at the dock (it dedupes) and noisy in the log. The stored entry is
// left alone on a repeat, so the ORIGINAL `asked_at` survives.
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

// TakeIfPresent claims one ask: it deletes the entry and returns it, reporting
// false when something else got there first.
//
// The lock spans BOTH the lookup and the delete, which is the whole point —
// TakePendingPerm's reasoning applies unchanged. Two surfaces are offered the same
// ask (the launching chat's dock and the run tab's, on any number of devices) and
// KAS accepts exactly one answer: `tryResumeStepWithMessage` returns false once the
// step is no longer paused, and the loser's `session/prompt` would then fall
// through to an ORDINARY prompt on the step's session — a message injected into a
// step nobody asked to steer. So the claim has to be decided here.
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

// Restore puts a claimed ask back, reporting whether it went in.
//
// Required rather than tidy: the answer path claims BEFORE it sends, so a
// transport failure on the prompt would otherwise leave the run parked with its
// card gone from every surface and no way to bring it back short of a restart.
//
// Restoring the ENTRY is only half of that, which is why callers go through
// (*Runs).restoreAsk rather than here: every client that held the card spliced it
// at the click, so an entry back in the registry with no frame behind it is
// visible to nobody until the next SSE connect.
func (r *pendingRunAsks) Restore(a *runAsk) bool {
	return r.Add(a)
}

// HasRun reports whether a run's question is already accounted for: an ask
// nobody has answered, OR an answer in flight for one just claimed.
//
// The read the reconcile gates on, and BOTH arms because the answer path claims
// before it sends: in that window the registry holds nothing while `inspect` still
// reports a need_input pause, so an entries-only read lets a concurrent refetch
// mint a text-less TWIN that the following settle cannot retire (it names the
// ORIGINAL ask id).
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

// beginAnswer opens an answer window for a run; endAnswer closes it.
//
// PAIRED by the CALLER: AnswerInput opens it BEFORE it claims and defers the
// close, so the window strictly contains the interval HasRun above covers.
// Opening it after the claim would leave that gap one statement wide.
//
// A COUNT, not a flag: two parked steps of one run (a parallel branch) can be
// answered at once, and a flag would let the first close the second's window.
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

// TakeRun claims every ask of a run whose wait is over and RETURNS them, so the
// caller can tell the surfaces still showing them.
//
// It returns rather than merely clearing because dropping an entry changes
// nothing anybody can SEE: the card lives in the launching chat's dock and in the
// run tab's, on any number of devices, and the head of a per-chat queue is the
// only entry rendered — so a stale run card also hides every permission,
// elicitation and user-input card that arrives behind it for that chat. That is
// settleAskForNode's rule applied to the lifecycle door.
//
// IDEMPOTENT, and that is load-bearing: the answer path claims its own entry and
// the lifecycle path clears the rest, so both run for one ask without either
// needing to know whether the other did. Also the boot-time bound — a run that
// ended while this process was down leaves nothing here, because nothing here
// survives the process.
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

// TakeNode claims every ask naming one node and returns them.
//
// NODE-scoped rather than run-scoped, which is what makes it safe to call on a
// `node_complete`: a parallel branch's node can finish while a sibling branch's
// step is still parked, and dropping the whole run's asks would take that
// sibling's live card with it. An ask carrying an EMPTY node id is left alone —
// it cannot be matched, and the terminal clear collects it.
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

// ClearChat drops every ask keyed to a chat that has gone away.
//
// The chat-teardown door, beside ClearPendingPermsForChat. A chat's own delete
// also cancels the runs its sessions launched, so an ask keyed here is answerable
// by nobody afterwards; keeping it would replay a card for a conversation that no
// longer exists.
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

// List snapshots every unanswered ask, optionally filtered to one surface.
//
// EVERY entry is replayed however old, on pendingPermsTracker's reasoning taken
// one step further: a parked run has no deadline of its own at all, so an ask a
// client saw an hour ago is still the only thing standing between that run and its
// next step. The filter matches the SSE subscriber's topic, and a `run:<id>` key
// is deliberately not a chat, so a chat-filtered stream never sees a parentless
// run's ask.
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

// --- The two doors -----------------------------------------------------------

// handleSessionNotify records a step's question and broadcasts it.
//
// RECORD BEFORE BROADCAST, which is the ordering the connect race needs: a client
// that opens its stream between the two gets the ask from the replay rather than
// missing the event and waiting for a frame that will never re-fire.
//
// chatID is the ask's QUEUE KEY and comes from the door: the launching chat's id
// on a chat bridge, `run:<workflowId>` on a run bridge. Nothing derives it from
// the payload, because the payload's own `sessionId` names a KAS session and the
// client indexes nothing by session.
func (rs *Runs) handleSessionNotify(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	p, ok := rs.translate.SessionNotifyAsk(msg)
	if !ok {
		return
	}
	a := &runAsk{chatID: chatID, payload: p}
	if !rs.asks.Add(a) {
		slog.Debug("run ask: already recorded, not re-broadcast",
			"workflow_id", p.WorkflowID, "ask_id", p.AskID)
		return
	}
	slog.Info("a workflow step is waiting for an answer",
		"workflow_id", p.WorkflowID, "node_id", p.NodeID,
		"agent", p.AgentName, "chat_id", chatID)
	rs.bus.Broadcast(ctx, a.event())
}

// settleAskForNode retires the asks a node was holding and tells every surface
// so.
//
// The announcement is not optional: an ask is offered to the launching chat's
// dock AND to the run tab's, on any number of devices, so an entry disappearing
// from this registry changes nothing anybody can see. `run_input_settled` is what
// takes those cards down, exactly as `decision_settled` does for the three
// request-shaped asks.
//
// `by` is the caller's, because the two doors here mean different things and only
// one of them is an answer. A reader clicking Continue-without-answering DID
// settle it (SettledByUser); a node completing did not — vibekit's own answer path
// claims its entry before it sends, so anything reaching this from a node frame
// was never answered HERE, and the frame's own status covers failed and aborted
// as readily as completed. That case is SettledByMoot: the question stopped being
// answerable rather than being decided.
func (rs *Runs) settleAskForNode(
	ctx context.Context, workflowID, nodeID string, by vibekit.SettledBy,
) {
	for _, a := range rs.asks.TakeNode(workflowID, nodeID) {
		slog.Info("a parked step moved on, so its question is retired",
			"workflow_id", workflowID, "node_id", nodeID, "ask_id", a.payload.AskID,
			"settled_by", string(by))
		rs.announceSettled(ctx, a, by)
	}
}

// settleAsksForRun retires every ask a run still held and tells every surface so.
//
// SettledByMoot, always: the run ending is not an answer, and it is the one door
// no answer path can have claimed first — the answer path settles the entry it
// took, so anything left here is a question nobody replied to.
func (rs *Runs) settleAsksForRun(ctx context.Context, workflowID string) {
	for _, a := range rs.asks.TakeRun(workflowID) {
		slog.Info("a run ended still holding a question, so the card is retired",
			"workflow_id", workflowID, "node_id", a.payload.NodeID, "ask_id", a.payload.AskID)
		rs.announceSettled(ctx, a, vibekit.SettledByMoot)
	}
}

// restoreAsk puts a claimed ask back AND re-offers it.
//
// BOTH halves, because the claim is what took the card down: `settle` splices the
// dock entry at the click, before the dispatch, so every client that was showing
// this ask has already forgotten it. Restoring only the registry entry leaves the
// run parked with a live ask nobody can see until the next SSE connect refills it
// from the replay — which is exactly the outcome Restore exists to prevent, one
// layer up.
//
// Re-broadcast rather than a settle: the question is still open, so the honest
// frame is the one that offers it. The dock de-duplicates by (kind, askID), so a
// client that somehow kept its card takes the repeat as a no-op.
func (rs *Runs) restoreAsk(ctx context.Context, a *runAsk) {
	if !rs.asks.Restore(a) {
		return
	}
	slog.Info("an answer did not reach the step, so its question is offered again",
		"workflow_id", a.payload.WorkflowID, "ask_id", a.payload.AskID)
	rs.bus.Broadcast(ctx, a.event())
}

// announceSettled publishes one ask's settlement on the surface it was keyed to.
//
// Keyed to the ASK's own chat id rather than to the run, because that is where
// the card is: a client filtering its stream to one chat has to receive the
// retirement of the ask it was shown.
func (rs *Runs) announceSettled(ctx context.Context, a *runAsk, by vibekit.SettledBy) {
	rs.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventRunInputSettled, a.chatID,
		vibekit.RunInputSettledPayload{
			WorkflowID: a.payload.WorkflowID,
			AskID:      a.payload.AskID,
			SettledBy:  by,
		}))
}

// --- The restart reconcile ---------------------------------------------------

// The two pauseReason literals KAS writes for a step waiting on a person.
//
// The first is `send_message`'s own park; the second is what a plain Resume
// produces, because KAS's resume clears `state.pauseReason` without clearing the
// step node's `completionSignal`, so the next `executeStep` re-parks on a fallback
// sentence naming the node. Matched as literals for stalePauseReason's reason:
// several sites write a pauseReason and only these two mean "a person owes an
// answer".
const (
	needInputPauseReason  = "Step requested user input via send_message."
	reparkPausePrefix     = "Step '"
	reparkPauseSuffix     = "' is waiting for user input."
	waitingForNextMessage = "' is waiting for the next user message."
)

// needInputPause reports whether a pause reason means a step is waiting on a
// person.
//
// Pure and reason-only, like resumablePause beside it, so the table test is the
// reason list rather than a set of RPC fixtures. The re-park sentence is matched
// by its two ends because KAS interpolates the node id into it; both spellings are
// specific enough that no involuntary pause reason can reach them.
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

// askInspect is the reconcile's own minimal decode of an `inspect` reply.
//
// Its own rather than a share of internal/workflow's State, which deliberately
// decodes only what step identification needs and says so: the read endpoint
// passes the whole tree through, so a field decoded there becomes a second
// definition that can only drift. run_http.go's `status()` reads `state.status`
// the same way, off its own anonymous struct.
//
// Pointers ahead of the strings for govet's fieldalignment; the JSON tags carry
// the wire names, so declaration order means nothing here.
type askInspect struct {
	State *struct {
		PauseDetail *struct {
			OccurredAt string `json:"occurredAt"`
		} `json:"pauseDetail"`
		Root        *askNode `json:"root"`
		Status      string   `json:"status"`
		PauseReason string   `json:"pauseReason"`
	} `json:"state"`
}

// askNode is one node of the state tree, decoded to what naming a parked step
// needs: which node, which session answers it, and who is running it.
type askNode struct {
	NodeID    string    `json:"nodeId"`
	Status    string    `json:"status"`
	SessionID string    `json:"sessionId"`
	AgentName string    `json:"agentName"`
	Children  []askNode `json:"children"`
}

// reconcileNeedInput mints an ask for a run that is parked on a person with
// nothing in the registry to answer with.
//
// The container-restart path FIRST, and it exists because the registry is IN
// MEMORY while the run is not: a restart loses the question text and leaves the
// run parked forever, so without this the user's only recourse would be cancelling
// work that is one sentence from finishing. The synthesised ask carries an EMPTY
// question, which the card renders as "a step is waiting for your answer" — honest,
// and never a dead end.
//
// NOT ONLY a restart, which is why the surface is resolved rather than assumed
// (askChatID): SessionNotifyAsk drops a notify frame carrying no message, so a live
// run whose step asked with empty text arrives here with its bridges still up.
//
// IDEMPOTENT twice over, which it has to be: it runs on every read of the run, so
// the ask id is derived from the paused leaf's node PATH (deterministic) and the
// whole pass is skipped while the run already holds an ask OR has an answer in
// flight (HasRun covers both).
//
// Called from the READ path rather than a sweep, because a read is when a person
// is looking: the ask has to exist before the surface asking for it renders, and
// synthesising one for a run nobody has opened would broadcast a card for work
// nobody is watching.
func (rs *Runs) reconcileNeedInput(ctx context.Context, workflowID string, raw json.RawMessage) {
	var res askInspect
	if json.Unmarshal(raw, &res) != nil || res.State == nil {
		return
	}
	if res.State.Status != runStatusPaused || !needInputPause(res.State.PauseReason) {
		return
	}
	if rs.asks.HasRun(workflowID) {
		return
	}
	leaf, path := pausedLeaf(res.State.Root, nil)
	if leaf == nil {
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
			// Deliberately empty: the text was in the registry this process lost, and
			// inventing a question would put words in the step's mouth.
			Question: "",
			AskedAt:  askedAt,
		},
	}
	if !rs.asks.Add(a) {
		return
	}
	slog.Info("a parked run had no ask on this server, so one was reconstructed from its state",
		"workflow_id", workflowID, "node_id", leaf.NodeID, "chat_id", a.chatID,
		"pause_reason", res.State.PauseReason)
	rs.bus.Broadcast(ctx, a.event())
}

// askChatID resolves the surface a synthesised ask is keyed to: the LAUNCHING
// CHAT when a live bridge still hosts the run, `run:<workflowId>` when none does.
//
// The chat is preferred because keying there reaches BOTH docks while `run:`
// reaches one — the composer's matcher is the chat id alone and can never match a
// `run:` key, while the run tab's also matches the payload's run id. The fallback
// is the honest answer for a run nothing hosts: no bridge means no launching chat's
// dock to key to, and it is already the right key for a parentless run.
func (rs *Runs) askChatID(ctx context.Context, workflowID string) vibekit.ChatID {
	if chatID, sb := rs.hostBridgeChat(ctx, workflowID); sb != nil && chatID != "" {
		return chatID
	}
	return runChatID(workflowID)
}

// pausedLeaf finds the paused LEAF a run is waiting at, with its node path.
//
// The leaf rather than the tree's own status: a run reports `paused` at the root
// while the step actually holding the question is somewhere below it, and the
// step's session id is the answer address. Depth-first, first match wins — a run
// waiting on a person waits on one step, since KAS parks the whole run on the
// signal.
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
