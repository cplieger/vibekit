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
	"github.com/cplieger/pathinside"
	"github.com/cplieger/vibekit/internal/api"
	"golang.org/x/sync/singleflight"
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
	store      StoreAccess
	preArchive func(chatID api.ChatID)
	onArchive  func(chatID api.ChatID)
	onPurge    func(chatID api.ChatID)
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

// confineStore opens a kernel-confined os.Root on the chat store
// directory and translates absolute paths into the root-relative names
// os.Root's methods take. Used by the two operations that MUTATE the
// archive tree (RestoreArchived's rename, DeleteArchived's remove); the
// caller closes the returned root.
//
// Why a confined handle on top of the lexical pathinside.Inside gates
// those callers already run: the two answer different questions and both
// apply here. Inside is a NAME test and says nothing about what a path
// RESOLVES to, so a symlinked INTERMEDIATE component — most plausibly
// <storeDir>/archive itself, which anything able to write /config can
// plant — redirects an ambient os.Rename/os.Remove clean out of the
// tree while reading as contained. A lexical check also cannot close the
// window between itself and the syscall: the path can be repointed in
// between. os.Root re-resolves every component against a pinned
// directory handle and refuses to traverse a symlink that leaves the
// root, so the check and the operation can no longer disagree. Keeping
// both is what pathinside's own doc comment prescribes: the cheap
// lexical gate first, the confined handle for the operation. (The final
// component needs neither — rename and remove act on the link, not its
// target — so the exposure closed here is strictly the ancestors.)
//
// The root is opened on store.Dir() rather than on the archive dir
// because a restore renames ACROSS the two and os.Root.Rename cannot
// cross handles; store.Dir() is their nearest common ancestor, so one
// handle covers both sides.
//
// A symlinked store dir keeps working. os.OpenRoot resolves the root
// path itself with ordinary path resolution, so <configDir>/chats — or
// configDir above it — may be a symlink onto another filesystem and the
// confinement simply applies to the resolved tree (vibekit invariant 6:
// the operator reshapes /config). What is newly refused is a symlink
// BELOW the store dir whose target leaves that tree, and — a Go
// os.Root rule worth knowing — a symlink with an ABSOLUTE target even
// when it points back inside; a relative in-tree symlink
// (archive -> real-archive) is still followed.
//
// Opened per operation instead of cached on the Service, deliberately:
// restore and delete are user-triggered and rare, while a cached handle
// would keep pointing at a directory the operator pruned or replaced,
// and a broken state must be able to heal itself (invariant 6 again).
// Re-resolving picks up the repair on the next call.
func (s *Service) confineStore(absPaths ...string) (*os.Root, []string, error) {
	dir := s.store.Dir()
	names := make([]string, len(absPaths))
	for i, abs := range absPaths {
		rel, err := filepath.Rel(dir, abs)
		if err != nil || pathinside.RelEscapes(rel) {
			return nil, nil, fmt.Errorf("path %q escapes store dir %q", abs, dir)
		}
		names[i] = rel
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("confine chat store dir %q: %w", dir, err)
	}
	return root, names, nil
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
	// SEAM: this move is still AMBIENT, while RestoreArchived and
	// DeleteArchived below run their syscalls through a confined os.Root
	// (see confineStore). Deliberate, not an oversight: the two confined
	// operations are the ones a lexical-only guard was covering, whereas
	// confining the rest of the chat store is a store-wide decision that
	// has not been taken — the core store's removes, plan_draft.go,
	// purge.go's remove and every atomicfile write are ambient too, so
	// singling this one out would only move the inconsistency. Anything
	// new added here should reach for confineStore rather than os.Rename.
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
	// Defence-in-depth: ValidChatID above blocks traversal patterns,
	// but CodeQL's go/path-injection analyzer doesn't follow that
	// guard across the function boundaries used here. Verify both
	// paths stay within the expected parent directories before any
	// FS mutation. The containment check is what CodeQL recognises
	// as a sanitiser.
	//
	// pathinside.Inside is that check: it cleans both sides itself
	// (so the hand-rolled trailing-separator prefix strings are gone)
	// and is separator-precise, so a sibling directory whose name
	// merely extends the archive dir's cannot read as containment.
	// Root-equality is unreachable here — both paths are a Join of the
	// directory with a validated non-empty chat id plus a suffix.
	cleanSrc := filepath.Clean(srcPath)
	cleanDst := filepath.Clean(dstPath)
	if !pathinside.Inside(archiveDir, cleanSrc) {
		m.Unlock()
		return fmt.Errorf("src path %q escapes archive dir %q", srcPath, archiveDir)
	}
	if !pathinside.Inside(s.store.Dir(), cleanDst) {
		m.Unlock()
		return fmt.Errorf("dst path %q escapes store dir %q", dstPath, s.store.Dir())
	}
	// Kernel-confined access for the mutating syscalls below: the lexical
	// gates above answer the NAME question, this answers the ACCESS one
	// (see confineStore for why both run).
	root, names, err := s.confineStore(cleanSrc, cleanDst)
	if err != nil {
		m.Unlock()
		return err
	}
	defer func() { _ = root.Close() }()
	srcRel, dstRel := names[0], names[1]
	// Collision guard: refuse to overwrite an active chat file. Stats
	// through the same handle as the rename, so a destination NAME that is
	// itself a symlink out of the store reads as an escape error instead
	// of as a free slot the rename would then silently replace.
	if _, err := root.Stat(dstRel); err == nil {
		m.Unlock()
		slog.Warn("chat restore: refused, id is in use", "chat_id", chatID)
		return &IDInUseError{ID: string(chatID)}
	} else if !errors.Is(err, os.ErrNotExist) {
		m.Unlock()
		return err
	}
	if err := root.Rename(srcRel, dstRel); err != nil {
		m.Unlock()
		return fmt.Errorf("confined restore rename in chat store %q: %w", s.store.Dir(), err)
	}
	// Also restore the plan draft if it exists. Derive names via a suffix
	// swap on the already-validated srcRel/dstRel so the new names
	// inherit both the containment proof and the confinement.
	draftSrcRel := strings.TrimSuffix(srcRel, chatFileSuffix) + planDraftSuffix
	draftDstRel := strings.TrimSuffix(dstRel, chatFileSuffix) + planDraftSuffix
	if err := root.Rename(draftSrcRel, draftDstRel); err != nil && !errors.Is(err, os.ErrNotExist) {
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
	// WithMaxBytes mirrors the readCappedFile bound: never persist a chat
	// file the archive's own read path would refuse to load.
	_, err = atomicfile.WriteFile(ctx, path, data,
		atomicfile.WithMode(fileMode), atomicfile.WithMkdirMode(dirMode),
		atomicfile.WithMaxBytes(maxChatFileBytes))
	return err
}

// DeleteArchived permanently removes a single archived chat file and its
// plan draft. Fires onPurge so checkpoint data is cleaned up.
func (s *Service) DeleteArchived(ctx context.Context, chatID api.ChatID) error {
	// Validate up-front so CodeQL's path-injection analysis sees the
	// guard at the entry point. archivePathFor below also validates,
	// but the second filepath.Join (for the plan-draft path) is not
	// proven-safe by the analyzer without an explicit check here.
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
	// containment check is what CodeQL recognises as a sanitiser —
	// pathinside.Inside, the same separator-precise containment rule
	// RestoreArchived uses.
	archiveDir := s.archivePath()
	cleanChatPath := filepath.Clean(chatPath)
	if !pathinside.Inside(archiveDir, cleanChatPath) {
		return fmt.Errorf("chat path %q escapes archive dir %q", chatPath, archiveDir)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	// The lexical gate above answers the NAME question; the confined
	// handle answers the ACCESS one (see confineStore). Both syscalls
	// below run through it, so a symlinked intermediate component — the
	// archive dir itself, most plausibly — can no longer redirect this
	// remove out of the store, and the check can no longer disagree with
	// the syscall that follows it.
	root, names, err := s.confineStore(cleanChatPath)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	chatRel := names[0]
	m := s.store.Lock(chatID)
	m.Lock()
	if err := root.Remove(chatRel); err != nil {
		m.Unlock()
		return fmt.Errorf("confined delete of archived chat in store %q: %w", s.store.Dir(), err)
	}
	// draftRel shares the directory of chatRel; the suffix swap keeps it
	// inside the same already-verified, confined directory.
	draftRel := strings.TrimSuffix(chatRel, chatFileSuffix) + planDraftSuffix
	if err := root.Remove(draftRel); err != nil && !errors.Is(err, os.ErrNotExist) {
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
