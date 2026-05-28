// Content-addressed blob store.
//
// Every file content vibekit ever checkpoints is stored once, keyed by
// SHA-256. Two chats editing the same file to the same bytes share one
// blob. The old shadow-git-per-chat approach wasted disk + duplicated
// git's own object store logic; this replacement is ~80 lines and
// produces identical disk-size gains on small repos plus massive wins
// when several chats touch the same files.
//
// Layout on disk:
//
//	<root>/blobs/<first-2-hex>/<remaining-62-hex>
//
// The two-char fanout mirrors git's loose-object layout — keeps any
// single directory bounded to at most 256 entries so very-long-lived
// vibekit instances don't hit filesystem degradation on flat dirs.
//
// Thread safety: blob writes are atomic (temp + fsync + rename +
// parent-dir fsync), reads are lock-free. Callers don't need to
// serialize.

package checkpoint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// blobStore owns a content-addressed storage root. Shared by every
// chat's Manager so dedup works across chats; created once in Store.
type blobStore struct {
	root string // <configDir>/snapshots/blobs
}

// newBlobStore builds a blob store rooted under configDir. The
// directory is created on first use; the constructor itself only
// records the path so a nil-configured vibekit (no configDir) skips
// the mkdir until someone actually writes a blob.
func newBlobStore(configDir string) *blobStore {
	return &blobStore{root: blobsRoot(configDir)}
}

// hashOf returns the hex SHA-256 of data. Separate function so callers
// can hash without creating the blob — used by the snapshot path to
// detect "content unchanged, no new blob needed" without an extra
// disk round-trip.
func hashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Put stores data and returns its hash. Idempotent: if the blob
// already exists (same content), we skip the write. Atomic and
// durable on the write path:
//
//   - temp-file write → Sync (flush data to disk)
//   - Close → Rename
//   - Parent-dir Sync (persist the rename in directory metadata)
//
// Without the temp-file Sync, a rename can succeed while the data
// pages are still in the kernel page cache; a power loss leaves a
// correctly-named but truncated blob that reads would accept as
// truth (since nothing re-verifies the hash). Without the parent-
// dir Sync, the rename itself can roll back post-reboot on ext4 /
// xfs even though the events.jsonl reference (fsynced by
// eventLog.Append) survives — mismatched durability guarantees
// produce events that point at missing blobs.
func (b *blobStore) Put(ctx context.Context, data []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	hash := hashOf(data)
	p := b.pathFor(hash)
	if p == "" {
		return "", errors.New("empty blob hash")
	}
	if _, statErr := os.Stat(p); statErr == nil {
		return hash, nil
	} else if !os.IsNotExist(statErr) {
		// Transient I/O error during the existence check. Fall
		// through to the write path (content-addressed, so
		// rewriting the same bytes is safe) but surface the
		// original error so operators have a breadcrumb when
		// the filesystem is misbehaving.
		slog.Warn("checkpoint: blob stat failed, proceeding with write",
			"hash", hash, "error", statErr)
	}
	parent := filepath.Dir(p)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", fmt.Errorf("mkdir blob parent: %w", errors.Join(ErrTransient, err))
	}
	// Atomic write: create under a .tmp-<suffix> sibling, then
	// rename into place. The suffix lets two goroutines storing
	// the same blob concurrently succeed (distinct temp files, both
	// rename to the same final path — whichever wins doesn't matter
	// since the content is identical).
	tmp, err := os.CreateTemp(parent, "blob-*")
	if err != nil {
		return "", fmt.Errorf("create temp blob: %w", errors.Join(ErrTransient, err))
	}
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", fmt.Errorf("write blob: %w", err)
	}
	if err := ctx.Err(); err != nil {
		tmp.Close()
		return "", err
	}
	// fsync the data before rename so a crash between Rename
	// and the next page-cache flush doesn't leave a correctly-
	// named truncated blob.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", fmt.Errorf("fsync blob: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close blob: %w", err)
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return "", fmt.Errorf("rename blob: %w", err)
	}
	cleanupTmp = false
	// fsync the parent dir so the rename survives power loss.
	// Cheap (one syscall per blob) and closes the window where
	// events.jsonl would reference a blob whose filesystem entry
	// didn't make it to disk.
	syncDir(parent)
	return hash, nil
}

// Get reads the blob content for hash. Returns (nil, ErrBlobNotFound)
// when the blob isn't in the store — callers distinguish "not found"
// (blob may have been GC'd or never existed) from I/O errors.
//
// Caps the read at contentCap to prevent an oversized on-disk blob
// (corruption, manual operator copy, partial write) from OOM-ing
// the process. Rejects non-regular files so a symlink planted at a
// hash path can't serve arbitrary filesystem content.
func (b *blobStore) Get(ctx context.Context, hash string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p := b.pathFor(hash)
	if p == "" {
		slog.Warn("checkpoint: invalid blob hash requested",
			"hash", hash)
		return nil, ErrBlobNotFound
	}
	// Lstat first so we see the symlink itself, not the target.
	// os.Open + f.Stat would follow the link and happily report
	// the target as a regular file, defeating the guard below.
	lInfo, err := os.Lstat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrBlobNotFound
		}
		return nil, err
	}
	if !lInfo.Mode().IsRegular() {
		// A planted symlink would otherwise let Get serve an
		// arbitrary file to any chat whose event log references
		// a matching SHA. Reject non-regular files the same way
		// the GC sweep skips them.
		slog.Warn("checkpoint: non-regular blob file rejected",
			"path", p)
		return nil, ErrBlobNotFound
	}
	f, err := os.Open(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrBlobNotFound
		}
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		// Defense-in-depth: the inode swapped between Lstat
		// and Open. Extremely unlikely in practice but the
		// check is free.
		slog.Warn("checkpoint: non-regular blob file rejected",
			"path", p)
		return nil, ErrBlobNotFound
	}
	if info.Size() > contentCap {
		slog.Warn("checkpoint: blob exceeds read cap",
			"path", p, "size", info.Size(), "cap", contentCap)
		return nil, fmt.Errorf("%w: %d bytes (cap %d)",
			ErrBlobTooLarge, info.Size(), contentCap)
	}
	data, err := io.ReadAll(io.LimitReader(f, contentCap))
	if err != nil {
		return nil, err
	}
	// Integrity check: the filename IS the SHA-256 of the content.
	// If they don't match, the blob is silently corrupted (bitrot,
	// truncated write, bad sector). Warn loudly but return the data
	// anyway — partial/corrupt content is better than a hard failure
	// that blocks the entire restore.
	actual := fmt.Sprintf("%x", sha256.Sum256(data))
	if actual != hash {
		slog.Error("checkpoint: blob integrity check FAILED — content does not match hash",
			"expected", hash, "actual", actual, "path", p, "size", len(data))
	}
	return data, nil
}

// Exists reports whether a blob for hash is on disk. Cheap — one
// stat call, no content read.
func (b *blobStore) Exists(hash string) bool {
	p := b.pathFor(hash)
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

// pathFor returns the on-disk path where hash would be stored. Does
// not check existence. Validates hash as 64-char lowercase hex before
// joining. Defense-in-depth against CWE-22 (path traversal).
func (b *blobStore) pathFor(hash string) string {
	if len(hash) != 64 {
		return ""
	}
	for i := range 64 {
		c := hash[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return ""
		}
	}
	return filepath.Join(b.root, hash[:2], hash[2:])
}

// syncDir fsyncs a directory so recent renames/creates in it
// survive power loss. The d.Sync error is logged at Debug (not
// propagated); filesystems that don't support directory fsync
// (rare; most tmpfs variants do) return an error that doesn't
// imply data loss, and a reported error here would have no
// actionable recovery. The Debug log lets operators confirm
// directory-fsync behaviour in test environments without
// spamming Warn on every blob write on tmpfs.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	if serr := d.Sync(); serr != nil {
		slog.Debug("checkpoint: dir sync failed",
			"dir", dir, "error", serr)
	}
	_ = d.Close()
}

// ErrBlobNotFound signals that a blob was requested but isn't in the
// store. Distinct from os.ErrNotExist so callers can tell "blob GC'd"
// apart from "filesystem error reading a blob that should exist".
var ErrBlobNotFound = errors.New("blob not found")

// ErrBlobTooLarge signals that a blob exceeds the read cap. Distinct
// from a generic error so the HTTP layer can map it to 413 without
// string matching.
var ErrBlobTooLarge = errors.New("blob too large")
