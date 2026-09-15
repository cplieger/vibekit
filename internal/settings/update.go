package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"

	"github.com/cplieger/atomicfile/v3"
)

// ErrUnreadable marks the one failure Update reports that is about the STORED
// document rather than about the write: config.json exists and could not be read
// or parsed. A caller that answers a user distinguishes it, because the remedy is
// the file itself.
var ErrUnreadable = errors.New("settings: stored document unreadable")

// Update applies fn to the stored settings document and writes the result back
// atomically, holding ONE lock across the read, the merge and the write. Two
// concurrent writers of one file would otherwise read-modify-write over each
// other, and one of them dropping a preference is the silent loss the atomic
// write alone cannot prevent. The lock is keyed per configDir, so two config
// directories never serialize against each other.
//
// A document that cannot be READ refuses the write (ErrUnreadable). The write
// replaces the whole file, so an empty map is indistinguishable from "nothing was
// stored" and merging over one would durably destroy every key fn does not name.
// An ABSENT file is not that case: a fresh volume has no config.json, and its
// first write is ordinary rather than a failure.
//
// The merged document is returned so a caller can announce the change without
// reading the file again.
func Update(ctx context.Context, configDir string, fn func(doc map[string]json.RawMessage) error) (map[string]json.RawMessage, error) {
	if configDir == "" {
		return nil, errors.New("settings: no config dir")
	}
	c := getCache(configDir)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	path, err := filepath.Abs(filepath.Join(configDir, filename))
	if err != nil {
		return nil, err
	}
	doc, err := readDocument(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnreadable, err)
	}
	if mErr := fn(doc); mErr != nil {
		return nil, mErr
	}
	pretty, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	res, err := atomicfile.WriteFile(ctx, path, append(pretty, '\n'),
		atomicfile.WithMode(0o644), atomicfile.WithMkdirMode(0o755))
	if err != nil {
		return nil, err
	}
	if !res.Durable {
		slog.Warn("settings: saved but parent-dir fsync unconfirmed; not guaranteed durable across an immediate crash",
			"path", path)
	}
	c.forget()
	return doc, nil
}

// readDocument reads and parses the document at path, refusing anything a merge
// cannot safely be built on. An absent file yields an empty map and a nil error;
// every other outcome is an error, including a file this package's own readers
// would tolerate.
//
// It goes through readRegular, so a FIFO or a directory at the name is refused
// rather than blocking in open(2) — Update holds the lock across this read, so
// one planted FIFO would otherwise wedge every later settings write.
func readDocument(path string) (map[string]json.RawMessage, error) {
	data, info, err := readRegular(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]json.RawMessage{}, nil
		}
		return nil, err
	}
	if info.Size() > MaxBytes {
		return nil, fmt.Errorf("settings: %s is %d bytes, over the %d-byte cap", path, info.Size(), MaxBytes)
	}
	doc := make(map[string]json.RawMessage)
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	// A stored top-level `null` parses into a NIL map and returns no error,
	// overriding the make above — after which maps.Copy panics. `[]` and `"str"`
	// both error on their own; null is the gap.
	if doc == nil {
		return nil, fmt.Errorf("settings: %s contains a top-level null, not an object", path)
	}
	return doc, nil
}
