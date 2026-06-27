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
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/chat/archive"
	"golang.org/x/sync/singleflight"
)

// errInvalidUTF8 is returned when a chat mutation produces content that
// cannot round-trip through JSON (the storage format).
var errInvalidUTF8 = errors.New("chat: content contains invalid UTF-8")

// Compile-time interface assertion.
var _ api.ChatStore = (*Store)(nil)

// Compile-time assertion: Store satisfies archive.StoreAccess.
var _ archive.StoreAccess = (*Store)(nil)

// fileMode is the on-disk mode for chat files. The parent dir uses 0o700
// because chat content may contain secrets the user pasted into prompts.
const (
	fileMode        = 0o600
	dirMode         = 0o700
	chatFileSuffix  = ".json"
	planDraftSuffix = ".plan.md"
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
	broadcast        api.Broadcaster
	listSF           singleflight.Group
	onArchive        func(chatID api.ChatID)
	onPurge          func(chatID api.ChatID)
	oldestCheckpoint func(ctx context.Context, chatID api.ChatID) string
	tombstone        map[api.ChatID]time.Time
	archive          *archive.Service
	locks            sync.Map
	dir              string
	archiveOnce      sync.Once
	tombMu           sync.Mutex
}

// tombstoneTTL is how long a deleted chat id blocks re-creation via
// Mutate. Comfortably longer than any real prompt roundtrip (kiro-cli
// turns can run minutes) but short enough that new chats with ids
// recycled by the client (e.g. same timestamp after a clock jump)
// aren't permanently blacklisted. 10 minutes matches our idempotency
// cache TTL (2x safety margin).
const tombstoneTTL = 10 * time.Minute

// NewStore opens (or creates) the chat directory at dir. Returns an error
// if the directory cannot be created or its permissions cannot be
// enforced — callers must fail startup rather than return a store whose
// every op will fail.
func NewStore(dir string, opts ...StoreOption) (*Store, error) {
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return nil, fmt.Errorf("chat store: mkdir %s: %w", dir, err)
	}
	// MkdirAll only sets perms on creation; enforce on a pre-existing dir
	// in case a prior process or a user-mounted config volume left wider
	// perms. A read-only bind mount may reject chmod; log and continue.
	if err := os.Chmod(dir, dirMode); err != nil {
		slog.Warn("chat store: chmod", "dir", dir, "error", err)
	}
	// Startup breadcrumb so Loki can answer "did the store come up
	// cleanly after restart?" without having to read the hub's wiring
	// log. Matches the single-Info-line-on-init pattern used by other
	// vibekit package constructors.
	slog.Info("chat store: opened", "dir", dir, "mode", os.FileMode(dirMode).String())
	s := &Store{
		dir:       dir,
		tombstone: make(map[api.ChatID]time.Time),
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
func WithBroadcaster(b api.Broadcaster) StoreOption {
	return func(s *Store) { s.broadcast = b }
}

// WithOnArchive registers a callback fired after a chat is archived.
func WithOnArchive(fn func(chatID api.ChatID)) StoreOption {
	return func(s *Store) { s.onArchive = fn }
}

// WithOldestCheckpointFn wires the lookup used to populate
// ChatHeader.OldestCheckpointTag.
func WithOldestCheckpointFn(fn func(ctx context.Context, chatID api.ChatID) string) StoreOption {
	return func(s *Store) { s.oldestCheckpoint = fn }
}

// WithOnPurge registers a callback fired after an archived chat is purged.
func WithOnPurge(fn func(chatID api.ChatID)) StoreOption {
	return func(s *Store) { s.onPurge = fn }
}

// SetBroadcaster implements api.ChatStore. Prefer WithBroadcaster at
// construction time; this exists for interface satisfaction and test fakes.
func (s *Store) SetBroadcaster(b api.Broadcaster) { s.broadcast = b }

// --- Path helpers ---

// chatIDPattern reports whether id is a valid chat identifier. Delegates
// to api.ValidChatID — the single source of truth for chat ID validation.
// Prefer api.ParseChatID when a typed ChatID is needed downstream.
func chatIDPattern(id api.ChatID) bool {
	return api.ValidChatID(string(id))
}

// --- Public API ---

// Get returns the full chat at chatID, or false if it does not exist.
func (s *Store) Get(ctx context.Context, chatID api.ChatID) (*api.Chat, bool) {
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
func (s *Store) Mutate(ctx context.Context, chatID api.ChatID, mutate func(c *api.Chat, exists bool) bool) error {
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
		c = &api.Chat{ID: string(chatID), CreatedAt: time.Now().UnixMilli()}
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
	if err := s.save(c); err != nil {
		return err
	}
	s.broadcastMutation(ctx, chatID, c, exists)
	slog.Debug("chat mutate", "chat_id", chatID, "existed", exists)
	return nil
}

// validateChatUTF8 returns errInvalidUTF8 if the chat name or any message
// content is not valid UTF-8 — content that would not round-trip through
// the JSON storage format. Extracted from Mutate so the single write path
// stays within the cognitive-complexity ceiling without changing behaviour.
func validateChatUTF8(c *api.Chat) error {
	if !utf8.ValidString(c.Name) {
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
func (s *Store) broadcastMutation(ctx context.Context, chatID api.ChatID, c *api.Chat, exists bool) {
	if s.broadcast == nil {
		return
	}
	evt := api.EventChatUpdated
	if !exists {
		evt = api.EventChatCreated
	}
	s.broadcast.Broadcast(ctx, api.NewEvent(evt, chatID, s.header(ctx, c)))
}

// Delete removes the chat file and broadcasts chat_deleted. No-op if the
// chat does not exist. This is the only function that removes chat data.
// After the file is gone we mark the id tombstoned so any in-flight
// handler racing with the delete can't re-create it via Mutate — see
// markDeleted for the race the tombstone guards against.
//
// Broadcast fires even when the chat file never existed so a stale DELETE
// from a second device still propagates the UI update. We skip the
// tombstone in that case — tombstoning a never-existed id would block a
// legitimate new chat using the same id for 10 minutes.
func (s *Store) Delete(ctx context.Context, chatID api.ChatID) error {
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
	// Best-effort cleanup of any plan draft for this chat. ENOENT is the
	// common case (most chats have no draft); anything else is logged so
	// orphan drafts surface in monitoring rather than silently outliving
	// the chat they belonged to (e.g. if the volume remounts read-only
	// between the chat remove and the draft remove). Inline the path
	// join: pathFor already accepted chatID, so planDraftPathFor would
	// re-run the same chatIDPattern validation and rebuild the same
	// filepath.Join — wasted work. Matches the inline-join pattern
	// already used in SetPlanDraft.
	draftPath := filepath.Join(s.dir, string(chatID)+planDraftSuffix)
	if rmDraftErr := os.Remove(draftPath); rmDraftErr != nil && !errors.Is(rmDraftErr, os.ErrNotExist) {
		slog.Warn("chat delete: plan-draft removal failed",
			"chat_id", chatID, "error", rmDraftErr)
	}
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
		s.broadcast.Broadcast(ctx, api.NewEvent(api.EventChatDeleted, chatID, api.ChatDeletedPayload{ID: string(chatID)}))
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
func (s *Store) AppendMessage(ctx context.Context, chatID api.ChatID, msg *api.Message) error {
	var appended bool
	err := s.Mutate(ctx, chatID, func(c *api.Chat, exists bool) bool {
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
	s.broadcast.Broadcast(ctx, api.NewEvent(api.EventMessageAppended, chatID, msg))
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
func (s *Store) UpdateMessage(ctx context.Context, chatID api.ChatID, msgID string, mutate func(*api.Message)) error {
	var updated *api.Message
	err := s.Mutate(ctx, chatID, func(c *api.Chat, exists bool) bool {
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
	s.broadcast.Broadcast(ctx, api.NewEvent(api.EventMessageUpdated, chatID, updated))
	slog.Debug("chat update_message", "chat_id", chatID, "msg_id", msgID)
	return nil
}
