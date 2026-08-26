package vibekit

// TabKind is what a tab SHOWS. Together with a subject's Ref it NAMES the thing
// that is open, which is what lets the id be opaque: nothing parses a string
// prefix to learn that a tab is an editor, so there is no isEditorTabID and no
// startsWith("__") typing.
//
// The set is EXHAUSTIVE and validated at the door — see Valid, and see
// tabs.Store.Open, which refuses a kind it does not know. A subject carrying an
// unknown kind is one no client can render: the strip builds its view spec from
// a total per-kind factory, so a ninth value would reach a switch with no case
// for it on every connected device at once, and it would already be persisted by
// then.
//
// It lives here rather than in internal/tabs because TabSubject references it and
// TabSubject is a wire type: a tabs.Kind would make the wire package import tabs
// while tabs imports the wire package, which is a cycle. That is the whole reason
// for the placement, and it is why the type carries the Tab prefix its sibling
// kinds (ToolKind, PushKind, EventKind) carry too.
type TabKind string

// The eight tab kinds. Each string is the wire value AND the client's TabKind
// union member, so a rename here is a cross-language change.
//
// There is deliberately no "plan". The client's TabKind.plan was dead — nothing
// produced one and its only reference was a toolbar check that could never be
// true — and it was deleted on 2026-08-25, so declaring it here would put a value
// back on the wire that nothing opens. roles.ts's "plan" is a MODE id, a
// different vocabulary that happens to share a word.
const (
	TabKindChat     TabKind = "chat"
	TabKindEditor   TabKind = "editor"
	TabKindRun      TabKind = "run"
	TabKindSettings TabKind = "settings"
	TabKindGit      TabKind = "git"
	TabKindFiles    TabKind = "files"
	TabKindHistory  TabKind = "history"
	TabKindDocs     TabKind = "docs"
)

// tabKinds is the authoritative set, and the bool answers the question a caller
// always asks next: is this kind a SINGLETON, the one tab of its type, whose Ref
// is therefore empty?
//
// ONE table rather than two, because two tables can disagree: a ninth kind added
// to the valid set and forgotten in the singleton set would be a kind that is
// accepted and then required to carry a ref it has no meaning for, which reaches
// a reader as "Settings opens twice" rather than as an error.
var tabKinds = map[TabKind]bool{
	TabKindChat:     false,
	TabKindEditor:   false,
	TabKindRun:      false,
	TabKindSettings: true,
	TabKindGit:      true,
	TabKindFiles:    true,
	TabKindHistory:  true,
	TabKindDocs:     true,
}

// Valid reports whether k is one of the eight kinds. Used at the command
// boundary and again inside the store, because a kind that reaches the persisted
// set is a kind every client has to render.
func (k TabKind) Valid() bool {
	_, ok := tabKinds[k]
	return ok
}

// Singleton reports whether k has exactly one tab — settings, git, files,
// history and docs — so its subject's Ref is empty and a second open of it
// returns the tab already open.
//
// An unknown kind reports false, so this answer is only meaningful for a kind
// Valid accepts. The one place that matters is tabs.Store.Open, which checks
// Valid first.
func (k TabKind) Singleton() bool { return tabKinds[k] }

// TabSubject is the SHARED fact about one open tab: what it shows, where it
// sits, and whether closing it tears the thing down. It is what gets persisted
// and the only tab shape that crosses the wire.
//
// What is NOT here is the point of the split. The client's TabViewSpec — the
// view selector, the typed route, onShow, onClose, the local activity dot — is
// produced from a subject by a total per-kind factory, so nothing about
// activation or teardown moves server-side, and a subject carries no behaviour.
// The editor's loaded content, dirty state and line selection stay in
// fileStates; a singleton's lazy import stays in its factory. A factory needs
// nothing from a subject beyond (Kind, Ref).
//
// There is NO Order field: the position in tabs.Store's slice IS the order, so
// there is one representation of it rather than a slice and an integer that can
// disagree.
//
// Field order is govet fieldalignment's (the pointer-bearing strings lead, the
// bools trail), not reading order.
type TabSubject struct {
	// ID is opaque and server-minted (tabs.Store mints it at open). Opaque
	// because nothing should be able to branch on it: Kind and Ref name the
	// subject, so an id encoding its kind in a prefix would be a second
	// representation parsed in three places. It also keeps the API path
	// unambiguous under a reverse proxy that normalizes %2F.
	ID string `json:"id"`
	// Kind and Ref are the subject's identity: at most one tab exists per
	// (Kind, Ref) pair, which is what makes an open idempotent.
	Kind TabKind `json:"kind"`
	// Ref is a chat id, an absolute path, or a run id — empty for a singleton.
	// The store treats it as opaque text: whether it is a VALID chat id or a
	// path inside a granted root is the command boundary's question, because
	// that is where ids.ValidChatID and the file-browser roots live.
	Ref string `json:"ref"`
	// Parent is the tab this one hangs under, empty for a top-level tab.
	//
	// Set at open and NEVER reassigned, which is what makes a cycle
	// unrepresentable: a child's parent already existed when the child was
	// minted, so no chain can close on itself and no reparent check is needed
	// anywhere.
	Parent string `json:"parent"`
	// Pinned sorts a tab ahead of every unpinned one. The partition is applied
	// by the client when it renders (applyPinOrder); the stored slice keeps the
	// order it was given.
	Pinned bool `json:"pinned"`
	// Owns means closing this tab tears down what it shows, and it exists
	// because two tabs can otherwise be indistinguishable while differing in
	// authority: a run REVIEW opened from History and a launcher-OWNED run share
	// (Kind, Ref), and closing the owned one cancels the run. Set at open, like
	// Parent, so the authority cannot change under a reader.
	Owns bool `json:"owns"`
}

// OpenTab is the argument to tabs.Store.Open: everything a subject needs that
// the store cannot mint or derive.
//
// An argument struct rather than four positional parameters because Ref and
// Parent are adjacent same-typed strings — the transposition no compiler and no
// test can detect — and a struct puts the field name beside every value at the
// call site.
//
// It carries no op_id and no idempotency key: those are the command envelope's,
// and the store has no opinion about either.
type OpenTab struct {
	// Kind is required and must be one of the eight (see TabKind.Valid).
	Kind TabKind `json:"kind"`
	// Ref is required for every kind but a singleton, where it must be empty.
	Ref string `json:"ref,omitempty"`
	// Parent names an already-open tab. A parent that is not open PROMOTES the
	// new tab to top level rather than refusing it, which is what the client's
	// insertSpec does with an orphan for the same reason: a tab nobody can see
	// is worse than a tab in the wrong place.
	Parent string `json:"parent,omitempty"`
	// Owns is the authority flag described on TabSubject.Owns. The caller
	// decides it, because only the caller knows whether it launched the thing
	// this tab shows.
	Owns bool `json:"owns,omitempty"`
}

// TabList is the answer to GET /api/tabs: the open set in order, plus the
// version it reflects.
//
// The two fields travel together because they are ONE fact, captured in one
// critical section by tabs.Store.List. A caller that read them separately could
// pair a stale set with a fresh version and then discard the very event the set
// was missing — which is the defect that killed an earlier revision's SSE-head
// watermark, where the snapshot and the event hub sat behind different locks.
//
// Tabs is never omitted, even when empty: an empty arrangement is a real state
// (someone closed the last tab) and a missing field would read as "no answer".
type TabList struct {
	Tabs    []TabSubject `json:"tabs"`
	Version uint64       `json:"version"`
}
