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

// WhoamiState discriminates /api/whoami's three answers.
//
// The endpoint used to answer {email, error} and the client read only `email`,
// so a 5-second kiro-cli timeout and a genuine sign-out were the same response:
// the client took its !authenticated branch and put a sign-in prompt over an app
// it had already hidden. Three states make "I could not ask" a state of its own,
// which is the one the UI must render as a retry rather than as a sign-out.
type WhoamiState string

// The three arms. Every response carries exactly one.
const (
	// WhoamiSignedIn carries Email, and Auth/AccountType/StartURL/Region when
	// kiro-cli reported them.
	WhoamiSignedIn WhoamiState = "signed_in"
	// WhoamiSignedOut is a working kiro-cli reporting nobody signed in. It
	// carries nothing else.
	WhoamiSignedOut WhoamiState = "signed_out"
	// WhoamiUnavailable is vibekit not knowing, and carries Reason.
	WhoamiUnavailable WhoamiState = "unavailable"
)

// WhoamiResponse is the typed wire shape returned by /api/whoami.
//
// State is the discriminator; the remaining fields belong to one arm each. Any
// kiro-cli field not represented here is dropped at the wire boundary, so a
// compromised or upgraded CLI cannot leak arbitrary attributes into the
// browser.
type WhoamiResponse struct {
	State WhoamiState `json:"state"`
	// Email and the four labels below belong to the signed_in arm. The
	// frontend uses Email for the sidebar identity row and Auth for the
	// humanised label.
	Email       string `json:"email,omitempty"`
	Auth        string `json:"auth,omitempty"`
	AccountType string `json:"accountType,omitempty"`
	StartURL    string `json:"startUrl,omitempty"`
	Region      string `json:"region,omitempty"`
	// Reason belongs to the unavailable arm: a short server-authored phrase
	// the client shows in a retry banner. Never CLI output.
	Reason string `json:"reason,omitempty"`
}

// handleWhoami answers from the cached identity. No subprocess, ever.
//
// The endpoint fires on every page load and every SSE reconnect, and it used to
// fork kiro-cli for each one — measured over 88 calls at p50 457 ms, with
// 4,420-5,002 ms on 8 of them and three hard 5-second timeouts. The server owns
// the identity, so a page load has no business forking a Rust binary for it;
// identityCache owns when the fork happens instead.
//
// Always 200: all three states are answers, not errors.
func (h *Handler) handleWhoami(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	webhttp.WriteJSON(w, h.identity.snapshot())
}

// whoamiInfo parses kiro-cli's --format json whoami output and normalises it
// into the typed WhoamiResponse. Accepts both snake_case and camelCase field
// names on input and emits canonical camelCase on output.
//
// The EMAIL decides the arm: a payload without one is a working CLI saying
// nobody is signed in, which is signed_out. A null payload is the same claim.
// Note what this means for the failure paths — they never reach here, so
// signed_out can only be produced by an answer vibekit actually received.
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
func whoamiInfo(out []byte) (WhoamiResponse, error) {
	dec := json.NewDecoder(bytes.NewReader(out))
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return WhoamiResponse{}, err
	}
	if raw == nil {
		return signedOutIdentity(), nil
	}
	// Field names accept both snake_case (earlier CLI) and camelCase
	// (kiro-cli 2.0.1+); the first non-empty string value wins.
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
