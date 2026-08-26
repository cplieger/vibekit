package policyfile

import (
	"errors"
	"slices"
	"strconv"
	"testing"
)

// signatures projects a file's rules onto the vocabulary SetProfileRules is
// defined in, so an expectation can be written and printed as one comparable
// value. Order is the file's own, because a profile's rules are appended after
// whatever survived and that is the order a reader of permissions.yaml sees.
func signatures(rules []Rule) []string {
	out := make([]string, 0, len(rules))
	for i := range rules {
		out = append(out, Signature(&rules[i]))
	}
	return out
}

func equalSignatures(got, want []Rule) bool {
	g, w := signatures(got), signatures(want)
	if len(g) != len(w) {
		return false
	}
	for i := range g {
		if g[i] != w[i] {
			return false
		}
	}
	return true
}

// TestSetProfileRules_MergesByOwnership is decision 0.3 end to end: a selection
// removes only what the profile mechanism could have written and preserves
// everything else.
//
// The hand-authored rule in these cases is `shell: allow`, which is not arbitrary
// — it is the rule the live container holds, added by hand as the workaround for
// the very defect this change fixes, and the blanket overwrite this replaced would
// have destroyed it silently on the next profile click.
func TestSetProfileRules_MergesByOwnership(t *testing.T) {
	loosest, ok := ProfileFor(ProfileUnrestricted)
	if !ok {
		t.Fatal("ProfileFor(unrestricted) missing")
	}
	handAuthored := Rule{Capability: "shell", Effect: EffectAllow}
	narrower := Rule{Capability: capAll, Effect: EffectAllow, Match: []string{"src/**"}}
	bareAll := Rule{Capability: capAll, Effect: EffectAllow}

	for _, tc := range []struct {
		name     string
		desc     string
		start    []Rule
		incoming []Rule
		want     []Rule
	}{
		{
			name:     "a_restrictive_rung_removes_what_the_loosest_wrote",
			desc:     "switching down the ladder has to genuinely narrow",
			start:    loosest.FileRules,
			incoming: nil,
			want:     nil,
		},
		{
			name:     "a_hand_authored_rule_survives_a_restrictive_rung",
			desc:     "the live container's shell:allow, kept while the profile's own rules go",
			start:    append(append([]Rule{}, loosest.FileRules...), handAuthored),
			incoming: nil,
			want:     []Rule{handAuthored},
		},
		{
			name:     "a_hand_authored_rule_coexists_with_the_profiles_own",
			desc:     "selecting the loosest rung adds to the user's rules rather than replacing them",
			start:    []Rule{handAuthored},
			incoming: loosest.FileRules,
			want:     append([]Rule{handAuthored}, loosest.FileRules...),
		},
		{
			name:     "a_narrower_rule_for_the_same_capability_survives",
			desc:     "a glob list is a different Signature, so only the bare rule is the profile's",
			start:    []Rule{bareAll, narrower},
			incoming: nil,
			want:     []Rule{narrower},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &File{Rules: append([]Rule{}, tc.start...)}
			if err := f.SetProfileRules(tc.incoming); err != nil {
				t.Fatalf("SetProfileRules(%v) on %v: %v", tc.incoming, tc.start, err)
			}
			if !equalSignatures(f.Rules, tc.want) {
				t.Errorf("SetProfileRules(%v) on %v left %v, want %v (%s)",
					tc.incoming, tc.start, f.Rules, tc.want, tc.desc)
			}
		})
	}
}

// TestSetProfileRules_IsIdempotent: the panel repaints from a refetch after every
// selection and a client can re-post the profile already in force, so applying the
// same rules twice must not accumulate a second copy of each. Upsert dedups by
// Signature; what this pins is that the removal pass does not undo that by leaving
// room for a duplicate.
func TestSetProfileRules_IsIdempotent(t *testing.T) {
	loosest, ok := ProfileFor(ProfileUnrestricted)
	if !ok {
		t.Fatal("ProfileFor(unrestricted) missing")
	}
	f := &File{Rules: []Rule{{Capability: "power", Effect: EffectAsk}}}
	for pass := 1; pass <= 2; pass++ {
		if err := f.SetProfileRules(loosest.FileRules); err != nil {
			t.Fatalf("SetProfileRules(%v) pass %d: %v", loosest.FileRules, pass, err)
		}
	}
	want := append([]Rule{{Capability: "power", Effect: EffectAsk}}, loosest.FileRules...)
	if !equalSignatures(f.Rules, want) {
		t.Errorf("SetProfileRules(%v) applied twice left %v, want %v",
			loosest.FileRules, f.Rules, want)
	}
}

// TestSetProfileRules_RefusesAFullFile: the rule cap keeps a pathological file out
// of the policy and out of the CLI argv, and a selection that hits it must report
// the refusal rather than persist a partial posture that grants some capabilities
// and not others.
//
// It also pins the all-or-nothing half, which is what makes the refusal safe for a
// caller that does not know the failure order. The removal pass runs BEFORE the
// Upsert loop, so building in place would hand back a file with the outgoing
// profile's rules already gone and part of the incoming set already added — a
// posture narrower than the old one and looser than the new one, which a caller
// that Saved after the error would write to disk. The staged rule is profile-owned
// so the removal pass is guaranteed to have something to take: without the scratch
// value the received file comes back one rule shorter.
func TestSetProfileRules_RefusesAFullFile(t *testing.T) {
	loosest, ok := ProfileFor(ProfileUnrestricted)
	if !ok {
		t.Fatal("ProfileFor(unrestricted) missing")
	}
	f := &File{Rules: make([]Rule, 0, maxRulesPerFile)}
	// One profile-owned rule the removal pass must drop, then hand-authored filler
	// up to the cap so the first Upsert of the incoming set is refused.
	f.Rules = append(f.Rules, loosest.FileRules[0])
	for i := range maxRulesPerFile - 1 {
		f.Rules = append(f.Rules, Rule{
			Capability: "fs_read", Effect: EffectAsk,
			Match: []string{"p" + strconv.Itoa(i)},
		})
	}
	before := signatures(f.Rules)

	err := f.SetProfileRules(loosest.FileRules)
	if !errors.Is(err, ErrTooManyRules) {
		t.Errorf("SetProfileRules(%v) on a %d-rule file = %v, want ErrTooManyRules",
			loosest.FileRules, maxRulesPerFile, err)
	}
	// Reported as ONE message naming the first divergence rather than per rule: an
	// in-place build shifts all 512 signatures by one, and 512 near-identical lines
	// bury the fact that the file simply lost its profile-owned rule.
	after := signatures(f.Rules)
	if !slices.Equal(after, before) {
		t.Errorf("after a refused SetProfileRules the file holds %d rules (%s), want the %d it "+
			"started with unchanged (%s); a refusal must leave the receiver untouched",
			len(after), firstDivergence(after, before), len(before), before[0])
	}
}

// firstDivergence describes where two signature projections part company, so a
// failure carries one line instead of one per rule. A pure projection, so it takes
// no *testing.T and cannot fail.
func firstDivergence(got, want []string) string {
	for i := range min(len(got), len(want)) {
		if got[i] != want[i] {
			return "rule " + strconv.Itoa(i) + " = " + got[i]
		}
	}
	if len(got) == len(want) {
		return "same rules"
	}
	return "length " + strconv.Itoa(len(got))
}
