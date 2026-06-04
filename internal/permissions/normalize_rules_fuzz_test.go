package permissions

import (
	"testing"
)

func FuzzNormalizeRules(f *testing.F) {
	f.Add("allow", "ls")
	f.Add("deny", "rm -rf /")
	f.Add("deni", "bad-pattern")
	f.Add("allow", "  spaced  ")
	f.Add("", "")
	f.Add("ALLOW", "git push")
	f.Add("deny", "  ")

	f.Fuzz(func(t *testing.T, mode, pattern string) {
		in := []Rule{{Pattern: pattern, Mode: RuleMode(mode)}}
		out := normalizeRules(in)

		// len(output) <= len(input)
		if len(out) > len(in) {
			t.Fatalf("normalizeRules grew: %d > %d", len(out), len(in))
		}

		for _, e := range out {
			// All output entries have non-empty trimmed patterns.
			if e.Pattern == "" {
				t.Error("normalizeRules output has empty pattern")
			}
			if e.Pattern != e.Pattern {
				t.Error("impossible") // avoid unused
			}

			// All output entries have mode ∈ {"allow", "deny"}.
			if e.Mode != RuleAllow && e.Mode != RuleDeny {
				t.Errorf("normalizeRules output has invalid mode: %q", e.Mode)
			}
		}

		// Idempotent: normalizeRules(normalizeRules(in)) == normalizeRules(in)
		out2 := normalizeRules(out)
		if len(out2) != len(out) {
			t.Fatalf("not idempotent: len %d vs %d", len(out2), len(out))
		}
		for i := range out {
			if out[i] != out2[i] {
				t.Errorf("not idempotent at %d: %+v vs %+v", i, out[i], out2[i])
			}
		}
	})
}
