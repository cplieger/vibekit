// Package chat implements per-chat persistence: one JSON file per chat under
// <dir>/<chat_id>.json, atomically rewritten on every mutation via
// write-temp-then-rename. The directory listing is the index, and the store is the
// single source of truth for chat state. A chat's ACP session id lives in the chat
// file's header so a container restart can resume via session/load.
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
	"github.com/cplieger/vibekit/internal/subject"
	"github.com/cplieger/vibekit/internal/vibekit"
	"golang.org/x/sync/singleflight"
)

// errInvalidUTF8 marks content that cannot round-trip through JSON, the storage format.
var errInvalidUTF8 = errors.New("chat: content contains invalid UTF-8")

// errDraftTooLarge is returned when a composer draft exceeds vibekit.MaxDraftBytes.
var errDraftTooLarge = errors.New("chat: draft exceeds the size cap")

// The two ways a staged attachment list is refused at the store: more entries than
// vibekit.MaxAttachments, and a path empty, over the byte cap or not UTF-8.
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

// fileMode is the on-disk mode for chat files. The parent dir uses 0o700 because
// chat content may contain secrets the user pasted into prompts.
const (
	fileMode       = 0o600
	dirMode        = 0o700
	chatFileSuffix = ".json"
)

// Store owns the chat directory. Each chat has its own mutex so different chats
// never block each other; same-chat mutations serialize. A short-TTL tombstone set
// closes the delete-during-turn race: without it, an AppendMessage arriving after a
// concurrent Delete would re-create the chat file as a ghost row.
type Store struct {
	broadcast   broadcaster
	versions    *subject.Versions
	epoch       func() string
	listSF      singleflight.Group
	onPurge     func(chatID vibekit.ChatID, sessionChain []string)
	isLive      func(chatID vibekit.ChatID) bool
	hasOpenTab  func(chatID vibekit.ChatID) bool
	turnOpen    func(chatID vibekit.ChatID) vibekit.TurnOpenState
	liveTurn    func(chatID vibekit.ChatID) (vibekit.LiveTurn, bool)
	tombstone   map[vibekit.ChatID]time.Time
	archive     *archive.Service
	locks       sync.Map
	dir         string
	index       searchIndex
	fileCap     chatFileCap
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
		fileCap:   resolveChatFileCap(),
		tombstone: make(map[vibekit.ChatID]time.Time),
		versions:  &subject.Versions{},
	}
	// Options land AFTER the derivation so WithChatFileCap overrides it, and the
	// derivation's own log line still records what the container asked for.
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// StoreOption configures optional dependencies on a Store at construction time.
type StoreOption func(*Store)

// WithBroadcaster sets the SSE broadcaster used by the store to emit
// chat_created / chat_updated / chat_deleted / message_* events.
func WithBroadcaster(b broadcaster) StoreOption {
	return func(s *Store) { s.broadcast = b }
}

// WithVersions makes the store mint its `chat` and `chats` versions into the
// shared registry the digest resolver and the REST envelopes read. Without it
// the store mints into a private registry, which keeps every return honest but
// reaches no resolver.
func WithVersions(v *subject.Versions) StoreOption {
	return func(s *Store) { s.versions = v }
}

// WithEpoch supplies the hub epoch the two chat GET envelopes stamp beside their
// version. Without it the stamps carry no epoch, which a client's version map
// refuses, so composition always wires it.
func WithEpoch(fn func() string) StoreOption {
	return func(s *Store) { s.epoch = fn }
}

// WithLive registers the live-chat predicate purging exempts. See
// archive.WithLiveChats.
func WithLive(fn func(chatID vibekit.ChatID) bool) StoreOption {
	return func(s *Store) { s.isLive = fn }
}

// WithOpenTab registers retention's second exemption: a chat with an open TAB is
// never purged, however old. See archive.WithOpenTabs for what that costs.
func WithOpenTab(fn func(chatID vibekit.ChatID) bool) StoreOption {
	return func(s *Store) { s.hasOpenTab = fn }
}

// WithTurnOpen registers the runtime's turn-in-flight predicate, so this package's
// HTTP surface can STATE whether a chat has a turn open — and whose it is — instead of
// leaving a reader to infer either from an absent carrier. Injected post-construction
// because the agent runtime needs the store, so the store cannot import it.
func WithTurnOpen(fn func(chatID vibekit.ChatID) vibekit.TurnOpenState) StoreOption {
	return func(s *Store) { s.turnOpen = fn }
}

// TurnOpen reports whether chatID has a turn in flight and whose it is, or the zero
// state when no predicate was injected.
//
// NIL-TOLERANT so an unwired Store reads as it did before the predicate existed:
// the record is taken as final. That is the safe direction — a client told "no turn
// is open" derives the outcome the record supports rather than inventing one.
func (s *Store) TurnOpen(chatID vibekit.ChatID) vibekit.TurnOpenState {
	if s.turnOpen == nil {
		return vibekit.TurnOpenState{}
	}
	return s.turnOpen(chatID)
}

// WithLiveTurn registers the runtime's in-flight-turn READER, the content half of what
// WithTurnOpen states. Without it this package's HTTP surface can say a turn is running
// and carry nothing that describes it — and it is the ONE channel for that content, the
// SSE connect carrying `busy_chats` and no turn transcript — so a client that finds a
// chat busy at connect renders the prompt over an empty body.
//
// Injected post-construction for WithTurnOpen's reason: the agent runtime needs the store,
// so the store cannot import it. The signature carries only internal/vibekit types
// deliberately — internal/chat imports no internal/buffer and must not start.
func WithLiveTurn(fn func(chatID vibekit.ChatID) (vibekit.LiveTurn, bool)) StoreOption {
	return func(s *Store) { s.liveTurn = fn }
}

// LiveTurn returns the chat's in-flight turn as accumulated so far, or false when no turn
// is open — or when no reader was injected, which is the same NIL-TOLERANCE TurnOpen
// carries: an unwired Store serves exactly what it served before this field existed, so a
// wiring mistake costs a missing carrier rather than a nil dereference.
func (s *Store) LiveTurn(chatID vibekit.ChatID) (vibekit.LiveTurn, bool) {
	if s.liveTurn == nil {
		return vibekit.LiveTurn{}, false
	}
	return s.liveTurn(chatID)
}

// WithOnPurge registers a callback fired after a retention purge removes a chat.
// sessionChain carries every KAS session the chat ran on, captured before the chat
// file was removed, so the purge can reap its own session directories.
func WithOnPurge(fn func(chatID vibekit.ChatID, sessionChain []string)) StoreOption {
	return func(s *Store) { s.onPurge = fn }
}

// chatIDPattern reports whether id is a valid chat identifier.
func chatIDPattern(id vibekit.ChatID) bool {
	return ids.ValidChatID(string(id))
}

// Get returns the full chat at chatID, or false if it does not exist.
func (s *Store) Get(ctx context.Context, chatID vibekit.ChatID) (*vibekit.Chat, bool) {
	c, _, ok := s.GetStamped(ctx, chatID)
	return c, ok
}

// GetStamped is Get plus the `chat` stamp the REST envelope carries: the current
// version read under the same per-chat mutex the load holds, so a Mutate or a
// composer write cannot land between the record and the version that vouches
// for it. A chat never mutated this process stamps subject.Unminted.
func (s *Store) GetStamped(ctx context.Context, chatID vibekit.ChatID) (*vibekit.Chat, *vibekit.SubjectStamp, bool) {
	if ctx.Err() != nil {
		return nil, nil, false
	}
	m := s.lock(chatID)
	m.Lock()
	defer m.Unlock()
	c, err := s.load(chatID)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Error("chat get", "chat_id", chatID, "error", err)
		}
		return nil, nil, false
	}
	version, _ := s.versions.Current(subject.KindChat, string(chatID))
	return c, s.restStamp(subject.KindChat, string(chatID), version), true
}

// restStamp builds a REST envelope's stamp: the version with the hub epoch.
func (s *Store) restStamp(kind subject.Kind, ref, version string) *vibekit.SubjectStamp {
	stamp := vibekit.NewSubjectStamp(string(kind), ref, version)
	if s.epoch != nil {
		stamp.Epoch = s.epoch()
	}
	return stamp
}

// Mutate is the single mutation primitive: load → apply → save → broadcast. The
// mutator runs under the per-chat mutex on the current chat, or on a fresh
// zero-value chat when it does not exist; returning false aborts without side
// effects. A write to a recently deleted id is refused with ErrTombstoned.
//
// A mutator must not overwrite c.ID — that retargets the save to another file under
// the wrong per-chat mutex, so Mutate refuses it. c.CreatedAt is snapshotted and
// restored, so a zero-value overwrite cannot corrupt the sidebar sort order.
//
// The returned version is the `chat:<id>` version this save minted, bumped under
// the per-chat mutex so the transcript frame the caller broadcasts next can carry
// it. A mutator that declines returns "" and moves no counter.
func (s *Store) Mutate(ctx context.Context, chatID vibekit.ChatID, mutate func(c *vibekit.Chat, exists bool) bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	m := s.lock(chatID)
	m.Lock()
	defer m.Unlock()
	c, err := s.load(chatID)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if !exists {
		// Delete-during-turn race: a concurrent Delete may have removed the file
		// while a late AppendMessage is about to resurrect it as a ghost row. The
		// refusal is NAMED rather than reported as success, because a caller that
		// cannot tell it from a persisted write spawns a bridge and spends credits
		// for output discarded at persist.
		if s.isTombstoned(chatID) {
			slog.Info("chat: refused to resurrect tombstoned id", "chat_id", chatID)
			return "", ErrTombstoned
		}
		c = &vibekit.Chat{ID: string(chatID), CreatedAt: time.Now().UnixMilli()}
	}
	originalCreatedAt := c.CreatedAt
	if !mutate(c, exists) {
		return "", nil
	}
	// A reassigned id would let s.save write under a mismatched per-chat mutex.
	if c.ID != string(chatID) {
		slog.Error("chat mutate: mutator reassigned chat id",
			"expected", chatID, "got", c.ID)
		return "", fmt.Errorf("chat mutate: mutator reassigned id %q → %q", chatID, c.ID)
	}
	c.CreatedAt = originalCreatedAt
	if err := validateChatUTF8(c); err != nil {
		return "", err
	}
	if err := s.save(chatID, c); err != nil {
		return "", err
	}
	version := s.versions.BumpCounter(subject.KindChat, string(chatID))
	s.broadcastMutation(ctx, chatID, c, exists)
	slog.Debug("chat mutate", "chat_id", chatID, "existed", exists)
	return version, nil
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

// broadcastMutation mints the `chats` version for a successful Mutate and emits the
// post-save lifecycle event stamped with it: chat_created for a freshly created
// chat, chat_updated otherwise. The header list is a live projection of every
// saved Mutate (title, updated_at and sort order move on each), which is why every
// save bumps `chats` and not only a create. The bump happens with or without a
// broadcaster, so a digest never reads unchanged for a list that moved.
func (s *Store) broadcastMutation(ctx context.Context, chatID vibekit.ChatID, c *vibekit.Chat, exists bool) {
	chatsVersion := s.versions.BumpCounter(subject.KindChats, "")
	if s.broadcast == nil {
		return
	}
	evt := vibekit.EventChatUpdated
	if !exists {
		evt = vibekit.EventChatCreated
	}
	frame := vibekit.NewEvent(evt, chatID, c.Header())
	frame.Subject = vibekit.NewSubjectStamp(string(subject.KindChats), "", chatsVersion)
	s.broadcast.Broadcast(ctx, frame)
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
	// The composer is part of the `chat` projection: a draft typed on one device
	// is a change every other device must see, so the write bumps `chat` even
	// though it bypasses save. The version rides the returned state, so the
	// broadcaster's draft_changed stamp comes from this critical section.
	state.Version = s.versions.BumpCounter(subject.KindChat, string(chatID))
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
	chatsVersion, rmErr := s.Remove(chatID)
	missing := errors.Is(rmErr, os.ErrNotExist)
	m.Unlock()
	if rmErr != nil && !missing {
		return rmErr
	}
	if s.broadcast != nil {
		frame := vibekit.NewEvent(vibekit.EventChatDeleted, chatID, vibekit.ChatDeletedPayload{ID: string(chatID)})
		frame.Subject = vibekit.NewSubjectStamp(string(subject.KindChats), "", chatsVersion)
		s.broadcast.Broadcast(ctx, frame)
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
	version, err := s.Mutate(ctx, chatID, func(c *vibekit.Chat, exists bool) bool {
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
	frame := vibekit.NewEvent(vibekit.EventMessageAppended, chatID, msg)
	frame.Subject = vibekit.NewSubjectStamp(string(subject.KindChat), string(chatID), version)
	s.broadcast.Broadcast(ctx, frame)
	slog.Debug("chat append", "chat_id", chatID, "msg_id", msg.ID, "role", msg.Role)
	return nil
}

// UpsertTurnPlan records the agent's plan for the turn in flight: it overwrites this
// turn's existing plan row, or appends msg when there is none, broadcasting
// message_updated or message_appended to match.
//
// ONE row per turn, because the wire resends the WHOLE entries array on every update, so
// an append per frame persists N snapshots of one plan. "This turn" is the tail up to the
// first PROMPT, matching projectTurns' rule that a prompt opens a turn while a steer joins
// the one running. Ts is NOT restamped: it marks where the plan entered the chat.
func (s *Store) UpsertTurnPlan(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.Message) error {
	var updated *vibekit.Message
	var appended bool
	version, err := s.Mutate(ctx, chatID, func(c *vibekit.Chat, exists bool) bool {
		if !exists {
			return false
		}
		for i := len(c.Messages) - 1; i >= 0; i-- {
			if c.Messages[i].IsPrompt() {
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
	stamp := vibekit.NewSubjectStamp(string(subject.KindChat), string(chatID), version)
	switch {
	case updated != nil:
		frame := vibekit.NewEvent(vibekit.EventMessageUpdated, chatID, updated)
		frame.Subject = stamp
		s.broadcast.Broadcast(ctx, frame)
		slog.Debug("chat plan update", "chat_id", chatID, "msg_id", updated.ID, "entries", len(updated.Plan))
	case appended:
		frame := vibekit.NewEvent(vibekit.EventMessageAppended, chatID, msg)
		frame.Subject = stamp
		s.broadcast.Broadcast(ctx, frame)
		slog.Debug("chat plan append", "chat_id", chatID, "msg_id", msg.ID, "entries", len(msg.Plan))
	}
	return nil
}

// UpdateMessage mutates an existing message by ID and broadcasts message_updated
// after the save succeeds. No-op when the message is not found.
func (s *Store) UpdateMessage(ctx context.Context, chatID vibekit.ChatID, msgID string, mutate func(*vibekit.Message)) error {
	var updated *vibekit.Message
	version, err := s.Mutate(ctx, chatID, func(c *vibekit.Chat, exists bool) bool {
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
	frame := vibekit.NewEvent(vibekit.EventMessageUpdated, chatID, updated)
	frame.Subject = vibekit.NewSubjectStamp(string(subject.KindChat), string(chatID), version)
	s.broadcast.Broadcast(ctx, frame)
	slog.Debug("chat update_message", "chat_id", chatID, "msg_id", msgID)
	return nil
}
