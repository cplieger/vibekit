// ONE `git status` invocation per repository, plus its parser.
//
// It replaced five spawns per repo: `branch --show-current`, `rev-list` for the
// ahead/behind counts, `status --porcelain=v1`, `stash list`, and a second
// `status` for the tracked/untracked split. Porcelain v2 adds all of those as
// header records beside the file list; only `remote get-url origin` survives,
// because v2 reports the upstream REF and the panel needs the URL. Format
// reference: gitstatus(1) "Porcelain Format Version 2".

package git

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cplieger/vibekit/internal/logsafe"
)

// statusArgs is the one status invocation. `-z` for NUL-delimited records (git
// never quotes a path in that form — see parsePorcelainV2), and `-uall` so
// untracked files inside a new directory are listed individually instead of
// collapsing to the directory, which the entry walk skips.
var statusArgs = []string{
	"status", "--porcelain=v2", "--branch", "--show-stash", "-z", "-uall",
}

// porcelainStatus is everything one status invocation reports.
//
// Branch is empty for a detached HEAD, as `branch --show-current` answered. Ahead
// and Behind are both 0 when the branch tracks nothing (git omits the header),
// which this shape deliberately does not distinguish from being in sync.
// Conflicted is v2's own answer rather than a reading of the letters: an unmerged
// path is its own RECORD TYPE, so the seven XY pairs the short format had to
// classify by hand are one prefix here.
type porcelainStatus struct {
	Branch     string
	Files      []gitFile
	Ahead      int
	Behind     int
	Stashes    int
	Conflicted bool
}

// readStatus runs the status invocation in dir and parses it.
//
// The error survives because the three callers want different things from a
// failure: the dashboard degrades to a row with empty counts, a discard refuses to
// act on a status it does not have, and pull-all must treat an unreadable tree as
// unsafe rather than clean.
//
// A FAILED read still answers the branch, read off .git/HEAD rather than from a
// second spawn. Collapsing five invocations into two made the branch a casualty of
// a failed status: `branch --show-current` used to be its own process and could
// succeed where `status` failed, so the dashboard row for a wedged repository lost
// its one piece of orientation along with its counts. The file read costs no
// process at all, which is cheaper than what it replaces.
func readStatus(ctx context.Context, dir string) (porcelainStatus, error) {
	raw, err := gitExec(ctx, dir, statusArgs...).CombinedOutput()
	if err != nil {
		// Cancellation (per-repo budget, server shutdown) SIGKILLs the subprocess —
		// an expected partial result, not a git failure; keep WARN for genuine
		// errors only so a busy dashboard doesn't flood the log with kill noise.
		if ctx.Err() != nil {
			slog.Debug("git status canceled", "repo", logsafe.Field(dir), "cause", ctx.Err())
			return porcelainStatus{Branch: headBranch(dir)}, err
		}
		slog.Warn("git status failed", "repo", logsafe.Field(dir),
			"error", logsafe.Field(err.Error()), "out", scrubAuth(string(raw)))
		return porcelainStatus{Branch: headBranch(dir)}, err
	}
	return parsePorcelainV2(raw), nil
}

// headRefPrefix is what .git/HEAD holds for a checked-out branch. Anything else
// is a detached HEAD (a bare object id) or not a HEAD document at all.
const headRefPrefix = "ref: refs/heads/"

// headDocMaxBytes bounds the HEAD and .git reads. A HEAD document is one line and
// a .git file is one `gitdir:` line, so this is the hostile-content bound. The
// entry's own name is gitDirName (repos.go).
const headDocMaxBytes = 4 << 10

// headBranch reads the checked-out branch straight off .git/HEAD, with no
// subprocess. Empty for a detached HEAD — which is what `branch --show-current`
// answered too — and empty for anything it cannot read or does not recognise.
func headBranch(dir string) string {
	gitPath := filepath.Join(dir, gitDirName)
	info, err := os.Stat(gitPath) // #nosec G703 -- dir is resolved through repoDir, which refuses ".." and absolute paths
	if err != nil {
		return ""
	}
	if !info.IsDir() {
		// A linked worktree or a submodule: .git is a FILE naming the real git
		// directory, and HEAD lives there rather than beside this file.
		gitPath = resolveGitDirFile(dir, gitPath)
		if gitPath == "" {
			return ""
		}
	}
	return parseHeadRef(readSmallFile(filepath.Join(gitPath, "HEAD")))
}

// resolveGitDirFile reads a `.git` FILE and returns the git directory it names,
// resolving a relative pointer against the directory holding the file (git's own
// rule). Empty when the file is not a gitdir pointer.
func resolveGitDirFile(dir, gitFile string) string {
	target, ok := strings.CutPrefix(strings.TrimSpace(readSmallFile(gitFile)), "gitdir:")
	if !ok {
		return ""
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	if filepath.IsAbs(target) {
		return target
	}
	return filepath.Join(dir, target)
}

// readSmallFile reads at most headDocMaxBytes of path, answering "" for anything
// it cannot read. Capped rather than os.ReadFile so a file that is not the
// one-liner this expects cannot be pulled into memory whole.
func readSmallFile(path string) string {
	f, err := os.Open(path) // #nosec G703 -- see headBranch; the value is only used when it parses as a HEAD document
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, headDocMaxBytes))
	if err != nil {
		return ""
	}
	return string(raw)
}

// parseHeadRef extracts the branch name from a .git/HEAD document.
//
// Empty for a detached HEAD (a bare object id) and for anything that is neither
// form. The ref-name check is what makes the recovery safe on a repository this
// server did not write: a `.git` file can point its `gitdir:` anywhere, so
// requiring the exact prefix plus a valid ref name means an unrelated file's first
// line answers nothing rather than reaching the dashboard as a branch.
func parseHeadRef(doc string) string {
	name, ok := strings.CutPrefix(strings.TrimSpace(doc), headRefPrefix)
	if !ok || !isValidGitRef(name) {
		return ""
	}
	return name
}

// Field counts of the three entry records, from gitstatus(1): the path is the last
// field of a SplitN at that count, so it keeps any spaces it contains.
//
//	1 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <path>
//	2 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <X><score> <path>
//	u <XY> <sub> <m1> <m2> <m3> <mW> <h1> <h2> <h3> <path>
const (
	changedFields  = 9
	renamedFields  = 10
	unmergedFields = 11
)

// parsePorcelainV2 parses the status output. A pure function, so the record
// grammar is testable without git.
//
// Records are NUL-separated, headers included. -z is deliberate: git never quotes
// a path in that form, where the newline form C-quotes anything non-ASCII
// (café.txt → "caf\303\251.txt") and those strings were shown verbatim AND fed
// back to git, matching nothing. Malformed records are skipped, which also makes
// the stderr CombinedOutput folds in harmless.
func parsePorcelainV2(raw []byte) porcelainStatus {
	var st porcelainStatus
	records := strings.Split(string(raw), "\x00")
	for i := 0; i < len(records); i++ {
		rec := records[i]
		if len(rec) < 2 {
			continue
		}
		switch rec[0] {
		case '#':
			st.applyHeader(rec)
		case '1':
			st.appendEntry(rec, changedFields, "")
		case '2':
			// Take the origin path and skip it, whether or not the entry parses: a
			// record left behind would be walked as its own entry next iteration.
			orig := ""
			if i+1 < len(records) {
				orig = records[i+1]
				i++
			}
			st.appendEntry(rec, renamedFields, orig)
		case 'u':
			st.Conflicted = true
			st.appendEntry(rec, unmergedFields, "")
		case '?':
			// `? <path>`: no XY pair of its own, so it carries v1's spelling of
			// untracked into the shared row builder.
			st.appendPath('?', '?', rec[2:], "")
		case '!':
			// Ignored files. Only emitted with --ignored, which statusArgs does not
			// pass, so this arm is the format's completeness rather than a live path.
		}
	}
	return st
}

// appendHeader applies one `# <key> <value>` header record.
func (st *porcelainStatus) applyHeader(rec string) {
	key, val, found := strings.Cut(strings.TrimPrefix(rec, "# "), " ")
	if !found {
		return
	}
	switch key {
	case "branch.head":
		// git spells a detached HEAD "(detached)"; the wire contract's empty branch
		// is what the panel renders for it, and what `branch --show-current` gave.
		if val != "(detached)" {
			st.Branch = val
		}
	case "branch.ab":
		st.Ahead, st.Behind = parseAheadBehind(val)
	case "stash":
		if n, err := strconv.Atoi(val); err == nil && n > 0 {
			st.Stashes = n
		}
	}
}

// appendEntry parses one changed, renamed or unmerged record and appends its
// rows. fields is the record's field count; orig is the rename origin, empty for
// every other record.
func (st *porcelainStatus) appendEntry(rec string, fields int, orig string) {
	parts := strings.SplitN(rec, " ", fields)
	if len(parts) < fields {
		return
	}
	xy := parts[1]
	if len(xy) != 2 {
		return
	}
	// v2 writes '.' where v1 writes a space for "unchanged on this side".
	st.appendPath(unchangedToSpace(xy[0]), unchangedToSpace(xy[1]), parts[fields-1], orig)
}

// appendPath appends the rows for one status pair through the shared row builder,
// which is where a status letter's meaning for the UI lives.
func (st *porcelainStatus) appendPath(x, y byte, path, orig string) {
	// A path ending in "/" is a directory, which git reports when it collapses an
	// untracked tree. -uall stops that, so this is the guard for a git that does
	// not honour it rather than an expected record.
	if path == "" || strings.HasSuffix(path, "/") {
		return
	}
	st.Files = appendStatusEntries(st.Files, x, y, path, orig)
}

// unchangedToSpace maps porcelain v2's '.' onto v1's space, so one row builder
// serves both spellings of "nothing changed on this side".
func unchangedToSpace(c byte) byte {
	if c == '.' {
		return ' '
	}
	return c
}

// parseAheadBehind parses a `# branch.ab` value ("+1 -2"). Both counts are 0 for
// anything that does not parse, which is the answer the panel showed before this
// header existed.
func parseAheadBehind(val string) (ahead, behind int) {
	parts := strings.Fields(val)
	if len(parts) != 2 {
		return 0, 0
	}
	if n, err := strconv.Atoi(strings.TrimPrefix(parts[0], "+")); err == nil && n >= 0 {
		ahead = n
	}
	if n, err := strconv.Atoi(strings.TrimPrefix(parts[1], "-")); err == nil && n >= 0 {
		behind = n
	}
	return ahead, behind
}
