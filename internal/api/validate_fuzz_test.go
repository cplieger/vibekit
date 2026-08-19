package api

import (
	"strings"
	"testing"
)

func FuzzValidMessageID(f *testing.F) {
	f.Add("")
	f.Add("msg-123")
	f.Add("a.b-c:d_e")
	f.Add("msg/bad")
	f.Add("\x00null")

	f.Fuzz(func(t *testing.T, id string) {
		// Must not panic.
		_ = ValidMessageID(id)
	})
}

func FuzzValidRequestID(f *testing.F) {
	f.Add("")
	f.Add("req-abc.123")
	f.Add("req/bad")
	f.Add("\x00null")

	f.Fuzz(func(t *testing.T, id string) {
		// Must not panic.
		_ = ValidRequestID(id)
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
