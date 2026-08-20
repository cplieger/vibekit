package ids

import (
	"regexp"
	"strings"
	"testing"
	"unicode"
)

// safeRe is the message/request-id charset, restated independently of
// validMessageIDRe so a target cross-checks the rule rather than the regexp
// the implementation happens to hold.
var safeRe = regexp.MustCompile(`^[A-Za-z0-9_.\-:]+$`)

// FuzzValidMessageID absorbed an identical target from internal/agent, which
// fuzzed this function through a forwarding wrapper while calling it nowhere in
// production. Its invariants and seeds are the ones kept: the pair here was
// crash-only, which on a gate this feeds catches nothing but a panic.
func FuzzValidMessageID(f *testing.F) {
	for _, s := range []string{
		"", "msg-123", "a.b-c:d_e", "msg/bad", "\x00null",
		"abc123", "msg_id:001", "a", string(make([]byte, 129)), "has\nnewline",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, id string) {
		result := ValidMessageID(id)
		if result {
			// Invariant 1: accepted IDs have length in [1, 128].
			if len(id) == 0 || len(id) > 128 {
				t.Errorf("accepted id with len %d", len(id))
			}
			// Invariant 2: no control characters or newlines.
			for _, r := range id {
				if unicode.IsControl(r) {
					t.Errorf("accepted id with control char %U", r)
				}
			}
			// Invariant 3: matches the safe character set.
			if !safeRe.MatchString(id) {
				t.Errorf("accepted id not matching safe regex: %q", id)
			}
		}
		// Empty string always rejected.
		if id == "" && result {
			t.Error("empty string accepted")
		}
		// Idempotent.
		if ValidMessageID(id) != result {
			t.Error("non-idempotent result")
		}
	})
}

func FuzzValidChatID(f *testing.F) {
	// Seed corpus from TestChatIDPattern cases in chat/store_test.go.
	seeds := []string{
		"abc",
		"ABC",
		"01HXYZ",
		"550e8400-e29b-41d4-a716-446655440000",
		"chat-1716000000000",
		"a_b",
		"a-b",
		strings.Repeat("x", 128),
		"",
		"a/b",
		"..",
		"a.b",
		"a b",
		"a\x00b",
		"a\nb",
		strings.Repeat("x", 129),
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		ok := ValidChatID(s)
		// Invariant: idempotent.
		if ok2 := ValidChatID(s); ok2 != ok {
			t.Errorf("ValidChatID(%q) not idempotent: %v then %v", s, ok, ok2)
		}
		// Invariant: if valid, must not contain forbidden characters.
		if ok {
			for _, r := range s {
				if r == '/' || r == '\\' || r == 0 || r == '.' || r == ' ' {
					t.Errorf("ValidChatID(%q)=true but contains forbidden rune %q", s, r)
				}
			}
			if len(s) > 128 {
				t.Errorf("ValidChatID(%q)=true but len=%d > 128", s, len(s))
			}
		}
	})
}

func FuzzValidIdent(f *testing.F) {
	seeds := []string{
		"",
		"abc",
		"ABC",
		"a.b",
		"a-b",
		"a_b",
		".hidden",
		"-start",
		"...",
		"a..b",
		strings.Repeat("x", 128),
		strings.Repeat("x", 129),
		"valid_ident-1.0",
		".",
		"..",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		ok := ValidIdent(s)
		if ok && s != "" {
			if !identRe.MatchString(s) {
				t.Errorf("ValidIdent(%q)=true but identRe does not match", s)
			}
			if s[0] == '.' || s[0] == '-' {
				t.Errorf("ValidIdent(%q)=true but starts with forbidden char", s)
			}
			allDots := true
			for _, r := range s {
				if r != '.' {
					allDots = false
					break
				}
			}
			if allDots {
				t.Errorf("ValidIdent(%q)=true but is all-dots", s)
			}
		}
	})
}

func FuzzValidSessionID(f *testing.F) {
	seeds := []string{
		"",
		"abc",
		"a/b",
		"a\\b",
		"a\x00b",
		".",
		"..",
		"a..b",
		strings.Repeat("x", 128),
		strings.Repeat("x", 129),
		"valid-session-id",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		ok := ValidSessionID(s)
		if ok {
			if len(s) == 0 || len(s) > 128 {
				t.Errorf("ValidSessionID(%q)=true but len=%d", s, len(s))
			}
			if strings.ContainsAny(s, "/\\\x00") {
				t.Errorf("ValidSessionID(%q)=true but contains path separator or NUL", s)
			}
			if s == "." || s == ".." || strings.Contains(s, "..") {
				t.Errorf("ValidSessionID(%q)=true but contains traversal", s)
			}
		}
	})
}
