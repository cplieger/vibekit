// Shared parsing helpers used by all CLI provider implementations.

package forges

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Status/state vocabulary shared by the CLI provider implementations.
// The canonical Check.Status / Check.Conclusion sets are documented on
// those fields in provider.go.
const (
	stateMerged    = "merged"
	stateCompleted = "completed"
	stateClosed    = "closed"
	stateClose     = "close"
	stateComplete  = "complete"
	statusFailure  = "failure"
	flagHostname   = "--hostname"
	protoHTTPS     = "https"
	versionLatest  = "latest"
	stateOpen      = "open"
	stateSkipped   = "skipped"
	statePending   = "pending"
	mergeSquash    = "squash"
	mergeRebase    = "rebase"
	statusError    = "error"
	statusSuccess  = "success"
	stateOpened    = "opened"
	fieldUser      = "user"
	fieldMethod    = "method"
	fieldRepo      = "repo"
	fieldURL       = "url"
	fieldHeadSHA   = "head_sha"
	fieldAuto      = "auto"
)

// PR.CheckStatus vocabulary: the folded CI verdict for a PR's head
// commit. The empty string is a fourth, meaningful value — the forge
// reported no checks — and is never rendered as a passing state.
const (
	checkPending = "pending"
	checkPassing = "passing"
	checkFailing = "failing"
)

// PR.MergeBlocked vocabulary: why the forge refuses a merge. Each value
// names one cause; blockUnknown is for a forge that reports the refusal
// without a reason, which is honest where a guess would not be.
const (
	blockDraft         = "draft"
	blockConflicts     = "conflicts"
	blockChecksFailing = "checks_failing"
	blockChecksRunning = "checks_running"
	blockBehind        = "behind"
	blockProtected     = "blocked"
	blockUnknown       = "unknown"
)

// isHexSHA reports whether s is a plausible git object id: 7 to 64 hex
// digits. The head-commit pin arrives as a query parameter and travels
// into a subprocess argv and a JSON body, so it is validated at that
// boundary rather than handed to the forge to reject.
func isHexSHA(s string) bool {
	if len(s) < 7 || len(s) > 64 {
		return false
	}
	for i := range len(s) {
		switch c := s[i]; {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

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
	n, _ := strconv.Atoi(m[1])
	return n
}

// extractIssueNumberFromURL parses the issue number from a forge URL.
func extractIssueNumberFromURL(url string) int {
	m := issueURLRegex.FindStringSubmatch(url)
	if len(m) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// normalizePRState maps provider-specific state strings into our
// canonical set: stateOpen, stateClosed, stateMerged, "draft".
func normalizePRState(s string) string {
	switch strings.ToLower(s) {
	case stateOpen, stateOpened:
		return stateOpen
	case stateClosed, stateClose:
		return stateClosed
	case stateMerged:
		return stateMerged
	case "draft":
		return "draft"
	}
	return strings.ToLower(s)
}

// normalizeIssueState maps provider-specific issue states into our
// canonical set: stateOpen, stateClosed.
func normalizeIssueState(s string) string {
	switch strings.ToLower(s) {
	case stateOpen, stateOpened:
		return stateOpen
	case stateClosed, stateClose:
		return stateClosed
	}
	return strings.ToLower(s)
}
