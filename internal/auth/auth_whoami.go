package auth

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/cplieger/runesafe/v2"
	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/webhttp/v2"
)

// WhoamiState discriminates /api/whoami's three answers. "I could not ask" is a
// state of its own so the UI can render a retry rather than a sign-out.
type WhoamiState string

// Every response carries exactly one arm.
const (
	// WhoamiSignedIn carries Email, and Auth/AccountType/StartURL/Region when
	// kiro-cli reported them.
	WhoamiSignedIn WhoamiState = "signed_in"
	// WhoamiSignedOut is a working kiro-cli reporting nobody signed in, and
	// carries nothing else.
	WhoamiSignedOut WhoamiState = "signed_out"
	// WhoamiUnavailable is vibekit not knowing, and carries Reason.
	WhoamiUnavailable WhoamiState = "unavailable"
)

// WhoamiResponse is the typed wire shape returned by /api/whoami. State is the
// discriminator; the remaining fields belong to one arm each. Any kiro-cli field
// not represented here is dropped at the wire boundary, so a compromised or
// upgraded CLI cannot leak arbitrary attributes into the browser.
type WhoamiResponse struct {
	State WhoamiState `json:"state"`
	// Email and the four labels below belong to the signed_in arm.
	Email       string `json:"email,omitempty"`
	Auth        string `json:"auth,omitempty"`
	AccountType string `json:"accountType,omitempty"`
	StartURL    string `json:"startUrl,omitempty"`
	Region      string `json:"region,omitempty"`
	// Reason belongs to the unavailable arm: a server-authored phrase, never
	// CLI output.
	Reason string `json:"reason,omitempty"`
}

// handleWhoami answers from the cached identity, never a subprocess: the
// endpoint fires on every page load and SSE reconnect, and identityCache owns
// when kiro-cli is forked. Always 200 — all three states are answers.
func (h *Handler) handleWhoami(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	webhttp.WriteJSON(w, h.identity.snapshot())
}

// whoamiInfo normalises kiro-cli's --format json whoami output into a
// WhoamiResponse, accepting both snake_case and camelCase field names and
// emitting canonical camelCase.
//
// The email decides the arm: a payload without one — a null payload included —
// is a working CLI reporting nobody signed in, so signed_out. kiro-cli 2.0.1+
// appends a non-JSON footer, so decode with json.Decoder: it consumes exactly
// one JSON value and ignores the trailing bytes.
func whoamiInfo(out []byte) (WhoamiResponse, error) {
	dec := json.NewDecoder(bytes.NewReader(out))
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return WhoamiResponse{}, err
	}
	if raw == nil {
		return signedOutIdentity(), nil
	}
	// Lowercase wins; "Email" is the fallback spelling.
	email := firstNonEmptyString(raw, "email", "Email")
	if email == "" {
		return signedOutIdentity(), nil
	}
	resp := WhoamiResponse{State: WhoamiSignedIn, Email: email}
	resp.AccountType = firstNonEmptyString(raw, "account_type", "accountType")
	if resp.AccountType != "" {
		resp.Auth = identityText(humanizeAccountType(resp.AccountType))
	}
	resp.StartURL = firstNonEmptyString(raw, "startUrl", "start_url")
	resp.Region = firstNonEmptyString(raw, "region")
	return resp, nil
}

// maxIdentityFieldBytes bounds one identity string on its way to the sidebar and
// the log.
const maxIdentityFieldBytes = 256

// identityText prepares one upstream identity string for a single-line UI row.
// The single-line preset maps C0/C1, DEL and Bidi controls to spaces rather than
// deleting them, because these are labels.
func identityText(s string) string {
	return runesafe.SanitizeSingleLineBounded(s, maxIdentityFieldBytes)
}

// firstNonEmptyString returns the first non-empty string value among keys in raw,
// or "" when none maps to one. The winner is sanitized here rather than at each
// assignment so no future field can be added without it.
func firstNonEmptyString(raw map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := raw[k].(string); ok && v != "" {
			return identityText(v)
		}
	}
	return ""
}

// The phrasing kiro-cli's own plaintext output uses.
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
