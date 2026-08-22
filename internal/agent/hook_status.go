package agent

import (
	"encoding/json"
	"os"
	"sync"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/vibekit/internal/workspace"
)

// kiroSettingsPath returns the path to kiro-cli's settings file.
// Resolves through workspace.KiroHome() so vibekit and kiro-cli agree on
// the location regardless of whether KIRO_HOME is set.
//
// The file is cli.json: `kiro-cli settings <key> <value>` (what the
// entrypoint seeding and vibekit's /api/kiro-settings shell out to)
// persists every key there — verified on a live 2.12 deployment where
// settings/ contains only cli.json, holding hooks.showStatus alongside
// the other seeded flags. The previously-read settings.json does not
// exist on current installs, which made this cache permanently return
// the default.
func kiroSettingsPath() string {
	if workspace.KiroHome() == ".kiro" {
		// Defensive fallback inside KiroHome — when both KIRO_HOME
		// and HOME are unset, returning a relative path here means
		// we'd read from CWD/settings/cli.json which is wrong.
		// Caller treats empty as "no overrides; fall through to
		// defaults".
		return ""
	}
	return workspace.KiroSettingsPath("cli.json")
}

// cachedBoolField reads a boolean value from a JSON file, cutting the per-call
// cost to one os.Stat once warm. Staleness is three legs —
// atomicfile.FileIdentity (mtime AND os.SameFile) plus size — because neither
// pair alone catches both a same-mtime rename and a same-inode rewrite. The
// subject is kiro-cli's cli.json, published by RENAME (measured on the 2.19.0
// binary here: a new inode per write), so the SameFile leg is live here. The
// zero identity reports Changed, so the cold cache needs no valid flag.
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

	// The lock is never held across the stat, the read or the unmarshal. This
	// is consulted per tool call, so a slow or hung filesystem would block
	// every caller behind one reader. Two concurrent misses may both read the
	// file, which is benign for a cache: they derive the same answer, and each
	// stores the (identity, size) it actually observed.
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

	// Cache miss — re-read and parse outside the lock.
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

	// Pair the parsed value with the identity stat'ed above. If the file changed
	// in between, the pairing is stale in the safe direction: the next call
	// stats a different generation and re-reads.
	c.mu.Lock()
	c.value, c.id, c.size = parsed, atomicfile.Identify(info), info.Size()
	c.mu.Unlock()
	return parsed
}

// hookStatusCache caches the hooks.showStatus setting from
// ~/.kiro/settings/cli.json, invalidating on the file's identity and size.
// Reduces per-tool-call cost from os.ReadFile+json.Unmarshal to a
// single os.Stat in the common case (file changes at most once per
// user session).
type hookStatusCache struct {
	field *cachedBoolField
}

func newHookStatusCache(path string) *hookStatusCache {
	return &hookStatusCache{field: newCachedBoolField(path, "hooks.showStatus", true)}
}

// IsHookStatusEnabled reads the kiro-cli hooks.showStatus setting, under the
// name translate's role asks for. Returns true (show hooks) on any error or when
// the setting is unset, matching kiro-cli's own default.
//
// The setting is persisted by kiro-cli itself at ~/.kiro/settings/cli.json
// (dotted key, not snake_case), and is toggled through `kiro-cli settings
// hooks.showStatus …` via vibekit's /api/kiro-settings endpoint. Reading from
// vibekit's configDir would be wrong: vibekit's config.json uses underscore keys
// (supervised_default, chat_retention_days, …) and has no entry for this toggle —
// so the prior lookup of "hooks_show_status" against that file was a permanent
// no-op and the Settings → General switch was silently dead. See vibekit.md
// "Experimental kiro-cli flags".
//
// One method where there were three. The runtime carried a forward that NOTHING
// in production called: translate reads the cache directly, and the forward
// stayed alive only because a test called it, which is why `unused` — configured
// to count test usage — could not see it. A `get` shim sat between the two.
func (c *hookStatusCache) IsHookStatusEnabled() bool {
	return c.field.get()
}
