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
	"time"

	"github.com/cplieger/atomicfile/v2"
	"github.com/cplieger/vibekit/internal/api"
	"golang.org/x/sync/singleflight"
)

// Subdir is the subdirectory name under the chats directory where
// archived chats are stored. Exported so composition-layer code can
// reference it without hardcoding the literal.
const Subdir = "archive"

const (
	chatFileSuffix = ".json"
	dirMode        = 0o700
	fileMode       = 0o600
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
	store      StoreAccess
	preArchive func(chatID api.ChatID)
	onArchive  func(chatID api.ChatID)
	onPurge    func(chatID api.ChatID, sessionChain []string)
	listSF     singleflight.Group
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

// WithPreArchive registers a callback fired BEFORE a chat's file is moved
// to the archive dir. Used to run the hub's in-memory teardown (close the
// bridge, kill terminals, clear pending state, remove the .partial) while
// the chat record is still active, so archiving can't orphan a live bridge
// or leave a ghost .partial.
func WithPreArchive(fn func(chatID api.ChatID)) Option {
	return func(s *Service) { s.preArchive = fn }
}

// WithOnArchive registers a callback fired after a chat is archived.
func WithOnArchive(fn func(chatID api.ChatID)) Option {
	return func(s *Service) { s.onArchive = fn }
}

// WithOnPurge registers a callback fired after an archived chat is purged.
//
// sessionChain is every KAS session the purged chat ran on, read before the
// chat file was removed. The purge reaps its OWN session directories through
// this: retention must not depend on the hourly orphan sweep finding them,
// because that sweep's keep-list is derived by reading every chat file and is
// the destructive leg of the system.
func WithOnPurge(fn func(chatID api.ChatID, sessionChain []string)) Option {
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
	// Pre-archive teardown (hub): close the bridge, kill agent terminals,
	// clear pending perms + supervised trust, close+remove the .partial —
	// BEFORE the file moves, so a live bridge can't outlive its chat record
	// and no orphan .partial survives. Runs outside the per-chat lock,
	// mirroring the delete path's CleanupChatState-then-Delete order.
	if s.preArchive != nil {
		s.preArchive(chatID)
	}
	m := s.store.Lock(chatID)
	m.Lock()
	srcPath, err := s.store.PathFor(chatID)
	if err != nil {
		m.Unlock()
		return err
	}
	// Stamp ArchivedAt on the still-active file before the move so purge
	// ages the entry from archive time, not the last-activity mtime (which
	// a skipped/failed post-archive summary write would otherwise leave
	// stale, purging an old-but-just-archived chat almost immediately).
	// Best-effort: on failure purge falls back to mtime.
	if err := s.stampArchivedAt(ctx, chatID, srcPath); err != nil {
		slog.Warn("chat archive: stamp ArchivedAt failed", "chat_id", chatID, "error", err)
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

// stampArchivedAt records the archive timestamp on the still-active chat
// file at srcPath before it is moved. Written via atomicfile.WriteFile
// (not the store's save) so UpdatedAt is preserved — the History sort order
// keeps reflecting last activity, while purge ages from ArchivedAt. Caller
// holds the per-chat mutex.
func (s *Service) stampArchivedAt(ctx context.Context, chatID api.ChatID, srcPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c, err := s.store.Load(chatID)
	if err != nil {
		return err
	}
	c.ArchivedAt = time.Now().UnixMilli()
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// WithMaxBytes mirrors the readCappedFile bound: never persist a chat
	// file the archive's own read path would refuse to load.
	_, err = atomicfile.WriteFile(ctx, srcPath, data,
		atomicfile.WithMode(fileMode), atomicfile.WithMkdirMode(dirMode),
		atomicfile.WithMaxBytes(maxChatFileBytes))
	return err
}

// ListArchived returns headers for all archived chats, sorted by
// UpdatedAt desc. Files that fail to read or parse are logged and
// skipped. Always returns a non-nil slice.
func (s *Service) ListArchived(ctx context.Context) []api.ChatHeader {
	headers, _ := s.ListArchivedWithCompleteness(ctx)
	return headers
}

// ListArchivedWithCompleteness is ListArchived plus whether every archived
// chat that exists was read. The retention sweep needs the flag: an archived
// chat dropped from this list takes its session chain's keep-entries with it.
//
// Coalesces concurrent scans into one directory read.
func (s *Service) ListArchivedWithCompleteness(ctx context.Context) ([]api.ChatHeader, bool) {
	r := sfDo(&s.listSF, "list", func() archivedListResult {
		headers, complete := s.listArchivedOnce(ctx)
		return archivedListResult{headers: headers, complete: complete}
	})
	if r.headers == nil {
		return []api.ChatHeader{}, r.complete
	}
	return r.headers, r.complete
}

// archivedListResult carries a scan and its completeness through one
// singleflight slot.
type archivedListResult struct {
	headers  []api.ChatHeader
	complete bool
}

func (s *Service) listArchivedOnce(ctx context.Context) ([]api.ChatHeader, bool) {
	archiveDir := s.archivePath()
	entries, err := os.ReadDir(archiveDir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Error("chat list_archived", "dir", archiveDir, "error", err)
			// The directory exists but is unreadable: nothing is known about
			// which archived chats there are.
			return []api.ChatHeader{}, false
		}
		// No archive dir yet -- genuinely nothing archived.
		return []api.ChatHeader{}, true
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
		return []api.ChatHeader{}, true
	}

	headers, complete := readHeadersParallel(ctx, valid, "archived chat", s.store.OldestCheckpoint())
	sort.Slice(headers, func(i, j int) bool {
		return headers[i].UpdatedAt > headers[j].UpdatedAt
	})
	return headers, complete
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
	// Defence-in-depth: ValidChatID above blocks traversal patterns,
	// but CodeQL's go/path-injection analyzer doesn't follow that
	// guard across the function boundaries used here. Verify both
	// paths stay within the expected parent directories before any
	// FS mutation. The containment check is what CodeQL recognises
	// as a sanitiser.
	cleanSrc := filepath.Clean(srcPath)
	cleanDst := filepath.Clean(dstPath)
	cleanArchiveDir := filepath.Clean(archiveDir) + string(filepath.Separator)
	cleanStoreDir := filepath.Clean(s.store.Dir()) + string(filepath.Separator)
	if !strings.HasPrefix(cleanSrc, cleanArchiveDir) {
		m.Unlock()
		return fmt.Errorf("src path %q escapes archive dir %q", srcPath, archiveDir)
	}
	if !strings.HasPrefix(cleanDst, cleanStoreDir) {
		m.Unlock()
		return fmt.Errorf("dst path %q escapes store dir %q", dstPath, s.store.Dir())
	}
	// Collision guard: refuse to overwrite an active chat file.
	if _, err := os.Stat(cleanDst); err == nil {
		m.Unlock()
		slog.Warn("chat restore: refused, id is in use", "chat_id", chatID)
		return &IDInUseError{ID: string(chatID)}
	} else if !errors.Is(err, os.ErrNotExist) {
		m.Unlock()
		return err
	}
	if err := os.Rename(cleanSrc, cleanDst); err != nil {
		m.Unlock()
		return err
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
	// WithMaxBytes mirrors the readCappedFile bound: never persist a chat
	// file the archive's own read path would refuse to load.
	_, err = atomicfile.WriteFile(ctx, path, data,
		atomicfile.WithMode(fileMode), atomicfile.WithMkdirMode(dirMode),
		atomicfile.WithMaxBytes(maxChatFileBytes))
	return err
}

// DeleteArchived permanently removes a single archived chat file.
// Fires onPurge so checkpoint data is cleaned up.
func (s *Service) DeleteArchived(ctx context.Context, chatID api.ChatID) error {
	// Validate up-front so CodeQL's path-injection analysis sees the
	// guard at the entry point. archivePathFor below also validates, but
	// the analyzer does not track that guard across the return boundary.
	if !api.ValidChatID(string(chatID)) {
		return fmt.Errorf("invalid chat id: %q", chatID)
	}
	chatPath, err := s.archivePathFor(chatID)
	if err != nil {
		return err
	}
	// Defence-in-depth: archivePathFor already validated chatID via
	// api.ValidChatID, but CodeQL's go/path-injection analyzer doesn't
	// track that guard across the function-return boundary. Resolve
	// chatPath to its absolute, cleaned form and verify it's still
	// under the archive directory before any FS mutation. The
	// containment check is what CodeQL recognises as a sanitiser.
	archiveDir := s.archivePath()
	cleanChatPath := filepath.Clean(chatPath)
	cleanArchiveDir := filepath.Clean(archiveDir) + string(filepath.Separator)
	if !strings.HasPrefix(cleanChatPath, cleanArchiveDir) {
		return fmt.Errorf("chat path %q escapes archive dir %q", chatPath, archiveDir)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m := s.store.Lock(chatID)
	m.Lock()
	// Read the session chain before the remove — afterwards the ids are gone.
	// A load failure is not fatal to the delete: the chat file still goes, and
	// the orphan sweep remains the backstop for its session directories.
	var chain []string
	if c, lErr := s.loadArchived(chatID); lErr == nil {
		chain = c.SessionChain()
	}
	if err := os.Remove(cleanChatPath); err != nil {
		m.Unlock()
		return err
	}
	m.Unlock()
	if s.onPurge != nil {
		s.onPurge(chatID, chain)
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
