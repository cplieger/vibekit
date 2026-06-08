package eval

import "testing"

// allowMatcher is a RuleMatcher that always matches with "allow".
type allowMatcher struct{}

func (allowMatcher) Evaluate(string) (string, bool) { return "allow", true }
func (allowMatcher) MatchesAllow(string) bool       { return true }
func (allowMatcher) MatchesDeny(string) bool        { return false }

func BenchmarkEvaluateShellCommand(b *testing.B) {
	cases := []struct {
		rules   RuleMatcher
		name    string
		policy  ShellPolicy
		command string
	}{
		{name: "safe_builtin", policy: PolicySafe, command: "ls -la", rules: nil},
		{name: "safe_prefix", policy: PolicySafe, command: "git status --short", rules: nil},
		{name: "metachar_disqualified", policy: PolicySafe, command: "echo hello; rm -rf /", rules: nil},
		{name: "write_option", policy: PolicySafe, command: "curl -o output.txt http://example.com", rules: nil},
		{name: "unknown_ask", policy: PolicySafe, command: "terraform apply", rules: nil},
		{name: "with_allow_rule", policy: PolicySafe, command: "npm install", rules: allowMatcher{}},
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
