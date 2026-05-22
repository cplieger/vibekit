// Shared parsing helpers used by all CLI provider implementations.

package forges

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// parseRFC3339Millis parses an RFC 3339 timestamp string into Unix
// milliseconds. Returns 0 on parse failure (caller decides whether
// the missing field is fatal).
func parseRFC3339Millis(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		// Some providers emit RFC 3339 without nanoseconds and a
		// trailing 'Z'; the standard parser accepts both. Try
		// without timezone for the rare case of a naive string.
		t, err = time.Parse("2006-01-02T15:04:05", s)
		if err != nil {
			return 0
		}
	}
	return t.UnixMilli()
}

// trimSpace is a thin alias for strings.TrimSpace, used widely enough
// in this package to warrant a one-letter shorthand.
func trimSpace(s string) string {
	return strings.TrimSpace(s)
}

// prURLRegex matches the trailing PR/issue number in a forge URL:
// https://github.com/owner/repo/pull/123
// https://gitlab.com/group/proj/-/merge_requests/45
// https://codeberg.org/user/repo/pulls/7
var prURLRegex = regexp.MustCompile(`/(?:pull|merge_requests|pulls)/(\d+)`)

// issueURLRegex matches the trailing issue number in a forge URL.
var issueURLRegex = regexp.MustCompile(`/issues/(\d+)`)

// extractPRNumberFromURL parses the PR/MR number from a forge URL.
// Returns 0 if no number can be extracted.
func extractPRNumberFromURL(url string) int {
	m := prURLRegex.FindStringSubmatch(url)
	if len(m) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(m[1]) //nolint:errcheck // regex guarantees decimal digits
	return n
}

// extractIssueNumberFromURL parses the issue number from a forge URL.
func extractIssueNumberFromURL(url string) int {
	m := issueURLRegex.FindStringSubmatch(url)
	if len(m) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(m[1]) //nolint:errcheck // regex guarantees decimal digits
	return n
}

// normalizePRState maps provider-specific state strings into our
// canonical set: "open", "closed", "merged", "draft".
func normalizePRState(s string) string {
	switch strings.ToLower(s) {
	case "open", "opened":
		return "open"
	case "closed", "close":
		return "closed"
	case "merged":
		return "merged"
	case "draft":
		return "draft"
	}
	return strings.ToLower(s)
}

// normalizeIssueState maps provider-specific issue states into our
// canonical set: "open", "closed".
func normalizeIssueState(s string) string {
	switch strings.ToLower(s) {
	case "open", "opened":
		return "open"
	case "closed", "close":
		return "closed"
	}
	return strings.ToLower(s)
}
