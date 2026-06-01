package bridge

import (
	"strings"
	"testing"
	"unicode/utf8"

	"vibekit/internal/api"
)

// FuzzValidSessionID exercises the ACP session-id path-traversal
// validation gate with arbitrary byte sequences. The invariant: for
// any s where validSessionID(s)==true, s must not contain '/', '\\',
// '\x00', "..", and must satisfy 1 <= len(s) <= 128.
func FuzzValidSessionID(f *testing.F) {
	// Seed corpus from TestValidSessionID.
	seeds := []string{
		"", "abc-123", "01HXY8B6N9",
		"../../../etc/passwd", "sess/with/slash",
		"sess\\with\\backslash", "sess\x00null",
		"..", ".", "ok.but..has-dotdot",
		strings.Repeat("a", 128),
		strings.Repeat("a", 129),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		result := api.ValidSessionID(s)
		if !result {
			return // rejected — no invariant to check
		}
		// Accepted: verify invariants.
		if len(s) == 0 || len(s) > 128 {
			t.Errorf("validSessionID(%q) = true but len=%d", s, len(s))
		}
		if strings.Contains(s, "/") {
			t.Errorf("validSessionID(%q) = true but contains '/'", s)
		}
		if strings.Contains(s, "\\") {
			t.Errorf("validSessionID(%q) = true but contains '\\'", s)
		}
		if strings.Contains(s, "\x00") {
			t.Errorf("validSessionID(%q) = true but contains NUL", s)
		}
		if strings.Contains(s, "..") {
			t.Errorf("validSessionID(%q) = true but contains '..'", s)
		}
		if s == "." {
			t.Errorf("validSessionID(%q) = true but is '.'", s)
		}
	})
}

// FuzzValidIdent exercises the agent/model identifier validation gate
// with arbitrary byte sequences. The invariant: for any s where
// validIdent(s)==true, s must match ^[A-Za-z0-9_.-]{1,128}$ AND s[0]
// is not '.' or '-' AND s is not all-dots.
func FuzzValidIdent(f *testing.F) {
	// Seed corpus from TestValidIdent.
	seeds := []string{
		"", "kiro_default", "kiro-planner", "my.custom.agent",
		"Claude3_5", "a", "kiro..planner",
		"../../../etc/passwd", "agent/with/slashes",
		"agent with spaces", "agent;rm -rf /", "agent\nname",
		"agent\x00name", "agent$(whoami)",
		".", "..", "...", ".hidden", "-flag",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		result := validIdent(s)
		if !result {
			return // rejected — no invariant to check
		}
		// Accepted: verify invariants.
		if s == "" {
			return // empty is explicitly allowed
		}
		if len(s) > 128 {
			t.Errorf("validIdent(%q) = true but len=%d > 128", s, len(s))
		}
		if s[0] == '.' || s[0] == '-' {
			t.Errorf("validIdent(%q) = true but starts with %q", s, s[0])
		}
		allDots := true
		for _, r := range s {
			if !utf8.ValidRune(r) {
				t.Errorf("validIdent(%q) = true but contains invalid rune", s)
			}
			if !isIdentChar(r) {
				t.Errorf("validIdent(%q) = true but contains %q", s, r)
			}
			if r != '.' {
				allDots = false
			}
		}
		if allDots {
			t.Errorf("validIdent(%q) = true but is all-dots", s)
		}
	})
}

func isIdentChar(r rune) bool {
	return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') ||
		(r >= '0' && r <= '9') || r == '_' || r == '.' || r == '-'
}
