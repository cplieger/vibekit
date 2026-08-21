// The GitHub OAuth device flow: vibekit runs the whole protocol, then hands
// the resulting token to `gh auth login --with-token` (see login.go).
//
// This was internal/forges/oauth, reachable only from here through three
// forwarding declarations in login.go. Rolling it up deleted them; what stays
// exported is the pair the HTTP surface serves (DeviceFlowResponse and
// StartGitHubDeviceFlow), while the raw token poll became pollDeviceToken —
// unexported, because the exported PollGitHubDeviceFlow is the one that also
// logs gh in, and a caller must not be able to reach the token-bearing half.

package forges

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

// deviceTokenResult is one raw token-poll outcome. Distinct from PollResult,
// which is what the HTTP surface returns: this one carries the access token, so
// it never leaves the package.
type deviceTokenResult struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	Token  string `json:"-"` // populated on success
}

// githubOAuthClientID is the OAuth app ID for GitHub device flow.
const githubOAuthClientID = "178c6fc778ccc68e1d6a"

// scopes requested for GitHub OAuth: repo ops + org listing (gh's own
// login minimum) plus workflow so pushes touching .github/workflows
// aren't rejected. No gist — vibekit has no gist feature (the old list
// was inherited from gh's own client, which has `gh gist`).
const githubOAuthScopes = "repo,read:org,workflow"

// oauthHTTPClient is the shared HTTP client for OAuth requests.
var oauthHTTPClient = &http.Client{Timeout: 30 * time.Second}

// StartGitHubDeviceFlow initiates the OAuth device flow with GitHub.
func StartGitHubDeviceFlow(ctx context.Context) (*DeviceFlowResponse, error) {
	form := url.Values{
		"client_id": {githubOAuthClientID},
		"scope":     {githubOAuthScopes},
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

// parseDeviceFlowResponse decodes a GitHub device-flow start response body
// into a DeviceFlowResponse, surfacing an embedded OAuth error as a Go
// error. This is the pure, testable core of StartGitHubDeviceFlow.
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
// On success, deviceTokenResult.Token contains the access token.
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

// interpretPollResponse maps a GitHub device-flow token-poll response body
// to a deviceTokenResult. This is the pure, testable core of pollDeviceToken:
// the OAuth error code drives the status, and an empty access_token on an
// otherwise-successful response is treated as an error, never "complete".
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
