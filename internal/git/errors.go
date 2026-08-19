package git

import (
	"context"
	"errors"
	"net/http"
	"os/exec"
	"strings"

	"github.com/cplieger/vibekit/internal/httpreply"
)

const (
	jsonKeyOutput  = httpreply.JSONKeyOutput
	refHEAD        = "HEAD"
	msgNotAGitRepo = "not a git repo"
)

// --- git error taxonomy ---

// ErrorKind is a machine-readable discriminator for git handler errors.
// Clients switch on the "error" field; the "detail" field carries variable
// context (e.g. branch name).
type ErrorKind string

// KindNoStaged and the following constants define the ErrorKind values for git handler errors.
const (
	KindNoStaged         ErrorKind = "no_staged_changes"
	KindNoChanges        ErrorKind = "no_changes"
	KindGenerationFailed ErrorKind = "generation_failed"
	KindShowFailed       ErrorKind = "show_failed"
	// KindNotInRepo means no discovered repository owns the path, so there
	// is no committed revision of it to show. Distinct from KindShowFailed
	// because it is not a failure: a file in the workspace but outside every
	// repo (or a workspace root that is not itself a repo) simply has no
	// "before", and a client showing it as an all-add diff is CORRECT. Fold
	// the two and a real git failure renders as "this file is brand new",
	// silently claiming every line was added.
	KindNotInRepo ErrorKind = "not_in_repo"
)

// writeGitError writes a structured error response with a stable
// machine-readable kind and an optional human-readable detail field.
func writeGitError(w http.ResponseWriter, kind ErrorKind, detail string) {
	resp := httpreply.ErrorJSON(string(kind))
	if detail != "" {
		resp["detail"] = detail
	}
	httpreply.WriteJSON(w, resp)
}

// --- git show error classification ---

// ErrPathNotInRef indicates the requested path does not exist at the
// given ref (new file, deleted file, or invalid object name). Callers
// should surface this as empty content rather than a hard error.
var ErrPathNotInRef = errors.New("path not found at ref")

// gitShowCmd runs `git show <ref>:<path>` and classifies the error.
// Returns ErrPathNotInRef when the file doesn't exist at the ref
// (exit code 128 is the fatal error signal for `git show ref:path`).
// When the ref has been validated via isValidGitRef by the caller,
// exit 128 reliably means "path not found" — except when the
// directory isn't a git repo at all, which is a different failure.
func gitShowCmd(ctx context.Context, dir, ref, path string) (string, error) {
	// --no-textconv PINS the raw-blob default rather than closing an open path.
	// `git show <ref>:<path>` is a blob dump: measured against git 2.55.0, the
	// bare form prints the raw object and only an explicit --textconv runs a
	// repo-supplied diff.<driver>.textconv command (TestTextconv_FixtureIsArmed
	// asserts both halves against an armed fixture). The driver IS reachable
	// from this subcommand, though, so the flag makes "this reads bytes, it does
	// not invoke a program" a stated property of the call instead of one
	// inherited from a default an untrusted repo has no say over but a future
	// git could change. Per call site because the flag belongs to the diff
	// family and would be rejected by the plumbing subcommands gitCmd also
	// funnels.
	out, err := gitCmd(ctx, dir, "show", "--no-textconv", ref+":"+path)
	if err == nil {
		return out, nil
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok && exitErr.ExitCode() == 128 {
		// "not a git repository" is a repo-level failure, not a
		// path-not-found. Let it fall through as a generic error.
		if strings.Contains(out, "not a git repository") {
			return out, err
		}
		return "", ErrPathNotInRef
	}
	return out, err
}
