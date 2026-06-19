// Storage-layout path constants and helpers. Single source of truth
// for the on-disk directory structure AND operational tuning constants
// so all consumers stay in sync if the layout or thresholds change.

package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"time"
)

const (
	dirSnapshots = "snapshots"
	dirBlobs     = "blobs"
	dirChats     = "chats"
	FileEvents   = "events.jsonl"

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
//
// chatID is confined to a single directory level under the chats
// root: a malformed or hostile id containing ".." or path separators
// must never let the returned path escape configDir/snapshots/chats
// (CWE-22). Callers MkdirAll and RemoveAll filepath.Dir of this path
// (eventLog.Append / eventLog.Wipe), so an un-sanitized id would be an
// arbitrary-filesystem write/delete primitive.
func chatLogPath(configDir, chatID string) string {
	return filepath.Join(chatsRoot(configDir), safeChatID(chatID), FileEvents)
}

// safeChatID reduces an arbitrary chatID to a single path component
// that cannot traverse out of the chats root. A legitimate
// server-generated id (alphanumerics + '-') is returned unchanged; an
// id that is empty, ".", "..", or contains a path separator is
// replaced with a deterministic SHA-256 of the raw bytes so distinct
// ids still map to distinct directories without escaping the root.
func safeChatID(chatID string) string {
	if chatID != "" && chatID != "." && chatID != ".." &&
		!strings.ContainsRune(chatID, '/') &&
		!strings.ContainsRune(chatID, filepath.Separator) {
		return chatID
	}
	sum := sha256.Sum256([]byte(chatID))
	return hex.EncodeToString(sum[:])
}
