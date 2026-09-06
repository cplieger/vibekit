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

// maxChatFileBytes caps a single chat file loaded by `load`, so a corrupted or
// runaway file cannot OOM the process via List() walking every chat.
const maxChatFileBytes = 32 * 1024 * 1024 // 32 MiB

// Store owns the chat directory. Each chat has its own mutex so different
// chats never block each other; same-chat mutations serialize.
//
// A short-TTL tombstone set guards the delete-during-turn race: a late
// AppendMessage on an id another tab just deleted would otherwise re-create the
// chat file as a ghost row, so Mutate refuses to create for a tombstoned id.
type Store struct {
	broadcast   broadcaster
	listSF      singleflight.Group
	onPurge     func(chatID vibekit.ChatID, sessionChain []string)
	isLive      func(chatID vibekit.ChatID) bool
	hasOpenTab  func(chatID vibekit.ChatID) bool
	turnOpen    func(chatID vibekit.ChatID) bool
	tombstone   map[vibekit.ChatID]time.Time
	archive     *archive.Service
	locks       sync.Map
	dir         string
	archiveOnce sync.Once
	tombMu      sync.Mutex
}

// tombstoneTTL is how long a deleted chat id blocks re-creation via Mutate: longer
// than any real prompt roundtrip, short enough not to blacklist a recycled id.
const tombstoneTTL = 10 * time.Minute

// NewStore opens (or creates) the chat directory at dir. Returns an error if the
// directory cannot be created — callers must fail startup rather than return a
// store whose every op fails. A mode that cannot be ENFORCED is not one of those
// errors: it warns and continues, because the container coming up is the
// operator's only way in to repair /config (invariant 6).
func NewStore(dir string, opts ...StoreOption) (*Store, error) {
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return nil, fmt.Errorf("chat store: mkdir %s: %w", dir, err)
	}
	// MkdirAll applies its mode only on CREATION, and even then the mode is a
	// REQUEST: a setgid parent adds its bit, and an inheritable group-write ACL
	// stores 0770 for a 0o700 mkdir. EnforceDir re-stats the descriptor it
	// chmod'ed, so the mode logged below is a fact, and it refuses a symlink at
	// the name instead of chmod'ing through it.
	stored, err := filemode.EnforceDir(dir, dirMode)
	mode := stored.String()
	if err != nil {
		slog.Warn("chat store: chat dir is not 0700 and could not be made 0700; chat content may be readable by other users on this host",
			"dir", dir, "error", err)
		// The mode is genuinely UNKNOWN here (the open or the stat failed), so the
		// breadcrumb must not print a zero FileMode as an observation.
		mode = "unverified"
	}
	// The mode logged is the one the FILESYSTEM stored, read back from the handle,
	// not the constant asked for.
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

// WithTurnOpen registers the runtime's turn-in-flight predicate, so this package's
// HTTP surface can STATE whether a chat has a turn open instead of leaving a reader
// to infer it from an absent carrier. Injected post-construction because the agent
// runtime needs the store, so the store cannot import it.
func WithTurnOpen(fn func(chatID vibekit.ChatID) bool) StoreOption {
	return func(s *Store) { s.turnOpen = fn }
}

// TurnOpen reports whether chatID has a turn in flight, or false when no predicate
// was injected.
//
// NIL-TOLERANT so an unwired Store reads as it did before the predicate existed:
// the record is taken as final. That is the safe direction — a client told "no turn
// is open" derives the outcome the record supports rather than inventing one.
func (s *Store) TurnOpen(chatID vibekit.ChatID) bool {
	if s.turnOpen == nil {
		return false
	}
	return s.turnOpen(chatID)
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

// Mutate is the single mutation primitive: load → apply → save → broadcast. The
// mutator runs under the per-chat mutex on the current chat, or on a fresh
// zero-value chat when it does not exist; returning false aborts without side
// effects. A write to a recently deleted id is refused with ErrTombstoned.
//
// A mutator must not overwrite c.ID — that retargets the save to another file under
// the wrong per-chat mutex, so Mutate refuses it. c.CreatedAt is snapshotted and
// restored, so a zero-value overwrite cannot corrupt the sidebar sort order.
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
		// Delete-during-turn race: a concurrent Delete may have removed the file
		// while a late AppendMessage is about to resurrect it as a ghost row. The
		// refusal is NAMED rather than reported as success, because a caller that
		// cannot tell it from a persisted write spawns a bridge and spends credits
		// for output discarded at persist.
		if s.isTombstoned(chatID) {
			slog.Info("chat: refused to resurrect tombstoned id", "chat_id", chatID)
			return ErrTombstoned
		}
		c = &vibekit.Chat{ID: string(chatID), CreatedAt: time.Now().UnixMilli()}
	}
	originalCreatedAt := c.CreatedAt
	if !mutate(c, exists) {
		return nil
	}
	// A reassigned id would let s.save write under a mismatched per-chat mutex.
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

// validateChatUTF8 returns errInvalidUTF8 when the chat name, the composer draft or
// any message content is not valid UTF-8 — content that would not round-trip through
// the JSON storage format.
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

// broadcastMutation emits the post-save lifecycle event for a successful Mutate:
// chat_created for a freshly created chat, chat_updated otherwise. No-op when no
// broadcaster is wired.
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
// call: it must leave UpdatedAt alone (the retention purge ages a chat from it) and
// it broadcasts nothing. Silent on a missing chat, because a chat only becomes a
// server record on its first prompt.
//
// The returned state is nil when nothing was written and the chat's WHOLE composer
// state otherwise, so the caller can broadcast draft_changed without a second read.
func (s *Store) SetDraft(ctx context.Context, chatID vibekit.ChatID, text string) (*vibekit.ComposerState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// The command boundary already refuses both, but the store owns what reaches the
	// file, and a draft that cannot round-trip through JSON is unloadable.
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
// whatever was there. The draft's twin: no Mutate, so UpdatedAt is untouched, and no
// record means no-op rather than a created chat. An empty slice clears the row.
func (s *Store) SetAttachments(ctx context.Context, chatID vibekit.ChatID, paths []string) (*vibekit.ComposerState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// The store owns what reaches the file; see SetDraft.
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
		// nil rather than empty: `omitempty` keeps the field out of the chat file.
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

// setComposer is the shared body of the two composer writers: load under the chat's
// own lock, apply, write only when something moved, and report the state that
// landed. `what` names the caller in the mismatch log.
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
	// This file claims to be a different chat, so nothing about it can be persisted
	// under this id's lock.
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

// Delete removes the chat file and broadcasts chat_deleted. Records a
// tombstone first so a concurrent Mutate cannot resurrect the id.
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
		// Marked while the per-chat lock is still held, so a racing Mutate has to
		// queue behind it. Only a chat that actually existed is tombstoned.
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
		// Read outside markDeleted's lock; a creep pattern needs no dedicated gauge.
		s.tombMu.Lock()
		tombCount := len(s.tombstone)
		s.tombMu.Unlock()
		slog.Debug("chat delete", "chat_id", chatID, "tombstones", tombCount)
	}
	return nil
}

// AppendMessage adds one message through Mutate, broadcasting message_appended in
// addition to the usual chat_updated — after the save succeeds, so a failed write
// emits no phantom event referencing content that was never persisted.
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

// UpsertTurnPlan records the agent's plan for the turn in flight: it overwrites this
// turn's existing plan row, or appends msg when there is none, broadcasting
// message_updated or message_appended to match.
//
// ONE row per turn, because ACP resends the WHOLE entries array on every update, so
// an append per frame persists N snapshots of one plan. "This turn" is the tail up
// to the first user message, matching projectTurns' rule that a user message opens
// one. Ts is NOT restamped on update: it marks where the plan entered the chat.
func (s *Store) UpsertTurnPlan(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.Message) error {
	var updated *vibekit.Message
	var appended bool
	err := s.Mutate(ctx, chatID, func(c *vibekit.Chat, exists bool) bool {
		if !exists {
			return false
		}
		for i := len(c.Messages) - 1; i >= 0; i-- {
			if c.Messages[i].Role == vibekit.RoleUser {
				break // turn boundary: this turn carries no plan row yet
			}
			if len(c.Messages[i].Plan) == 0 {
				continue
			}
			c.Messages[i].Plan = msg.Plan
			updated = &c.Messages[i]
			return true
		}
		if msg.Ts == 0 {
			msg.Ts = time.Now().UnixMilli()
		}
		c.Messages = append(c.Messages, *msg)
		appended = true
		return true
	})
	if err != nil || s.broadcast == nil {
		return err
	}
	switch {
	case updated != nil:
		s.broadcast.Broadcast(ctx, vibekit.NewEvent(vibekit.EventMessageUpdated, chatID, updated))
		slog.Debug("chat plan update", "chat_id", chatID, "msg_id", updated.ID, "entries", len(updated.Plan))
	case appended:
		s.broadcast.Broadcast(ctx, vibekit.NewEvent(vibekit.EventMessageAppended, chatID, msg))
		slog.Debug("chat plan append", "chat_id", chatID, "msg_id", msg.ID, "entries", len(msg.Plan))
	}
	return nil
}

// UpdateMessage mutates an existing message by ID and broadcasts message_updated
// after the save succeeds. No-op when the message is not found.
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
