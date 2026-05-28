// Package settings provides a single-source-of-truth reader for
// <configDir>/config.json. All packages that need to extract a key
// from the user's settings file should use ReadBytes or Field rather
// than implementing their own open+read+unmarshal pattern.
//
// ReadBytes returns the raw bytes with mtime-based caching and size
// capping (1 MiB). Field extracts a single typed key via generics.
package settings

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// MaxBytes caps config.json reads. Real settings files are well
// under 100 KB; 1 MiB is generous headroom and matches the HTTP PUT
// path's api.MaxJSONBody limit.
const MaxBytes = 1 << 20

// filename is the canonical settings file name.
const filename = "config.json"

// Filename is the on-disk basename of vibekit's primary config file
// (vibekit-managed settings — auto_update, debug_logs, agent_ignore_files,
// shell_policy, etc.). Distinct from kiro-cli's config.json which lives
// under $KIRO_HOME/settings/. Exported so callers across the codebase
// (tests, server handler, ignore reader) reference the same canonical
// name and can't drift.
const Filename = filename

// cache provides mtime-based caching for config.json reads.
type cache struct {
	mtime     time.Time
	sfGroup   singleflight.Group
	configDir string
	data      []byte
	parsed    map[string]json.RawMessage // cached parsed map, invalidated on gen change
	parsedGen uint64                     // gen at which parsed was computed
	gen       uint64                     // monotonic counter, incremented on every successful load
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
		path := filepath.Join(c.configDir, filename)
		info, statErr := os.Stat(path)

		c.mu.Lock()
		cached := c.data
		cachedMTime := c.mtime
		cachedSize := c.size
		c.mu.Unlock()

		if statErr != nil {
			if os.IsNotExist(statErr) {
				c.mu.Lock()
				c.data = nil
				c.mtime = time.Time{}
				c.size = 0
				c.gen++
				c.mu.Unlock()
				return result{data: nil, err: nil}, nil
			}
			return result{data: nil, err: statErr}, nil
		}

		if !cachedMTime.IsZero() &&
			info.ModTime().Equal(cachedMTime) &&
			info.Size() == cachedSize {
			return result{data: cached, err: nil}, nil
		}

		f, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				c.mu.Lock()
				c.data = nil
				c.mtime = time.Time{}
				c.size = 0
				c.gen++
				c.mu.Unlock()
				return result{data: nil, err: nil}, nil
			}
			return result{data: nil, err: err}, nil
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, MaxBytes))
		if err != nil {
			return result{data: nil, err: err}, nil
		}

		c.mu.Lock()
		c.data = data
		c.mtime = info.ModTime()
		c.size = info.Size()
		c.gen++
		c.mu.Unlock()

		return result{data: data, err: nil}, nil
	})
	//nolint:errcheck // sfGroup.Do's closure always returns a `result` value.
	r := v.(result)
	return r.data, r.err
}

// ReadBytes returns the raw config.json content for configDir with
// mtime-based caching. Returns (nil, nil) when the file is missing
// or configDir is empty.
func ReadBytes(ctx context.Context, configDir string) ([]byte, error) {
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
// fails. Parse failures are logged at Warn level with the provided
// tag for diagnostics.
func Field[T any](ctx context.Context, configDir, key, tag string) (T, bool) {
	var zero T
	raw, err := parsedMap(ctx, configDir)
	if err != nil {
		slog.Warn("settings: read config.json for "+tag, "error", err)
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
		slog.Warn("settings: parse "+tag, "error", err)
		return zero, false
	}
	return out, true
}

// FieldInto reads config.json, extracts the named key, and
// json-unmarshals it into the value pointed to by out. Returns true
// on success. This is the pointer-based variant of Field for callers
// that need to unmarshal into an existing variable.
func FieldInto(ctx context.Context, configDir, key, tag string, out any) bool {
	raw, err := parsedMap(ctx, configDir)
	if err != nil {
		slog.Warn("settings: read config.json for "+tag, "error", err)
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
		slog.Warn("settings: parse "+tag, "error", err)
		return false
	}
	return true
}

// parsedMap returns the cached parsed map[string]json.RawMessage for
// the given configDir. The map is invalidated when the underlying
// bytes change (mtime-based via ReadBytes).
func parsedMap(ctx context.Context, configDir string) (map[string]json.RawMessage, error) {
	data, err := ReadBytes(ctx, configDir)
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
