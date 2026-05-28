package eval

import "testing"

// allowMatcher is a RuleMatcher that always matches with "allow".
type allowMatcher struct{}

func (allowMatcher) Evaluate(string) (string, bool) { return "allow", true }
func (allowMatcher) MatchesAllow(string) bool        { return true }
func (allowMatcher) MatchesDeny(string) bool         { return false }

func BenchmarkEvaluateShellCommand(b *testing.B) {
	cases := []struct {
		name    string
		policy  ShellPolicy
		command string
		rules   RuleMatcher
	}{
		{"safe_builtin", PolicySafe, "ls -la", nil},
		{"safe_prefix", PolicySafe, "git status --short", nil},
		{"metachar_disqualified", PolicySafe, "echo hello; rm -rf /", nil},
		{"write_option", PolicySafe, "curl -o output.txt http://example.com", nil},
		{"unknown_ask", PolicySafe, "terraform apply", nil},
		{"with_allow_rule", PolicySafe, "npm install", allowMatcher{}},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			for range b.N {
				EvaluateShellCommand(tc.policy, tc.command, tc.rules)
			}
		})
	}
}

func BenchmarkHasWriteOption(b *testing.B) {
	cases := []struct {
		name    string
		command string
	}{
		{"short_command", "ls"},
		{"long_command", "some-tool --verbose --recursive --depth=10 --format=json --color=always --no-cache --timeout=30 --retry=3 --parallel=4 input.txt"},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			for range b.N {
				HasWriteOption(tc.command)
			}
		})
	}
}

func BenchmarkShellFields(b *testing.B) {
	cases := []struct {
		name    string
		command string
	}{
		{"simple", "git status"},
		{"complex", `docker run --rm -v "/home/user:/data" -e 'FOO=bar baz' --name "my container" image:latest`},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			for range b.N {
				ShellFields(tc.command)
			}
		})
	}
}
