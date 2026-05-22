package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// kiroSettingsPath returns the path to kiro-cli's settings file.
// Extracted so the composition root can compute the path once and
// inject it into hookStatusCache, making the dependency explicit
// and testable without manipulating HOME.
func kiroSettingsPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		// No home means no settings path. Caller handles empty string
		// as "no overrides; fall through to defaults".
		return ""
	}
	return filepath.Join(home, ".kiro", "settings", "settings.json")
}

// hookStatusCache caches the hooks.showStatus setting from
// ~/.kiro/settings/settings.json with mtime-based invalidation.
// Reduces per-tool-call cost from os.ReadFile+json.Unmarshal to a
// single os.Stat in the common case (file changes at most once per
// user session).
type hookStatusCache struct {
	mtime   time.Time
	path    string
	size    int64
	mu      sync.Mutex
	enabled bool
	valid   bool
}

func newHookStatusCache(path string) *hookStatusCache {
	return &hookStatusCache{path: path, enabled: true}
}

func (c *hookStatusCache) get() bool {
	if c.path == "" {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	info, err := os.Stat(c.path)
	if err != nil {
		return true
	}
	if c.valid && info.ModTime().Equal(c.mtime) && info.Size() == c.size {
		return c.enabled
	}
	// Cache miss — re-read and parse.
	data, err := os.ReadFile(c.path) // #nosec G304 -- fixed path
	if err != nil {
		return true
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return true
	}
	v, ok := raw["hooks.showStatus"]
	if !ok {
		c.enabled = true
	} else {
		var enabled bool
		if json.Unmarshal(v, &enabled) != nil {
			c.enabled = true
		} else {
			c.enabled = enabled
		}
	}
	c.mtime = info.ModTime()
	c.size = info.Size()
	c.valid = true
	return c.enabled
}

// isHookStatusEnabled reads the kiro-cli hooks.showStatus setting.
// Returns true (show hooks) on any error or when the setting is
// unset, matching kiro-cli's own default.
//
// The setting is persisted by kiro-cli itself at
// ~/.kiro/settings/settings.json (dotted key, not snake_case),
// and is toggled through `kiro-cli settings hooks.showStatus …`
// via vibekit's /api/kiro-settings endpoint. Reading from vibekit's
// configDir would be wrong: vibekit's settings.json uses underscore
// keys (shell_policy, trust_tools, …) and has no entry for this
// toggle — so the prior lookup of "hooks_show_status" against that
// file was a permanent no-op and the Settings → General switch was
// silently dead. See vibekit.md "Experimental kiro-cli flags".
func (h *Hub) isHookStatusEnabled() bool {
	return h.hookStatus.get()
}
