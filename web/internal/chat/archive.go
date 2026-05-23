package chat

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

	"vibekit/internal/api"
)

// Archive moves a chat from the active directory to the archive
// subdirectory. Takes the per-chat mutex so a concurrent Mutate /
// AppendMessage can't race the rename. Broadcasts chat_deleted so all
// connected clients see the entry disappear without a manual refresh.
func (s *Store) Archive(ctx context.Context, chatID api.ChatID) error {
	if !chatIDPattern(chatID) {
		return errInvalidChatID(chatID)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m := s.lock(chatID)
	m.Lock()
	srcPath, err := s.pathFor(chatID)
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
	if err := os.Rename(srcPath, dstPath); err != nil { //nolint:gosec // G703: paths built from validated chat ID
		m.Unlock()
		return err
	}
	// Also archive the plan draft if it exists. ENOENT is the common
	// case (most chats have no draft); surface anything else so orphan
	// drafts don't silently outlive the archived chat.
	draftSrc := filepath.Join(s.dir, string(chatID)+planDraftSuffix)
	draftDst := filepath.Join(archiveDir, string(chatID)+planDraftSuffix)
	if err := os.Rename(draftSrc, draftDst); err != nil && !errors.Is(err, os.ErrNotExist) { //nolint:gosec // G703: paths built from validated chat ID
		slog.Warn("chat archive: plan-draft move failed",
			"chat_id", chatID, "error", err)
	}
	s.markDeleted(chatID) // tombstone prevents resurrection
	m.Unlock()
	if s.broadcast != nil {
		s.broadcast.Broadcast(ctx, api.NewEvent(api.EventChatDeleted, chatID, api.ChatDeletedPayload{ID: string(chatID)}))
	}
	slog.Info("chat archived", "chat_id", chatID)
	if s.onArchive != nil {
		s.onArchive(chatID)
	}
	return nil
}

// ListArchived returns headers for all archived chats, sorted by
// UpdatedAt desc to match List()'s ordering. Files that fail to read or
// parse are logged and skipped so one bad entry never hides the rest.
// Files larger than maxChatFileBytes are skipped with a warn — an
// out-of-band writer cannot OOM the process via the history modal.
// Always returns a non-nil slice so JSON encoders emit `[]` rather
// than `null` for an empty archive.
func (s *Store) ListArchived(ctx context.Context) []api.ChatHeader {
	// Coalesce concurrent History-tab opens into a single directory scan.
	v, err, _ := s.listArchivedSF.Do("list", func() (any, error) {
		return s.listArchivedOnce(ctx), nil
	})
	if err != nil || v == nil {
		return []api.ChatHeader{}
	}
	headers, ok := v.([]api.ChatHeader)
	if !ok {
		slog.Error("chat list_archived: singleflight returned unexpected type",
			"type", fmt.Sprintf("%T", v))
		return []api.ChatHeader{}
	}
	if headers == nil {
		return []api.ChatHeader{}
	}
	return headers
}

func (s *Store) listArchivedOnce(ctx context.Context) []api.ChatHeader {
	archiveDir := s.archivePath()
	entries, err := os.ReadDir(archiveDir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Error("chat list_archived", "dir", archiveDir, "error", err)
		}
		return []api.ChatHeader{}
	}
	// Collect valid filenames first.
	var valid []chatEntry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), chatFileSuffix) {
			continue
		}
		id := strings.TrimSuffix(e.Name(), chatFileSuffix)
		if !chatIDPattern(api.ChatID(id)) {
			slog.Debug("chat list_archived: skipped non-chat file",
				"name", e.Name(), "reason", "invalid chat id pattern")
			continue
		}
		valid = append(valid, chatEntry{id: id, path: filepath.Join(archiveDir, e.Name())})
	}
	if len(valid) == 0 {
		return []api.ChatHeader{}
	}

	// Bounded-parallel header reads matching List()'s worker pool pattern.
	headers := readHeadersParallel(ctx, valid, "archived chat", s.oldestCheckpoint)
	sort.Slice(headers, func(i, j int) bool {
		return headers[i].UpdatedAt > headers[j].UpdatedAt
	})
	return headers
}

// RestoreArchived moves a chat from the archive back to the active
// directory. Validates chatID before building filesystem paths (every
// other public mutator does the same; this one must too). Takes the
// per-chat mutex so a concurrent Archive / UpdateArchivedSummary /
// PurgeArchived can't race the rename.
//
// Refuses the restore with *StoreError{Kind: ErrKindIDInUse} if an
// active chat already exists at the target id. The tombstone expires
// after 10 minutes; once expired, the same id can be reused for a
// brand-new chat, at which point a user clicking "Restore" on the old
// archive would
// silently overwrite the active chat's file. The collision check
// surfaces that case so the caller (HTTP 409) can tell the user to
// pick between keeping the new chat or archiving it first.
//
// Clears the tombstone so a resurrected chat can accept late-arriving
// writes, and broadcasts chat_created so all connected clients see
// the entry reappear without a manual refresh.
func (s *Store) RestoreArchived(ctx context.Context, chatID api.ChatID) error {
	if !chatIDPattern(chatID) {
		return errInvalidChatID(chatID)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m := s.lock(chatID)
	m.Lock()
	archiveDir := s.archivePath()
	srcPath := filepath.Join(archiveDir, string(chatID)+chatFileSuffix)
	dstPath := filepath.Join(s.dir, string(chatID)+chatFileSuffix)
	// Collision guard: refuse to overwrite an active chat file.
	if _, err := os.Stat(dstPath); err == nil {
		m.Unlock()
		slog.Warn("chat restore: refused, id is in use", "chat_id", chatID)
		return &StoreError{Kind: ErrKindIDInUse, Detail: string(chatID)}
	} else if !errors.Is(err, os.ErrNotExist) {
		m.Unlock()
		return err
	}
	if err := os.Rename(srcPath, dstPath); err != nil {
		m.Unlock()
		return err
	}
	// Also restore the plan draft if it exists.
	draftSrc := filepath.Join(archiveDir, string(chatID)+planDraftSuffix)
	draftDst := filepath.Join(s.dir, string(chatID)+planDraftSuffix)
	if err := os.Rename(draftSrc, draftDst); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("chat restore: plan-draft move failed",
			"chat_id", chatID, "error", err)
	}
	// Clear the tombstone so the restored chat can accept late-arriving
	// writes.
	s.tombMu.Lock()
	delete(s.tombstone, chatID)
	s.tombMu.Unlock()
	c, loadErr := s.load(chatID)
	m.Unlock()
	slog.Info("chat restored from archive", "chat_id", chatID)
	if loadErr == nil && s.broadcast != nil {
		s.broadcast.Broadcast(ctx, api.NewEvent(api.EventChatCreated, chatID, s.header(ctx, c)))
	}
	return nil
}

// LoadArchived reads the archived chat and returns the parsed *api.Chat.
// Takes the per-chat mutex so a concurrent RestoreArchived or
// UpdateArchivedSummary can't race the read.
func (s *Store) LoadArchived(ctx context.Context, chatID api.ChatID) (*api.Chat, error) {
	if !chatIDPattern(chatID) {
		return nil, errInvalidChatID(chatID)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m := s.lock(chatID)
	m.Lock()
	defer m.Unlock()
	return s.loadArchived(chatID)
}

// UpdateArchivedSummary rewrites an archived chat's Summary field in
// place. Used by the hub to populate the one-line summary produced by
// the utility bridge after archiving.
func (s *Store) UpdateArchivedSummary(ctx context.Context, chatID api.ChatID, summary string) error {
	path, err := s.archivePathFor(chatID)
	if err != nil {
		return err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	m := s.lock(chatID)
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
	return api.SaveBytes(path, data, fileMode)
}

// PurgeArchived deletes archived chats older than maxAge.
func (s *Store) PurgeArchived(ctx context.Context, maxAge time.Duration) {
	archiveDir := s.archivePath()
	entries, err := os.ReadDir(archiveDir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Error("chat purge_archived: readdir",
				"dir", archiveDir, "error", err)
		}
		return
	}
	cutoff := time.Now().Add(-maxAge)

	// Collect valid entries first (cheap, no I/O beyond the ReadDir above).
	type purgeEntry struct {
		name string
		path string
	}
	var valid []purgeEntry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), chatFileSuffix) {
			continue
		}
		name := strings.TrimSuffix(e.Name(), chatFileSuffix)
		if !chatIDPattern(api.ChatID(name)) {
			continue
		}
		valid = append(valid, purgeEntry{name: name, path: filepath.Join(archiveDir, e.Name())})
	}
	if len(valid) == 0 {
		return
	}

	// Bounded-parallel purge matching readHeadersParallel's worker pool.
	const maxWorkers = 8
	var purgedCount, keptCount, errCount int32
	var mu sync.Mutex

	boundedParallel(ctx, valid, maxWorkers, func(_ int, entry purgeEntry) {
		m := s.lock(api.ChatID(entry.name))
		m.Lock()
		info, err := os.Stat(entry.path)
		if err != nil {
			m.Unlock()
			if !errors.Is(err, os.ErrNotExist) {
				mu.Lock()
				errCount++
				mu.Unlock()
				slog.Warn("chat purge_archived: stat",
					"name", entry.name, "error", err)
			}
			return
		}
		if !info.ModTime().Before(cutoff) {
			m.Unlock()
			mu.Lock()
			keptCount++
			mu.Unlock()
			return
		}
		if err := os.Remove(entry.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			m.Unlock()
			mu.Lock()
			errCount++
			mu.Unlock()
			slog.Warn("chat purge_archived: remove",
				"chat_id", entry.name, "error", err)
			return
		}
		draftPath := filepath.Join(archiveDir, entry.name+planDraftSuffix)
		if err := os.Remove(draftPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("chat purge_archived: remove plan-draft",
				"chat_id", entry.name, "error", err)
		}
		m.Unlock()
		if s.onPurge != nil {
			s.onPurge(api.ChatID(entry.name))
		}
		mu.Lock()
		purgedCount++
		mu.Unlock()
	})

	purged := int(purgedCount)
	kept := int(keptCount)
	errs := int(errCount)
	if errs > 0 {
		slog.Warn("chat purge_archived: pass complete with errors",
			"purged", purged, "kept", kept, "errors", errs,
			"max_age", maxAge)
	} else {
		slog.Info("chat purge_archived: pass complete",
			"purged", purged, "kept", kept,
			"max_age", maxAge)
	}
}

// DeleteArchived permanently removes a single archived chat file and its
// plan draft. Fires onPurge so checkpoint data is cleaned up.
func (s *Store) DeleteArchived(ctx context.Context, chatID api.ChatID) error {
	chatPath, err := s.archivePathFor(chatID)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m := s.lock(chatID)
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

// loadArchived reads an archived chat file. Caller must hold the
// per-chat mutex.
func (s *Store) loadArchived(chatID api.ChatID) (*api.Chat, error) {
	path, err := s.archivePathFor(chatID)
	if err != nil {
		return nil, err
	}
	return readChatFile(path, "archived chat "+string(chatID))
}
