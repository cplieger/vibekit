package git

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cplieger/pathinside/v2"
	"github.com/cplieger/runesafe/v2"
)

// --- path validation ---

// isValidGitRef reports whether s is safe to pass as a git ref to a
// subprocess. It IMPLEMENTS git-check-ref-format(1)'s refname rules over the
// bare name, plus the leading-dash guard git's own branch path adds, plus one
// screen that is vibekit's and not git's.
//
// The rule set, and each rule's authority:
//
// From git-check-ref-format(1), applied per '/'-separated component: no empty
// component (so no leading '/', no trailing '/', no '//'), no component
// beginning with '.', no component ending with '.', no component ending with
// '.lock'. Applied to the whole string: no "..", no "@{", and none of the
// forbidden characters — ASCII space, '~', '^', ':', '?', '*', '[', '\'.
//
// From git's strbuf_check_branch_ref, which is what `git checkout -b` calls:
// reject a leading '-'. That is flag smuggling rather than a ref rule, and it
// matters because several call sites forward the value as a bare argv token.
//
// From vibekit, beyond git: reject any rune runesafe.IsUnsafeSingleLine
// refuses. git ACCEPTS C1 controls, bidi controls and U+2028/U+2029 in a
// refname (measured, git 2.47.3); vibekit must not, because a branch name is
// rendered in the git panel and travels into slog attributes, which is the
// surface internal/logsafe exists for. Using the app's own predicate rather
// than a fresh local set keeps that one policy in one edit, and it subsumes
// git's own C0-and-DEL rule for free — which is why neither appears above.
//
// This is deliberately a DENYLIST and must stay one. A full-match allowlist
// cannot express what git permits without being narrower than git: git accepts
// an accented or CJK branch name, and a character-class grammar refuses it. The
// defect this replaced was not the shape but the completeness — the old rule
// claimed in its own comment to mirror git's forbidden-char set while
// implementing a strict subset of it, missing all seven positional rules.
//
// ONE function serves every call site, the read paths included. There is no
// legitimate READ value check-ref-format rejects: "HEAD", "origin/main",
// "refs/heads/main" and a bare SHA all pass, and the values newly refused
// ("..", "foo.lock", ".foo", "//x", "foo@{1}") resolve to nothing at a read
// site either. Splitting it into a ref rule and a branch rule would be two
// tables and a per-site choice to get wrong, for one rule set.
//
// Two of the checks a sibling app applies are deliberately absent, on git's
// authority: "HEAD" and a Windows device stem ("CON") are both accepted, because
// git accepts them and this container is not a Windows filesystem.
func isValidGitRef(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") {
		return false
	}
	if strings.Contains(s, "..") || strings.Contains(s, "@{") {
		return false
	}
	if strings.ContainsFunc(s, func(r rune) bool {
		return runesafe.IsUnsafeSingleLine(r) || strings.ContainsRune(" ~^:?*[\\", r)
	}) {
		return false
	}
	for component := range strings.SplitSeq(s, "/") {
		if component == "" ||
			strings.HasPrefix(component, ".") ||
			strings.HasSuffix(component, ".") ||
			strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
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
