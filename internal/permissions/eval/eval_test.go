package eval

import "testing"

// fixedMatcher returns a fixed mode for any command.
type fixedMatcher struct {
	mode    string
	matched bool
}

func (f fixedMatcher) Evaluate(string) (string, bool) { return f.mode, f.matched }
func (f fixedMatcher) MatchesAllow(string) bool       { return f.mode == "allow" && f.matched }
func (f fixedMatcher) MatchesDeny(string) bool        { return f.mode == "deny" && f.matched }

func TestEvaluateShellCommand_PolicyMatrix(t *testing.T) {
	tests := []struct {
		name    string
		policy  ShellPolicy
		command string
		rules   RuleMatcher
		want    ShellDecision
	}{
		{"none_clean", PolicyNone, "ls", nil, ShellDeny},
		{"none_metachar", PolicyNone, "echo; rm", nil, ShellDeny},
		{"none_deny_rule", PolicyNone, "rm -rf /", fixedMatcher{"deny", true}, ShellDeny},
		{"none_allow_rule", PolicyNone, "npm install", fixedMatcher{"allow", true}, ShellDeny},
		{"safe_clean_builtin", PolicySafe, "ls", nil, ShellAllow},
		{"safe_metachar", PolicySafe, "echo; rm", nil, ShellAsk},
		{"safe_deny_rule", PolicySafe, "ls", fixedMatcher{"deny", true}, ShellAsk},
		{"safe_allow_rule", PolicySafe, "terraform apply", fixedMatcher{"allow", true}, ShellAllow},
		{"all_clean", PolicyAll, "ls", nil, ShellAllow},
		{"all_metachar", PolicyAll, "echo; rm", nil, ShellAllow},
		{"all_deny_rule", PolicyAll, "rm -rf /", fixedMatcher{"deny", true}, ShellAsk},
		{"all_allow_rule", PolicyAll, "npm install", fixedMatcher{"allow", true}, ShellAllow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EvaluateShellCommand(tt.policy, tt.command, tt.rules)
			if got != tt.want {
				t.Errorf("EvaluateShellCommand(%q, %q, rules) = %q, want %q",
					tt.policy, tt.command, got, tt.want)
			}
		})
	}
}

func TestEvaluateSafeCommand_BuiltinRules(t *testing.T) {
	for _, r := range safeCommandRules {
		cmd := r.Pattern
		if r.Mode == Prefix {
			cmd += " --flag"
		}
		t.Run(cmd, func(t *testing.T) {
			got := EvaluateSafeCommand(cmd, nil)
			if got != ShellAllow {
				t.Errorf("EvaluateSafeCommand(%q) = %q, want allow", cmd, got)
			}
		})
	}
}

func TestMatchPattern_ExactAndWildcard(t *testing.T) {
	tests := []struct {
		pattern, command string
		want             bool
	}{
		{"ls", "ls", true},
		{"ls", "cat", false},
		{"npm *", "npm install", true},
		{"npm *", "yarn install", false},
		{"*test*", "go test ./...", true},
		{"git *", "git status", true},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.command, func(t *testing.T) {
			got := MatchPattern(tt.pattern, tt.command)
			if got != tt.want {
				t.Errorf("MatchPattern(%q, %q) = %v, want %v", tt.pattern, tt.command, got, tt.want)
			}
		})
	}
}

func TestMatchWildcard_EdgeCases(t *testing.T) {
	tests := []struct {
		pattern, command string
		want             bool
	}{
		{"", "", true},
		{"*", "anything", true},
		{"*", "", true},
		{"a*b", "ab", true},
		{"a*b", "axb", true},
		{"a*b", "axc", false},
		{"*end", "the end", true},
		{"*end", "the beginning", false},
		{"start*", "start here", true},
		{"start*", "not start", false},
		{"**", "anything", true},
		{"a*b*c", "abc", true},
		{"a*b*c", "aXbYc", true},
		{"a*b*c", "aXbY", false},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.command, func(t *testing.T) {
			got := MatchWildcard(tt.pattern, tt.command)
			if got != tt.want {
				t.Errorf("MatchWildcard(%q, %q) = %v, want %v", tt.pattern, tt.command, got, tt.want)
			}
		})
	}
}

func TestHasSafePrefix_WordBoundary(t *testing.T) {
	tests := []struct {
		command, prefix string
		want            bool
	}{
		{"git status", "git status", true},
		{"git status --short", "git status", true},
		{"git statusx", "git status", false},
		{"git", "git status", false},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			got := HasSafePrefix(tt.command, tt.prefix)
			if got != tt.want {
				t.Errorf("HasSafePrefix(%q, %q) = %v, want %v", tt.command, tt.prefix, got, tt.want)
			}
		})
	}
}

func TestExtractBaseCommand_QuotedAndUnquoted(t *testing.T) {
	tests := []struct {
		command string
		want    string
	}{
		{"ls -la", "ls"},
		{`"my cmd" arg`, "my cmd"},
		{"", ""},
		{"single", "single"},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			got := ExtractBaseCommand(tt.command)
			if got != tt.want {
				t.Errorf("ExtractBaseCommand(%q) = %q, want %q", tt.command, got, tt.want)
			}
		})
	}
}
