// Package archive implements the chat archive lifecycle: move to archive,
// list, restore, update summary, delete, and age-based purge. Separated
// from the core chat store to give the archive subsystem its own package
// boundary and make the purge scheduler independently testable.
package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sync/singleflight"

	"vibekit/internal/api"
	"vibekit/internal/fileutil"
)

// Subdir is the subdirectory name under the chats directory where
// archived chats are stored. Exported so composition-layer code can
// reference it without hardcoding the literal.
const Subdir = "archive"

const (
	chatFileSuffix  = ".json"
	planDraftSuffix = ".plan.md"
	dirMode         = 0o700
	fileMode        = 0o600
)

// maxChatFileBytes caps the size of a single chat file loaded by the
// archive reader. Matches the core store's cap.
const maxChatFileBytes = 32 * 1024 * 1024 // 32 MiB

// StoreAccess is the narrow interface the archive subsystem requires
// from the chat store. Keeps the dependency minimal and testable.
type StoreAccess interface {
	// Lock returns the per-chat mutex for serialization.
	Lock(chatID api.ChatID) *sync.Mutex
	// Dir returns the store's base directory.
	Dir() string
	// PathFor returns the path to the active chat file.
	PathFor(chatID api.ChatID) (string, error)
	// Load reads a chat from the active directory.
	Load(chatID api.ChatID) (*api.Chat, error)
	// Header builds a ChatHeader from a Chat.
	Header(ctx context.Context, c *api.Chat) api.ChatHeader
	// MarkDeleted records a tombstone for the chat ID.
	MarkDeleted(chatID api.ChatID)
	// ClearTombstone removes the tombstone for a restored chat.
	ClearTombstone(chatID api.ChatID)
	// Broadcast returns the broadcaster (may be nil).
	Broadcast() api.Broadcaster
	// OldestCheckpoint returns the checkpoint lookup function (may be nil).
	OldestCheckpoint() func(ctx context.Context, chatID api.ChatID) string
}

// Service implements the archive lifecycle operations.
type Service struct {
	store        StoreAccess
	onArchive    func(chatID api.ChatID)
	onPurge      func(chatID api.ChatID)
	listSF       singleflight.Group
}

// New creates an archive Service backed by the given StoreAccess.
func New(store StoreAccess, opts ...Option) *Service {
	s := &Service{store: store}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Option configures the archive Service.
type Option func(*Service)

// WithOnArchive registers a callback fired after a chat is archived.
func WithOnArchive(fn func(chatID api.ChatID)) Option {
	return func(s *Service) { s.onArchive = fn }
}

// WithOnPurge registers a callback fired after an archived chat is purged.
func WithOnPurge(fn func(chatID api.ChatID)) Option {
	return func(s *Service) { s.onPurge = fn }
}

// archivePath returns the path to the archive subdirectory.
func (s *Service) archivePath() string {
	return filepath.Join(s.store.Dir(), Subdir)
}

// archivePathFor validates chatID and returns the path to the archived chat file.
func (s *Service) archivePathFor(chatID api.ChatID) (string, error) {
	if !api.ValidChatID(string(chatID)) {
		return "", fmt.Errorf("invalid chat id: %q", chatID)
	}
	return filepath.Join(s.store.Dir(), Subdir, string(chatID)+chatFileSuffix), nil
}

// Archive moves a chat from the active directory to the archive
// subdirectory. Takes the per-chat mutex so a concurrent Mutate /
// AppendMessage can't race the rename. Broadcasts chat_deleted so all
// connected clients see the entry disappear without a manual refresh.
func (s *Service) Archive(ctx context.Context, chatID api.ChatID) error {
	if !api.ValidChatID(string(chatID)) {
		return fmt.Errorf("invalid chat id: %q", chatID)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m := s.store.Lock(chatID)
	m.Lock()
	srcPath, err := s.store.PathFor(chatID)
	if err != nil {
		m.Unlock()
		return err
	}
	archiveDir := s.archivePath()
	if err := os.MkdirAll(archiveDir, dirMode); err != nil {
		m.Unlock()
		return err
	}
	dstPath := filepath.Join(archiveDir, string(chatID)+chatFileSuffix)
	if err := os.Rename(srcPath, dstPath); err != nil { //#nosec G703 -- paths built from validated chat ID
		m.Unlock()
		return err
	}
	// Also archive the plan draft if it exists.
	draftSrc := filepath.Join(s.store.Dir(), string(chatID)+planDraftSuffix)
	draftDst := filepath.Join(archiveDir, string(chatID)+planDraftSuffix)
	if err := os.Rename(draftSrc, draftDst); err != nil && !errors.Is(err, os.ErrNotExist) { //#nosec G703 -- paths built from validated chat ID
		slog.Warn("chat archive: plan-draft move failed",
			"chat_id", chatID, "error", err)
	}
	s.store.MarkDeleted(chatID)
	m.Unlock()
	if b := s.store.Broadcast(); b != nil {
		b.Broadcast(ctx, api.NewEvent(api.EventChatDeleted, chatID, api.ChatDeletedPayload{ID: string(chatID)}))
	}
	slog.Info("chat archived", "chat_id", chatID)
	if s.onArchive != nil {
		s.onArchive(chatID)
	}
	return nil
}

// ListArchived returns headers for all archived chats, sorted by
// UpdatedAt desc. Files that fail to read or parse are logged and
// skipped. Always returns a non-nil slice.
func (s *Service) ListArchived(ctx context.Context) []api.ChatHeader {
	headers := sfDo(&s.listSF, "list", func() []api.ChatHeader {
		return s.listArchivedOnce(ctx)
	})
	if headers == nil {
		return []api.ChatHeader{}
	}
	return headers
}

func (s *Service) listArchivedOnce(ctx context.Context) []api.ChatHeader {
	archiveDir := s.archivePath()
	entries, err := os.ReadDir(archiveDir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Error("chat list_archived", "dir", archiveDir, "error", err)
		}
		return []api.ChatHeader{}
	}
	var valid []chatEntry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), chatFileSuffix) {
			continue
		}
		id := strings.TrimSuffix(e.Name(), chatFileSuffix)
		if !api.ValidChatID(id) {
			slog.Debug("chat list_archived: skipped non-chat file",
				"name", e.Name(), "reason", "invalid chat id pattern")
			continue
		}
		valid = append(valid, chatEntry{id: id, path: filepath.Join(archiveDir, e.Name())})
	}
	if len(valid) == 0 {
		return []api.ChatHeader{}
	}

	headers := readHeadersParallel(ctx, valid, "archived chat", s.store.OldestCheckpoint())
	sort.Slice(headers, func(i, j int) bool {
		return headers[i].UpdatedAt > headers[j].UpdatedAt
	})
	return headers
}

// RestoreArchived moves a chat from the archive back to the active
// directory. Refuses with error if an active chat already exists at
// the target id.
func (s *Service) RestoreArchived(ctx context.Context, chatID api.ChatID) error {
	if !api.ValidChatID(string(chatID)) {
		return fmt.Errorf("invalid chat id: %q", chatID)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m := s.store.Lock(chatID)
	m.Lock()
	archiveDir := s.archivePath()
	srcPath := filepath.Join(archiveDir, string(chatID)+chatFileSuffix)
	dstPath := filepath.Join(s.store.Dir(), string(chatID)+chatFileSuffix)
	// Collision guard: refuse to overwrite an active chat file.
	if _, err := os.Stat(dstPath); err == nil {
		m.Unlock()
		slog.Warn("chat restore: refused, id is in use", "chat_id", chatID)
		return &IDInUseError{ID: string(chatID)}
	} else if !errors.Is(err, os.ErrNotExist) {
		m.Unlock()
		return err
	}
	if err := os.Rename(srcPath, dstPath); err != nil { //#nosec G703 -- paths built from validated chat ID
		m.Unlock()
		return err
	}
	// Also restore the plan draft if it exists.
	draftSrc := filepath.Join(archiveDir, string(chatID)+planDraftSuffix)
	draftDst := filepath.Join(s.store.Dir(), string(chatID)+planDraftSuffix)
	if err := os.Rename(draftSrc, draftDst); err != nil && !errors.Is(err, os.ErrNotExist) { //#nosec G703 -- paths built from validated chat ID
		slog.Warn("chat restore: plan-draft move failed",
			"chat_id", chatID, "error", err)
	}
	s.store.ClearTombstone(chatID)
	c, loadErr := s.store.Load(chatID)
	m.Unlock()
	slog.Info("chat restored from archive", "chat_id", chatID)
	if loadErr == nil {
		if b := s.store.Broadcast(); b != nil {
			b.Broadcast(ctx, api.NewEvent(api.EventChatCreated, chatID, s.store.Header(ctx, c)))
		}
	}
	return nil
}

// LoadArchived reads the archived chat and returns the parsed *api.Chat.
func (s *Service) LoadArchived(ctx context.Context, chatID api.ChatID) (*api.Chat, error) {
	if !api.ValidChatID(string(chatID)) {
		return nil, fmt.Errorf("invalid chat id: %q", chatID)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m := s.store.Lock(chatID)
	m.Lock()
	defer m.Unlock()
	return s.loadArchived(chatID)
}

// UpdateArchivedSummary rewrites an archived chat's Summary field in
// place. Used by the hub to populate the one-line summary produced by
// the utility bridge after archiving.
func (s *Service) UpdateArchivedSummary(ctx context.Context, chatID api.ChatID, summary string) error {
	path, err := s.archivePathFor(chatID)
	if err != nil {
		return err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	m := s.store.Lock(chatID)
	m.Lock()
	defer m.Unlock()
	c, err := s.loadArchived(chatID)
	if err != nil {
		return fmt.Errorf("load archived chat: %w", err)
	}
	c.Summary = summary
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.SaveBytes(path, data, fileMode)
}

// DeleteArchived permanently removes a single archived chat file and its
// plan draft. Fires onPurge so checkpoint data is cleaned up.
func (s *Service) DeleteArchived(ctx context.Context, chatID api.ChatID) error {
	chatPath, err := s.archivePathFor(chatID)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m := s.store.Lock(chatID)
	m.Lock()
	if err := os.Remove(chatPath); err != nil { //nolint:gosec // G703: path within workspace root
		m.Unlock()
		return err
	}
	archiveDir := s.archivePath()
	draftPath := filepath.Join(archiveDir, string(chatID)+planDraftSuffix)
	if err := os.Remove(draftPath); err != nil && !errors.Is(err, os.ErrNotExist) { //nolint:gosec // G703: path within workspace root
		slog.Warn("chat delete_archived: remove plan-draft",
			"chat_id", chatID, "error", err)
	}
	m.Unlock()
	if s.onPurge != nil {
		s.onPurge(chatID)
	}
	return nil
}

// loadArchived reads an archived chat file. Caller must hold the per-chat mutex.
func (s *Service) loadArchived(chatID api.ChatID) (*api.Chat, error) {
	path, err := s.archivePathFor(chatID)
	if err != nil {
		return nil, err
	}
	return readChatFile(path, "archived chat "+string(chatID))
}

// IDInUseError indicates the target chat ID is already used by an
// active (non-archived) chat.
type IDInUseError struct {
	ID string
}

func (e *IDInUseError) Error() string {
	return "chat id in use: " + e.ID
}
