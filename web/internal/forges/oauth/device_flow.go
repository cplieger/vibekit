// Package oauth implements the GitHub OAuth device flow protocol.
package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MIMETypeJSON is the Accept header for JSON responses.
const MIMETypeJSON = "application/json"

// DeviceFlowResponse describes a started OAuth device flow.
type DeviceFlowResponse struct {
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	DeviceCode      string `json:"device_code"`
	Interval        int    `json:"interval"`
	ExpiresIn       int    `json:"expires_in"`
}

// PollResult is the per-poll status during the device flow.
type PollResult struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	Token  string `json:"-"` // populated on success
}

// githubOAuthClientID is the OAuth app ID for GitHub device flow.
const githubOAuthClientID = "178c6fc778ccc68e1d6a"

// scopes requested for GitHub OAuth.
const githubOAuthScopes = "repo,read:org,gist,workflow"

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
	req.Header.Set("Accept", MIMETypeJSON)
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

// PollGitHubDeviceFlow checks if the user has approved the device.
// On success, PollResult.Token contains the access token.
func PollGitHubDeviceFlow(ctx context.Context, deviceCode string) (PollResult, error) {
	if deviceCode == "" {
		return PollResult{}, fmt.Errorf("oauth: missing device_code")
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
		return PollResult{}, fmt.Errorf("poll: build request: %w", err)
	}
	req.Header.Set("Accept", MIMETypeJSON)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return PollResult{}, fmt.Errorf("poll: github contact: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var raw struct {
		AccessToken      string `json:"access_token"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return PollResult{}, fmt.Errorf("poll: decode: %w", err)
	}
	if raw.Error != "" {
		switch raw.Error {
		case "authorization_pending", "slow_down":
			return PollResult{Status: "pending"}, nil
		case "expired_token":
			return PollResult{Status: "expired", Error: "device code expired"}, nil
		case "access_denied":
			return PollResult{Status: "error", Error: "access denied"}, nil
		}
		return PollResult{Status: "error", Error: raw.ErrorDescription}, nil
	}
	if raw.AccessToken == "" {
		return PollResult{Status: "error", Error: "empty access_token"}, nil
	}
	return PollResult{Status: "complete", Token: raw.AccessToken}, nil
}
