// Package settings provides a single-source-of-truth reader for
// <configDir>/config.json. All packages that need to extract a key
// from the user's settings file should use Field or FieldInto rather
// than implementing their own open+read+unmarshal pattern.
//
// Field extracts a single typed key via generics; FieldInto is its
// pointer-target variant. Both read through one mtime-keyed cache with a
// 1 MiB size cap (MaxBytes).
//
// There is deliberately no exported raw-bytes reader. The cache OWNS the
// slice it hands out — one backing array, shared by every caller and
// retained until the file's mtime or size changes — so an exported
// accessor for it would let any caller silently corrupt the settings of
// every other, including the agent_ignore_files list that decides which
// files the agent may read. Nothing outside this package ever wanted the
// whole file: the typed getters were the entire production surface.
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/cplieger/atomicfile/v3"
	"golang.org/x/sync/singleflight"
)

// MaxBytes caps config.json reads. Real settings files are well
// under 100 KB; 1 MiB is generous headroom and matches the HTTP PUT
// path's webhttp.MaxJSONBody limit.
const MaxBytes = 1 << 20

// filename is the canonical settings file name.
const filename = "config.json"

// Filename is the on-disk basename of vibekit's primary config file
// (vibekit-managed settings — debug_logs, agent_ignore_files,
// supervised_default, etc.). Distinct from kiro-cli's settings.json which lives
// under $KIRO_HOME/settings/. Exported so callers across the codebase
// (tests, server handler, ignore reader) reference the same canonical
// name and can't drift.
const Filename = filename

// cache provides mtime-based caching for config.json reads.
type cache struct {
	mtime     time.Time
	sfGroup   singleflight.Group
	parsed    map[string]json.RawMessage
	configDir string
	data      []byte
	parsedGen uint64
	gen       uint64
	size      int64
	mu        sync.Mutex
}

var (
	globalCacheMu sync.Mutex
	globalCaches  = map[string]*cache{}
)

func getCache(configDir string) *cache {
	globalCacheMu.Lock()
	defer globalCacheMu.Unlock()
	c, ok := globalCaches[configDir]
	if !ok {
		c = &cache{configDir: configDir}
		globalCaches[configDir] = c
	}
	return c
}

type result struct {
	err  error
	data []byte
}

func (c *cache) load() ([]byte, error) {
	v, _, _ := c.sfGroup.Do("load", func() (any, error) {
		data, err := c.reload()
		return result{data: data, err: err}, nil
	})
	//nolint:errcheck // sfGroup.Do's closure always returns a `result` value.
	r := v.(result)
	return r.data, r.err
}

// reload is the body of the singleflight slot: resolve the path, take the
// mtime/size fast path, otherwise read and cache. Extracted from load so the
// slot stays a two-liner and this stays under the cognitive-complexity ceiling.
func (c *cache) reload() ([]byte, error) {
	// Absolute because atomicfile.OpenRegular requires it. os.Open resolved a
	// relative configDir against the process cwd and filepath.Abs preserves
	// exactly that, so no deployment's meaning changes.
	path, err := filepath.Abs(filepath.Join(c.configDir, filename))
	if err != nil {
		return nil, err
	}
	// os.Stat, not an open: stat never blocks on a FIFO (measured), so the
	// mtime/size fast path stays one syscall.
	info, statErr := os.Stat(path)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			c.forget()
			return nil, nil
		}
		return nil, statErr
	}
	if cached, ok := c.hit(info); ok {
		return cached, nil
	}
	data, err := readRegular(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			c.forget()
			return nil, nil
		}
		return nil, err
	}
	c.store(data, info)
	return data, nil
}

// readRegular reads path under MaxBytes, refusing anything that is not a regular
// file.
//
// OpenRegular, not os.Open: os.Open on a FIFO blocks in open(2) until a writer
// appears and no context deadline rescues it (measured on go1.27.0 — os.Open over
// a FIFO was still blocked past 2s). config.json sits on the /config volume the
// operator reshapes and the agent's own shell can reach, and the caller runs
// inside a singleflight slot, so one mkfifo wedged every concurrent settings
// reader behind it — including the agent-ignore filter and the prompt path.
// OpenRegular refuses a FIFO, directory, device node or socket with
// ErrNotRegular, and refuses a symlink at the final component, which stops a link
// at config.json from making another file's bytes decide the agent read filter and
// the retention window.
func readRegular(path string) ([]byte, error) {
	f, _, err := atomicfile.OpenRegular(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, MaxBytes))
}

// hit reports the cached bytes when info matches what they were read from.
func (c *cache) hit(info os.FileInfo) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mtime.IsZero() || !info.ModTime().Equal(c.mtime) || info.Size() != c.size {
		return nil, false
	}
	return c.data, true
}

// store records freshly read bytes under the identity they were read at.
func (c *cache) store(data []byte, info os.FileInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data = data
	c.mtime = info.ModTime()
	c.size = info.Size()
	c.gen++
}

// forget drops the cached bytes for a file that is no longer there, and bumps the
// generation so parsedMap re-derives instead of serving the parse of a file that
// has since been deleted. It replaces two byte-identical inline copies, which is
// one place fewer for the two to disagree.
func (c *cache) forget() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data = nil
	c.mtime = time.Time{}
	c.size = 0
	c.gen++
}

// readBytes returns the raw config.json content for configDir with
// mtime-based caching. Returns (nil, nil) when the file is missing
// or configDir is empty.
//
// UNEXPORTED, and that is the aliasing fix: the returned slice IS the cache's
// own, so a caller that wrote into it would change what every later reader sees,
// process-wide. Held to the one caller that provably only reads it (parsedMap
// hands it to json.Unmarshal), the sharing costs nothing and saves a copy of the
// file per key lookup.
func readBytes(ctx context.Context, configDir string) ([]byte, error) {
	if configDir == "" {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return getCache(configDir).load()
}

// Field reads config.json, extracts the named key, and
// json-unmarshals it into the target type T. Returns the zero value
// and false when the file is missing, the key is absent, or parsing
// fails. Parse failures are logged at Warn level naming the key.
func Field[T any](ctx context.Context, configDir, key string) (T, bool) {
	var zero T
	raw, err := parsedMap(ctx, configDir)
	if err != nil {
		slog.Warn("settings: read config.json for "+key, "error", err)
		return zero, false
	}
	if raw == nil {
		return zero, false
	}
	v, ok := raw[key]
	if !ok {
		return zero, false
	}
	var out T
	if err := json.Unmarshal(v, &out); err != nil {
		slog.Warn("settings: parse "+key, "error", err)
		return zero, false
	}
	return out, true
}

// FieldInto reads config.json, extracts the named key, and
// json-unmarshals it into the value pointed to by out. Returns true
// on success. This is the pointer-based variant of Field for callers
// that need to unmarshal into an existing variable.
func FieldInto(ctx context.Context, configDir, key string, out any) bool {
	raw, err := parsedMap(ctx, configDir)
	if err != nil {
		slog.Warn("settings: read config.json for "+key, "error", err)
		return false
	}
	if raw == nil {
		return false
	}
	v, ok := raw[key]
	if !ok {
		return false
	}
	if err := json.Unmarshal(v, out); err != nil {
		slog.Warn("settings: parse "+key, "error", err)
		return false
	}
	return true
}

// parsedMap returns the cached parsed map[string]json.RawMessage for
// the given configDir. The map is invalidated when the underlying
// bytes change (mtime-based via readBytes).
func parsedMap(ctx context.Context, configDir string) (map[string]json.RawMessage, error) {
	data, err := readBytes(ctx, configDir)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	c := getCache(configDir)
	c.mu.Lock()
	if c.parsed != nil && c.parsedGen == c.gen {
		m := c.parsed
		c.mu.Unlock()
		return m, nil
	}
	curGen := c.gen
	c.mu.Unlock()

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.parsed = raw
	c.parsedGen = curGen
	c.mu.Unlock()
	return raw, nil
}
