package agent

import (
	"cmp"
	"log/slog"
	"slices"
	"sync"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// pendingPermsTracker tracks unresolved permission_needed events, keyed by CHAT
// and request id together (see permKey) and replayed on every new SSE connection
// so they survive a reconnect. THERE IS DELIBERATELY NO TTL: the ACP request stays
// open until answered or cancelled, so an expiry would invent a deadline nothing
// upstream has. Growth is bounded by the Take and Clear paths instead.
type pendingPermsTracker struct {
	perms map[permKey]vibekit.ServerEvent
	mu    sync.Mutex
}

// permKey is a pending decision's identity: the chat that owns it, plus the ACP
// request id it will be answered on. THE PAIR, never the id alone — each bridge
// mints ids from zero and there is one bridge per chat, so two live chats holding
// request 7 is ordinary. No generation is needed: every path that replaces a
// bridge first drops that chat's entries.
type permKey struct {
	chat vibekit.ChatID
	id   int64
}

func newPendingPermsTracker() *pendingPermsTracker {
	return &pendingPermsTracker{perms: make(map[permKey]vibekit.ServerEvent)}
}

// Add records a permission_needed event under its own chat's id, taken off the
// event because that is what the answer and ClearForChat will both carry.
func (t *pendingPermsTracker) Add(id int64, evt vibekit.ServerEvent) {
	t.mu.Lock()
	t.perms[permKey{chat: evt.ChatID, id: id}] = evt
	t.mu.Unlock()
}

// TakeIfPresent claims one chat's request: it deletes the entry and returns it,
// reporting false when the request was already answered by somebody else. The
// lock spans BOTH the lookup and the delete, replacing a Has-then-Remove pair
// whose window let two surfaces each answer — two tabs, or a human racing the
// unattended floor. Presence is the only test (see the type comment for the
// missing age check); the returned event names WHICH kind of decision settled.
func (t *pendingPermsTracker) TakeIfPresent(chatID vibekit.ChatID, id int64) (vibekit.ServerEvent, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	k := permKey{chat: chatID, id: id}
	evt, ok := t.perms[k]
	if !ok {
		return vibekit.ServerEvent{}, false
	}
	delete(t.perms, k)
	return evt, true
}

// TakePermissionOption validates one advertised option and claims the request
// in the same critical section. An off-list answer leaves the request pending.
func (t *pendingPermsTracker) TakePermissionOption(chatID vibekit.ChatID, id int64, optionID string) (evt vibekit.ServerEvent, pending, offered bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	k := permKey{chat: chatID, id: id}
	evt, pending = t.perms[k]
	if !pending {
		return vibekit.ServerEvent{}, false, false
	}
	payload, ok := evt.Payload.(vibekit.PermissionNeededPayload)
	if !ok || !slices.ContainsFunc(payload.Options, func(option vibekit.PermissionOption) bool {
		return option.OptionID == optionID
	}) {
		return evt, true, false
	}
	delete(t.perms, k)
	return evt, true, true
}

// ClearForChat drops every unresolved permission_needed entry owned by chatID.
func (t *pendingPermsTracker) ClearForChat(chatID vibekit.ChatID) {
	if chatID == "" {
		return
	}
	t.mu.Lock()
	for k := range t.perms {
		if k.chat == chatID {
			delete(t.perms, k)
		}
	}
	t.mu.Unlock()
}

// ClearForRun drops every unresolved decision a workflow RUN raised, wherever it
// is filed. A step's ask dies with its bridge but is tracked here for the
// connect-time replay, so without this a client that dropped the card locally was
// re-offered it on the next connect. The run comes off the PAYLOAD because the key
// carries none, and an empty id is refused or it would match the whole tracker. It
// does NOT announce; another client still rendering keeps its card until reload.
func (t *pendingPermsTracker) ClearForRun(workflowID string) {
	if workflowID == "" {
		return
	}
	t.mu.Lock()
	for k, evt := range t.perms {
		if vibekit.DecisionRunID(evt.Payload) == workflowID {
			delete(t.perms, k)
		}
	}
	t.mu.Unlock()
}

// List returns a snapshot of the unresolved decisions, optionally filtered to one
// chat. It feeds the connect-time replay and returns every tracked entry: exactly
// the set TakeIfPresent will still accept an answer for. ORDER IS PART OF THE
// CONTRACT, ascending by request id — the order the agent asked, so two tabs
// reconnecting stack the same queue. The chat is the TIE-BREAK and not a grouping,
// because ids are per bridge and two chats can hold the same one.
func (t *pendingPermsTracker) List(chatFilter vibekit.ChatID) []vibekit.ServerEvent {
	t.mu.Lock()
	keys := make([]permKey, 0, len(t.perms))
	for k := range t.perms {
		if chatFilter != "" && k.chat != "" && k.chat != chatFilter {
			continue
		}
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b permKey) int {
		return cmp.Or(cmp.Compare(a.id, b.id), cmp.Compare(a.chat, b.chat))
	})
	result := make([]vibekit.ServerEvent, 0, len(keys))
	for _, k := range keys {
		result = append(result, t.perms[k])
	}
	t.mu.Unlock()
	return result
}

// ClearPendingPermsForChat drops every unresolved decision owned by chatID.
func (b *bus) ClearPendingPermsForChat(chatID vibekit.ChatID) {
	b.pendingPerms.ClearForChat(chatID)
}

// ClearPendingPermsForRun drops every unresolved decision a run raised, at the
// run's terminal transition. See ClearForRun for why it is silent.
func (b *bus) ClearPendingPermsForRun(workflowID string) {
	b.pendingPerms.ClearForRun(workflowID)
}

// TakePendingPerm claims an unanswered decision so exactly one surface can answer
// it, reporting false when something else got there first. The winning take also
// announces itself (decision_settled), retiring the card every OTHER surface still
// shows. It takes the CHAT as well as the id, because an id is unique only within
// one bridge (see permKey). Order is the contract: TAKE first, then answer
// kiro-cli — a caller that loses the race must not send its answer at all.
func (b *bus) TakePendingPerm(chatID vibekit.ChatID, requestID int64, settledBy vibekit.SettledBy) bool {
	evt, ok := b.pendingPerms.TakeIfPresent(chatID, requestID)
	if !ok {
		return false
	}
	b.announceDecisionSettled(evt, requestID, settledBy)
	return true
}

// TakePendingPermissionOption validates and claims a permission response.
func (b *bus) TakePendingPermissionOption(chatID vibekit.ChatID, requestID int64, optionID string, settledBy vibekit.SettledBy) (pending, offered bool) {
	evt, pending, offered := b.pendingPerms.TakePermissionOption(chatID, requestID, optionID)
	if offered {
		b.announceDecisionSettled(evt, requestID, settledBy)
	}
	return pending, offered
}

// announceDecisionSettled retires a claimed decision on every other surface.
func (b *bus) announceDecisionSettled(evt vibekit.ServerEvent, requestID int64, settledBy vibekit.SettledBy) {
	kind, known := vibekit.DecisionKindForEvent(evt.Type)
	if !known {
		// Only the three *_needed events are tracked, so this is tracker misuse, not
		// the wire. The claim stands; only an unactionable announcement is skipped.
		slog.Error("sse: tracked decision has no kind, cannot announce it",
			"type", evt.Type, "request_id", requestID)
		return
	}
	b.emit(vibekit.NewEvent(vibekit.EventDecisionSettled, evt.ChatID, vibekit.DecisionSettledPayload{
		RequestID: requestID,
		Kind:      kind,
		SettledBy: settledBy,
	}))
}
