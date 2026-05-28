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
