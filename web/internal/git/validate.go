package git

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// --- path validation ---

// isValidGitRef reports whether s is safe to pass as a git ref to a
// subprocess. The rule mirrors git-check-ref-format's forbidden-char
// set plus a leading-dash guard to neutralise flag smuggling.
func isValidGitRef(s string) bool {
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "-") {
		return false
	}
	// git-check-ref-format(1) forbidden chars + whitespace + NUL.
	return !strings.ContainsAny(s, " \t\n\r\x00:?*[\\~^")
}

// maxRepoPaths caps how many paths a single stage/unstage/discard
// request can carry.
const maxRepoPaths = 1024

// sanitizeRepoPaths validates a client-supplied list of repo-relative
// paths used in stage/unstage/discard. Rejects absolute paths, null
// bytes, `..` traversal, and overly large batches.
func sanitizeRepoPaths(paths []string) ([]string, error) {
	if len(paths) > maxRepoPaths {
		return nil, fmt.Errorf("too many paths (max %d)", maxRepoPaths)
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		if strings.ContainsRune(p, '\x00') {
			return nil, errors.New("null byte in path")
		}
		if filepath.IsAbs(p) {
			return nil, errors.New("absolute path rejected")
		}
		clean := filepath.Clean(p)
		if clean == ".." ||
			strings.HasPrefix(clean, "../") ||
			strings.HasPrefix(clean, `..\`) {
			return nil, fmt.Errorf("path escapes repo: %q", p)
		}
		out = append(out, clean)
	}
	return out, nil
}
