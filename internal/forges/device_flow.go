// The GitHub OAuth device flow: vibekit runs the whole protocol, then
// hands the resulting token to `gh auth login --with-token` (see
// login.go).

package forges

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// mimeTypeJSON is the Accept header for JSON responses.
const mimeTypeJSON = "application/json"

// DeviceFlowResponse describes a started OAuth device flow.
type DeviceFlowResponse struct {
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	DeviceCode      string `json:"device_code"`
	Interval        int    `json:"interval"`
	ExpiresIn       int    `json:"expires_in"`
}

// deviceTokenResult is one raw token-poll outcome; it carries the
// access token and never leaves the package.
type deviceTokenResult struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	Token  string `json:"-"`
}

// githubOAuthClientID is the OAuth app ID for GitHub device flow.
const githubOAuthClientID = "178c6fc778ccc68e1d6a"

// githubOAuthBaselineScopes is the FLOOR vibekit needs, not the whole
// request: repo ops + org listing (gh's own login minimum) plus
// workflow so pushes touching .github/workflows aren't rejected.
//
// Do NOT add a capability scope here to serve a one-off need. Widening
// the baseline widens EVERY user's token for a feature vibekit does not
// have, which is inherited consent rather than a decision anyone made.
// The answer for a one-off is `gh auth refresh -s <scope>` inside the
// container, which the union below then preserves for good.
//
// StartGitHubDeviceFlow asks for this PLUS every scope the token gh
// already holds, because loginGH hands the result to `gh auth login
// --with-token`, which REPLACES gh's stored credential. A reconnect
// requesting this list alone would drop any scope granted out of band
// — a `gh auth refresh -s gist` to publish a gist, say — and nothing
// would report the loss, so the capability would come back missing
// weeks later as a 404 on unrelated work. GitHub issues tokens per
// user/application/scope combination, so a narrower request is a
// narrower token; asking for the union cannot narrow.
const githubOAuthBaselineScopes = "repo,read:org,workflow"

// maxScopeLen bounds one scope token. GitHub's longest today is
// security_events at 15; 64 leaves room for a scope invented later
// without letting a garbage value reach the request.
const maxScopeLen = 64

var oauthHTTPClient = &http.Client{Timeout: 30 * time.Second}

// validScope reports whether s has the shape of a GitHub OAuth scope:
// lowercase letters, digits, ':', '_' and '-'.
//
// Not a trust boundary — the scopes come from GitHub via gh, not from
// a user — but a fail-safe: ONE unrecognized token makes GitHub reject
// the whole device-code request, and a working login is worth more
// than preserving a scope nothing can name.
func validScope(s string) bool {
	if s == "" || len(s) > maxScopeLen {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == ':', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// parseScopeList splits a comma-separated scope list, dropping blanks
// and anything not scope-shaped. gh reports `auth status` scopes
// comma-and-space separated ("gist, read:org, repo"); GitHub's device
// endpoint accepts the comma form vibekit sends.
func parseScopeList(s string) []string {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if part = strings.TrimSpace(part); validScope(part) {
			out = append(out, part)
		}
	}
	return out
}

// scopeRequest builds the device-flow scope parameter: the baseline
// first in its declared order, then every already-granted scope the
// baseline does not name, sorted so one granted set always yields one
// request string.
func scopeRequest(granted []string) string {
	baseline := parseScopeList(githubOAuthBaselineScopes)
	seen := make(map[string]struct{}, len(baseline)+len(granted))
	for _, s := range baseline {
		seen[s] = struct{}{}
	}
	var extra []string
	for _, s := range granted {
		if !validScope(s) {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		extra = append(extra, s)
	}
	slices.Sort(extra)
	return strings.Join(slices.Concat(baseline, extra), ",")
}

// StartGitHubDeviceFlow initiates the OAuth device flow with GitHub,
// preserving the scopes the stored token already carries (see
// githubOAuthBaselineScopes).
func StartGitHubDeviceFlow(ctx context.Context) (*DeviceFlowResponse, error) {
	baseline := scopeRequest(nil)
	scopes := scopeRequest(ghGrantedScopes(ctx, KindGitHub.DefaultHost()))
	resp, err := requestDeviceCode(ctx, scopes)
	if err == nil {
		return resp, nil
	}
	if scopes == baseline {
		return nil, err
	}
	// A scope GitHub has since retired would otherwise cost the user
	// the login outright, so fall back to the floor: the set vibekit
	// knows GitHub still accepts.
	//
	// This is the ONE moment a capability loss is both happening and
	// detectable — every other way a scope goes missing (a container
	// reset, a token minted elsewhere) leaves nothing to compare
	// against — so the line names the dropped scopes and the command
	// that restores them rather than only the two lists. Scope names
	// are not secrets; the token is never logged.
	slog.Warn("forges: device flow rejected the preserved scopes, retrying with the baseline; these are dropped until re-added",
		"dropped", strings.Join(droppedScopes(scopes, baseline), ","),
		"remedy", "gh auth refresh -h "+KindGitHub.DefaultHost()+" -s <scope>",
		"requested", scopes, "baseline", baseline, "error", err)
	return requestDeviceCode(ctx, baseline)
}

// droppedScopes reports the scopes in requested that baseline does not
// carry — what the fallback costs, which is the actionable half of a
// two-list diff a reader would otherwise have to do by eye.
func droppedScopes(requested, baseline string) []string {
	keep := make(map[string]struct{})
	for _, s := range parseScopeList(baseline) {
		keep[s] = struct{}{}
	}
	var out []string
	for _, s := range parseScopeList(requested) {
		if _, ok := keep[s]; !ok {
			out = append(out, s)
		}
	}
	return out
}

// requestDeviceCode asks GitHub for a device code carrying one scope
// list.
func requestDeviceCode(ctx context.Context, scopes string) (*DeviceFlowResponse, error) {
	form := url.Values{
		"client_id": {githubOAuthClientID},
		"scope":     {scopes},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://github.com/login/device/code",
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("device flow: build request: %w", err)
	}
	req.Header.Set("Accept", mimeTypeJSON)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("device flow: github contact: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device flow: github status %d: %s", resp.StatusCode, body)
	}
	return parseDeviceFlowResponse(body)
}

// parseDeviceFlowResponse is the pure, testable core of
// StartGitHubDeviceFlow.
func parseDeviceFlowResponse(body []byte) (*DeviceFlowResponse, error) {
	var raw struct {
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		DeviceCode      string `json:"device_code"`
		Error           string `json:"error"`
		Interval        int    `json:"interval"`
		ExpiresIn       int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("device flow: decode: %w", err)
	}
	if raw.Error != "" {
		return nil, fmt.Errorf("device flow: %s", raw.Error)
	}
	return &DeviceFlowResponse{
		UserCode:        raw.UserCode,
		VerificationURI: raw.VerificationURI,
		DeviceCode:      raw.DeviceCode,
		Interval:        raw.Interval,
		ExpiresIn:       raw.ExpiresIn,
	}, nil
}

// pollDeviceToken checks whether the user has approved the device.
func pollDeviceToken(ctx context.Context, deviceCode string) (deviceTokenResult, error) {
	if deviceCode == "" {
		return deviceTokenResult{}, errors.New("oauth: missing device_code")
	}
	form := url.Values{
		"client_id":   {githubOAuthClientID},
		"device_code": {deviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://github.com/login/oauth/access_token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return deviceTokenResult{}, fmt.Errorf("poll: build request: %w", err)
	}
	req.Header.Set("Accept", mimeTypeJSON)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return deviceTokenResult{}, fmt.Errorf("poll: github contact: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return interpretPollResponse(body)
}

// interpretPollResponse is the pure, testable core of pollDeviceToken:
// an empty access_token on an otherwise-successful response is
// treated as an error, never "complete".
func interpretPollResponse(body []byte) (deviceTokenResult, error) {
	var raw struct {
		AccessToken      string `json:"access_token"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return deviceTokenResult{}, fmt.Errorf("poll: decode: %w", err)
	}
	if raw.Error != "" {
		switch raw.Error {
		case "authorization_pending", "slow_down":
			return deviceTokenResult{Status: "pending"}, nil
		case "expired_token":
			return deviceTokenResult{Status: "expired", Error: "device code expired"}, nil
		case "access_denied":
			return deviceTokenResult{Status: statusError, Error: "access denied"}, nil
		}
		return deviceTokenResult{Status: statusError, Error: raw.ErrorDescription}, nil
	}
	if raw.AccessToken == "" {
		return deviceTokenResult{Status: statusError, Error: "empty access_token"}, nil
	}
	return deviceTokenResult{Status: "complete", Token: raw.AccessToken}, nil
}
