package chat

import (
	"context"
	"time"

	"github.com/cplieger/vibekit/internal/chat/archive"
)

// archiveSvc returns the Store's archive service, creating it lazily.
// The service is created on first access rather than in NewStore to
// avoid a construction-time dependency cycle (the archive.Service
// needs the Store to be fully constructed first).
func (s *Store) archiveSvc() *archive.Service {
	s.archiveOnce.Do(func() {
		var opts []archive.Option
		if s.onPurge != nil {
			opts = append(opts, archive.WithOnPurge(s.onPurge))
		}
		if s.isLive != nil {
			opts = append(opts, archive.WithLiveChats(s.isLive))
		}
		s.archive = archive.New(s, opts...)
	})
	return s.archive
}

// PurgeArchived deletes chats whose last activity is older than maxAge.
// Delegates to the archive sub-package. Named for the retention concept, not a
// directory: nothing is archived any more.
func (s *Store) PurgeArchived(ctx context.Context, maxAge time.Duration) {
	s.archiveSvc().Purge(ctx, maxAge)
}
