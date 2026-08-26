package policyfile

import (
	"slices"
	"testing"
)

// TestPresetIDs_MatchKAS is the contract test the whole profile mechanism rests
// on, and it guards a failure mode worse than a wrong grant.
//
// KAS's validatePresetIds throws InvalidParamsError on an unknown id, so
// `session/new` never completes: an upstream rename does not quietly grant less,
// it takes every chat down at its first prompt. The ids below are therefore a hard
// dependency, snapshotted from the 2.19.1 PRESET_REGISTRY, and no RPC enumerates
// them so this list cannot be derived at runtime.
//
// Keep this in sync BY HAND on a kiro-cli bump, the same discipline kascap's
// census uses: grep the bundle for `var PRESET_REGISTRY` and compare. The
// alternative — deriving the set from the bundle at test time — was declined
// because the bundle is not present in CI.
func TestPresetIDs_MatchKAS(t *testing.T) {
	want := []string{
		"allow-all",
		"dev-shell",
		"edit-workspace",
		"read-all",
		"read-only-shell",
		"read-workspace",
	}
	got := []string{
		PresetAllowAll, PresetDevShell, PresetEditWorkspace,
		PresetReadAll, PresetReadOnlyShell, PresetReadWorkspace,
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("preset ids = %v, want %v (KAS 2.19.1 PRESET_REGISTRY)", got, want)
	}
}

// TestProfiles_OnlyNameKnownPresets is the same guard applied to the ladder: a
// profile naming a preset id that is not in the registry is a profile that cannot
// start a session at all.
func TestProfiles_OnlyNameKnownPresets(t *testing.T) {
	known := []string{
		PresetAllowAll, PresetDevShell, PresetEditWorkspace,
		PresetReadAll, PresetReadOnlyShell, PresetReadWorkspace,
	}
	for _, p := range Profiles() {
		for _, id := range p.Presets {
			if !slices.Contains(known, id) {
				t.Errorf("profile %q names unknown preset %q; session/new would fail outright", p.ID, id)
			}
		}
	}
}

// TestProfiles_AreAMonotonicLadder pins the property the picker's ORDER claims:
// each named profile's presets are a superset of the one before it, so moving down
// the list only ever widens.
//
// Yolo is the deliberate exception and the assertion says so rather than skipping
// it: `allow-all` is a single alias that subsumes every other preset's effect
// without naming any of them, so a set comparison is the wrong test there and the
// right one is that it grants the umbrella.
func TestProfiles_AreAMonotonicLadder(t *testing.T) {
	order := []string{ProfileGuarded, ProfileReadOnly, ProfileTrusted}
	var prev []string
	for _, id := range order {
		p, ok := ProfileFor(id)
		if !ok {
			t.Fatalf("ProfileFor(%q) missing; the ladder is incomplete", id)
		}
		for _, earlier := range prev {
			if !slices.Contains(p.Presets, earlier) {
				t.Errorf("profile %q drops preset %q that a stricter profile grants; the ladder is not monotonic", id, earlier)
			}
		}
		if len(p.Presets) <= len(prev) {
			t.Errorf("profile %q grants %d presets, no more than the stricter %d; it adds nothing", id, len(p.Presets), len(prev))
		}
		prev = p.Presets
	}
	yolo, ok := ProfileFor(ProfileUnrestricted)
	if !ok {
		t.Fatal("ProfileFor(unrestricted) missing")
	}
	if !slices.Contains(yolo.Presets, PresetAllowAll) {
		t.Errorf("unrestricted = %v, want the allow-all umbrella", yolo.Presets)
	}
}

// TestProfileCustom_SendsNoPresets is what makes the files authoritative in Custom.
// A single preset here would silently keep a floor the editable table does not
// show, so the table would stop being the whole policy it claims to be.
func TestProfileCustom_SendsNoPresets(t *testing.T) {
	p, ok := ProfileFor(ProfileCustom)
	if !ok {
		t.Fatal("ProfileFor(custom) missing")
	}
	if len(p.Presets) != 0 {
		t.Errorf("custom sends %v; it must send nothing, or the table is not the whole policy", p.Presets)
	}
}

// TestProfileFor_ReportsAbsenceInBand keeps the two callers honest. A default
// returned for an unknown id would hand the session door a posture nobody chose;
// the settings reader wants the fallback, and it applies it itself with a log line.
func TestProfileFor_ReportsAbsenceInBand(t *testing.T) {
	if _, ok := ProfileFor("nonexistent"); ok {
		t.Error("ProfileFor reported an unknown id as found")
	}
	if p, ok := ProfileFor(DefaultProfile); !ok || p.ID != ProfileGuarded {
		t.Errorf("DefaultProfile %q does not resolve to guarded", DefaultProfile)
	}
}

// TestProfiles_ReturnsAnIsolatedCopy: the value being handed out is a security
// posture, so a caller appending to its Presets must not rewrite the profile for
// every later caller.
func TestProfiles_ReturnsAnIsolatedCopy(t *testing.T) {
	first := Profiles()
	for i := range first {
		first[i].Presets = append(first[i].Presets, "tampered")
	}
	for _, p := range Profiles() {
		if slices.Contains(p.Presets, "tampered") {
			t.Fatalf("profile %q kept a caller's append; Profiles() shares its backing array", p.ID)
		}
	}
	if p, _ := ProfileFor(ProfileUnrestricted); slices.Contains(p.Presets, "tampered") {
		t.Error("ProfileFor shares its backing array with the package copy")
	}
}
