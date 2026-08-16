package policyfile

import (
	"slices"
	"testing"
)

// TestRelaxCapabilities_ExactSet pins the workspace relaxation's membership.
// The set is derived, so this is what turns a change to either input (the
// vocabulary snapshot or the exclusion list) into a deliberate edit rather than
// a silent widening of a blanket allow.
func TestRelaxCapabilities_ExactSet(t *testing.T) {
	want := []string{
		"context",
		"diagnostics",
		"fs_read",
		"fs_write",
		"mcp",
		"sandbox_network",
		"shell",
		"skill",
		"subagent",
		"web_fetch",
		"web_search",
	}
	got := RelaxCapabilities()
	if !slices.Equal(got, want) {
		t.Errorf("RelaxCapabilities() = %v, want %v", got, want)
	}
}

// TestRelaxCapabilities_ExcludesUmbrellasAndPower states each exclusion as its
// own assertion, so a failure names which reason was violated instead of
// reporting a set diff. An umbrella back in the set means the relaxation grants
// more than its members and can no longer be reversed member-by-member; `power`
// back in the set means Cedar has stopped guarding the one path where it is the
// only guard.
func TestRelaxCapabilities_ExcludesUmbrellasAndPower(t *testing.T) {
	got := RelaxCapabilities()
	for _, c := range []string{"all", "builtin", "filesystem"} {
		if slices.Contains(got, c) {
			t.Errorf("RelaxCapabilities() contains the umbrella %q: it over-grants and makes reversal all-or-nothing", c)
		}
	}
	if slices.Contains(got, "power") {
		t.Error("RelaxCapabilities() contains \"power\": a power's MCP servers are governed by Cedar alone, so a blanket allow removes their only guard")
	}
}

// TestRelaxCapabilities_SubsetOfVocabulary is the derivation's real guard: every
// member must be a capability the snapshot knows, or the relaxation would write
// a rule KAS skips as unknown — silently, since an unknown capability is
// non-fatal and only rides policy/changed's warnings array.
func TestRelaxCapabilities_SubsetOfVocabulary(t *testing.T) {
	vocabulary := Capabilities()
	for _, c := range RelaxCapabilities() {
		if !slices.Contains(vocabulary, c) {
			t.Errorf("RelaxCapabilities() names %q, which is not in the capability vocabulary", c)
		}
	}
}

// TestRelaxCapabilities_ExclusionsAreRealCapabilities keeps the exclusion list
// honest in the other direction: an entry naming nothing in the vocabulary looks
// like a guard while excluding no capability at all, which is how a typo becomes
// a silent widening.
func TestRelaxCapabilities_ExclusionsAreRealCapabilities(t *testing.T) {
	for c := range relaxExcluded {
		if _, ok := suggestedCapabilities[c]; !ok {
			t.Errorf("relaxExcluded names %q, which is not a known capability; it excludes nothing", c)
		}
	}
}

// TestRelaxCapabilities_RulesAreExactlyReversible pins the property the UI's off
// path depends on: the bare rules the relaxation writes are removable by
// Signature, and removing them leaves a narrower hand-authored rule for the same
// capability in place. A match/exclude on the written rule would break both
// halves.
func TestRelaxCapabilities_RulesAreExactlyReversible(t *testing.T) {
	handAuthored := Rule{Capability: "fs_write", Effect: EffectAllow, Match: []string{"src/**"}}
	f := &File{Rules: []Rule{handAuthored}}

	caps := RelaxCapabilities()
	for i := range caps {
		r := Rule{Capability: caps[i], Effect: EffectAllow}
		changed, err := f.Upsert(&r)
		if err != nil {
			t.Fatalf("Upsert(%s): %v", caps[i], err)
		}
		if !changed {
			t.Errorf("Upsert(%s) reported no change; the relaxation would silently write nothing", caps[i])
		}
	}
	if len(f.Rules) != len(caps)+1 {
		t.Fatalf("after relaxation the file holds %d rules, want %d", len(f.Rules), len(caps)+1)
	}

	for i := range caps {
		r := Rule{Capability: caps[i], Effect: EffectAllow}
		if !f.Remove(&r) {
			t.Errorf("Remove(%s) found nothing to remove; the relaxation is not reversible", caps[i])
		}
	}
	if len(f.Rules) != 1 || Signature(&f.Rules[0]) != Signature(&handAuthored) {
		t.Errorf("reversing the relaxation left %v, want only the hand-authored rule", f.Rules)
	}
}
