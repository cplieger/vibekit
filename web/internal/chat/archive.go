package chat

import (
	"context"
	"errors"
	"time"

	"vibekit/internal/api"
	"vibekit/internal/chat/archive"
)

// archiveSvc returns the Store's archive service, creating it lazily.
// The service is created on first access rather than in NewStore to
// avoid a construction-time dependency cycle (the archive.Service
// needs the Store to be fully constructed first).
func (s *Store) archiveSvc() *archive.Service {
	s.archiveOnce.Do(func() {
		var opts []archive.Option
		if s.onArchive != nil {
			opts = append(opts, archive.WithOnArchive(s.onArchive))
		}
		if s.onPurge != nil {
			opts = append(opts, archive.WithOnPurge(s.onPurge))
		}
		s.archive = archive.New(s, opts...)
	})
	return s.archive
}

// Archive moves a chat from the active directory to the archive
// subdirectory. Delegates to the archive sub-package.
func (s *Store) Archive(ctx context.Context, chatID api.ChatID) error {
	return s.archiveSvc().Archive(ctx, chatID)
}

// ListArchived returns headers for all archived chats, sorted by
// UpdatedAt desc. Delegates to the archive sub-package.
func (s *Store) ListArchived(ctx context.Context) []api.ChatHeader {
	return s.archiveSvc().ListArchived(ctx)
}

// RestoreArchived moves a chat from the archive back to the active
// directory. Delegates to the archive sub-package.
func (s *Store) RestoreArchived(ctx context.Context, chatID api.ChatID) error {
	err := s.archiveSvc().RestoreArchived(ctx, chatID)
	if err != nil {
		var idErr *archive.IDInUseError
		if errors.As(err, &idErr) {
			return &StoreError{Kind: ErrKindIDInUse, Detail: idErr.ID}
		}
	}
	return err
}

// LoadArchived reads the archived chat. Delegates to the archive sub-package.
func (s *Store) LoadArchived(ctx context.Context, chatID api.ChatID) (*api.Chat, error) {
	return s.archiveSvc().LoadArchived(ctx, chatID)
}

// UpdateArchivedSummary rewrites an archived chat's Summary field.
// Delegates to the archive sub-package.
func (s *Store) UpdateArchivedSummary(ctx context.Context, chatID api.ChatID, summary string) error {
	return s.archiveSvc().UpdateArchivedSummary(ctx, chatID, summary)
}

// DeleteArchived permanently removes a single archived chat.
// Delegates to the archive sub-package.
func (s *Store) DeleteArchived(ctx context.Context, chatID api.ChatID) error {
	return s.archiveSvc().DeleteArchived(ctx, chatID)
}

// PurgeArchived deletes archived chats older than maxAge.
// Delegates to the archive sub-package.
func (s *Store) PurgeArchived(ctx context.Context, maxAge time.Duration) {
	s.archiveSvc().Purge(ctx, maxAge)
}
