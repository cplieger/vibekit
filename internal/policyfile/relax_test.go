package policyfile

import (
	"slices"
	"testing"
)

// TestRelaxCapabilities_ExactSet pins the relaxation's membership. Two entries,
// each load-bearing for a different reason: `all` is the alias that keeps the
// rule short and lets a capability a later KAS version adds to BUILTIN be covered
// without a vibekit release, and `sandbox_network` is the one vocabulary member
// that alias genuinely does not reach. A set of just ["all"] means the switch is
// claiming to allow everything while one capability still asks.
//
// The set is derived, so this is what turns a change to either input (the
// vocabulary snapshot or the alias-membership snapshot) into a deliberate edit
// rather than a silent change to what one click grants.
func TestRelaxCapabilities_ExactSet(t *testing.T) {
	want := []string{"all", "sandbox_network"}
	got := RelaxCapabilities()
	if !slices.Equal(got, want) {
		t.Errorf("RelaxCapabilities() = %v, want %v", got, want)
	}
}

// TestRelaxCapabilities_CoversTheWholeVocabulary is the property the switch's
// LABEL promises: every capability vibekit knows about is either named in the set
// or a member of the `all` alias it names. Written as a coverage check rather than
// a set comparison so a capability added to the vocabulary fails HERE — the
// switch would otherwise keep its name while quietly leaving the newcomer asking.
func TestRelaxCapabilities_CoversTheWholeVocabulary(t *testing.T) {
	relax := RelaxCapabilities()
	for _, c := range Capabilities() {
		if _, alias := umbrellas[c]; alias {
			continue // an alias is not a capability anything is granted for
		}
		if slices.Contains(relax, c) {
			continue
		}
		if _, viaAll := allMembers[c]; viaAll && slices.Contains(relax, "all") {
			continue
		}
		t.Errorf("RelaxCapabilities() leaves %q asking, so the switch cannot claim to allow everything", c)
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

// TestRelaxCapabilities_NamesNoRedundantMember keeps the derivation from
// regrowing the thing it replaced. A member the `all` alias already covers is a
// second rule granting what the first one granted: harmless to the engine, but it
// is one more row in the Active policy list and one more write per click, and a
// full set of them is the twelve-rule shape this switch deliberately is not.
func TestRelaxCapabilities_NamesNoRedundantMember(t *testing.T) {
	for _, c := range RelaxCapabilities() {
		if c == "all" {
			continue
		}
		if _, covered := allMembers[c]; covered {
			t.Errorf("RelaxCapabilities() names %q, which the `all` rule beside it already covers", c)
		}
	}
}

// TestAllMembers_AreRealCapabilities keeps the snapshot of KAS's alias table
// honest. A typo here silently moves a capability out of "covered by all" and
// into the explicit remainder, which changes what the switch writes without
// changing what it claims.
func TestAllMembers_AreRealCapabilities(t *testing.T) {
	for c := range allMembers {
		if _, ok := suggestedCapabilities[c]; !ok {
			t.Errorf("allMembers names %q, which is not a known capability", c)
		}
		if _, alias := umbrellas[c]; alias {
			t.Errorf("allMembers names the umbrella %q; an alias is not a member of another alias", c)
		}
	}
}

// TestUmbrellas_AreRealCapabilities is the same guard on the other snapshot: a
// name here that is not a capability excludes nothing, and a capability MISSING
// here would be written as its own rule beside the alias that already covers it.
func TestUmbrellas_AreRealCapabilities(t *testing.T) {
	for c := range umbrellas {
		if _, ok := suggestedCapabilities[c]; !ok {
			t.Errorf("umbrellas names %q, which is not a known capability", c)
		}
	}
}

// TestRelaxCapabilities_RulesAreExactlyReversible pins the property the UI's off
// path depends on: the bare rules the relaxation writes are removable by
// Signature, and removing them leaves a narrower hand-authored rule for the same
// capability in place. A match/exclude on the written rule would break both
// halves.
//
// The hand-authored rule here is `power: ask`, which is not an arbitrary choice:
// it is the documented way to keep the one prompt this switch removes that
// somebody might want back, so it is the rule most likely to be sitting beside
// the relaxation in a real file.
func TestRelaxCapabilities_RulesAreExactlyReversible(t *testing.T) {
	handAuthored := Rule{Capability: "power", Effect: EffectAsk}
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
