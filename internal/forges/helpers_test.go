package forges

import "testing"

func TestParseRFC3339Millis(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int64
	}{
		{"valid RFC3339 with Z", "2024-01-15T10:30:00Z", 1705314600000},
		{"valid RFC3339 with offset", "2024-01-15T12:30:00+02:00", 1705314600000},
		{"empty string", "", 0},
		{"garbage", "not-a-date", 0},
		{"naive string", "2024-01-15T10:30:00", 1705314600000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseRFC3339Millis(tc.in)
			if got != tc.want {
				t.Errorf("parseRFC3339Millis(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizePRState(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"open", "open"},
		{"opened", "open"},
		{"closed", "closed"},
		{"close", "closed"},
		{"merged", "merged"},
		{"draft", "draft"},
		{"OPEN", "open"},
		{"unknown", "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := normalizePRState(tc.in); got != tc.want {
				t.Errorf("normalizePRState(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeIssueState(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"open", "open"},
		{"opened", "open"},
		{"closed", "closed"},
		{"close", "closed"},
		{"unknown", "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := normalizeIssueState(tc.in); got != tc.want {
				t.Errorf("normalizeIssueState(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestMapGiteaStatus(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"pending", "queued"},
		{"running", "in_progress"},
		{"success", "completed"},
		{"failure", "completed"},
		{"error", "completed"},
		{"warning", "completed"},
		{"unknown", "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := mapGiteaStatus(tc.in); got != tc.want {
				t.Errorf("mapGiteaStatus(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestMapGiteaConclusion(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"success", "success"},
		{"failure", "failure"},
		{"error", "failure"},
		{"warning", "skipped"},
		{"unknown", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := mapGiteaConclusion(tc.in); got != tc.want {
				t.Errorf("mapGiteaConclusion(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestIsHexSHA(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"short abbreviated sha", "abc1234", true},
		{"full sha", "5f2c1e4a9b8d7c6e5f4a3b2c1d0e9f8a7b6c5d4e", true},
		{"64 hex digits", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", true},
		{"uppercase hex", "ABC1234DEF", true},
		{"too short", "abc123", false},
		{"65 digits", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0", false},
		{"empty", "", false},
		{"non-hex letters", "zzzzzzz", false},
		{"flag-shaped", "--match-head-commit", false},
		{"with a space", "abc1234 def", false},
		{"shell metacharacter", "abc1234;rm", false},
		{"path traversal", "../../etc", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isHexSHA(tc.in); got != tc.want {
				t.Errorf("isHexSHA(%q) = %t, want %t", tc.in, got, tc.want)
			}
		})
	}
}
