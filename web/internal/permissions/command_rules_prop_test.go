package permissions

import (
	"fmt"
	"strings"
	"testing"

	"vibekit/internal/permissions/eval"

	"pgregory.net/rapid"
)

func TestEvaluateShellCommand_RapidMetacharWithRules(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		dir := t.TempDir()
		rules := NewCommandRules(dir)

		// Generate 1-5 allow rules from safe patterns (no metachar).
		nRules := rapid.IntRange(1, 5).Draw(rt, "nRules")
		for i := range nRules {
			pattern := rapid.StringMatching(`[a-z]{1,5}( \*)?`).Draw(rt, fmt.Sprintf("pattern%d", i))
			_ = rules.Add(pattern, RuleAllow)
		}

		// Generate a command that contains at least one shell metacharacter.
		base := rapid.StringMatching(`[a-z]{1,10}`).Draw(rt, "base")
		meta := rapid.SampledFrom([]string{";", "|", "&", "`", "$", ">", "<"}).Draw(rt, "meta")
		suffix := rapid.StringMatching(`[a-z]{0,5}`).Draw(rt, "suffix")
		command := base + meta + suffix

		// Under policySafe, a command with metachar must NEVER get ShellAllow.
		result := eval.EvaluateShellCommand(eval.PolicySafe, command, rules)
		if result == eval.ShellAllow {
			t.Errorf("EvaluateShellCommand(safe, %q, rules) = allow; metachar guard violated", command)
		}
	})
}

func TestCommandRules_RapidNormalizeRejectsMetachars(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// Generate a pattern containing a shell metacharacter.
		base := rapid.StringMatching(`[a-z]{1,5}`).Draw(rt, "base")
		meta := rapid.SampledFrom([]string{";", "|", "&", "`", "$", ">", "<"}).Draw(rt, "meta")
		pattern := base + meta

		// normalizeRules should not reject patterns with metachar
		// (that's a valid pattern — it just won't match clean commands).
		// But the MetaGuard ensures such patterns can't match clean commands.
		// Verify: MatchPattern(metachar_pattern, clean_command) == false.
		cleanCmd := rapid.StringMatching(`[a-z ]{1,20}`).Draw(rt, "cleanCmd")
		if !strings.ContainsAny(pattern, eval.ShellMetacharacters) {
			return // skip if our generation didn't produce a metachar
		}
		if eval.MatchPattern(pattern, cleanCmd) {
			t.Errorf("MatchPattern(%q, %q) = true; metachar pattern matched clean command", pattern, cleanCmd)
		}
	})
}
