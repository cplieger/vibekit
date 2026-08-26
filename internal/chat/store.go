// Package chat implements per-chat persistence: one JSON file per chat under
// <dir>/<chat_id>.json. The directory listing is the index. Each file is
// atomically rewritten on every mutation via write-temp-then-rename.
//
// The store is the single source of truth for chat state. No sessions.json,
// no index.json, no event log replay. A chat's ACP session id lives in the
// chat file's header so a container restart can resume via session/load.
package chat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cplieger/vibekit/internal/chat/archive"
	"github.com/cplieger/vibekit/internal/filemode"
	"github.com/cplieger/vibekit/internal/ids"
	"github.com/cplieger/vibekit/internal/vibekit"
	"golang.org/x/sync/singleflight"
)

// errInvalidUTF8 is returned when a chat mutation produces content that
// cannot round-trip through JSON (the storage format).
var errInvalidUTF8 = errors.New("chat: content contains invalid UTF-8")

// errDraftTooLarge is returned when a composer draft exceeds vibekit.MaxDraftBytes.
var errDraftTooLarge = errors.New("chat: draft exceeds the size cap")

// errTooManyAttachments and errBadAttachmentPath are the two ways a staged
// attachment list is refused at the store: more entries than vibekit.MaxAttachments,
// and a path that is empty, over vibekit.MaxAttachmentPathBytes or not UTF-8.
var (
	errTooManyAttachments = errors.New("chat: too many attachments")
	errBadAttachmentPath  = errors.New("chat: attachment path is empty or too long")
)

// broadcaster is the SSE fan-out this store emits chat lifecycle and message
// events through. *agent.Runtime satisfies it.
//
// Declared HERE, at the consumer, rather than in a shared contract package.
// 1 method — the whole of what a store needs to say "this happened" — against a
// *agent.Runtime that exports well over a hundred. Nothing about a bridge, a session
// or a turn is any business of a file writer.
type broadcaster interface {
	Broadcast(ctx context.Context, evt vibekit.ServerEvent)
}

// Compile-time assertion: Store satisfies archive.StoreAccess.
var _ archive.StoreAccess = (*Store)(nil)

// fileMode is the on-disk mode for chat files. The parent dir uses 0o700
// because chat content may contain secrets the user pasted into prompts.
const (
	fileMode       = 0o600
	dirMode        = 0o700
	chatFileSuffix = ".json"
)

// maxChatFileBytes caps the size of a single chat file loaded by `load`.
// Well above any realistic chat (hundreds of KB even with dense history)
// and well below typical container memory limits, so a corrupted or
// runaway file can't OOM the process via List() walking every chat.
const maxChatFileBytes = 32 * 1024 * 1024 // 32 MiB

// Store owns the chat directory. Each chat has its own mutex so different
// chats never block each other; same-chat mutations serialize.
//
// A short-TTL tombstone set guards against the delete-during-turn race:
// if cmdPrompt auto-creates a chat via Mutate at the same moment the
// user deletes it from another tab, the delayed AppendMessage calls
// would otherwise re-create the chat file as a ghost. Tombstones make
// Mutate refuse to create for a recently-deleted id — late writes
// become no-ops instead of undead resurrections.
type Store struct {
	broadcast   broadcaster
	listSF      singleflight.Group
	onPurge     func(chatID vibekit.ChatID, sessionChain []string)
	isLive      func(chatID vibekit.ChatID) bool
	hasOpenTab  func(chatID vibekit.ChatID) bool
	tombstone   map[vibekit.ChatID]time.Time
	archive     *archive.Service
	locks       sync.Map
	dir         string
	archiveOnce sync.Once
	tombMu      sync.Mutex
}

// tombstoneTTL is how long a deleted chat id blocks re-creation via
// Mutate. Comfortably longer than any real prompt roundtrip (kiro-cli
// turns can run minutes) but short enough that new chats with ids
// recycled by the client (e.g. same timestamp after a clock jump)
// aren't permanently blacklisted. 10 minutes matches our idempotency
// cache TTL (2x safety margin).
const tombstoneTTL = 10 * time.Minute

// NewStore opens (or creates) the chat directory at dir. Returns an error
// if the directory cannot be created — callers must fail startup rather
// than return a store whose every op will fail. A mode that cannot be
// enforced is NOT one of those errors: it warns and continues, because
// the container coming up is the operator's only way in to repair a
// /config this process neither created nor owns.
func NewStore(dir string, opts ...StoreOption) (*Store, error) {
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return nil, fmt.Errorf("chat store: mkdir %s: %w", dir, err)
	}
	// MkdirAll only applies its mode on CREATION, and even then the mode is a
	// request: a directory created under a setgid parent carries the bit whether
	// or not it was asked for, and an inheritable group-write ACL stores 0770 for
	// a 0o700 mkdir. So the enforcement covers both a pre-existing directory a
	// user-mounted config volume left wide and a fresh one the kernel widened —
	// and EnforceDir re-stats the descriptor it chmod'ed, which is the only
	// thing that makes the mode below a fact rather than a request. It refuses a
	// symlink or a non-directory at the name instead of chmod'ing through it.
	//
	// Warn-and-continue is KEPT, against this function's own doc claim that it
	// fails when perms cannot be enforced: a chmod refused on a mounted volume
	// (read-only, or owned by another uid while still writable by us) must not
	// abort boot, because the operator's way IN to repair /config is the container
	// coming up (vibekit invariant 6). The doc comment above is corrected to say
	// what the code does.
	stored, err := filemode.EnforceDir(dir, dirMode)
	mode := stored.String()
	if err != nil {
		slog.Warn("chat store: chat dir is not 0700 and could not be made 0700; chat content may be readable by other users on this host",
			"dir", dir, "error", err)
		// The mode is genuinely UNKNOWN on this branch — the open or the stat is
		// what failed — so the breadcrumb must not print a zero FileMode as if it
		// were an observation.
		mode = "unverified"
	}
	// Startup breadcrumb so Loki can answer "did the store come up cleanly
	// after restart?" without having to read the runtime's wiring log. Matches the
	// single-Info-line-on-init pattern used by other vibekit package
	// constructors. The mode logged is the one the FILESYSTEM stored, read back
	// from the handle — not the constant we asked for. The old line reported
	// dirMode unconditionally, so it claimed 0700 on exactly the filesystems
	// where that was false, which is the one case an operator needs this
	// breadcrumb for.
	slog.Info("chat store: opened", "dir", dir, "mode", mode)
	s := &Store{
		dir:       dir,
		tombstone: make(map[vibekit.ChatID]time.Time),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// StoreOption configures optional dependencies on a Store at construction
// time. Use With* functions to create options.
type StoreOption func(*Store)

// WithBroadcaster sets the SSE broadcaster used by the store to emit
// chat_created / chat_updated / chat_deleted / message_* events.
func WithBroadcaster(b broadcaster) StoreOption {
	return func(s *Store) { s.broadcast = b }
}

// WithLive registers the live-chat predicate purging exempts. See
// archive.WithLiveChats.
func WithLive(fn func(chatID vibekit.ChatID) bool) StoreOption {
	return func(s *Store) { s.isLive = fn }
}

// WithOpenTab registers retention's second exemption: a chat with an open TAB is
// never purged, however old. See archive.WithOpenTabs for what that costs (it
// makes retention opt-out for a chat left open forever, which is accepted).
func WithOpenTab(fn func(chatID vibekit.ChatID) bool) StoreOption {
	return func(s *Store) { s.hasOpenTab = fn }
}

// WithOnPurge registers a callback fired after a retention purge removes a chat.
// sessionChain carries every KAS session the chat ran on, captured before the
// chat file was removed, so the purge can reap its own session directories.
func WithOnPurge(fn func(chatID vibekit.ChatID, sessionChain []string)) StoreOption {
	return func(s *Store) { s.onPurge = fn }
}

// --- Path helpers ---

// chatIDPattern reports whether id is a valid chat identifier. Delegates
// to ids.ValidChatID — the single source of truth for chat ID validation.
func chatIDPattern(id vibekit.ChatID) bool {
	return ids.ValidChatID(string(id))
}

// --- Public API ---

// Get returns the full chat at chatID, or false if it does not exist.
func (s *Store) Get(ctx context.Context, chatID vibekit.ChatID) (*vibekit.Chat, bool) {
	if ctx.Err() != nil {
		return nil, false
	}
	m := s.lock(chatID)
	m.Lock()
	defer m.Unlock()
	c, err := s.load(chatID)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Error("chat get", "chat_id", chatID, "error", err)
		}
		return nil, false
	}
	return c, true
}

// Mutate is the single mutation primitive: load → apply → save → broadcast.
// The mutator runs under the per-chat mutex and receives the current chat
// (or a fresh zero-value chat if it does not exist). If mutator returns
// false, the mutation is aborted without side effects. If it returns true,
// the chat is persisted and a chat_updated event is broadcast.
//
// To create a new chat, call Mutate with an ID that does not exist. The
// store pre-fills c.ID and c.CreatedAt before invoking the mutator;
// mutators must not overwrite these — reassigning c.ID would retarget
// the save to a different file under the wrong per-chat mutex, which
// Mutate refuses with an error so the broken caller surfaces loudly.
// c.CreatedAt is authoritative (derived from the original create or
// loaded from disk); Mutate snapshots it before the mutator runs and
// restores it after so a caller that accidentally overwrites it
// (e.g. by assigning a fresh zero value on the auto-create path) can
// not corrupt the sidebar sort order or the broadcast payload.
func (s *Store) Mutate(ctx context.Context, chatID vibekit.ChatID, mutate func(c *vibekit.Chat, exists bool) bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m := s.lock(chatID)
	m.Lock()
	defer m.Unlock()
	c, err := s.load(chatID)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !exists {
		// Delete-during-turn race: a concurrent cmdDeleteChat may have
		// just removed the file while a cmdPrompt or translate_* handler
		// is about to call AppendMessage (or similar) on the stale id.
		// Tombstone check blocks re-creation for a short window so the
		// late write becomes a no-op instead of resurrecting the chat
		// as a ghost row the user has to delete again. Callers that
		// already gate with `if !exists { return false }` are unaffected;
		// the guard kicks in for the narrow "mutate auto-creates"
		// paths (cmdCreateChat, cmdPrompt's user-message append).
		if s.isTombstoned(chatID) {
			slog.Info("chat: refused to resurrect tombstoned id", "chat_id", chatID)
			return nil
		}
		c = &vibekit.Chat{ID: string(chatID), CreatedAt: time.Now().UnixMilli()}
	}
	// Snapshot the authoritative CreatedAt before the mutator runs.
	// Mutators have no legitimate reason to reassign it — the value
	// is either the original create timestamp (new chat) or the
	// on-disk persisted value (existing chat). Restoring it after
	// the mutator runs prevents a caller that blanket-assigns fields
	// from a zero-value struct from silently corrupting the header
	// sort order and every subsequent header broadcast.
	originalCreatedAt := c.CreatedAt
	if !mutate(c, exists) {
		return nil
	}
	// Defensive invariant: the mutator must not reassign c.ID. Doing so
	// would retarget s.save to a different file path serialised by a
	// different per-chat mutex, allowing concurrent writes under
	// mismatched locks. Fail loudly so the broken caller is visible
	// rather than silently corrupting another chat's file.
	if c.ID != string(chatID) {
		slog.Error("chat mutate: mutator reassigned chat id",
			"expected", chatID, "got", c.ID)
		return fmt.Errorf("chat mutate: mutator reassigned id %q → %q", chatID, c.ID)
	}
	c.CreatedAt = originalCreatedAt
	if err := validateChatUTF8(c); err != nil {
		return err
	}
	if err := s.save(chatID, c); err != nil {
		return err
	}
	s.broadcastMutation(ctx, chatID, c, exists)
	slog.Debug("chat mutate", "chat_id", chatID, "existed", exists)
	return nil
}

// validateChatUTF8 returns errInvalidUTF8 if the chat name, the composer draft
// or any message content is not valid UTF-8 — content that would not round-trip
// through the JSON storage format. Extracted from Mutate so the single write
// path stays within the cognitive-complexity ceiling without changing
// behaviour.
func validateChatUTF8(c *vibekit.Chat) error {
	if !utf8.ValidString(c.Name) {
		return errInvalidUTF8
	}
	if !utf8.ValidString(c.Draft) {
		return errInvalidUTF8
	}
	for i := range c.Messages {
		if !utf8.ValidString(c.Messages[i].Content) {
			return errInvalidUTF8
		}
	}
	return nil
}

// broadcastMutation emits the post-save lifecycle event for a successful
// Mutate: chat_created for a freshly created chat (exists == false), or
// chat_updated otherwise. No-op when no broadcaster is wired. The header
// is computed once and reused for whichever event fires.
func (s *Store) broadcastMutation(ctx context.Context, chatID vibekit.ChatID, c *vibekit.Chat, exists bool) {
	if s.broadcast == nil {
		return
	}
	evt := vibekit.EventChatUpdated
	if !exists {
		evt = vibekit.EventChatCreated
	}
	s.broadcast.Broadcast(ctx, vibekit.NewEvent(evt, chatID, c.Header()))
}

// SetDraft persists the chat's unsent composer text. Deliberately not a Mutate
// call: it must leave UpdatedAt alone (the retention purge ages a chat from it)
// and it broadcasts nothing.
//
// Deliberately silent on a missing chat. A chat only becomes a server record on
// its first prompt, so a draft typed into a brand-new chat has nowhere to land
// yet, and creating the file here would put a row in every client's sidebar for
// a conversation nobody has started. The client keeps that draft locally until
// the first prompt creates the record.
//
// The returned state is nil when nothing was written — no such chat, or the
// stored draft already equalled text — and is the chat's WHOLE composer state
// otherwise, so the caller can broadcast draft_changed without reading the file
// a second time. Unspecified when err is non-nil.
func (s *Store) SetDraft(ctx context.Context, chatID vibekit.ChatID, text string) (*vibekit.ComposerState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Defensive: the command boundary already answers 413 above this cap and 400
	// on invalid UTF-8, but the store owns what reaches the file, and a draft
	// that cannot round-trip through JSON would make the chat unloadable.
	if len(text) > vibekit.MaxDraftBytes {
		return nil, errDraftTooLarge
	}
	if !utf8.ValidString(text) {
		return nil, errInvalidUTF8
	}
	return s.setComposer(chatID, "chat draft", func(c *vibekit.Chat) bool {
		if c.Draft == text {
			return false
		}
		c.Draft = text
		return true
	})
}

// SetAttachments persists the paths staged beside the chat's draft, replacing
// whatever was there. The draft's twin in every respect that matters here: no
// Mutate, so UpdatedAt is untouched, and no record means no-op rather than a
// created chat. See SetDraft for both reasons.
//
// An empty or nil slice clears the row, which is what a send and an emptied pill
// row both mean, so it is stored as nil rather than refused.
func (s *Store) SetAttachments(ctx context.Context, chatID vibekit.ChatID, paths []string) (*vibekit.ComposerState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Defensive, for the reason SetDraft's caps are: the command boundary answers
	// 413 above these bounds, and the store owns what reaches the file.
	if len(paths) > vibekit.MaxAttachments {
		return nil, errTooManyAttachments
	}
	for _, p := range paths {
		if p == "" || len(p) > vibekit.MaxAttachmentPathBytes {
			return nil, errBadAttachmentPath
		}
		if !utf8.ValidString(p) {
			return nil, errInvalidUTF8
		}
	}
	next := slices.Clone(paths)
	if len(next) == 0 {
		// nil rather than an empty slice: `omitempty` then keeps the field out of
		// the chat file entirely, so a chat with nothing staged reads the way it
		// did before this field existed.
		next = nil
	}
	return s.setComposer(chatID, "chat attachments", func(c *vibekit.Chat) bool {
		if slices.Equal(c.Attachments, next) {
			return false
		}
		c.Attachments = next
		return true
	})
}

// setComposer is the shared body of the two composer writers: load under the
// chat's own lock, apply, write only when something moved, and report the state
// that landed.
//
// One body rather than two because the part that is easy to get wrong is
// identical and is not the assignment — it is the id check, the no-change
// shortcut and the deliberate absence of a Mutate. `what` names the caller in the
// mismatch log, which is the one line where the two differ.
func (s *Store) setComposer(chatID vibekit.ChatID, what string, apply func(*vibekit.Chat) bool) (*vibekit.ComposerState, error) {
	m := s.lock(chatID)
	m.Lock()
	defer m.Unlock()
	c, err := s.load(chatID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	// Before the field is touched, and before the no-change shortcut below: this
	// chat file claims to be a different chat, so nothing about it can be
	// persisted under this id's lock. Mutate refuses the same disagreement when a
	// mutator causes it; the file is the other way to reach it, and the loud
	// refusal is what stops an autosave for c1 from writing the whole object over
	// c2. Checking here rather than only in writeChat also means a draft that
	// happens to equal what is already stored is reported instead of returning
	// nil through the shortcut, so the corruption surfaces on the first keystroke.
	if c.ID != string(chatID) {
		slog.Error(what+": chat file holds another chat's id",
			"chat_id", chatID, "stored_id", c.ID)
		return nil, errChatIDMismatch(chatID, c.ID)
	}
	if !apply(c) {
		return nil, nil
	}
	if err := s.writeChat(chatID, c); err != nil {
		return nil, err
	}
	state := c.Composer()
	return &state, nil
}

// DeleteFamily and PromoteRewind are GONE with the rewind-branch family. Both
// existed only because a chat could own other chats: DeleteFamily supplied the
// ordering contract that kept a crash from leaving a child pointing at a deleted
// parent, and PromoteRewind cleared a child's parent linkage under one lock so a
// promote could never report success while the relationship was intact. A rewind
// reverts the chat it is in, so no chat has a parent or children and neither
// transition has a subject. Delete is the whole delete path again.

// Delete removes the chat file and broadcasts chat_deleted. Records a tombstone
// first so a concurrent Mutate cannot resurrect the id as a ghost row.
func (s *Store) Delete(ctx context.Context, chatID vibekit.ChatID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m := s.lock(chatID)
	m.Lock()
	path, err := s.pathFor(chatID)
	if err != nil {
		m.Unlock()
		return err
	}
	rmErr := os.Remove(path)
	missing := errors.Is(rmErr, os.ErrNotExist)
	if !missing {
		// Mark tombstone while we still hold the per-chat lock so any
		// racing Mutate attempt has to queue behind us and will then
		// see the tombstone. Only tombstone chats that actually
		// existed — phantom deletes shouldn't block future creates.
		s.markDeleted(chatID)
	}
	m.Unlock()
	if rmErr != nil && !missing {
		return rmErr
	}
	if s.broadcast != nil {
		s.broadcast.Broadcast(ctx, vibekit.NewEvent(vibekit.EventChatDeleted, chatID, vibekit.ChatDeletedPayload{ID: string(chatID)}))
	}
	if missing {
		slog.Info("chat delete: no-op on missing chat", "chat_id", chatID)
	} else {
		// Snapshot tombstone map size outside markDeleted's lock so
		// operators can spot a tombstone-map creep pattern without a
		// dedicated gauge. At steady state this stays at or near zero
		// because opportunistic pruning runs inside markDeleted.
		s.tombMu.Lock()
		tombCount := len(s.tombstone)
		s.tombMu.Unlock()
		slog.Debug("chat delete", "chat_id", chatID, "tombstones", tombCount)
	}
	return nil
}

// AppendMessage is a convenience wrapper around Mutate for the common
// "add one message" case. It broadcasts message_appended in addition to the
// usual chat_updated.
//
// The broadcast fires only after the save succeeds — if the write fails
// clients never see a phantom event referencing content that was never
// persisted.
func (s *Store) AppendMessage(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.Message) error {
	var appended bool
	err := s.Mutate(ctx, chatID, func(c *vibekit.Chat, exists bool) bool {
		if !exists {
			return false
		}
		if msg.Ts == 0 {
			msg.Ts = time.Now().UnixMilli()
		}
		c.Messages = append(c.Messages, *msg)
		appended = true
		return true
	})
	if err != nil || !appended || s.broadcast == nil {
		return err
	}
	s.broadcast.Broadcast(ctx, vibekit.NewEvent(vibekit.EventMessageAppended, chatID, msg))
	slog.Debug("chat append", "chat_id", chatID, "msg_id", msg.ID, "role", msg.Role)
	return nil
}

// UpdateMessage mutates an existing message by ID and broadcasts
// message_updated. Used by tool_call_update to reflect streaming status.
// No-op if the message is not found.
//
// The broadcast fires only after the save succeeds — if the write fails
// clients never see a phantom event referencing content that was never
// persisted.
func (s *Store) UpdateMessage(ctx context.Context, chatID vibekit.ChatID, msgID string, mutate func(*vibekit.Message)) error {
	var updated *vibekit.Message
	err := s.Mutate(ctx, chatID, func(c *vibekit.Chat, exists bool) bool {
		if !exists {
			return false
		}
		for i := range c.Messages {
			if c.Messages[i].ID == msgID {
				mutate(&c.Messages[i])
				updated = &c.Messages[i]
				return true
			}
		}
		return false
	})
	if err != nil || updated == nil || s.broadcast == nil {
		return err
	}
	s.broadcast.Broadcast(ctx, vibekit.NewEvent(vibekit.EventMessageUpdated, chatID, updated))
	slog.Debug("chat update_message", "chat_id", chatID, "msg_id", msgID)
	return nil
}
