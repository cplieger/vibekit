// Path helpers for kiro-cli's per-user state directory.
package api

import (
	"os"
	"path/filepath"
	"sync"
)

// kiroHome is the cached result of resolving the kiro home directory.
// Resolved once via kiroHomeOnce; tests override via SetKiroHomeForTest.
var kiroHome string

var kiroHomeOnce sync.Once

// kiroHomeResolver is the function used to resolve the kiro home path.
// Set via SetKiroHomeResolver at startup from the config layer (main.go).
// When not set, falls back to $HOME/.kiro via os.UserHomeDir().
var kiroHomeResolver func() string

// SetKiroHomeResolver sets the function that resolves the kiro home
// directory. Must be called once at startup from the config layer
// before any call to KiroHome(). This keeps the env-var read explicit
// at the composition root rather than buried in the api package.
func SetKiroHomeResolver(fn func() string) {
	kiroHomeResolver = fn
}

// KiroHome returns the directory kiro-cli uses for per-user state
// (auth.db, sessions/cli, settings, agents, steering). Resolution
// order:
//
//  1. $KIRO_HOME if set (kiro-cli 2.3+ canonical override)
//  2. $HOME/.kiro otherwise
//
// Vibekit's entrypoint sets KIRO_HOME=/config/kiro so this resolves
// to a sibling of /config/chats and /config/mcp.json rather than
// being buried inside the user's HOME. Code that reads or writes
// kiro-cli state must go through this helper so vibekit and kiro-cli
// agree on where state lives.
func KiroHome() string {
	if kiroHomeResolver != nil {
		kiroHomeOnce.Do(func() {
			kiroHome = kiroHomeResolver()
		})
		return kiroHome
	}
	// Fallback: resolve on every call (no caching) so tests using
	// t.Setenv("HOME", ...) see the updated value.
	home, err := os.UserHomeDir()
	if err != nil {
		return ".kiro"
	}
	return filepath.Join(home, ".kiro")
}

// SetKiroHomeForTest overrides the cached kiro home for testing.
// It registers a t.Cleanup to restore the original value.
func SetKiroHomeForTest(t interface {
	Helper()
	Cleanup(func())
}, path string) {
	t.Helper()
	old := kiroHome
	// Replace the entire sync.Once with a fresh one to invalidate the cache.
	// Don't copy the existing kiroHomeOnce by value (sync.Once cannot be copied).
	kiroHome = path
	kiroHomeOnce = sync.Once{}
	kiroHomeOnce.Do(func() {}) // mark as done so KiroHome() returns the override
	t.Cleanup(func() {
		kiroHome = old
		kiroHomeOnce = sync.Once{}
		kiroHomeOnce.Do(func() {})
	})
}

// KiroSteeringPath returns the path to a file under KiroHome()/steering/.
func KiroSteeringPath(name string) string {
	return filepath.Join(KiroHome(), "steering", name)
}

// KiroSettingsPath returns the path to a file under KiroHome()/settings/.
func KiroSettingsPath(name string) string {
	return filepath.Join(KiroHome(), "settings", name)
}

// KiroSessionsCLIDir returns the directory kiro-cli uses for v2 ACP
// session state files (~/.kiro/sessions/cli or $KIRO_HOME/sessions/cli).
func KiroSessionsCLIDir() string {
	return filepath.Join(KiroHome(), "sessions", "cli")
}
