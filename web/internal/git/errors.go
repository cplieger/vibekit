package git

import (
	"context"
	"errors"
	"net/http"
	"os/exec"
	"strings"

	"vibekit/internal/api"
)

// --- git error taxonomy ---

// ErrorKind is a machine-readable discriminator for git handler errors.
// Clients switch on the "error" field; the "detail" field carries variable
// context (e.g. branch name).
type ErrorKind string

const (
	KindNoStaged         ErrorKind = "no_staged_changes"
	KindNoChanges        ErrorKind = "no_changes"
	KindGenerationFailed ErrorKind = "generation_failed"
	KindShowFailed       ErrorKind = "show_failed"
)

// writeGitError writes a structured error response with a stable
// machine-readable kind and an optional human-readable detail field.
func writeGitError(w http.ResponseWriter, kind ErrorKind, detail string) {
	resp := map[string]string{"error": string(kind)}
	if detail != "" {
		resp["detail"] = detail
	}
	api.WriteJSON(w, resp)
}

// --- git show error classification ---

// ErrPathNotInRef indicates the requested path does not exist at the
// given ref (new file, deleted file, or invalid object name). Callers
// should surface this as empty content rather than a hard error.
var ErrPathNotInRef = errors.New("path not found at ref")

// ErrNoStagedChanges indicates no staged changes are available for the
// requested operation (e.g. commit message generation).
var ErrNoStagedChanges = errors.New("no staged changes")

// ErrGenerationFailed indicates the AI prompt generation failed (utility
// bridge returned an error or empty result).
var ErrGenerationFailed = errors.New("generation failed")

// ErrNoChanges indicates no diff changes were found against the
// specified base branch.
var ErrNoChanges = errors.New("no changes found")

// gitShowCmd runs `git show <ref>:<path>` and classifies the error.
// Returns ErrPathNotInRef when the file doesn't exist at the ref
// (exit code 128 is the fatal error signal for `git show ref:path`).
// When the ref has been validated via isValidGitRef by the caller,
// exit 128 reliably means "path not found" — except when the
// directory isn't a git repo at all, which is a different failure.
func gitShowCmd(ctx context.Context, dir, ref, path string) (string, error) {
	out, err := gitCmd(ctx, dir, "show", ref+":"+path)
	if err == nil {
		return out, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 128 {
		// "not a git repository" is a repo-level failure, not a
		// path-not-found. Let it fall through as a generic error.
		if strings.Contains(out, "not a git repository") {
			return out, err
		}
		return "", ErrPathNotInRef
	}
	return out, err
}
