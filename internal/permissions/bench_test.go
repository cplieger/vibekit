package permissions

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/vibekit/internal/permissions/eval"
)

// writeSettingsB is the benchmark variant of writeSettings.
func writeSettingsB(b *testing.B, body string) string {
	b.Helper()
	dir := b.TempDir()
	if body != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
			b.Fatalf("write settings: %v", err)
		}
	}
	return dir
}

// --- EvaluateShellCommand per-invocation latency (test-arch-b11-p4) ---

func BenchmarkEvaluateShellCommand(b *testing.B) {
	for _, ruleCount := range []int{0, 10, 50} {
		b.Run(fmt.Sprintf("rules=%d", ruleCount), func(b *testing.B) {
			dir := writeSettingsB(b, `{"shell_policy":"safe_commands"}`)
			r := NewCommandRules(dir)
			for i := range ruleCount {
				_ = r.Add(fmt.Sprintf("bench-pattern-%d *", i), RuleAllow)
			}
			cmd := "git status"
			ctx := context.Background()
			b.ResetTimer()
			for range b.N {
				EvaluateShellCommand(ctx, dir, cmd, r)
			}
		})
	}
}

func BenchmarkMatchPattern(b *testing.B) {
	patterns := []struct {
		name    string
		pattern string
	}{
		{"exact", "npm install"},
		{"prefix_wildcard", "npm *"},
		{"infix_wildcard", "docker * build"},
	}
	commands := []string{
		"npm install",
		"npm run build",
		"docker compose build",
		"git status",
		"ls -la",
	}

	for _, ruleCounts := range []int{1, 10, 50} {
		for _, p := range patterns {
			b.Run(fmt.Sprintf("%s/%d_rules", p.name, ruleCounts), func(b *testing.B) {
				// Build a slice of patterns to simulate rule iteration.
				rules := make([]string, ruleCounts)
				for i := range rules {
					if i == ruleCounts-1 {
						rules[i] = p.pattern // matching rule last
					} else {
						rules[i] = fmt.Sprintf("no-match-pattern-%d *", i)
					}
				}
				cmd := commands[0]
				b.ResetTimer()
				for range b.N {
					for _, r := range rules {
						matchPattern(r, cmd)
					}
				}
			})
		}
	}
}

// --- tarch-b12-c7-p2: Benchmark for evaluateSafeCommand pipeline throughput ---

func BenchmarkEvaluateSafeCommand(b *testing.B) {
	cases := []struct {
		name    string
		command string
		nRules  int
	}{
		{"fast_path_hit", "ls", 0},
		{"prefix_rule_hit", "git status --short", 0},
		{"disqualifier_metachar", "ls; rm -rf /", 0},
		{"fall_through_0_rules", "docker build .", 0},
		{"fall_through_10_rules", "docker build .", 10},
		{"fall_through_50_rules", "docker build .", 50},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			dir := writeSettingsB(b, `{"shell_policy":"safe_commands"}`)
			var r *CommandRules
			if tc.nRules > 0 {
				r = NewCommandRules(dir)
				for i := range tc.nRules {
					_ = r.Add(fmt.Sprintf("bench-allow-%d *", i), RuleAllow)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				var matcher eval.RuleMatcher
				if r != nil {
					matcher = r
				}
				eval.EvaluateSafeCommand(tc.command, matcher)
			}
		})
	}
}

// --- tarch-b8-c7-p3: BenchmarkEvaluateShellCommand_WithRules ---

func BenchmarkEvaluateShellCommand_WithRules(b *testing.B) {
	for _, ruleCount := range []int{10, 50, 200} {
		b.Run(fmt.Sprintf("rules=%d", ruleCount), func(b *testing.B) {
			dir := writeSettingsB(b, `{"shell_policy":"safe_commands"}`)
			r := NewCommandRules(dir)
			for i := range ruleCount {
				_ = r.Add(fmt.Sprintf("allowed-command-%d *", i), RuleAllow)
			}
			// Command that won't match any rule — forces full linear scan.
			cmd := "unknown-command --flag value"
			ctx := context.Background()
			b.ResetTimer()
			for range b.N {
				EvaluateShellCommand(ctx, dir, cmd, r)
			}
		})
	}
}
