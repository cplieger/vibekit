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
		// A non-empty trailing literal must anchor at the end: 'b'
		// appears mid-string in "abx" but the command does not end
		// with it, so the match must fail.
		{"a*b", "abx", false},
		// A trailing '*' leaves an empty final literal, so any command
		// satisfying the prefix matches.
		{"ab*", "abc", true},
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

func TestWriteOption_Matches_ShortPrefix(t *testing.T) {
	// ShortPrefix matches either the exact short option or a longer
	// token (>2 chars) that carries the option's value inline.
	tests := []struct {
		name  string
		token string
		input string
		want  bool
	}{
		// A value-bearing short option matches via the HasPrefix clause.
		{"value_bearing_short_option", "-o", "-ofile.txt", true},
		// The exact short option matches via the equality clause.
		{"exact_short_option", "-o", "-o", true},
		// A 1-char token plus a 2-char input that shares the prefix but
		// is not equal must not match: the inline-value clause requires
		// len > 2, so a length-2 token is rejected.
		{"two_char_token_not_inline_value", "-", "-x", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := writeOption{token: tt.token, mode: ShortPrefix}.Matches(tt.input)
			if got != tt.want {
				t.Errorf("writeOption{%q, ShortPrefix}.Matches(%q) = %v, want %v",
					tt.token, tt.input, got, tt.want)
			}
		})
	}

	// The public path that depends on the inline-value clause.
	if !HasWriteOption("cmd -ofile.txt") {
		t.Errorf("HasWriteOption(%q) = false, want true", "cmd -ofile.txt")
	}
}

func TestResolveExplicitAllow(t *testing.T) {
	tests := []struct {
		name  string
		rules RuleMatcher
		want  ShellDecision
	}{
		// No rule set at all: nothing has been explicitly allowed, so the
		// escape hatch falls through to ask.
		{"nil_rules_ask", nil, ShellAsk},
		// An explicit allow rule is the whole point of the escape hatch.
		{"allow_rule_allows", fixedMatcher{"allow", true}, ShellAllow},
		// A deny rule does not grant the escape hatch.
		{"deny_rule_asks", fixedMatcher{"deny", true}, ShellAsk},
		// A rule set that does not match this command asks.
		{"no_match_asks", fixedMatcher{"allow", false}, ShellAsk},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveExplicitAllow("some command", tt.rules)
			if got != tt.want {
				t.Errorf("ResolveExplicitAllow(rules=%v) = %q, want %q", tt.rules, got, tt.want)
			}
		})
	}
}
