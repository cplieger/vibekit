package git

// Request-prelude helpers used by every handler in this package. Keeps
// each handler to its git-specific logic without repeating the method
// check + body-decode + error-response dance.

import (
	"context"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"vibekit/internal/api"
	"vibekit/internal/gitexec"
)

// requirePOST writes a 405 if the method isn't POST and returns false.
// Returns true when the caller should proceed.
func requirePOST(w http.ResponseWriter, r *http.Request) bool {
	return api.RequireMethod(w, r, http.MethodPost)
}

// decodePostBody does the common POST prelude:
//   - enforces the JSON body size cap (api.MaxJSONBody)
//   - decodes into v
//
// On decode failure, writes a 400 with the given error message and
// returns false. Callers that need a method check should call
// requirePOST first.
func decodePostBody(w http.ResponseWriter, r *http.Request, v any, decodeErrMsg string) bool {
	return api.DecodeBody(w, r, v, decodeErrMsg)
}

// decodePostBodyOptional enforces the body-size cap and decodes into v.
// Unlike decodePostBody, a decode failure is silently ignored (v keeps
// its zero value) because the caller accepts an empty request body.
// Used by POST handlers whose JSON body is purely advisory
// (e.g. push/pull/stash with no required fields).
func decodePostBodyOptional(w http.ResponseWriter, r *http.Request, v any) {
	api.DecodeBodyOptional(w, r, v)
}

// writeCmdResult writes a git-command result: {"output":
// gitexec.ScrubAuth(out)} on success, {"error": gitexec.ScrubAuth(errMsg)} on
// failure. errMsg is the subprocess combined output when non-empty;
// otherwise err.Error().
//
// On failure the "output" field is intentionally omitted so clients
// cannot confuse a partial stdout stream with a successful response
// — presence-of-field is the success signal, not string emptiness.
//
// gitexec.ScrubAuth runs on BOTH paths. Git progress output routinely
// echoes the remote URL (https://user:token@host/…), so redacting
// on success prevents credentials from leaking through a legitimate
// clone or push whose stderr happened to include the helper-
// rewritten URL. Matches the forges package semantics;
// see the gitExec comment about eventual consolidation into
// internal/gitexec (.review/TODO.md D1).
func writeCmdResult(w http.ResponseWriter, out string, err error) {
	if err != nil {
		msg := out
		if strings.TrimSpace(msg) == "" {
			msg = err.Error()
		}
		api.WriteJSON(w, map[string]string{"error": gitexec.ScrubAuth(msg)})
		return
	}
	api.WriteJSON(w, map[string]string{"output": gitexec.ScrubAuth(out)})
}

// gitExec delegates to the shared gitexec package for hardened
// subprocess construction.
func gitExec(ctx context.Context, dir string, args ...string) *exec.Cmd {
	return gitexec.Cmd(ctx, dir, args...)
}

// gitCmd runs a git subprocess, trimming whitespace from combined
// output. Uses gitExec for the hardened env block.
func gitCmd(ctx context.Context, dir string, args ...string) (string, error) {
	return gitexec.Run(ctx, dir, args...)
}

// gitCmdWithCreds runs a git subprocess for network operations
// (push, pull, fetch). Credentials are served by the global git
// credential helpers configured by each forge CLI's setup-git
// (e.g. `gh auth setup-git`), so no per-call env injection is
// needed — git picks them up from ~/.gitconfig and the helper.
func (h *Handler) gitCmdWithCreds(ctx context.Context, timeout time.Duration, dir, _ string, args ...string) (string, error) {
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := gitExec(tctx, dir, args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
