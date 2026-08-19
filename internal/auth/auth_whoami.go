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

	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/procout"
	"github.com/cplieger/webhttp"
)

// WhoamiResponse is the typed wire shape returned by /api/whoami. The
// frontend uses Email for the sidebar identity row and Auth for the
// humanised label; AccountType / StartURL / Region are surfaced for
// future use. Any kiro-cli field not represented here is dropped at
// the wire boundary so a compromised or upgraded CLI cannot leak
// arbitrary attributes (e.g. account_id, profile, ARN) into the
// browser. Error is the fail-soft sentinel populated when the
// subprocess fails or its output isn't parseable; the HTTP layer
// always returns 200 with this shape so the banner caller sees a
// consistent JSON envelope regardless of the failure mode.
const msgWhoamiUnavailable = "whoami unavailable"

// WhoamiResponse is the typed response from /api/whoami; see the block
// comment above for the full field semantics and security rationale.
type WhoamiResponse struct {
	Email       string `json:"email,omitempty"`
	Auth        string `json:"auth,omitempty"`
	AccountType string `json:"accountType,omitempty"`
	StartURL    string `json:"startUrl,omitempty"`
	Region      string `json:"region,omitempty"`
	Error       string `json:"error,omitempty"`
}

// handleWhoami shells out to `kiro-cli whoami --format json` and returns
// the parsed JSON with an added "auth" field humanised via
// humanizeAccountType. Fails soft on command or parse error — the client
// uses this for a banner, not an auth gate; we write HTTP 200 with an
// "error" field so the UI can render a "not logged in" state. A
// wall-clock timeout caps the subprocess so a wedged SSO/STS refresh
// can't pin the HTTP handler indefinitely.
func (h *Handler) handleWhoami(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.WhoamiTimeout)
	defer cancel()
	// h.cliPath resolves the install manager's active version (see
	// Handler.cliPath), never user input — no G204 risk. The
	// repo-wide gosec G204 exclusion already suppresses the warning;
	// no //nolint needed.
	cmd := exec.CommandContext(ctx, h.cliPath(), "whoami", "--format", "json") //nolint:gosec // G204: binary path from config
	stderr := procout.NewBuffer(stderrCap)
	stdoutBuf := procout.NewBuffer(whoamiMaxOutput)
	// Bounded stderr capture so a runaway or hostile kiro-cli can't
	// OOM the container via unbounded stderr on this per-page-load
	// endpoint. Mirrors the login/logout pattern.
	cmd.Stderr = stderr
	// Bounded stdout capture so a runaway CLI can't OOM the container.
	cmd.Stdout = stdoutBuf
	err := cmd.Run()
	out := stdoutBuf.Bytes()
	if err != nil {
		// Distinguish timeout vs missing-binary vs generic failure
		// so Grafana can alert on each independently. All three
		// still return the generic msgWhoamiUnavailable sentinel
		// to the client (fail-soft banner). Every stderr log
		// attribute is run through sanitize.Output so ANSI /
		// hidden Unicode from a compromised kiro-cli can't inject
		// into Loki or any downstream AI log pipeline.
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			attrs := make([]any, 0, 4)
			attrs = append(attrs, "timeout", h.cfg.WhoamiTimeout)
			attrs = append(attrs, stderrAttr(stderr)...)
			slog.Warn("whoami: kiro-cli timed out", attrs...)
		case errors.Is(err, exec.ErrNotFound), errors.Is(err, fs.ErrNotExist):
			// Warn (not Error): /api/whoami fires on every page
			// load and every SSE reconnect, so a single broken
			// deployment would otherwise flood the generic
			// level=error alerter. The fail-soft "whoami
			// unavailable" sentinel still reaches the client.
			slog.Warn("whoami: kiro-cli binary not found",
				"cli_path", h.cliPath())
		default:
			// Log full details server-side; don't leak raw CLI
			// output to the client (it can contain filesystem
			// paths or upstream diagnostics). The banner caller
			// only needs to see that whoami failed.
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

// whoamiInfo parses kiro-cli's --format json whoami output and
// normalises it into the typed WhoamiResponse. Accepts both
// snake_case (earlier CLI) and camelCase (kiro-cli 2.0.1+) field
// names on input and emits canonical camelCase on output. A null
// JSON payload becomes a zero-valued WhoamiResponse (no fields set)
// so the frontend always sees a consistent shape. Non-string or
// empty account-type fields leave Auth unset so the UI can render
// the malformed-upstream case as "not signed in".
//
// kiro-cli 2.0.1+ appends a non-JSON footer after the JSON payload
// (e.g. a "Profile: ..." banner from Identity Center). Decoding via
// json.Decoder consumes exactly one JSON value so the trailing bytes
// are ignored.
//
// Fields kiro-cli emits that aren't represented on WhoamiResponse
// (account_id, profile, ARN, etc.) are deliberately dropped at this
// boundary to keep the wire surface narrow and stable; per the
// rewrite proposal AUTH-01, this prevents accidental field leakage
// from CLI version bumps.
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
	// (kiro-cli 2.0.1+); the first non-empty string value wins, so a
	// canonical key takes precedence over its alias. Email also accepts
	// the capital "Email" key seen in kiro-cli 2.0.1. Empty / non-string
	// account-type leaves Auth unset so the UI renders "not signed in".
	resp.Email = firstNonEmptyString(raw, "email", "Email")
	resp.AccountType = firstNonEmptyString(raw, "account_type", "accountType")
	if resp.AccountType != "" {
		resp.Auth = humanizeAccountType(resp.AccountType)
	}
	resp.StartURL = firstNonEmptyString(raw, "startUrl", "start_url")
	resp.Region = firstNonEmptyString(raw, "region")
	return resp, nil
}

// firstNonEmptyString returns the first non-empty string value found among
// keys in raw, in order, or "" when none maps to a non-empty string. Lets
// whoamiInfo accept both snake_case and camelCase spellings of the same
// kiro-cli field while preferring the canonical key, without repeating the
// type-assert-and-empty-check pattern per field.
func firstNonEmptyString(raw map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := raw[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// humanizeAccountType turns kiro-cli's enum values into the same
// phrasing the plaintext output uses (stable copy the UI already
// understands).
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
