// Package workspace provides path helpers for kiro-cli's per-user state directory.
package workspace

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
// at the composition root rather than buried in the vibekit package.
func SetKiroHomeResolver(fn func() string) {
	kiroHomeResolver = fn
}

// KiroHome returns the directory kiro-cli uses for per-user state
// (sessions, settings, agents, steering, logs). Resolution order:
//
//  1. $KIRO_HOME if set (kiro-cli 2.3+ override, honored by the Rust
//     wrapper only)
//  2. $HOME/.kiro otherwise
//
// The Dockerfile sets KIRO_HOME=/config/home/.kiro, i.e. $HOME/.kiro.
// The two MUST stay equal: the v3 engine (KAS) never reads KIRO_HOME —
// it resolves its home as os.homedir()/.kiro (verified against the KAS
// 2.12 bundle: zero KIRO_HOME references; the only override is a
// --home-dir argv the Rust wrapper never passes) — while the Rust
// wrapper (`kiro-cli settings`, v2 paths) honors KIRO_HOME. Pointing
// KIRO_HOME inside HOME is what makes vibekit, the wrapper, and KAS
// agree on one directory. Code that reads or writes kiro-cli state
// must go through this helper.
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

// SetKiroHomeForTest overrides the kiro home for testing, registering a
// t.Cleanup that restores the previous state.
//
// It installs a RESOLVER as well as the cached value. Seeding the cache alone
// was not enough and silently did nothing: KiroHome() only consults the cache
// when a resolver is present, and in a test binary there is none — so it fell
// through to os.UserHomeDir() and the "override" was ignored. A test using this
// for ISOLATION then wrote the developer's real ~/.kiro, which is exactly the
// failure this helper exists to prevent.
func SetKiroHomeForTest(t interface {
	Helper()
	Cleanup(func())
}, path string,
) {
	t.Helper()
	oldHome, oldResolver := kiroHome, kiroHomeResolver
	// Replace the entire sync.Once with a fresh one to invalidate the cache.
	// Don't copy the existing kiroHomeOnce by value (sync.Once cannot be copied).
	kiroHome = path
	kiroHomeResolver = func() string { return path }
	kiroHomeOnce = sync.Once{}
	kiroHomeOnce.Do(func() {}) // mark as done so KiroHome() returns the override
	t.Cleanup(func() {
		kiroHome, kiroHomeResolver = oldHome, oldResolver
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
