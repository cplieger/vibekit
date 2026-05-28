// Storage-layout path constants and helpers. Single source of truth
// for the on-disk directory structure AND operational tuning constants
// so all consumers stay in sync if the layout or thresholds change.

package checkpoint

import (
	"path/filepath"
	"time"
)

const (
	dirSnapshots = "snapshots"
	dirBlobs     = "blobs"
	dirChats     = "chats"
	fileEvents   = "events.jsonl"

	// contentCap bounds the bytes a single file read will load into
	// memory for both snapshot (pre-write capture) and blob retrieval
	// (restore/diff). A single constant ensures the two paths stay in
	// sync: any file we snapshot is a file we can also restore.
	// 16 MiB gives 4× headroom over the 4 MiB fsWriteCap while
	// preventing pathological files from OOM-ing the process.
	contentCap = 16 << 20

	// blobGCInterval is the tick period for the background blob GC.
	// One sweep per hour is sufficient: the 5-minute age gate
	// (gc.BlobGCMinAge) prevents premature reaping, so the interval
	// only controls how long orphaned blobs linger before cleanup.
	blobGCInterval = 1 * time.Hour
)

// blobsRoot returns the root directory for the content-addressed
// blob store.
func blobsRoot(configDir string) string {
	return filepath.Join(configDir, dirSnapshots, dirBlobs)
}

// chatsRoot returns the root directory containing per-chat
// subdirectories.
func chatsRoot(configDir string) string {
	return filepath.Join(configDir, dirSnapshots, dirChats)
}

// chatLogPath returns the path to a specific chat's event log.
func chatLogPath(configDir, chatID string) string {
	return filepath.Join(configDir, dirSnapshots, dirChats, chatID, fileEvents)
}
