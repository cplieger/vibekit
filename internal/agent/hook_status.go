package agent

import (
	"encoding/json"
	"os"
	"sync"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/vibekit/internal/workspace"
)

// kiroSettingsPath returns the path to kiro-cli's settings file, via
// workspace.KiroHome() so vibekit and kiro-cli agree on the location
// regardless of whether KIRO_HOME is set. That file is cli.json —
// `kiro-cli settings <key> <value>` persists every key there, not the
// settings.json this used to read.
func kiroSettingsPath() string {
	if workspace.KiroHome() == ".kiro" {
		// Both KIRO_HOME and HOME unset: a relative path here would read from
		// CWD/settings/cli.json. Caller treats empty as "fall through to defaults".
		return ""
	}
	return workspace.KiroSettingsPath("cli.json")
}

// cachedBoolField reads a boolean value from a JSON file, cutting the
// per-call cost to one os.Stat once warm. Staleness is checked via mtime AND
// inode identity plus size, because kiro-cli publishes cli.json by rename
// (new inode per write) and neither mtime nor size alone catches both a
// same-mtime rename and a same-inode rewrite.
type cachedBoolField struct {
	id         atomicfile.FileIdentity
	path       string
	key        string
	size       int64
	mu         sync.Mutex
	defaultVal bool
	value      bool
}

func newCachedBoolField(path, key string, defaultVal bool) *cachedBoolField {
	return &cachedBoolField{path: path, key: key, defaultVal: defaultVal, value: defaultVal}
}

func (c *cachedBoolField) get() bool {
	if c.path == "" {
		return c.defaultVal
	}

	// Never hold the lock across the stat/read/unmarshal: this is consulted per
	// tool call, and a slow filesystem would block every caller behind one
	// reader. Two concurrent misses may both read the file, which is benign.
	info, err := os.Stat(c.path)
	if err != nil {
		return c.defaultVal
	}

	c.mu.Lock()
	id, size, cached := c.id, c.size, c.value
	c.mu.Unlock()
	if id.Matches(info) && info.Size() == size {
		return cached
	}

	data, err := os.ReadFile(c.path) // #nosec G304 -- fixed path
	if err != nil {
		return c.defaultVal
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return c.defaultVal
	}
	parsed := c.defaultVal
	if v, ok := raw[c.key]; ok {
		var b bool
		if json.Unmarshal(v, &b) == nil {
			parsed = b
		}
	}

	// If the file changed again in between, the pairing is stale in the safe
	// direction: the next call stats a different generation and re-reads.
	c.mu.Lock()
	c.value, c.id, c.size = parsed, atomicfile.Identify(info), info.Size()
	c.mu.Unlock()
	return parsed
}

// hookStatusCache caches the hooks.showStatus setting from
// ~/.kiro/settings/cli.json, invalidating on the file's identity and size.
type hookStatusCache struct {
	field *cachedBoolField
}

func newHookStatusCache(path string) *hookStatusCache {
	return &hookStatusCache{field: newCachedBoolField(path, "hooks.showStatus", true)}
}

// IsHookStatusEnabled reads the kiro-cli hooks.showStatus setting. Returns
// true (show hooks) on any error or when the setting is unset, matching
// kiro-cli's own default.
//
// Read from vibekit's own configDir would be wrong: that file uses
// underscore keys with no entry for this toggle, so the prior lookup of
// "hooks_show_status" against it was a permanent no-op. See vibekit.md
// "Experimental kiro-cli flags".
func (c *hookStatusCache) IsHookStatusEnabled() bool {
	return c.field.get()
}
