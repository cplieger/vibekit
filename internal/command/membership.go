package command

// The membership coordinator: every operation that spans the chat store and the
// open-tab set, under ONE operation lock.
//
// # Why a coordinator exists at all
//
// "The same operation" here spans two documents — chats/<id>.json and tabs.json
// — and an atomic rewrite protects one file, not a transaction. So the property
// has to come from ORDERING plus one lock, and the failure of the alternative is
// concrete: endpoint-side validation is check-then-act against a concurrent
// delete, and a capacity precheck at the door reserves nothing.
//
// # THE CHAT RECORD IS THE GATE: it leads on create and leads on delete
//
// One rule covers both directions, which is why it is one rule and not two that
// can each be got backwards.
//
//   - CREATE writes the chat FIRST and its tab second. A crash between them
//     leaves a CLOSED CHAT, which is benign: a chat without a tab is exactly what
//     every chat the user has closed already looks like.
//   - DELETE removes the record FIRST and closes its tabs second. An Open that
//     slips between them finds no chat and is refused (see OpenTab's gate), so
//     the window cannot produce a tab pointing at nothing.
//
// The other order is wrong in both directions: a tab minted before its chat is a
// tab every client renders for a chat that may never exist, and tabs closed
// before the record is removed leave a window where an Open succeeds and its tab
// outlives the chat until the next restart.
//
// # THERE ARE TWO DELETE PATHS, and they lead with different writes
//
// delete_chat keeps the record-first order above. The retention-off CLOSE
// escalation (see CloseTab) is close-first: the tab close is the commit point
// and the record delete follows inside the same lock hold. Close-first does not
// reopen the ordering hole, because the LOCK is the race argument — OpenTab
// takes this same mutex, so no open can land between the tab close and the
// record delete — and the frame order is the tiebreak: tabs_changed applying
// the local teardown and then a no-op chat_deleted is exactly the order the
// delete path already produces cross-device.
//
// # CAPACITY IS RESERVED BEFORE ANYTHING MINTS
//
// A create ends by opening a tab, so a full set has to refuse BEFORE the chat
// record is written. Reversed, the refusal lands after the chat exists and the
// gesture leaves an orphan — a chat nothing can open, created by an operation
// that reported failure.
//
// # THE LOCK ORDER, and why it is acyclic
//
//	Membership.mu  ->  chat record lock  ->  tabs.Store writeMu
//
// Acyclic because the arrow never points back: no tabs.Store method calls into
// the chat store, and no chat.Store method calls into the tab set. The two
// callers that could invert it do not. The retention purge checks its predicates
// BEFORE taking a record lock (HasOpenTab reads the tab set under neither this
// lock nor a record lock) and fires its onPurge hook AFTER releasing it, so
// RetentionClose takes mu with no record lock held. And chat.Store.Mutate's
// broadcast happens inside the record lock, which reaches the SSE hub and never
// this type.
//
// # WHY EVERY TAB MUTATION COMES THROUGH HERE, even the ones that touch one store
//
// tabs.Store deliberately does not emit its own events: it returns the version a
// mutation committed and leaves the caller to broadcast it, having rejected both
// a WithinWrite seam (a mutation called inside a callback holding writeMu
// deadlocks) and a commit hook (it could not carry the op_id, which the caller
// has and the store does not). What it asks for in exchange is stated on Store:
// the caller must hand version N's event to the hub before it starts the
// mutation that produces N+1.
//
// A caller can only honour that by serializing mutate-and-emit, and THIS LOCK IS
// that serialization. So pin_tab and reorder_tabs come through here too, even
// though neither reads the chat store: routed straight at the store they would
// commit under writeMu and then race each other to the hub, and two frames
// arriving in the wrong version order is a client that stops applying and
// re-lists — or, worse, applies an order derived from a set it does not hold.
//
// The one writer that does NOT hold this lock is Prune, which runs once at load
// before the listener serves anything, so there is nothing to order it against.

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
	// errTabsFull is the 409 for an open at MaxOpenTabs. It is a real product
	// limit with a user-visible consequence — at the limit, New chat stops
	// working — so the message is the remedy rather than the fault.
	errTabsFull = errors.New("too many tabs are open; close a tab first")
	// errTabsUnavailable is the 503 for a build with no tab store wired (no
	// config dir). Answered rather than swallowed: a command whose effect did not
	// happen must not report success.
	errTabsUnavailable = errors.New("the tab store is unavailable")
	// errOpenChatUnknown is the 404 an open_tab for a chat that does not exist
	// gets. This is the delete-ordering gate's refusal: the record is removed
	// before its tabs are, so an open racing a delete lands here.
	errOpenChatUnknown = errors.New("that chat no longer exists")
	// errTabUnknown is the 404 for a pin or a close naming an id the set does not
	// hold... except it is NOT used for a close, which treats an absent id as
	// nothing to do. Only pin_tab reports it, because a pin is a statement about a
	// tab and silently succeeding would tell a client its tab is pinned.
	errTabUnknown = errors.New("that tab is not open")
)

// TabSet is the open-tab set as this package uses it: the four mutations plus
// the paired reads. Declared here, at the consumer, because internal/tabs exports
// no interface of its own — *tabs.Store is what satisfies it.
//
// List is in the set for two reasons beyond reading: the capacity reservation
// needs the count, and every event needs the expanded order, which no mutation
// returns. Subtree is the close escalation's read — what a Close of this id
// will remove, asked BEFORE the close commits. All are read while mu is held,
// so what they see is what the mutation just committed.
type TabSet interface {
	Open(ctx context.Context, spec vibekit.OpenTab) (subject vibekit.TabSubject, created bool, version uint64, err error)
	Close(ctx context.Context, id string) ([]vibekit.TabSubject, uint64, error)
	Reorder(ctx context.Context, ids []string) (uint64, error)
	SetPinned(ctx context.Context, id string, pinned bool) (uint64, error)
	List() ([]vibekit.TabSubject, uint64)
	Subtree(id string) []vibekit.TabSubject
}

// chatCloser is the tab-close teardown for a CHAT tab: cancel the turn, cancel
// the chat's runs, tear the bridge down, and keep the record.
//
// A function seam rather than three more role interfaces on this type, because
// what the coordinator needs is one decision ("this chat's work stops now"), not
// the bridge, the pending-permission tracker and the teardown separately. The
// binding lives in RegisterDefaults beside the roles it composes.
type chatCloser func(ctx context.Context, chatID vibekit.ChatID)

// chatDeleter is the DELETE grade of the same teardown, for a chat the close
// escalation has already erased: everything the record used to answer travels
// in as the session chain captured before the commit. Same seam shape as
// chatCloser, bound in RegisterDefaults beside it, so the two grades cannot
// drift apart.
type chatDeleter func(ctx context.Context, chatID vibekit.ChatID, sessionChain []string)

// retentionRead answers whether chat retention is ON — whether a closed chat's
// record is KEPT. A function seam for chatCloser's reason: the coordinator
// needs one predicate, not the settings machinery, and the read must FAIL
// TOWARD KEEPING (settings.RetentionEnabled, bound in RegisterDefaults). A nil
// read means retention ON, the same safe direction.
type retentionRead func(ctx context.Context) bool

// doomedChat is one chat a retention-off close will delete: the record's id
// and the KAS session chain, both captured under the lock while the record was
// still readable — NOTHING that runs after the record delete may re-read it.
type doomedChat struct {
	chatID vibekit.ChatID
	chain  []string
}

// closeTeardownBudget bounds the close escalation's post-commit work. The
// teardown runs under a context DETACHED from the HTTP request
// (context.WithoutCancel), because a client that times out or walks away must
// not cancel roll-forward — the same reasoning as the git handlers' detached
// scans — and WithoutCancel alone has no deadline, so this is the bound that
// keeps an unresponsive bridge from holding the goroutine forever. Generous
// because the run cancel is a workflow RPC per live run.
const closeTeardownBudget = time.Minute

// Membership owns every operation that spans the chat store and the tab set.
//
// Safe for concurrent use; the zero value is not usable, construct with
// NewMembership. A nil TabSet is a supported state and means no tab store was
// wired: the chat half of every operation still runs and the tab half reports
// errTabsUnavailable, which is the same answer internal/server gives for an
// unwired ui-state store and for the same reason — a build without a config dir
// must still work.
type Membership struct {
	chats      ChatStore
	tabs       TabSet
	bus        Broadcaster
	teardown   ChatTeardown
	closeChat  chatCloser
	deleteChat chatDeleter
	retention  retentionRead
	// ops is the create ledger: op_id -> chat id, so a retry resolves to the chat
	// its first attempt made. It lives HERE rather than in the handlers because
	// resolving an op and reserving a tab slot have to happen in the same
	// critical section — see CreateChatAndOpen.
	ops *createLedger
	// mu is THE operation lock. It is held across the capacity reservation, the
	// mint, both durable writes and the event, which is what makes each of those
	// pairs atomic against every other operation.
	mu sync.Mutex
}

// MembershipDeps is Membership's constructor argument.
//
// A struct because eight collaborators of seven different interface types is
// exactly the positional argument list a transposition hides in, and because
// TabSet and the funcs are each independently optional-looking at a call
// site. Every field is required except Tabs, DeleteChat and Retention — the
// last two default to the safe direction (no escalation, retention ON).
type MembershipDeps struct {
	Chats      ChatStore
	Tabs       TabSet
	Bus        Broadcaster
	Teardown   ChatTeardown
	CloseChat  chatCloser
	DeleteChat chatDeleter
	Retention  retentionRead
}

// NewMembership builds the coordinator. deps travels by pointer: it is a
// wiring record read once at construction, not a value worth copying.
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

// ChatCreate is one create-and-open request: what to write on the new record,
// and where its tab goes.
type ChatCreate struct {
	// Init fills the new record's fields. Called INSIDE chat.Store.Mutate, so it
	// runs under that chat's record lock and must not reach either store — see
	// the lock order on this file.
	//
	// Not called at all when the record already exists, which is both the replay
	// case and the reason a create cannot reshape a chat it did not make.
	Init func(c *vibekit.Chat)
	// OpID correlates every attempt of ONE create gesture. A repeat resolves to
	// the chat the first attempt made instead of minting a second one, and it
	// FINISHES A MISSING TAB WRITE — the first attempt can have created the chat
	// and failed on the tab, and answering with a chat that has no tab would
	// leave the caller with nothing to show.
	OpID string
	// ChatID is the id the envelope supplied, or empty to mint one. A supplied id
	// bypasses the ledger: the caller chose the key, so a retry carries it again
	// and Mutate's exists branch is already idempotent.
	ChatID vibekit.ChatID
	// ParentChat names the chat whose tab the new tab hangs under, empty for a top
	// level tab. Its one user is the tangent, which is the only create that nests.
	//
	// A CHAT id rather than a tab id, so the resolution happens inside the
	// operation lock: a caller that looked the tab up itself would be reading the
	// set before this operation takes the lock, so a parent tab closing in between
	// would leave a Parent naming nothing. An absent parent promotes the new tab
	// to top level, which is tabs.Store.Open's own rule for an orphan.
	ParentChat vibekit.ChatID
}

// ChatOpened is what a create answers with.
type ChatOpened struct {
	// Chat is read back from the STORE rather than assembled from what was
	// written: the store is canonical, and a replayed op resolves to a chat this
	// request did not write at all.
	Chat *vibekit.Chat
	// Subject is the tab. Zero-valued only when no tab store is wired.
	Subject vibekit.TabSubject
	Version uint64
	// Replay reports that this op_id had already created its chat, so the caller
	// can derive its answer from the record instead of restating this attempt's.
	Replay bool
}

// TabOpened is what an open answers with.
type TabOpened struct {
	Subject vibekit.TabSubject
	Version uint64
	// Created is false for an already-open (Kind, Ref), which mutates nothing,
	// bumps no version and therefore emits NO event. That flag closes a real
	// hole: with the event as the only render path, a caller that waits for a
	// frame would wait forever, so it resolves from the response instead.
	Created bool
}

// CreateChatAndOpen writes a chat record and opens its tab as ONE operation.
//
// The whole sequence — reservation, mint, record, tab, event — runs under the
// operation lock, so the final slot cannot be consumed between minting and
// opening and a concurrent delete cannot land between the two writes.
//
// Returns errTabsFull (409) when the set is at MaxOpenTabs, and errChatNotCreated
// (409) when the record is absent after a Mutate that reported no error, whose
// one reachable cause is a tombstoned id the caller supplied.
//
// A tab write that FAILS leaves the chat created and returns the error. That is
// the deliberate direction: the record is the gate, invariant 4 says only
// delete_chat removes a chat, and a retry carrying the same op_id finishes the
// tab write. Rolling the chat back would be a second delete path.
func (m *Membership) CreateChatAndOpen(ctx context.Context, req ChatCreate) (ChatOpened, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// THE RESERVATION, before anything mints. peek rather than resolve, because
	// the exemption has to be decided before the mint: a repeat of an op whose
	// tab is already open needs no slot, and refusing that at the limit would
	// strand the chat the first attempt created with no way to finish it.
	prior, replay := m.priorChat(req)
	if err := m.reserveSlot(vibekit.TabKindChat, string(prior)); err != nil {
		return ChatOpened{}, err
	}

	chatID := prior
	if chatID == "" {
		chatID, _ = m.ops.resolve(req.OpID, vibekit.NewChatID)
	}

	// THE RECORD LEADS.
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

	// THE TAB SECOND. Unconditional, including on a replay: this is what finishes
	// a tab write the first attempt did not, and Open is idempotent by
	// (Kind, Ref) so the ordinary replay costs one scan and emits nothing.
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
// one.
//
// It exists for fork_chat and nothing else. A fork's record cannot be built
// until KAS has answered session/fork, and that round trip must NOT happen under
// the operation lock — a bridge Call has no client-side timeout, so one wedged
// agent process would block every tab mutation in the workspace. So fork asks
// this first and skips its RPC when the op has already produced a chat, which is
// the guarantee it needs: a retry must not fork a second session that nothing
// binds.
//
// The read is not atomic with the create that follows it, and it does not need to
// be. The ledger's own resolve under the lock is still the authority, so the
// worst case for two genuinely concurrent attempts of one op is the same one
// stage 1b had — one extra KAS session, logged — while the membership half stays
// atomic.
func (m *Membership) ResolvedChat(opID string) (vibekit.ChatID, bool) {
	return m.ops.peek(opID)
}

// OpenTab opens a tab for something that already exists. It NEVER mints a chat.
//
// For a chat tab it gates on the record, and that gate is the other half of the
// delete ordering: the record is removed first, so an open that raced a delete
// finds no chat and is refused with errOpenChatUnknown (404) instead of leaving a
// tab nothing can open. The check and the open are in one critical section, so it
// is a gate rather than a check-then-act.
//
// The capacity refusal comes from the store here rather than from a reservation:
// nothing has been minted, so there is nothing to strand, and one refusal in one
// place is less mechanism than two that must agree. tabs.ErrTooMany maps to 409.
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
// showed — and, with retention OFF, DELETES each chat the close left tabless.
//
// An id that is not open closes nothing and is NOT an error: two devices can
// close the same tab, and an empty closed list already says so.
//
// A parent with children is ONE store mutation, so it is one version bump and
// one event naming every removed id — which is why this returns everything that
// went rather than a count.
//
// # The retention-off escalation, ordered
//
// Under the operation lock: (a) read the retention predicate and decide the
// DOOMED set — the close's subtree × remaining refs × the predicate × a record
// that exists — capturing each doomed chat's {id, session chain} while the
// record is still readable; (b) tabs.Close, THE COMMIT POINT: from here the
// response answers success with the closed ids, and a failed Close is
// nothing-committed — error response, records untouched, a client rollback
// legitimate; (c) chats.Delete each doomed record, tombstone and chat_deleted
// broadcast inside Delete, AFTER the tabs frame. The lock is what makes
// close-first safe (see the package doc's two-paths statement), and the brief
// live-bridge/no-record window between (c) and the teardown below is invariant
// 3's, closed by the delete tombstone — auto-create refuses the id.
//
// After the commit point there is NO rollback, only roll-forward: a record
// delete or teardown failure logs ERROR and the close still answers success —
// the authoritative fact is the tab close. Post-commit work therefore runs
// under a context DETACHED from the request (a client abandoning the call must
// not cancel roll-forward) with its own bound.
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
	// COMMITTED. Everything from here rolls forward under its own bound; the
	// request context governed the operation only UP TO the commit point.
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

	// The teardown runs AFTER the lock is released and after the membership fact
	// is published. Two reasons, and the first is the load-bearing one: it issues
	// a session/cancel over the bridge, a round trip with no client-side timeout,
	// so holding the operation lock across it would let one wedged agent process
	// block every other tab mutation. And closing the tab is what the user asked
	// for — a teardown that fails cannot un-close it, so the tab's removal is not
	// its to gate.
	//
	// EXACTLY ONE grade per chat: the DELETE grade for a chat whose record went
	// with this close — driven from the chain captured under the lock, because
	// the record is gone and a record-reading teardown would silently no-op —
	// and the ordinary close grade for every other chat tab, never both. A
	// doomed chat whose record delete FAILED is demoted to the close grade: its
	// record survives, so reaping its sessions would strand a reopenable chat.
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

// doomedChats decides what a retention-off close of id will DELETE: every chat
// whose open tabs ALL lie inside the closing subtree, whose record exists, with
// the KAS session chain captured off that record — under the lock, before the
// commit, because nothing that runs after the record delete may re-read it.
//
// Recordless chats are SKIPPED: no chats.Delete and no chat_deleted for an id
// no device knows. Zero-message chats WITH records are doomed like any other —
// a stated behavior change closing the orphan-record leak the client's old
// message_count === 0 skip created under retention 0.
//
// Caller holds mu. The predicate is read INSIDE the lock, once per close, so
// one operation decides against one setting.
func (m *Membership) doomedChats(ctx context.Context, id string) []doomedChat {
	if m.deleteChat == nil || m.retention == nil {
		// Escalation unwired: a close can only close. Retention defaults ON —
		// the fail-toward-keeping direction.
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

// tablessChatRefs returns the chat refs the close of this subtree leaves with
// no open tab: each distinct chat ref in the subtree, minus any ref that still
// has a chat tab OUTSIDE it. Caller holds mu, so the answer is still true when
// the close commits.
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
// chat_deleted broadcast happen inside Delete — and returns the session chains
// of the ones that actually went, keyed by chat id. A failed delete logs ERROR
// and is left OUT of the result, which demotes that chat to the close-grade
// teardown: the close still answers success (roll-forward), and the surviving
// record keeps its sessions so it stays reopenable rather than becoming a
// record whose history was reaped out from under it.
//
// Caller holds mu; ctx is the DETACHED roll-forward context, so a client that
// abandoned the request cannot leave records half-deleted.
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

// ReorderTabs replaces the order. ids must name every open tab exactly once;
// tabs.ErrOrderMismatch maps to 409.
//
// No base-version precondition, deliberately: the exact-set check IS the
// precondition, and a version one would discard a valid drag whenever an
// unrelated pin landed first.
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
	// An order identical to the one already held changes nothing, so it commits
	// nothing and must emit nothing. The version is what says which happened —
	// the store returns the current one unchanged for a no-op.
	if version != before {
		m.emit(ctx, &vibekit.TabsChangedPayload{Order: m.order(), Version: version, OpID: opID})
	}
	return version, nil
}

// SetPinned pins or unpins one tab. Idempotent in both directions: a tab already
// in that state commits nothing and emits nothing.
//
// An id that is not open is errTabUnknown (404), unlike a close: a pin is a
// statement ABOUT a tab, so answering success would tell the caller its tab is
// pinned when no such tab exists.
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

// DeleteChatAndCloseTabs is the delete path: tear the chat's work down, remove
// the RECORD, then close its tabs.
//
// The record leads, and that ordering is the whole reason an open racing this
// cannot leave a tab behind: OpenTab's gate reads the record, so once it is gone
// every later open is refused, and any open that landed BEFORE the delete has
// its tab in the set that closeTabsFor then walks.
//
// The teardown runs before the lock, in the order CmdDeleteChat has always used:
// the chat's runs are cancelled before the bridge goes, because a run is durable
// state a dead process only PAUSES. It is outside the lock for CloseTab's reason
// — it reaches the bridge.
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

// RetentionClose closes the tabs of a chat the retention purge has already
// removed.
//
// Normally a no-op, and that is the point: retention's own predicate skips a
// chat that HAS an open tab (see HasOpenTab), so reaching this with tabs to close
// means one was opened between the predicate and the remove. It exists so that
// race resolves in the same pass rather than at the next restart.
//
// Called from the purge's onPurge hook, which fires after the per-chat record
// lock is released — see the lock order on this file.
func (m *Membership) RetentionClose(ctx context.Context, chatID vibekit.ChatID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// No op_id: no client asked for this, so there is no dispatch to correlate.
	m.closeTabsFor(ctx, chatID, "")
}

// HasOpenTab reports whether any tab shows this chat. It is retention's second
// predicate.
//
// THIS MAKES RETENTION OPT-OUT for a chat left open forever, and that is
// accepted. It is the honest reading of "in use": it is what a reader expects
// from a tab they deliberately kept, and the alternative is closing a tab under
// someone to satisfy a timer. The first predicate (a non-empty draft) has the
// same shape and the same answer.
//
// Takes no operation lock, deliberately. It is a READ, and the lock exists to
// order writes against their events; taking it here would only make every purge
// pass queue behind whatever tab mutation is in flight.
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

// closeTabsFor closes every tab showing chatID, and it is the ONE place the
// live-repair rule lives.
//
// A tab close that FAILS after the chat record is already gone must not wait for
// a restart, because Prune only runs at load — so the close is RETRIED, and if
// the retry fails too the removal is emitted ANYWAY. The authoritative fact is
// that the chat is gone: a client still showing that tab is showing a chat
// nothing can open, which is strictly worse than a tab set this process failed to
// write.
//
// The emit-anyway frame is stamped at the store's current version PLUS ONE, and
// the cost of that is real and stated. A client's three version rules discard
// anything at or below its local version, so stamping the unchanged current
// version would make the frame silently useless — the whole point of emitting it.
// Stamping one past means the next mutation that DOES commit produces the same
// number and a client that already advanced reads it as a duplicate, so it misses
// one frame until its next gap or re-list. That is the price of telling clients
// the truth about the chat rather than about a tab store that has already failed
// to write, and it is only reachable on that path.
//
// Caller holds mu.
func (m *Membership) closeTabsFor(ctx context.Context, chatID vibekit.ChatID, opID string) {
	if m.tabs == nil {
		// No tab store wired, so there is nothing to close and nothing to announce.
		// The guard lives HERE rather than at each caller because both callers reach
		// this after their own work has already landed, and a nil-deref there would
		// take a completed delete down with it.
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

// reserveSlot refuses when opening a tab for (kind, ref) would have to MINT one
// and the set is already at MaxOpenTabs.
//
// A subject that is already open needs no slot, which is what lets a retry
// finishing its own tab write succeed at the limit. An empty ref never matches a
// chat tab, so a fresh mint always falls through to the count — which is exactly
// the case the reservation is for.
//
// Caller holds mu, so the count it read is still true when Open runs.
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

// priorChat returns the chat this create already resolves to, without minting:
// the envelope's id when the caller supplied one, else whatever the ledger
// already holds for the op.
func (m *Membership) priorChat(req ChatCreate) (id vibekit.ChatID, replay bool) {
	if req.ChatID != "" {
		return req.ChatID, false
	}
	return m.ops.peek(req.OpID)
}

// tabForChat returns the id of the tab showing chatID, or "" when none is open.
// Caller holds mu, which is what makes the answer still true when Open uses it.
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

// order is the EXPANDED order every event carries: every open tab including
// children. Caller holds mu, so it is the order the mutation just committed.
func (m *Membership) order() []string {
	open, _ := m.tabs.List()
	return subjectIDs(open)
}

// version is the collection version as the store currently holds it. Caller
// holds mu.
func (m *Membership) version() uint64 {
	_, v := m.tabs.List()
	return v
}

// subject reads one tab back after a mutation that returned only a version.
// Caller holds mu.
func (m *Membership) subject(id string) (vibekit.TabSubject, bool) {
	open, _ := m.tabs.List()
	if i := indexOfTab(open, id); i >= 0 {
		return open[i], true
	}
	return vibekit.TabSubject{}, false
}

// emit broadcasts one aggregate frame. Workspace-global, so the chat id is
// empty: the arrangement is not per chat, and a chat-scoped frame would be
// filtered by every consumer keyed on a session.
func (m *Membership) emit(ctx context.Context, p *vibekit.TabsChangedPayload) {
	if m.bus == nil {
		return
	}
	m.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventTabsChanged, "", *p))
}

// tabStatus maps the tab store's sentinels onto HTTP statuses.
//
// Per-site rather than by a table on the sentinel, the same reason statusError
// carries its code: ErrTooMany is the product limit's 409 here and would be a
// different answer anywhere the limit was advisory.
func tabStatus(err error) error {
	switch {
	case errors.Is(err, tabs.ErrTooMany):
		return StatusError(http.StatusConflict, errTabsFull)
	case errors.Is(err, tabs.ErrOrderMismatch):
		// 409 rather than 400: the request is well formed and the caller's view of
		// the set is simply not the server's, which is a conflict to re-list from
		// rather than a malformed body to fix.
		return StatusError(http.StatusConflict, err)
	case errors.Is(err, tabs.ErrBadKind), errors.Is(err, tabs.ErrBadRef):
		return StatusError(http.StatusBadRequest, err)
	}
	return StatusError(http.StatusInternalServerError, err)
}

// tabsForChat returns every tab showing chatID. A chat can legitimately have
// more than one row in the set only through a hand-edited file, but the walk
// costs nothing and returning a slice means the delete path has no "which one"
// question to get wrong.
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
