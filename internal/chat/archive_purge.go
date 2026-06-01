package chat

import (
	"context"
	"time"

	"vibekit/internal/chat/archive"
)

// NewPurgeScheduler builds a scheduler that runs purges based on the
// retention value returned by `retention`. Delegates to the archive
// sub-package.
func NewPurgeScheduler(ctx context.Context, store *Store, retention func() time.Duration) *archive.PurgeScheduler {
	return archive.NewPurgeScheduler(ctx, store.archiveSvc(), retention)
}
