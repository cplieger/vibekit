package git

// Request-prelude helpers used by every handler in this package. Keeps
// each handler to its git-specific logic without repeating the method
// check + body-decode + error-response dance.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/webhttp/v2"
)

// requirePOST writes a 405 if the method isn't POST and returns false.
// Returns true when the caller should proceed.
func requirePOST(w http.ResponseWriter, r *http.Request) bool {
	return httpreply.RequireMethod(w, r, http.MethodPost)
}

// decodePostBody does the common POST prelude:
//   - enforces the JSON body size cap (webhttp.MaxJSONBody)
//   - decodes into v
//
// On decode failure, writes a 400 with the given error message and
// returns false. Callers that need a method check should call
// requirePOST first.
func decodePostBody(w http.ResponseWriter, r *http.Request, v any, decodeErrMsg string) bool {
	return httpreply.DecodeBody(w, r, v, decodeErrMsg)
}

// decodePostBodyOptional enforces the body-size cap and decodes into v,
// reporting whether the caller may proceed. An absent or malformed body is
// ignored (v keeps its zero value, result true) because the body is purely
// advisory here — push/pull/stash name no required field. An oversize body
// returns false with a 413 already written: an unread Repo would resolve to the
// workspace root and silently retarget the git command.
func decodePostBodyOptional(w http.ResponseWriter, r *http.Request, v any) bool {
	return httpreply.DecodeBodyOptional(w, r, v)
}

// writeCmdResult writes a git-command result: {jsonKeyOutput:
// scrubAuth(out)} on success, {"error": scrubAuth(errMsg)} on
// failure. errMsg is the subprocess combined output when non-empty;
// otherwise err.Error().
//
// On failure the jsonKeyOutput field is intentionally omitted so clients
// cannot confuse a partial stdout stream with a successful response
// — presence-of-field is the success signal, not string emptiness.
//
// scrubAuth runs on BOTH paths. Git progress output routinely
// echoes the remote URL (https://user:token@host/…), so redacting
// on success prevents credentials from leaking through a legitimate
// clone or push whose stderr happened to include the helper-
// rewritten URL. Matches the forges package semantics.
func writeCmdResult(w http.ResponseWriter, out string, err error) {
	if err != nil {
		webhttp.WriteJSON(w, httpreply.ErrorJSON(scrubAuth(cmdFailure(out, err))))
		return
	}
	webhttp.WriteJSON(w, map[string]string{jsonKeyOutput: scrubAuth(out)})
}

// cmdFailure names WHY a git subprocess failed, for a caller that composes
// several failures into one message rather than writing the envelope itself.
// A git subprocess can fail with nothing on either stream — a rejected
// subcommand used to be the routine case — and a caller interpolating that
// empty output produced a message ending at its own colon ("clean:"), which
// reads as truncated and names no cause. The exit status is a poor message
// and a present one, so it stands in.
func cmdFailure(out string, err error) string {
	if strings.TrimSpace(out) != "" {
		return out
	}
	return err.Error()
}

// gitCmdWithCreds runs a git subprocess for network operations
// (push, pull, fetch). Credentials are served by the global git
// credential helpers configured by each forge CLI's setup-git
// (e.g. `gh auth setup-git`), so no per-call env injection is
// needed — git picks them up from ~/.gitconfig and the helper.
func gitCmdWithCreds(ctx context.Context, timeout time.Duration, dir, _ string, args ...string) (string, error) {
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := gitExec(tctx, dir, args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
