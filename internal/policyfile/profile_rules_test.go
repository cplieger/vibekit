package policyfile

import (
	"slices"
	"testing"
)

// TestProfiles_OnlyTheLoosestRungWritesFileRules is the restrictive-rung guard,
// and the reason the fix is scoped the way it is.
//
// A rule at USER scope is durable: it survives a vibekit restart and applies to
// every ACP client sharing this HOME, with no session boundary to expire it. So
// materialising guarded, read-only or trusted would WIDEN those postures rather
// than deliver them — read-only's read-all in particular would leave every file on
// the machine readable forever. Only the rung whose own description is "never
// asks" earns one.
func TestProfiles_OnlyTheLoosestRungWritesFileRules(t *testing.T) {
	for _, tc := range []struct {
		id        string
		wantRules bool
	}{
		{ProfileGuarded, false},
		{ProfileReadOnly, false},
		{ProfileTrusted, false},
		{ProfileUnrestricted, true},
		{ProfileCustom, false},
	} {
		t.Run(tc.id, func(t *testing.T) {
			p, ok := ProfileFor(tc.id)
			if !ok {
				t.Fatalf("ProfileFor(%q) missing; the ladder is incomplete", tc.id)
			}
			if got := len(p.FileRules) > 0; got != tc.wantRules {
				t.Errorf("ProfileFor(%q).FileRules = %v, want any rules = %t",
					tc.id, p.FileRules, tc.wantRules)
			}
		})
	}
}

// TestProfiles_FileRulesAreBareAllows pins two properties of what a profile
// writes, each load-bearing for a different reason.
//
// Bare (nil match, nil exclude) is what makes ownership removal exact: Signature
// keys on capability + effect + sorted globs, so a rule carrying a glob list could
// not be removed by the value that wrote it, and SetProfileRules would leave the
// outgoing rung's grant standing.
//
// Allow-only is what keeps KAS's shell hazard unreachable. evaluateShell
// short-circuits to allow only while EVERY loaded shell rule is an allow; one ask
// or deny routes every command through tree-sitter, where a command substitution
// or a parse error returns ask whatever allow rule matched. So a materialised
// "restrictive" rung would INCREASE prompts across unrelated commands.
func TestProfiles_FileRulesAreBareAllows(t *testing.T) {
	for _, p := range Profiles() {
		for i := range p.FileRules {
			r := p.FileRules[i]
			if r.Effect != EffectAllow {
				t.Errorf("profile %q writes %q with effect %q, want %q",
					p.ID, r.Capability, r.Effect, EffectAllow)
			}
			if r.Match != nil || r.Exclude != nil {
				t.Errorf("profile %q writes %q with match %v exclude %v; want both nil, or Signature removal is not exact",
					p.ID, r.Capability, r.Match, r.Exclude)
			}
		}
	}
}

// TestProfileUnrestricted_FileRulesMatchRelaxCapabilities pins the two
// derivations together.
//
// RelaxCapabilities() is vibekit's tested answer to "the broadest grant a
// permissions file can express" — `all`, plus the one vocabulary member that alias
// genuinely does not reach — and the loosest rung claims exactly that ("Never
// asks"). A drift between the two would be a rung whose description promises a
// posture its rules do not write, which is the defect this whole change exists to
// remove.
func TestProfileUnrestricted_FileRulesMatchRelaxCapabilities(t *testing.T) {
	p, ok := ProfileFor(ProfileUnrestricted)
	if !ok {
		t.Fatal("ProfileFor(unrestricted) missing")
	}
	got := make([]string, 0, len(p.FileRules))
	for i := range p.FileRules {
		got = append(got, p.FileRules[i].Capability)
	}
	slices.Sort(got)
	want := RelaxCapabilities()
	if !slices.Equal(got, want) {
		t.Errorf("ProfileFor(%q).FileRules capabilities = %v, want RelaxCapabilities() = %v",
			ProfileUnrestricted, got, want)
	}
}

// TestProfileOwnedRules_CoversEveryRung: the removal set is the union over the
// ladder, so no rung can leave its rules orphaned on disk once the user moves off
// it. Deriving it from the OUTGOING rung instead would make the removal depend on
// a setting that can be absent or stale, and a narrowing that failed to narrow is
// the worst outcome this mechanism has.
func TestProfileOwnedRules_CoversEveryRung(t *testing.T) {
	owned := ProfileOwnedRules()
	if len(owned) == 0 {
		t.Fatal("ProfileOwnedRules() is empty; a profile selection would remove nothing")
	}
	sigs := make(map[string]struct{}, len(owned))
	for i := range owned {
		sigs[Signature(&owned[i])] = struct{}{}
	}
	for _, p := range Profiles() {
		for i := range p.FileRules {
			r := p.FileRules[i]
			if _, ok := sigs[Signature(&r)]; !ok {
				t.Errorf("ProfileOwnedRules() omits %q %q, which profile %q writes; selecting another profile would leave it on disk",
					r.Capability, r.Effect, p.ID)
			}
		}
	}
}
