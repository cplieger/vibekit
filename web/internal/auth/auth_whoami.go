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

	"vibekit/internal/api"
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
		api.MethodNotAllowed(w)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.WhoamiTimeout)
	defer cancel()
	// h.cliPath is operator-controlled (resolved at server start from
	// the bundled binary path), never user input — no G204 risk. The
	// repo-wide gosec G204 exclusion already suppresses the warning;
	// no //nolint needed.
	cmd := exec.CommandContext(ctx, h.cliPath, "whoami", "--format", "json")
	var stderr bytes.Buffer
	var stdoutBuf bytes.Buffer
	// Bounded stderr capture so a runaway or hostile kiro-cli can't
	// OOM the container via unbounded stderr on this per-page-load
	// endpoint. Mirrors the login/logout pattern.
	cmd.Stderr = &limitedWriter{W: &stderr, N: stderrCap}
	// Bounded stdout capture so a runaway CLI can't OOM the container.
	cmd.Stdout = &limitedWriter{W: &stdoutBuf, N: whoamiMaxOutput}
	err := cmd.Run()
	out := stdoutBuf.Bytes()
	if err != nil {
		// Distinguish timeout vs missing-binary vs generic failure
		// so Grafana can alert on each independently. All three
		// still return the generic "whoami unavailable" sentinel
		// to the client (fail-soft banner). Every stderr log
		// attribute is run through api.SanitizeOutput so ANSI /
		// hidden Unicode from a compromised kiro-cli can't inject
		// into Loki or any downstream AI log pipeline.
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			attrs := make([]any, 0, 4)
			attrs = append(attrs, "timeout", h.cfg.WhoamiTimeout)
			attrs = append(attrs, stderrAttr(&stderr)...)
			slog.Warn("whoami: kiro-cli timed out", attrs...)
		case errors.Is(err, exec.ErrNotFound), errors.Is(err, fs.ErrNotExist):
			// Warn (not Error): /api/whoami fires on every page
			// load and every SSE reconnect, so a single broken
			// deployment would otherwise flood the generic
			// level=error alerter. The fail-soft "whoami
			// unavailable" sentinel still reaches the client.
			slog.Warn("whoami: kiro-cli binary not found",
				"cli_path", h.cliPath)
		default:
			// Log full details server-side; don't leak raw CLI
			// output to the client (it can contain filesystem
			// paths or upstream diagnostics). The banner caller
			// only needs to see that whoami failed.
			attrs := make([]any, 0, 6)
			attrs = append(attrs, "error", err, "stdout_bytes", len(out))
			attrs = append(attrs, stderrAttr(&stderr)...)
			slog.Warn("whoami: kiro-cli invocation failed", attrs...)
		}
		api.WriteJSON(w, &WhoamiResponse{Error: "whoami unavailable"})
		return
	}
	info, err := whoamiInfo(out)
	if err != nil {
		slog.Warn("whoami: cli output not parseable as json",
			"error", err, "stdout_bytes", len(out))
		api.WriteJSON(w, &WhoamiResponse{Error: "whoami unavailable"})
		return
	}
	api.WriteJSON(w, info)
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
	// Email: prefer canonical lowercase; fall back to capital
	// "Email" key seen in kiro-cli 2.0.1.
	if email, ok := raw["email"].(string); ok && email != "" {
		resp.Email = email
	} else if email, ok := raw["Email"].(string); ok && email != "" {
		resp.Email = email
	}
	// AccountType: accept snake_case (earlier CLI) and camelCase
	// (kiro-cli 2.0.1+). Empty/non-string leaves Auth unset.
	if at, ok := raw["account_type"].(string); ok && at != "" {
		resp.AccountType = at
	} else if at, ok := raw["accountType"].(string); ok && at != "" {
		resp.AccountType = at
	}
	if resp.AccountType != "" {
		resp.Auth = humanizeAccountType(resp.AccountType)
	}
	// startUrl: accept camelCase (kiro-cli 2.0.1) and snake_case.
	if su, ok := raw["startUrl"].(string); ok && su != "" {
		resp.StartURL = su
	} else if su, ok := raw["start_url"].(string); ok && su != "" {
		resp.StartURL = su
	}
	if reg, ok := raw["region"].(string); ok && reg != "" {
		resp.Region = reg
	}
	return resp, nil
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
