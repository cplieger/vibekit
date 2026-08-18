package chat

import (
	"time"

	"github.com/cplieger/vibekit/internal/chat/archive"
)

// NewPurgeScheduler builds a scheduler that runs purges based on the
// retention value returned by `retention`. Delegates to the archive
// sub-package.
//
// The scheduler's context is not a construction input: it arrives at
// PurgeScheduler.Start, the method that runs the loop.
func NewPurgeScheduler(store *Store, retention func() time.Duration) *archive.PurgeScheduler {
	return archive.NewPurgeScheduler(store.archiveSvc(), retention)
}
