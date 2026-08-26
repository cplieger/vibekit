package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/cplieger/vibekit/internal/policyfile"
	"github.com/cplieger/vibekit/internal/settings"
)

// writeProfileSetting writes a config.json carrying just the security profile.
// A helper that FAILS inside itself rather than returning an error, because a
// fixture that cannot be staged is an environment failure and not a result any
// case wants to branch on.
func writeProfileSetting(t *testing.T, dir, value string) {
	t.Helper()
	body := map[string]any{}
	if value != "" {
		body[settings.KeySecurityProfile] = value
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
}

// TestSecurityPresets_ResolvesEachProfile pins that every profile in the ladder
// reaches the session door as its own preset set, and that Custom reaches it as
// none. The Custom row is the one that matters most: an empty result is what
// withholds the wire key and makes the permissions files the whole policy, so a
// fallback creeping in there would put a floor back that the editable table does
// not show.
func TestSecurityPresets_ResolvesEachProfile(t *testing.T) {
	for _, p := range policyfile.Profiles() {
		t.Run(p.ID, func(t *testing.T) {
			dir := t.TempDir()
			writeProfileSetting(t, dir, p.ID)
			got := securityPresets(t.Context(), dir)
			if !slices.Equal(got, p.Presets) {
				t.Errorf("securityPresets(%q) = %v, want %v", p.ID, got, p.Presets)
			}
		})
	}
}

// TestSecurityPresets_FallsBackLoudlyNotSilently is the guard on the failure that
// would look like a broken agent rather than a bad setting.
//
// An unknown id must resolve to the default profile, NOT to an empty set. Empty
// is the Custom profile's wire, so a typo in config.json would otherwise remove
// the fs_read floor and leave the agent asking permission to read a file, with
// nothing anywhere naming the cause.
func TestSecurityPresets_FallsBackLoudlyNotSilently(t *testing.T) {
	fallback, ok := policyfile.ProfileFor(policyfile.DefaultProfile)
	if !ok {
		t.Fatal("DefaultProfile does not resolve; the fallback itself is broken")
	}
	for _, value := range []string{"", "nonexistent", "Guarded", "yolo"} {
		t.Run("value="+value, func(t *testing.T) {
			dir := t.TempDir()
			writeProfileSetting(t, dir, value)
			got := securityPresets(t.Context(), dir)
			if len(got) == 0 {
				t.Fatalf("securityPresets(%q) returned nothing; that is the Custom wire and would drop the fs_read floor", value)
			}
			if !slices.Equal(got, fallback.Presets) {
				t.Errorf("securityPresets(%q) = %v, want the default profile's %v", value, got, fallback.Presets)
			}
		})
	}
}

// TestSecurityPresets_AbsentConfigResolvesToDefault covers a first boot, where no
// config.json exists at all. Same requirement as a malformed value and a separate
// case because it takes a different path through the settings reader.
func TestSecurityPresets_AbsentConfigResolvesToDefault(t *testing.T) {
	fallback, _ := policyfile.ProfileFor(policyfile.DefaultProfile)
	got := securityPresets(t.Context(), t.TempDir())
	if !slices.Equal(got, fallback.Presets) {
		t.Errorf("securityPresets with no config.json = %v, want %v", got, fallback.Presets)
	}
}

// TestSecurityPresets_CallerCannotMutateTheProfile: the returned slice reaches
// StartOpts and then the wire, and a caller appending to it must not rewrite the
// profile for every later session in the process.
func TestSecurityPresets_CallerCannotMutateTheProfile(t *testing.T) {
	dir := t.TempDir()
	writeProfileSetting(t, dir, policyfile.ProfileTrusted)
	first := securityPresets(t.Context(), dir)
	if len(first) == 0 {
		t.Fatal("trusted resolved to nothing")
	}
	for i := range first {
		first[i] = "tampered"
	}
	second := securityPresets(t.Context(), dir)
	if slices.Contains(second, "tampered") {
		t.Error("securityPresets hands out the package's own slice; one caller's write reaches every later session")
	}
}
