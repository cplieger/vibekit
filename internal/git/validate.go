package git

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cplieger/pathinside"
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
//
// The escape half of the traversal rule is pathinside.RelEscapes (the
// cleaned name IS ".." or begins with ".." + separator). The `..\` arm
// stays LOCAL and cannot move into the library: on Unix a backslash is
// an ordinary filename byte, so `..\x` is one legitimate component that
// RelEscapes deliberately accepts, and this validator refuses it anyway
// because a client-supplied repo path spelled with Windows separators
// is not a shape this server should forward to git.
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
		if pathinside.RelEscapes(clean) || strings.HasPrefix(clean, `..\`) {
			return nil, fmt.Errorf("path escapes repo: %q", p)
		}
		out = append(out, clean)
	}
	return out, nil
}

// validateFilePath reports whether path is safe for use in git show /
// git diff operations. Rejects leading dashes (flag smuggling), path
// traversal (a `..` component), control bytes (invisible chars that
// break log readability), and absolute paths.
//
// The traversal test is pathinside.HasDotDot, the SYNTACTIC-HYGIENE
// axis, and the axis choice is the whole point: this function judges a
// path AS WRITTEN against no root at all, and the value it accepts is
// forwarded to the git subprocess verbatim — never a cleaned rewrite of
// itself. pathinside.RelEscapes is the wrong function here because it
// CLEANS first, which collapses exactly the spellings this validator
// exists to refuse ("a/../b" normalises to "b" and would be accepted).
//
// It replaces strings.Contains(path, ".."), which refused any path with
// two adjacent dots anywhere in a NAME — "v1..v2.txt", "a..b/main.go",
// "..extras/x" — none of which traverses anything. Those are accepted
// now; every real `..` component ("..", "../x", "a/../b", "a/..") is
// still refused. Canonicality is deliberately NOT tested: "a/./b" and
// "a//b" stay acceptable, as they were.
func validateFilePath(path string) bool {
	if strings.HasPrefix(path, "-") ||
		pathinside.HasDotDot(path) ||
		strings.IndexFunc(path, func(r rune) bool { return r < 0x20 || r == 0x7f }) != -1 ||
		strings.HasPrefix(path, "/") {
		return false
	}
	return true
}
