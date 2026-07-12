// Package pending implements the staging store for Supervised mode.
//
// When a chat has SupervisedMode=true, the fs handler doesn't apply
// file writes immediately. It registers a pending op in this store and
// blocks on the op's resume channel until the user resolves it via the
// UI. The UI posts resolve_pending_change / resolve_all_pending_changes
// (see hub/command_pending.go); that resolution closes the channel with
// an accept/reject signal, the fs handler unblocks, and the write
// either applies or returns an error to the agent.
//
// Design points:
//
//   - In-memory only. Pending ops don't survive a server restart
//     because the blocked fs handlers are bound to the live ACP
//     session; if the process dies, the kiro-cli turn is already lost
//     and restoring pending state would point at callers that no
//     longer exist.
//
//   - Per-chat FIFO. Ops for a chat are tracked in insertion order so
//     the UI can render "3 pending · oldest to newest" without
//     re-sorting. The ordering also makes "accept all" predictable:
//     files get applied in the order kiro-cli emitted them, so a
//     create-then-edit sequence on the same path applies cleanly.
//
//   - Concurrent writes to the same path are refused. If op A is
//     already pending for path P and kiro-cli emits op B for P, B's
//     Add returns ErrPathBusy and the fs handler returns an error to
//     the agent without staging. Simpler than chained-op semantics
//     and matches the "pause the agent" intent — the agent should
//     wait on A to settle before queueing B.
//
//   - Idempotent resolution. Resolving a non-existent or already-
//     resolved op returns ErrUnknown (caller maps to 404/400); it
//     never panics or double-delivers.
//
//   - Text caps. OldText/NewText are stored verbatim because the
//     SSE payload needs them for the inline diff. Callers MUST
//     truncate above Cap (4 MiB per side) and set the Truncated
//     flag so the UI can fetch full content from disk on demand.
package pending

import (
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// Cap is the canonical per-side text cap for OldText/NewText in a
// pending op (4 MiB). This is the authoritative constant for the
// maximum file-write payload size across the application — both the
// Supervised staging path (this package) and the unstaged fs handler
// (hub/bridge_fs.go's fsWriteCap) MUST use this value so a staged
// write never holds more bytes than the unstaged path would accept.
// Callers that receive oversized content MUST truncate (marking
// Truncated=true) or reject; the store doesn't enforce.
const Cap = 4 << 20

// MaxPendingPerChat caps the number of staged ops per chat. A
// misbehaving agent that emits thousands of fs/write_text_file calls
// during a Supervised-mode turn would otherwise pin up to ~8 MiB per
// op (OldText + NewText at Cap each) × N ops in heap memory. 256 is
// generous for realistic supervised turns (typically <20 ops) and
// bounds the worst-case heap at ~2 GiB per chat.
const MaxPendingPerChat = 256

// MaxPendingTotal caps the total number of staged ops across every
// chat. Belt-and-braces for the case where one quiet chat is under
// the per-chat cap but ten concurrent chats each accumulate a
// handful. Bounds worst-case heap at ~16 GiB across the process.
const MaxPendingTotal = 2048

// Kind is an alias for api.PendingChangeKind, establishing a single
// source of truth for pending-change kinds. The api package owns the
// canonical type; this alias avoids a mass rename of internal callers.
type Kind = api.PendingChangeKind

// Kind constants — aliases for the canonical api.PendingChangeKind values.
const (
	KindCreate = api.PendingKindCreate
	KindEdit   = api.PendingKindEdit
	KindDelete = api.PendingKindDelete
)

// ActionAccept and ActionReject are retained as typed constants for
// callers that need the api.PendingAction value without importing api
// directly. Resolve now accepts api.PendingAction, making invalid
// actions a compile error.
const (
	ActionAccept = api.PendingActionAccept
	ActionReject = api.PendingActionReject
)

// Errors returned by the store. Callers map these to HTTP status
// codes in command_pending.go.
var (
	// ErrUnknown is returned by Resolve when the tool call id has
	// no pending op (never staged, or already settled).
	ErrUnknown = errors.New("pending: unknown tool call id")
	// ErrPathBusy is returned by Add when the chat already has a
	// pending op for the same path. The fs handler surfaces this to
	// the agent as a rejection, which is the cleanest signal; the
	// alternative (queueing) would require speculative application
	// order and is more work than the problem justifies.
	ErrPathBusy = errors.New("pending: path already staged")
	// ErrBadKind is returned when Add gets something other than
	// KindCreate / KindEdit / KindDelete.
	ErrBadKind = errors.New("pending: kind must be create, edit, or delete")
	// ErrEmptyID is returned when Add gets a blank ToolCallID.
	ErrEmptyID = errors.New("pending: tool_call_id is required")
	// ErrEmptyChatID is returned when Add gets a blank ChatID.
	ErrEmptyChatID = errors.New("pending: chat_id is required")
	// ErrEmptyPath is returned when Add gets a blank Path.
	ErrEmptyPath = errors.New("pending: path is required")
	// ErrTooManyPending is returned when the per-chat or total
	// pending-op caps are exhausted. The fs handler surfaces this
	// to the agent as a tool failure; the agent should wait for
	// existing ops to resolve before retrying.
	ErrTooManyPending = errors.New("pending: too many pending changes")
	// ErrMergeNotApplicable is returned by ResolveWithText when
	// the op kind is not KindEdit. Merged text only makes sense
	// for edit-kind ops; accepting a delete or create with a
	// merged body would result in the wrong file contents.
	ErrMergeNotApplicable = errors.New("pending: merged_text not applicable to delete ops")
)

// Resolution is the outcome of a pending op after the resume channel
// closes. Returned atomically by the readResolution closure from Add,
// eliminating the need for separate MergedText/ClearMergedText calls.
//
// Merged distinguishes a user-authored partial merge (via ResolveWithText)
// from a plain accept. It is the flag callers gate the write-override on —
// NOT MergedText != "" — so a partial merge that resolves to an empty
// file (e.g. a create whose only hunk was rejected) still overrides the
// agent's content with the empty result instead of silently writing the
// agent's original text.
type Resolution struct {
	MergedText string
	Accepted   bool
	Merged     bool
}

// op is one staged file operation. Opaque to callers; use Store.Get
// or Store.ListForChat to obtain an api.PendingChange snapshot of
// the wire form.
type op struct {
	CreatedAt  time.Time
	resume     chan struct{}
	cancelStop func() bool
	ToolCallID string
	ChatID     api.ChatID
	Path       string
	Kind       Kind
	OldText    string
	NewText    string
	mergedText string
	Truncated  bool
	accepted   bool
	merged     bool
}

// Snapshot returns the wire-form view of the op. Safe to call any
// time; the copy decouples callers from subsequent state changes
// (which only touch accepted and resume anyway).
func (o *op) Snapshot() api.PendingChange {
	return api.PendingChange{
		ToolCallID: o.ToolCallID,
		ChatID:     o.ChatID,
		Path:       o.Path,
		Kind:       o.Kind,
		OldText:    o.OldText,
		NewText:    o.NewText,
		CreatedAt:  o.CreatedAt.UnixMilli(),
		Truncated:  o.Truncated,
	}
}

// Store is the staging registry. One Store instance serves every chat
// in the hub.
type Store struct {
	ops       map[string]*op
	byChat    map[api.ChatID][]string
	pathIndex map[api.ChatID]map[string]struct{} // chatID → set of staged paths
	mu        sync.Mutex
}

// Compile-time assertion: *Store satisfies the consumer-side interface.
var _ api.PendingStore = (*Store)(nil)

// New returns a ready-to-use Store.
func New() *Store {
	return &Store{
		ops:       make(map[string]*op),
		byChat:    make(map[api.ChatID][]string),
		pathIndex: make(map[api.ChatID]map[string]struct{}),
	}
}

// AddParams is the caller's input to Add. Separate from op so the
// store controls the resume channel and timestamp.
type AddParams struct {
	ToolCallID string
	ChatID     api.ChatID
	Path       string
	Kind       Kind
	OldText    string
	NewText    string
	Truncated  bool
}

func removeID(s []string, target string) []string {
	i := slices.Index(s, target)
	if i < 0 {
		return s
	}
	return slices.Delete(s, i, i+1)
}
