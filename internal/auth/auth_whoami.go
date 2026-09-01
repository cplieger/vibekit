package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"

	"github.com/cplieger/runesafe/v2"
	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/procout"
	"github.com/cplieger/webhttp/v2"
)

// WhoamiResponse is the typed wire shape returned by /api/whoami. The
// frontend uses Email for the sidebar identity row and Auth for the
// humanised label. Any kiro-cli field not represented here is dropped at
// the wire boundary so a compromised or upgraded CLI cannot leak arbitrary
// attributes into the browser. Error is the fail-soft sentinel populated
// when the subprocess fails or its output isn't parseable.
const msgWhoamiUnavailable = "whoami unavailable"

// WhoamiResponse is the typed response from /api/whoami; see the block
// comment above for field semantics.
type WhoamiResponse struct {
	Email       string `json:"email,omitempty"`
	Auth        string `json:"auth,omitempty"`
	AccountType string `json:"accountType,omitempty"`
	StartURL    string `json:"startUrl,omitempty"`
	Region      string `json:"region,omitempty"`
	Error       string `json:"error,omitempty"`
}

// handleWhoami shells out to `kiro-cli whoami --format json` and returns
// the parsed JSON with an added "auth" field. Fails soft on command or
// parse error — the client uses this for a banner, not an auth gate; we
// write HTTP 200 with an "error" field. A wall-clock timeout caps the
// subprocess so a wedged SSO/STS refresh can't pin the HTTP handler
// indefinitely.
func (h *Handler) handleWhoami(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.WhoamiTimeout)
	defer cancel()
	// h.cliPath resolves the install manager's active version, never user
	// input — no G204 risk.
	cmd := exec.CommandContext(ctx, h.cliPath(), "whoami", "--format", "json") //nolint:gosec // G204: binary path from config
	// Honour WhoamiTimeout rather than the child's lifetime — see
	// boundChild. This endpoint fires on every page load and SSE reconnect.
	boundChild(cmd)
	stderr := procout.NewBuffer(stderrCap)
	stdoutBuf := procout.NewBuffer(whoamiMaxOutput)
	cmd.Stderr = stderr
	cmd.Stdout = stdoutBuf
	err := cmd.Run()
	out := stdoutBuf.Bytes()
	if err != nil {
		// Distinguish timeout vs missing-binary vs generic failure so
		// Grafana can alert on each independently; all three still return
		// the generic sentinel to the client. Every stderr attribute goes
		// through sanitize.Output so ANSI/hidden Unicode can't inject
		// into Loki.
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			attrs := make([]any, 0, 4)
			attrs = append(attrs, "timeout", h.cfg.WhoamiTimeout)
			attrs = append(attrs, stderrAttr(stderr)...)
			slog.Warn("whoami: kiro-cli timed out", attrs...)
		case errors.Is(err, exec.ErrNotFound), errors.Is(err, fs.ErrNotExist):
			// Warn, not Error: this fires on every page load and SSE
			// reconnect, so a broken deployment would flood an
			// error-level alerter.
			slog.Warn("whoami: kiro-cli binary not found",
				"cli_path", h.cliPath())
		default:
			// Log full details server-side; don't leak raw CLI output
			// to the client.
			attrs := make([]any, 0, 6)
			attrs = append(attrs, "error", err, "stdout_bytes", len(out))
			attrs = append(attrs, stderrAttr(stderr)...)
			slog.Warn("whoami: kiro-cli invocation failed", attrs...)
		}
		webhttp.WriteJSON(w, &WhoamiResponse{Error: msgWhoamiUnavailable})
		return
	}
	info, err := whoamiInfo(out)
	if err != nil {
		slog.Warn("whoami: cli output not parseable as json",
			"error", err, "stdout_bytes", len(out))
		webhttp.WriteJSON(w, &WhoamiResponse{Error: msgWhoamiUnavailable})
		return
	}
	webhttp.WriteJSON(w, info)
}

// whoamiInfo parses kiro-cli's --format json whoami output and normalises
// it into the typed WhoamiResponse. Accepts both snake_case and camelCase
// field names on input and emits canonical camelCase on output. A null
// JSON payload becomes a zero-valued WhoamiResponse. Non-string or empty
// account-type fields leave Auth unset so the UI renders "not signed in".
//
// EVERY STRING IS SANITIZED AND BOUNDED on its way out: the values
// originate at the user's identity provider (email, startUrl, region) and
// their only bound used to be the whole-output cap, so one oversized
// "email" reached the sidebar and a Bidi override in any field reordered
// the identity row.
//
// runesafe's single-line preset rather than sanitize.Output: these are
// one-line LABELS, so the preset turns C0/C1, DEL, Bidi controls and
// paragraph separators into spaces instead of deleting them.
//
// kiro-cli 2.0.1+ appends a non-JSON footer after the JSON payload;
// decoding via json.Decoder consumes exactly one JSON value so the
// trailing bytes are ignored.
func whoamiInfo(out []byte) (*WhoamiResponse, error) {
	dec := json.NewDecoder(bytes.NewReader(out))
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	resp := &WhoamiResponse{}
	if raw == nil {
		return resp, nil
	}
	// Field names accept both snake_case (earlier CLI) and camelCase
	// (kiro-cli 2.0.1+); the first non-empty string value wins.
	resp.Email = firstNonEmptyString(raw, "email", "Email")
	resp.AccountType = firstNonEmptyString(raw, "account_type", "accountType")
	if resp.AccountType != "" {
		resp.Auth = identityText(humanizeAccountType(resp.AccountType))
	}
	resp.StartURL = firstNonEmptyString(raw, "startUrl", "start_url")
	resp.Region = firstNonEmptyString(raw, "region")
	return resp, nil
}

// maxIdentityFieldBytes bounds one identity string on its way to the
// sidebar and the log.
const maxIdentityFieldBytes = 256

// identityText prepares one upstream identity string for a single-line UI
// row: runesafe's single-line preset, capped on a rune boundary. Same
// treatment translate.displayText gives upstream text bound for a banner.
func identityText(s string) string {
	return runesafe.SanitizeSingleLineBounded(s, maxIdentityFieldBytes)
}

// firstNonEmptyString returns the first non-empty string value found among
// keys in raw, or "" when none maps to a non-empty string. Lets whoamiInfo
// accept both snake_case and camelCase spellings of the same field.
//
// The winner is sanitized here rather than at each assignment so no future
// field can be added without it.
func firstNonEmptyString(raw map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := raw[k].(string); ok && v != "" {
			return identityText(v)
		}
	}
	return ""
}

// humanizeAccountType turns kiro-cli's enum values into the same phrasing
// the plaintext output uses.
const (
	authBuilderID      = "Logged in with Builder ID"
	authIdentityCenter = "Logged in with IAM Identity Center"
	authSocialLogin    = "Logged in with social login"
	authPrefixGeneric  = "Logged in with "
)

var accountTypeLabels = map[string]string{
	"builderid":         authBuilderID,
	"identitycenter":    authIdentityCenter,
	"iamidentitycenter": authIdentityCenter,
	"social":            authSocialLogin,
}

func humanizeAccountType(t string) string {
	if label, ok := accountTypeLabels[strings.ToLower(t)]; ok {
		return label
	}
	return authPrefixGeneric + t
}
