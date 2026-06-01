package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func FuzzSanitizeUnicode(f *testing.F) {
	// Seed corpus from TestSanitizeUnicode cases.
	seeds := []string{
		"",
		"hello world",
		"line1\n\tline2",
		"a\u200Bb",
		"a\u200Db",
		"\uFEFFhello",
		"he\u00ADllo",
		"a\u2060b",
		"\u200Ehello",
		"hello\u200F",
		"a\u202Ab\u202Ec",
		"a\u2066b\u2069c",
		"visible\U000E0041\U000E0062hidden",
		"a\u00A0b",
		"café",
		"hi 😀",
		"a\u200B\u200C\u200D\u2060b",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := SanitizeUnicode(s)
		// Invariant: output must contain no runes where isHiddenUnicode returns true.
		for _, r := range out {
			if isHiddenUnicode(r) {
				t.Errorf("SanitizeUnicode(%q) output contains hidden rune U+%04X", s, r)
			}
		}
		// Invariant: must not panic (implicit by reaching here).
		// Invariant: idempotent.
		if out2 := SanitizeUnicode(out); out2 != out {
			t.Errorf("SanitizeUnicode not idempotent: first=%q second=%q", out, out2)
		}
	})
}

func FuzzStripANSI(f *testing.F) {
	// Seed corpus from TestStripANSI + TestStripANSI_edge_cases.
	seeds := []string{
		"",
		"hello world",
		"\x1b[31mred\x1b[0m",
		"\x1b[1;32mbold green\x1b[0m",
		"\x1b]0;title\x07rest",
		"\x1b]0;title\x1b\\rest",
		"\x1b(B",
		"\x1b)0",
		"\x1b7\x1b8",
		"\x1b[?25l",
		"\x1b[38;2;255;0;0mtruecolor\x1b[0m",
		"no escapes here",
		"\x1b[m",
		"\x1b[K",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := StripANSI(s)
		// Invariant: output must contain no bytes matching ansiRe.
		if ansiRe.MatchString(out) {
			t.Errorf("StripANSI(%q) output still contains ANSI escapes", s)
		}
		// Invariant: must not panic (implicit by reaching here).
		// Invariant: idempotent.
		if out2 := StripANSI(out); out2 != out {
			t.Errorf("StripANSI not idempotent: first=%q second=%q", out, out2)
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

func FuzzStripCodeFence(f *testing.F) {
	// Seed corpus from existing table-driven cases + edge cases.
	seeds := []string{
		"",
		"hello world",
		"```\ncode\n```",
		"```go\npackage main\n```",
		"```\n```",
		"```",
		"no fence here",
		"```json\n{\"key\": \"value\"}\n```",
		"```\nnested\n```inner```\n```",
		"```verylonglanguagetag\ncontent\n```",
		"```\n\n\n```",
		"```\ncontent\n```  \n",
		"```go\nfunc main() {}\n```\n",
		"```\n" + strings.Repeat("x", 4096) + "\n```",
		"```日本語\ncontent\n```",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := StripCodeFence(s)
		// Invariant: must not panic (implicit by reaching here).
		// Invariant: if input did NOT start with ```, output must equal input (no-op).
		if !strings.HasPrefix(s, "```") && out != s {
			t.Errorf("StripCodeFence(%q) modified non-fenced input: %q", s, out)
		}
		// Invariant: if input started with ``` and contained a newline,
		// the output must not contain the opening fence line as a prefix.
		if strings.HasPrefix(s, "```") && strings.Contains(s, "\n") {
			fenceLine := s[:strings.IndexByte(s, '\n')]
			// The output should be a substring of the input (content after
			// the opening fence line), so it must be shorter or equal.
			if len(out) > len(s)-len(fenceLine)-1 {
				t.Errorf("StripCodeFence(%q) output longer than expected: %q", s, out)
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

func FuzzDecodeJSON_ContentType(f *testing.F) {
	seeds := []string{
		"",
		"application/json",
		"application/json; charset=utf-8",
		"application/json-patch+json",
		"application/jsonl",
		"text/plain",
		"text/html",
		"multipart/form-data",
		"APPLICATION/JSON",
		"application/xml",
		"application/json\x00evil",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, ct string) {
		// Build a minimal request with the fuzzed Content-Type.
		body := strings.NewReader(`{"key":"value"}`)
		req, err := http.NewRequest(http.MethodPost, "/test", body)
		if err != nil {
			t.Fatal(err)
		}
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		rec := httptest.NewRecorder()
		var dst map[string]string
		ok := DecodeJSON(rec, req, &dst)

		// Invariant: non-JSON content types must be rejected.
		if ct != "" && !strings.HasPrefix(ct, "application/json") {
			if ok {
				t.Errorf("DecodeJSON accepted non-JSON Content-Type %q", ct)
			}
		}
		// Invariant: empty Content-Type with valid JSON body must succeed.
		if ct == "" && !ok {
			t.Errorf("DecodeJSON rejected empty Content-Type with valid body")
		}
	})
}

func BenchmarkSanitizeOutput(b *testing.B) {
	cleanShort := strings.Repeat("Hello, world! ", 7)                                          // ~100 bytes
	ansiHeavy := strings.Repeat("\x1b[31mred\x1b[0m \x1b[1;32mbold\x1b[0m ", 200)              // ~4KB dense ANSI
	unicodeHeavy := strings.Repeat("text\u200B\u200C\u200D\u2060more ", 200)                   // ~4KB scattered hidden codepoints
	mixedLarge := strings.Repeat("\x1b[33m"+strings.Repeat("output ", 20)+"\x1b[0m\u200B", 50) // ~16KB realistic

	b.Run("clean_short", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			SanitizeOutput(cleanShort)
		}
	})
	b.Run("ansi_heavy", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			SanitizeOutput(ansiHeavy)
		}
	})
	b.Run("unicode_heavy", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			SanitizeOutput(unicodeHeavy)
		}
	})
	b.Run("mixed_large", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			SanitizeOutput(mixedLarge)
		}
	})
}
