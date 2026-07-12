package workspace

import (
	"sync"
	"testing"
)

// With no resolver configured, KiroHome falls back to $HOME/.kiro on
// every call (no caching), so a t.Setenv("HOME", ...) is reflected.
func TestKiroHome_fallsBackToHomeDotKiro(t *testing.T) {
	prev := kiroHomeResolver
	kiroHomeResolver = nil // force the os.UserHomeDir fallback path
	t.Cleanup(func() { kiroHomeResolver = prev })

	t.Setenv("HOME", "/test-home")
	if got := KiroHome(); got != "/test-home/.kiro" {
		t.Errorf("KiroHome() = %q, want %q", got, "/test-home/.kiro")
	}
}

// When a resolver is configured, KiroHome returns its result and caches it
// via sync.Once, so the resolver runs exactly once across repeated calls.
func TestKiroHome_resolverResultCachedOnce(t *testing.T) {
	prevResolver := kiroHomeResolver
	prevHome := kiroHome
	t.Cleanup(func() {
		kiroHomeResolver = prevResolver
		kiroHome = prevHome
		kiroHomeOnce = sync.Once{}
	})

	calls := 0
	kiroHomeResolver = func() string {
		calls++
		return "/resolved/kiro"
	}
	kiroHomeOnce = sync.Once{} // reset so the resolver fires fresh

	if got := KiroHome(); got != "/resolved/kiro" {
		t.Fatalf("KiroHome() = %q, want %q", got, "/resolved/kiro")
	}
	if got := KiroHome(); got != "/resolved/kiro" {
		t.Fatalf("KiroHome() second call = %q, want %q", got, "/resolved/kiro")
	}
	if calls != 1 {
		t.Errorf("resolver invoked %d times, want 1 (sync.Once caching)", calls)
	}
}

// The Kiro path helpers join their name argument under the matching
// subdirectory of KiroHome(). The "steering" / "settings" segments are
// load-bearing: kiro-cli and vibekit must agree on them.
func TestKiroPathHelpers(t *testing.T) {
	prev := kiroHomeResolver
	kiroHomeResolver = nil
	t.Cleanup(func() { kiroHomeResolver = prev })
	t.Setenv("HOME", "/cfg") // KiroHome() => /cfg/.kiro via the fallback

	if got, want := KiroSteeringPath("custom.md"), "/cfg/.kiro/steering/custom.md"; got != want {
		t.Errorf("KiroSteeringPath = %q, want %q", got, want)
	}
	if got, want := KiroSteeringPath("sub/file.md"), "/cfg/.kiro/steering/sub/file.md"; got != want {
		t.Errorf("KiroSteeringPath(nested) = %q, want %q", got, want)
	}
	if got, want := KiroSettingsPath("global.json"), "/cfg/.kiro/settings/global.json"; got != want {
		t.Errorf("KiroSettingsPath = %q, want %q", got, want)
	}
}
