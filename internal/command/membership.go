package command

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/tabs"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// The coordinator's own refusals.
var (
	// errTabsFull is the 409 for an open at MaxOpenTabs.
	errTabsFull = errors.New("too many tabs are open; close a tab first")
	// errTabsUnavailable is the 503 for a build with no tab store wired.
	errTabsUnavailable = errors.New("the tab store is unavailable")
	// errOpenChatUnknown is the 404 for an open_tab naming a chat that is gone.
	errOpenChatUnknown = errors.New("that chat no longer exists")
	// errTabUnknown is the 404 for a pin naming an id the set does not hold.
	errTabUnknown = errors.New("that tab is not open")
)

// TabSet is the open-tab set as this package uses it, declared at the consumer
// since internal/tabs exports no interface of its own. List is here beyond the
// mutations because the capacity reservation needs the count and every event
// needs the expanded order, which no mutation returns; Subtree is what a Close
// of an id will remove, asked before that close commits.
type TabSet interface {
	Open(ctx context.Context, spec vibekit.OpenTab) (subject vibekit.TabSubject, created bool, version uint64, err error)
	Close(ctx context.Context, id string) ([]vibekit.TabSubject, uint64, error)
	Reorder(ctx context.Context, ids []string) (uint64, error)
	SetPinned(ctx context.Context, id string, pinned bool) (uint64, error)
	List() ([]vibekit.TabSubject, uint64)
	Subtree(id string) []vibekit.TabSubject
}

// chatCloser is the tab-close teardown for a chat tab: cancel the turn, cancel
// the chat's runs, tear the bridge down, keep the record. Bound in
// RegisterDefaults.
type chatCloser func(ctx context.Context, chatID vibekit.ChatID)

// chatDeleter is the delete grade of the same teardown, for a chat the close
// escalation has already erased; the captured session chain travels in.
type chatDeleter func(ctx context.Context, chatID vibekit.ChatID, sessionChain []string)

// retentionRead answers whether a closed chat's record is kept. A nil read means
// retention on — the fail-toward-keeping direction.
type retentionRead func(ctx context.Context) bool

// doomedChat is one chat a retention-off close will delete, captured under the
// lock while the record was still readable: nothing after the record delete may
// re-read it.
type doomedChat struct {
	chatID vibekit.ChatID
	chain  []string
}

// closeTeardownBudget bounds the close escalation's post-commit work, which runs
// detached from the request so a client walking away cannot cancel roll-forward.
const closeTeardownBudget = time.Minute

// Membership owns every operation that spans the chat store and the tab set.
//
// Those two documents share no transaction, so ordering plus this type's one lock
// is the whole correctness argument: the chat record leads, written first on create
// and removed first on delete, so a crash leaves a closed chat rather than a tab
// for a chat that never exists. Every tab mutation goes through here, pin and
// reorder included, since tabs.Store emits no events of its own. Safe for
// concurrent use; construct with NewMembership. A nil TabSet is errTabsUnavailable.
type Membership struct {
	chats      ChatStore
	tabs       TabSet
	bus        Broadcaster
	teardown   ChatTeardown
	closeChat  chatCloser
	deleteChat chatDeleter
	retention  retentionRead
	// retentionWake asks the purge scheduler for a pass now; nil leaves its timer.
	retentionWake func()
	// ops is the create ledger, op_id -> chat id, so a retry resolves to the chat
	// its first attempt made. Here rather than in the handlers because resolving
	// an op and reserving a tab slot must happen in the same critical section.
	ops *createLedger
	// mu is THE operation lock, held across the reservation, the mint, both
	// durable writes and the event. Order: mu -> chat record lock -> tabs writeMu.
	mu sync.Mutex
}

// MembershipDeps is Membership's constructor argument. Every field is required
// except Tabs, DeleteChat and Retention, which default to the safe direction.
type MembershipDeps struct {
	Chats      ChatStore
	Tabs       TabSet
	Bus        Broadcaster
	Teardown   ChatTeardown
	CloseChat  chatCloser
	DeleteChat chatDeleter
	Retention  retentionRead
}

// NewMembership builds the coordinator.
func NewMembership(deps *MembershipDeps) *Membership {
	return &Membership{
		chats:      deps.Chats,
		tabs:       deps.Tabs,
		bus:        deps.Bus,
		teardown:   deps.Teardown,
		closeChat:  deps.CloseChat,
		deleteChat: deps.DeleteChat,
		retention:  deps.Retention,
		ops:        newCreateLedger(),
	}
}

// ChatCreate is one create-and-open request: what to write on the new
// record, and where its tab goes.
type ChatCreate struct {
	// Init fills the new record's fields, called inside chat.Store.Mutate under
	// that chat's record lock; it must not reach either store.
	Init func(c *vibekit.Chat)
	// OpID correlates every attempt of one create gesture, so a repeat resolves
	// to the chat the first attempt made instead of minting a second one.
	OpID string
	// ChatID is the id the envelope supplied, or empty to mint one. A supplied id
	// bypasses the ledger: Mutate's exists branch is already idempotent for it.
	ChatID vibekit.ChatID
	// ParentChat names the chat whose tab the new tab hangs under, empty for a
	// top-level tab. A chat id rather than a tab id, so it resolves inside the
	// operation lock; outside it, a parent tab closing would leave Parent naming
	// nothing.
	ParentChat vibekit.ChatID
}

// ChatOpened is what a create answers with.
type ChatOpened struct {
	// Chat is read back from the store, since a replay resolves to a chat this
	// request did not write.
	Chat *vibekit.Chat
	// Subject is the tab. Zero-valued only when no tab store is wired.
	Subject vibekit.TabSubject
	Version uint64
	// Replay reports that this op_id had already created its chat.
	Replay bool
}

// TabOpened is what an open answers with.
type TabOpened struct {
	Subject vibekit.TabSubject
	Version uint64
	// Created is false for an already-open (Kind, Ref), which mutates nothing and
	// emits no event, so a caller waiting on that event would wait forever.
	Created bool
}

// CreateChatAndOpen writes a chat record and opens its tab as one operation,
// under the operation lock so the final slot cannot be consumed between minting
// and opening and no delete can land between the two writes.
//
// Returns errTabsFull (409) at MaxOpenTabs, and errChatNotCreated (409) when the
// record is absent after a Mutate that reported no error. A failed tab write
// leaves the chat created and returns the error; a retry carrying the same op_id
// finishes the tab write.
func (m *Membership) CreateChatAndOpen(ctx context.Context, req ChatCreate) (ChatOpened, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Reserve before anything mints. peek rather than resolve: a repeat whose tab
	// is already open needs no slot, and refusing it would strand the chat.
	prior, replay := m.priorChat(req)
	if err := m.reserveSlot(vibekit.TabKindChat, string(prior)); err != nil {
		return ChatOpened{}, err
	}

	chatID := prior
	if chatID == "" {
		chatID, _ = m.ops.resolve(req.OpID, vibekit.NewChatID)
	}

	// The record leads.
	err := m.chats.Mutate(ctx, chatID, func(c *vibekit.Chat, exists bool) bool {
		if exists {
			return false
		}
		req.Init(c)
		return true
	})
	if err != nil {
		return ChatOpened{}, StatusError(http.StatusInternalServerError, err)
	}
	c, ok := m.chats.Get(ctx, chatID)
	if !ok {
		return ChatOpened{}, StatusError(http.StatusConflict, errChatNotCreated)
	}

	// The tab second, unconditional including on a replay, to finish a tab write
	// the first attempt did not: Open is idempotent by (Kind, Ref).
	if m.tabs == nil {
		return ChatOpened{Chat: c, Replay: replay}, nil
	}
	opened, err := m.openTab(ctx, vibekit.OpenTab{
		Kind:   vibekit.TabKindChat,
		Ref:    string(chatID),
		Parent: m.tabForChat(req.ParentChat),
	}, req.OpID)
	if err != nil {
		return ChatOpened{}, err
	}
	return ChatOpened{Chat: c, Subject: opened.Subject, Version: opened.Version, Replay: replay}, nil
}

// ResolvedChat reports the chat an op_id has already created, without minting
// one. Exists for fork_chat: a fork's record cannot be built until KAS answers
// session/fork, and that round trip must not happen under the operation lock
// (a bridge Call has no client-side timeout).
func (m *Membership) ResolvedChat(opID string) (vibekit.ChatID, bool) {
	return m.ops.peek(opID)
}

// OpenTab opens a tab for something that already exists; it never mints a chat.
// For a chat tab it gates on the record existing — the other half of the delete
// ordering — with the check and the open in one critical section.
func (m *Membership) OpenTab(ctx context.Context, spec vibekit.OpenTab, opID string) (TabOpened, error) {
	if m.tabs == nil {
		return TabOpened{}, StatusError(http.StatusServiceUnavailable, errTabsUnavailable)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if spec.Kind == vibekit.TabKindChat {
		if _, ok := m.chats.Get(ctx, vibekit.ChatID(spec.Ref)); !ok {
			return TabOpened{}, StatusError(http.StatusNotFound, errOpenChatUnknown)
		}
	}
	return m.openTab(ctx, spec, opID)
}

// CloseTab closes a tab and its descendants, then tears down what an owned tab
// showed — and, with retention off, deletes each chat the close left tabless. An id
// that is not open closes nothing and is not an error: two devices can close one.
//
// Escalation, ordered, under the operation lock: decide the doomed set, capturing
// each chat's {id, session chain} while its record is still readable; then
// tabs.Close, the COMMIT POINT; then chats.Delete each doomed record. Past the
// commit there is no rollback, only roll-forward on a detached context.
func (m *Membership) CloseTab(ctx context.Context, id, opID string) (closed []vibekit.TabSubject, version uint64, err error) {
	if m.tabs == nil {
		return nil, 0, StatusError(http.StatusServiceUnavailable, errTabsUnavailable)
	}
	m.mu.Lock()
	doomed := m.doomedChats(ctx, id)
	closed, version, err = m.tabs.Close(ctx, id)
	if err != nil {
		m.mu.Unlock()
		return nil, 0, StatusError(http.StatusInternalServerError, err)
	}
	if len(closed) == 0 {
		m.mu.Unlock()
		return closed, version, nil
	}
	// Committed. Everything from here rolls forward under its own bound;
	// the request context governed the operation only up to this point.
	rollCtx, done := context.WithTimeout(context.WithoutCancel(ctx), closeTeardownBudget)
	defer done()
	m.emit(rollCtx, &vibekit.TabsChangedPayload{
		RemovedIDs: subjectIDs(closed),
		Order:      m.order(),
		Version:    version,
		OpID:       opID,
	})
	deleted := m.deleteDoomedRecords(rollCtx, doomed)
	m.mu.Unlock()

	// The teardown runs after the lock is released and after the membership fact is
	// published: it issues a session/cancel over the bridge with no client-side
	// timeout, so holding the lock across it would let one wedged process block every
	// other tab mutation. Exactly one grade per chat: delete grade for a chat whose
	// record went with this close, close grade for every other chat tab.
	dispatched := make(map[vibekit.ChatID]bool, len(deleted))
	chatTabClosed := false
	for _, t := range closed {
		if t.Kind != vibekit.TabKindChat {
			continue
		}
		chatTabClosed = true
		chatID := vibekit.ChatID(t.Ref)
		if chain, isDoomed := deleted[chatID]; isDoomed {
			if !dispatched[chatID] {
				dispatched[chatID] = true
				m.deleteChat(rollCtx, chatID, chain)
			}
			continue
		}
		if m.closeChat != nil {
			m.closeChat(rollCtx, chatID)
		}
	}
	if chatTabClosed {
		// The tab that just closed may have been the last thing holding an expired chat
		// outside retention's age test, so ask the purge to reconsider now instead of at
		// the end of an idle back-off that doubles to an hour (SetRetentionWake carries
		// the measurement). After the lock, because the wake reads a field it guards.
		m.wakeRetention()
	}
	return closed, version, nil
}

// doomedChats decides what a retention-off close of id will delete: every chat
// whose open tabs all lie inside the closing subtree, whose record exists, with the
// KAS session chain captured off that record — under the lock, before the commit,
// since nothing after the record delete may re-read it. Recordless chats are
// skipped: no chats.Delete and no chat_deleted for an id no device knows. Holds mu.
func (m *Membership) doomedChats(ctx context.Context, id string) []doomedChat {
	if m.deleteChat == nil || m.retention == nil {
		// Escalation unwired: a close can only close. Retention defaults
		// on — the fail-toward-keeping direction.
		return nil
	}
	refs := m.tablessChatRefs(m.tabs.Subtree(id))
	if len(refs) == 0 || m.retention(ctx) {
		return nil
	}
	doomed := make([]doomedChat, 0, len(refs))
	for _, ref := range refs {
		chatID := vibekit.ChatID(ref)
		c, ok := m.chats.Get(ctx, chatID)
		if !ok {
			continue
		}
		doomed = append(doomed, doomedChat{chatID: chatID, chain: c.SessionChain()})
	}
	return doomed
}

// tablessChatRefs returns the chat refs the close of this subtree leaves
// with no open tab. Caller holds mu.
func (m *Membership) tablessChatRefs(subtree []vibekit.TabSubject) []string {
	if len(subtree) == 0 {
		return nil
	}
	inSubtree := make(map[string]bool, len(subtree))
	for _, t := range subtree {
		inSubtree[t.ID] = true
	}
	open, _ := m.tabs.List()
	remaining := make(map[string]bool)
	for _, t := range open {
		if t.Kind == vibekit.TabKindChat && !inSubtree[t.ID] {
			remaining[t.Ref] = true
		}
	}
	var refs []string
	seen := make(map[string]bool)
	for _, t := range subtree {
		if t.Kind != vibekit.TabKindChat || seen[t.Ref] || remaining[t.Ref] {
			continue
		}
		seen[t.Ref] = true
		refs = append(refs, t.Ref)
	}
	return refs
}

// deleteDoomedRecords removes each doomed chat's record — tombstone and
// chat_deleted happen inside Delete — and returns the session chains of
// the ones that actually went. A failed delete logs ERROR and is left out
// of the result, demoting that chat to the close-grade teardown.
//
// Caller holds mu; ctx is the detached roll-forward context.
func (m *Membership) deleteDoomedRecords(ctx context.Context, doomed []doomedChat) map[vibekit.ChatID][]string {
	if len(doomed) == 0 {
		return nil
	}
	deleted := make(map[vibekit.ChatID][]string, len(doomed))
	for _, d := range doomed {
		if err := m.chats.Delete(ctx, d.chatID); err != nil {
			slog.Error("close: retention-off record delete failed after the tab close committed; the record survives with close-grade teardown",
				"chat_id", d.chatID, keyError, err)
			continue
		}
		slog.Info("chat deleted on close (retention off)", "chat_id", d.chatID)
		deleted[d.chatID] = d.chain
	}
	return deleted
}

// ReorderTabs replaces the order. ids must name every open tab exactly
// once; tabs.ErrOrderMismatch maps to 409.
//
// No base-version precondition: the exact-set check is the precondition,
// and a version one would discard a valid drag whenever an unrelated pin
// landed first.
func (m *Membership) ReorderTabs(ctx context.Context, ids []string, opID string) (uint64, error) {
	if m.tabs == nil {
		return 0, StatusError(http.StatusServiceUnavailable, errTabsUnavailable)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	before := m.version()
	version, err := m.tabs.Reorder(ctx, ids)
	if err != nil {
		return 0, tabStatus(err)
	}
	// An order identical to the one already held commits nothing and must
	// emit nothing.
	if version != before {
		m.emit(ctx, &vibekit.TabsChangedPayload{Order: m.order(), Version: version, OpID: opID})
	}
	return version, nil
}

// SetPinned pins or unpins one tab. Idempotent in both directions.
//
// An id that is not open is errTabUnknown (404), unlike a close: a pin is
// a statement about a tab, so success would tell the caller its tab is
// pinned when it is not.
func (m *Membership) SetPinned(ctx context.Context, id string, pinned bool, opID string) (uint64, error) {
	if m.tabs == nil {
		return 0, StatusError(http.StatusServiceUnavailable, errTabsUnavailable)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	open, before := m.tabs.List()
	if indexOfTab(open, id) < 0 {
		return 0, StatusError(http.StatusNotFound, errTabUnknown)
	}
	version, err := m.tabs.SetPinned(ctx, id, pinned)
	if err != nil {
		return 0, StatusError(http.StatusInternalServerError, err)
	}
	if version != before {
		if changed, ok := m.subject(id); ok {
			m.emit(ctx, &vibekit.TabsChangedPayload{Changed: &changed, Version: version, OpID: opID})
		}
	}
	return version, nil
}

// DeleteChatAndCloseTabs is the delete path: tear the chat's work down, remove the
// record, then close its tabs.
//
// The record leads: once it is gone every later open is refused, and any open that
// landed before the delete has its tab in the set closeTabsFor then walks. The
// teardown runs before the lock (the run cancel must precede the bridge going down)
// and outside it, since it reaches the bridge.
func (m *Membership) DeleteChatAndCloseTabs(ctx context.Context, chatID vibekit.ChatID, opID string) error {
	m.teardown.DeleteChatState(ctx, chatID)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.chats.Delete(ctx, chatID); err != nil {
		return StatusError(http.StatusInternalServerError, err)
	}
	slog.Info("chat deleted", "chat_id", chatID)
	m.closeTabsFor(ctx, chatID, opID)
	return nil
}

// RetentionClose closes the tabs of a chat the retention purge has already removed.
//
// Normally a no-op, and that is the point: retention's own predicate skips a chat
// that HAS an open tab (see HasOpenTab), so reaching this with tabs to close means
// one was opened between the predicate and the remove. Called from the purge's
// onPurge hook, after the per-chat record lock is released.
func (m *Membership) RetentionClose(ctx context.Context, chatID vibekit.ChatID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closeTabsFor(ctx, chatID, "")
}

// SetRetentionWake registers the callback that asks the retention purge to run a
// pass now. A SETTER rather than a constructor field because the coordinator is
// built during Runtime construction and the purge scheduler after it.
//
// CLOSING A TAB IS THE EVENT THAT OWES THIS: HasOpenTab pins an expired chat
// outside the age test while its tab is open, and an exempt chat contributes no
// wake-up deadline, so a pass that saw only exempt chats backs off to a one-hour
// ceiling and the chat could outlive its window by that much.
func (m *Membership) SetRetentionWake(wake func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.retentionWake = wake
}

// wakeRetention asks the purge to reconsider, if a scheduler is wired.
func (m *Membership) wakeRetention() {
	m.mu.Lock()
	wake := m.retentionWake
	m.mu.Unlock()
	if wake != nil {
		wake()
	}
}

// HasOpenTab reports whether any tab shows this chat — retention's second
// predicate. This makes retention opt-out for a chat left open forever, which is
// accepted: closing a tab under someone to satisfy a timer is worse. Takes no
// operation lock: it is a read, and the lock orders writes against their events.
func (m *Membership) HasOpenTab(chatID vibekit.ChatID) bool {
	if m.tabs == nil {
		return false
	}
	open, _ := m.tabs.List()
	return len(tabsForChat(open, chatID)) > 0
}

// openTab opens and, when something was committed, emits. Caller holds mu.
func (m *Membership) openTab(ctx context.Context, spec vibekit.OpenTab, opID string) (TabOpened, error) {
	subject, created, version, err := m.tabs.Open(ctx, spec)
	if err != nil {
		return TabOpened{}, tabStatus(err)
	}
	if created {
		m.emit(ctx, &vibekit.TabsChangedPayload{
			Changed: &subject,
			Order:   m.order(),
			Version: version,
			OpID:    opID,
		})
	}
	return TabOpened{Subject: subject, Version: version, Created: created}, nil
}

// closeTabsFor closes every tab showing chatID, and is the one place the
// live-repair rule lives. Caller holds mu.
//
// A close that fails after the chat record is already gone is retried once, and if
// that fails too the removal is emitted anyway — the authoritative fact is that the
// chat is gone, which is worse to leave unstated than a tab set this process failed
// to write. The emit-anyway frame is stamped one past the current version, so it
// costs the next real mutation being read as a duplicate.
func (m *Membership) closeTabsFor(ctx context.Context, chatID vibekit.ChatID, opID string) {
	if m.tabs == nil {
		return
	}
	open, _ := m.tabs.List()
	for _, doomed := range tabsForChat(open, chatID) {
		closed, version, err := m.tabs.Close(ctx, doomed.ID)
		if err != nil {
			slog.Warn("tab close failed after its chat was removed, retrying",
				"chat_id", chatID, "tab", doomed.ID, keyError, err)
			closed, version, err = m.tabs.Close(ctx, doomed.ID)
		}
		if err != nil {
			slog.Error("tab close still failing after its chat was removed; announcing the removal anyway",
				"chat_id", chatID, "tab", doomed.ID, keyError, err)
			m.emit(ctx, &vibekit.TabsChangedPayload{
				RemovedIDs: []string{doomed.ID},
				Version:    m.version() + 1,
				OpID:       opID,
			})
			continue
		}
		if len(closed) == 0 {
			continue // already gone; another close won the race
		}
		m.emit(ctx, &vibekit.TabsChangedPayload{
			RemovedIDs: subjectIDs(closed),
			Order:      m.order(),
			Version:    version,
			OpID:       opID,
		})
	}
}

// reserveSlot refuses when opening a tab for (kind, ref) would have to
// mint one and the set is already at MaxOpenTabs. A subject already open
// needs no slot, which lets a retry finish its own tab write at the limit.
//
// Caller holds mu.
func (m *Membership) reserveSlot(kind vibekit.TabKind, ref string) error {
	if m.tabs == nil {
		return nil
	}
	open, _ := m.tabs.List()
	for _, t := range open {
		if t.Kind == kind && t.Ref == ref {
			return nil
		}
	}
	if len(open) >= tabs.MaxOpenTabs {
		return StatusError(http.StatusConflict, errTabsFull)
	}
	return nil
}

// priorChat returns the chat this create already resolves to, without
// minting: the envelope's id when supplied, else whatever the ledger
// already holds for the op.
func (m *Membership) priorChat(req ChatCreate) (id vibekit.ChatID, replay bool) {
	if req.ChatID != "" {
		return req.ChatID, false
	}
	return m.ops.peek(req.OpID)
}

// tabForChat returns the id of the tab showing chatID, or "" when none is
// open. Caller holds mu.
func (m *Membership) tabForChat(chatID vibekit.ChatID) string {
	if chatID == "" {
		return ""
	}
	open, _ := m.tabs.List()
	if found := tabsForChat(open, chatID); len(found) > 0 {
		return found[0].ID
	}
	return ""
}

// order is the expanded order every event carries: every open tab
// including children. Caller holds mu.
func (m *Membership) order() []string {
	open, _ := m.tabs.List()
	return subjectIDs(open)
}

// version is the collection version as the store currently holds it.
// Caller holds mu.
func (m *Membership) version() uint64 {
	_, v := m.tabs.List()
	return v
}

// subject reads one tab back after a mutation that returned only a
// version. Caller holds mu.
func (m *Membership) subject(id string) (vibekit.TabSubject, bool) {
	open, _ := m.tabs.List()
	if i := indexOfTab(open, id); i >= 0 {
		return open[i], true
	}
	return vibekit.TabSubject{}, false
}

// emit broadcasts one aggregate frame. Workspace-global: the arrangement
// is not per chat.
func (m *Membership) emit(ctx context.Context, p *vibekit.TabsChangedPayload) {
	if m.bus == nil {
		return
	}
	m.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventTabsChanged, "", *p))
}

// tabStatus maps the tab store's sentinels onto HTTP statuses.
func tabStatus(err error) error {
	switch {
	case errors.Is(err, tabs.ErrTooMany):
		return StatusError(http.StatusConflict, errTabsFull)
	case errors.Is(err, tabs.ErrOrderMismatch):
		// 409 rather than 400: the caller's view of the set is not the
		// server's, a conflict to re-list from rather than a malformed body.
		return StatusError(http.StatusConflict, err)
	case errors.Is(err, tabs.ErrBadKind), errors.Is(err, tabs.ErrBadRef):
		return StatusError(http.StatusBadRequest, err)
	}
	return StatusError(http.StatusInternalServerError, err)
}

// tabsForChat returns every tab showing chatID.
func tabsForChat(open []vibekit.TabSubject, chatID vibekit.ChatID) []vibekit.TabSubject {
	var out []vibekit.TabSubject
	for _, t := range open {
		if t.Kind == vibekit.TabKindChat && t.Ref == string(chatID) {
			out = append(out, t)
		}
	}
	return out
}

// subjectIDs projects subjects onto their ids, which is what both removed_ids
// and order carry.
func subjectIDs(subjects []vibekit.TabSubject) []string {
	out := make([]string, 0, len(subjects))
	for _, t := range subjects {
		out = append(out, t.ID)
	}
	return out
}

// indexOfTab returns the position of the tab with this id, or -1.
func indexOfTab(open []vibekit.TabSubject, id string) int {
	return slices.IndexFunc(open, func(t vibekit.TabSubject) bool { return t.ID == id })
}
