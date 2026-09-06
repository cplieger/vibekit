package command

// The membership coordinator: every operation spanning the chat store and the open-tab
// set, under one operation lock. Two documents and no transaction, so ordering plus this
// lock is the whole correctness argument. Lock order is Membership.mu -> chat record lock
// -> tabs.Store writeMu, acyclic because neither store reaches the other. THE CHAT RECORD
// IS THE GATE both ways: written before its tab, removed before its tabs. Every tab
// mutation comes through here, pin and reorder included, because tabs.Store emits no
// events of its own and this lock is what keeps mutate-and-emit atomic and frames in
// version order; Prune is the one writer that does not hold it.

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
	// ErrTabsUnavailable is the 503 for a build with no tab store wired. Typed rather
	// than a status because the run-tab offer answers no request and must tell this
	// permanent absence from a real failure to open; every HTTP door adds the status.
	ErrTabsUnavailable = errors.New("the tab store is unavailable")
	// errOpenChatUnknown is the 404 an open_tab for a chat that does not
	// exist gets — the delete-ordering gate's refusal.
	errOpenChatUnknown = errors.New("that chat no longer exists")
	// errTabUnknown is the 404 for a pin naming an id the set does not hold. Only
	// pin_tab reports it; a close treats an absent id as nothing to do.
	errTabUnknown = errors.New("that tab is not open")
)

// ErrNoParentTab means the chat a run tab would nest under has no tab in the
// set, so OpenRunTab opened nothing. It carries "retry this later" rather than
// "tell the user": TabSubject.Parent is immutable, so opening the tab top level
// instead would foreclose nesting for the life of the run.
var ErrNoParentTab = errors.New("the launching chat has no open tab to nest under")

// TabSet is the open-tab set as this package uses it: the four mutations
// plus the paired reads. Declared here, at the consumer, since internal/tabs
// exports no interface of its own — *tabs.Store satisfies it.
//
// List is included beyond reading because the capacity reservation needs
// the count and every event needs the expanded order, which no mutation
// returns. Subtree is the close escalation's read — what a Close of this id
// will remove, asked before the close commits.
type TabSet interface {
	Open(ctx context.Context, spec vibekit.OpenTab) (subject vibekit.TabSubject, created bool, version uint64, err error)
	Close(ctx context.Context, id string) ([]vibekit.TabSubject, uint64, error)
	Reorder(ctx context.Context, ids []string) (uint64, error)
	SetPinned(ctx context.Context, id string, pinned bool) (uint64, error)
	List() ([]vibekit.TabSubject, uint64)
	Subtree(id string) []vibekit.TabSubject
}

// RunOwner is the run surface as the coordinator uses it: which chat's agent
// launched a run. Declared here, at the consumer; *agent.Runs satisfies it.
//
// `ok` is false for a run with no lease — one this server never put on the
// wire, or one whose lease was released when it ended — so a finished run's
// parent is unknown here and History supplies it instead.
type RunOwner interface {
	RunChat(workflowID string) (chatID vibekit.ChatID, ok bool)
}

// chatCloser is the tab-close teardown for a chat tab: cancel the turn,
// cancel the chat's runs, tear the bridge down, and keep the record.
//
// A function seam rather than three more role interfaces, since what the
// coordinator needs is one decision ("this chat's work stops now"). Bound in
// RegisterDefaults.
type chatCloser func(ctx context.Context, chatID vibekit.ChatID)

// chatDeleter is the delete grade of the same teardown, for a chat the
// close escalation has already erased: the session chain travels in,
// captured before the commit.
type chatDeleter func(ctx context.Context, chatID vibekit.ChatID, sessionChain []string)

// retentionRead answers whether chat retention is on — whether a closed
// chat's record is kept. Must fail toward keeping; a nil read means
// retention on, the same safe direction.
type retentionRead func(ctx context.Context) bool

// doomedChat is one chat a retention-off close will delete: the record's
// id and the KAS session chain, both captured under the lock while the
// record was still readable — nothing that runs after the record delete
// may re-read it.
type doomedChat struct {
	chatID vibekit.ChatID
	chain  []string
}

// closeTeardownBudget bounds the close escalation's post-commit work. The
// teardown runs on a context detached from the HTTP request, since a
// client that walks away must not cancel roll-forward.
const closeTeardownBudget = time.Minute

// Membership owns every operation that spans the chat store and the tab
// set.
//
// Safe for concurrent use; the zero value is not usable, construct with
// NewMembership. A nil TabSet means no tab store was wired: the chat half
// of every operation still runs and the tab half reports
// ErrTabsUnavailable.
type Membership struct {
	chats      ChatStore
	tabs       TabSet
	bus        Broadcaster
	teardown   ChatTeardown
	closeChat  chatCloser
	deleteChat chatDeleter
	retention  retentionRead
	// runs resolves a run's launching chat when a client sends no parent. May
	// be nil, in which case no parent is filled and a run tab opens top level.
	runs RunOwner
	// ops is the create ledger: op_id -> chat id, so a retry resolves to the chat its
	// first attempt made. Here rather than in the handlers because resolving an op and
	// reserving a tab slot must happen in one critical section.
	ops *createLedger
	// mu is THE operation lock, held across the capacity reservation, the mint, both
	// durable writes and the event.
	mu sync.Mutex
}

// MembershipDeps is Membership's constructor argument. Every field is
// required except Tabs, DeleteChat and Retention — the last two default to
// the safe direction (no escalation, retention on).
type MembershipDeps struct {
	Chats      ChatStore
	Tabs       TabSet
	Bus        Broadcaster
	Teardown   ChatTeardown
	CloseChat  chatCloser
	DeleteChat chatDeleter
	Retention  retentionRead
	Runs       RunOwner
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
		runs:       deps.Runs,
		ops:        newCreateLedger(),
	}
}

// ChatCreate is one create-and-open request: what to write on the new
// record, and where its tab goes.
type ChatCreate struct {
	// Init fills the new record's fields. Called inside chat.Store.Mutate,
	// under that chat's record lock, and must not reach either store. Not
	// called at all when the record already exists.
	Init func(c *vibekit.Chat)
	// OpID correlates every attempt of one create gesture. A repeat
	// resolves to the chat the first attempt made instead of minting a
	// second one, and finishes a missing tab write.
	OpID string
	// ChatID is the id the envelope supplied, or empty to mint one. A
	// supplied id bypasses the ledger: Mutate's exists branch is already
	// idempotent for it.
	ChatID vibekit.ChatID
	// ParentChat names the chat whose tab the new tab hangs under, empty
	// for a top-level tab (the tangent is the only create that nests).
	//
	// A chat id rather than a tab id, so the resolution happens inside the
	// operation lock — resolving it outside would let a parent tab close
	// in between leave a Parent naming nothing.
	ParentChat vibekit.ChatID
}

// ChatOpened is what a create answers with.
type ChatOpened struct {
	// Chat is read back from the store rather than assembled from what was
	// written: a replayed op resolves to a chat this request did not write.
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
	// Created is false for an already-open (Kind, Ref), which mutates
	// nothing and emits no event — a caller waiting on that event would
	// wait forever, so it resolves from the response instead.
	Created bool
}

// CreateChatAndOpen writes a chat record and opens its tab as one operation, so the
// final slot cannot be consumed between minting and opening and no delete lands between
// the two writes. Returns errTabsFull (409) at MaxOpenTabs, and errChatNotCreated (409)
// when the record is absent after a Mutate that reported no error (a tombstoned id).
//
// A failed tab write leaves the chat created and returns the error; a retry carrying the
// same op_id finishes it.
func (m *Membership) CreateChatAndOpen(ctx context.Context, req ChatCreate) (ChatOpened, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Reserve before anything mints. peek rather than resolve, because a
	// repeat whose tab is already open needs no slot — refusing that at
	// the limit would strand the chat with no way to finish it.
	prior, replay := m.priorChat(req)
	if err := m.reserveSlot(vibekit.TabKindChat, string(prior), spendLastSlot); err != nil {
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

	// The tab second, unconditional even on a replay, to finish a tab write the first
	// attempt did not — Open is idempotent by (Kind, Ref). Owns is STATED rather than
	// defaulted, because its zero value is a legal value: a chat tab owns the chat it
	// shows, so the client counts it and runs its local teardown on close.
	if m.tabs == nil {
		return ChatOpened{Chat: c, Replay: replay}, nil
	}
	opened, err := m.openTab(ctx, vibekit.OpenTab{
		Kind:   vibekit.TabKindChat,
		Ref:    string(chatID),
		Parent: m.tabForChat(req.ParentChat),
		Owns:   true,
	}, req.OpID)
	if err != nil {
		return ChatOpened{}, err
	}
	return ChatOpened{Chat: c, Subject: opened.Subject, Version: opened.Version, Replay: replay}, nil
}

// ResolvedChat reports the chat an op_id has already created, without
// minting one. Exists for fork_chat: a fork's record cannot be built until
// KAS has answered session/fork, and that round trip must not happen under
// the operation lock (a bridge Call has no client-side timeout).
func (m *Membership) ResolvedChat(opID string) (vibekit.ChatID, bool) {
	return m.ops.peek(opID)
}

// OpenTab opens a tab for something that already exists; it never mints a
// chat. For a chat tab it gates on the record existing — the other half of
// the delete ordering — with the check and open in one critical section.
// The capacity refusal comes from the tab store, since nothing has been
// minted here to strand.
func (m *Membership) OpenTab(ctx context.Context, spec vibekit.OpenTab, opID string) (TabOpened, error) {
	if m.tabs == nil {
		return TabOpened{}, StatusError(http.StatusServiceUnavailable, ErrTabsUnavailable)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if spec.Kind == vibekit.TabKindChat {
		if _, ok := m.chats.Get(ctx, vibekit.ChatID(spec.Ref)); !ok {
			return TabOpened{}, StatusError(http.StatusNotFound, errOpenChatUnknown)
		}
	}
	spec.Parent = m.fillRunParent(spec)
	return m.openTab(ctx, spec, opID)
}

// fillRunParent answers a run tab's Parent, resolving the launching chat's tab
// when the caller supplied none — one rule for every door, so no door has to
// hold the run's launch history. A CLIENT-supplied parent is never overwritten:
// History knows the parent of a finished run whose lease is gone, and Parent is
// immutable after open, so this is the only chance to get it right.
//
// Caller holds mu.
func (m *Membership) fillRunParent(spec vibekit.OpenTab) string {
	if spec.Kind != vibekit.TabKindRun || spec.Parent != "" || m.runs == nil {
		return spec.Parent
	}
	chatID, ok := m.runs.RunChat(spec.Ref)
	if !ok || chatID == "" {
		// A parentless run (manual, scheduled), or one this server never
		// launched: top level.
		return ""
	}
	return m.tabForChat(chatID)
}

// OpenRunTab is the AUTOMATIC open: the tab a starting run offers the chat
// whose agent launched it. Separate from OpenTab because it answers no request
// — typed refusals rather than statuses, a slot held back rather than the last
// one spent, and always a VIEW (`Owns: false`, so closing it stops nothing).
//
// The two refusals mean opposite things. ErrNoParentTab is try again later;
// errTabsFull is stop, because the held-back slot belongs to the reader's next
// gesture and creating a chat opens a tab.
func (m *Membership) OpenRunTab(ctx context.Context, workflowID string, parentChat vibekit.ChatID, opID string) (TabOpened, error) {
	if m.tabs == nil {
		return TabOpened{}, ErrTabsUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	parent := m.tabForChat(parentChat)
	if parent == "" {
		return TabOpened{}, ErrNoParentTab
	}
	if err := m.reserveSlot(vibekit.TabKindRun, workflowID, holdLastSlot); err != nil {
		return TabOpened{}, err
	}
	return m.openTab(ctx, vibekit.OpenTab{
		Kind:   vibekit.TabKindRun,
		Ref:    workflowID,
		Parent: parent,
	}, opID)
}

// CloseTab closes a tab and its descendants, then tears down what an owned tab showed —
// and, with retention off, deletes each chat the close left tabless. An id that is not
// open closes nothing and is not an error: two devices can close the same tab.
//
// tabs.Close is the COMMIT POINT: the doomed set is decided before it, while each
// record is still readable, and every record delete follows it. Past that point there is
// no rollback, only roll-forward under a context detached from the request — a failed
// record delete or teardown logs ERROR and the close still answers success.
func (m *Membership) CloseTab(ctx context.Context, id, opID string) (closed []vibekit.TabSubject, version uint64, err error) {
	if m.tabs == nil {
		return nil, 0, StatusError(http.StatusServiceUnavailable, ErrTabsUnavailable)
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

	// After the lock: the teardown issues a session/cancel over the bridge with no
	// client-side timeout, so holding the lock across it would let one wedged process
	// block every other tab mutation. Exactly one grade per chat — delete grade for a
	// record that went with this close (driven from the captured chain), close grade for
	// every other chat tab, including a doomed chat whose delete failed.
	dispatched := make(map[vibekit.ChatID]bool, len(deleted))
	for _, t := range closed {
		if t.Kind != vibekit.TabKindChat {
			continue
		}
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
	return closed, version, nil
}

// doomedChats decides what a retention-off close of id will delete: every chat whose
// open tabs all lie inside the closing subtree and whose record exists, with the session
// chain captured off that record before the commit, since nothing after the record delete
// may re-read it. A recordless chat is skipped — no chat_deleted for an id no device
// knows.
//
// Caller holds mu.
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
		return 0, StatusError(http.StatusServiceUnavailable, ErrTabsUnavailable)
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
		return 0, StatusError(http.StatusServiceUnavailable, ErrTabsUnavailable)
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
// record, then close its tabs. Once the record is gone every later open is refused, and
// any open that landed before it has its tab in the set closeTabsFor then walks. The
// teardown runs before the lock, because the run cancel must precede the bridge going
// down and it reaches the bridge.
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
// Normally a no-op — HasOpenTab already skips a chat with an open tab, so reaching this
// with tabs to close means one was opened between the predicate and the remove.
func (m *Membership) RetentionClose(ctx context.Context, chatID vibekit.ChatID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closeTabsFor(ctx, chatID, "")
}

// HasOpenTab reports whether any tab shows this chat — retention's second
// predicate. This makes retention opt-out for a chat left open forever,
// which is accepted: closing a tab under someone to satisfy a timer is
// worse.
//
// Takes no operation lock: it is a read, and the lock exists to order
// writes against their events.
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

// closeTabsFor closes every tab showing chatID. A close that fails after the record is
// already gone is retried once, and if that fails too the removal is emitted ANYWAY: the
// chat being gone is worse to leave unstated than a tab set this process failed to write.
// That frame is stamped one past the current version, so it costs the next real mutation
// being read as a duplicate.
//
// Caller holds mu.
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

// slotReserve is how many open-tab slots an operation must leave unspent.
type slotReserve int

const (
	// spendLastSlot is a person's gesture: the last slot is theirs to spend.
	spendLastSlot slotReserve = 0
	// holdLastSlot is an automatic open, which leaves the last slot for the
	// reader's next gesture — creating a chat opens a tab, so at MaxOpenTabs
	// New chat stops working.
	holdLastSlot slotReserve = 1
)

// reserveSlot refuses when opening a tab for (kind, ref) would have to mint one
// and fewer than keep+1 slots remain below MaxOpenTabs. A subject already open
// needs no slot, which lets a retry finish its own tab write at the limit.
//
// Caller holds mu.
func (m *Membership) reserveSlot(kind vibekit.TabKind, ref string, keep slotReserve) error {
	if m.tabs == nil {
		return nil
	}
	open, _ := m.tabs.List()
	for _, t := range open {
		if t.Kind == kind && t.Ref == ref {
			return nil
		}
	}
	if len(open)+int(keep) >= tabs.MaxOpenTabs {
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
