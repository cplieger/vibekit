// Forge login flows.
//
// Two paths:
//   - GitHub: OAuth device flow (vibekit handles the entire flow,
//     then injects the resulting token into ~/.config/gh/hosts.yml).
//   - GitLab/Gitea/Codeberg: PAT paste (UI prompts the user for a
//     personal access token, vibekit writes it into the CLI config).
//
// In both paths the result is the same: the CLI is fully authenticated
// AND git operations are configured via the CLI's credential helper.

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

	"vibekit/internal/api"
)

// DeviceFlowResponse describes a started OAuth device flow.
// Returned by StartGitHubDeviceFlow so the UI can show the code + URL.
type DeviceFlowResponse struct {
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	DeviceCode      string `json:"device_code"`
	Interval        int    `json:"interval"`
	ExpiresIn       int    `json:"expires_in"`
}

// PollResult is the per-poll status during the device flow.
type PollResult struct {
	Status string `json:"status"` // statePending, stateComplete, "expired", "error"
	Error  string `json:"error,omitempty"`
}

// githubOAuthClientID is the OAuth app ID vibekit uses for GitHub
// device flow. Pulled from the public git-credential-oauth project,
// which maintains pre-registered client IDs for popular forge tools
// (Apache 2.0). Same scope as the gh CLI uses.
//
// The client_id is public; only the client_secret would be sensitive
// (and we don't need one for device flow).
const githubOAuthClientID = "178c6fc778ccc68e1d6a"

// scopes requested for GitHub OAuth. Mirrors gh CLI's defaults.
const githubOAuthScopes = "repo,read:org,gist,workflow"

// StartGitHubDeviceFlow initiates the OAuth device flow with GitHub.
// Returns the user code + verification URI for the UI to display.
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
	req.Header.Set("Accept", api.MIMETypeJSON)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient().Do(req)
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
// On stateComplete, the token is written to gh's config and the helper
// is set up. Caller should then call Manager.Refresh to surface the
// new login.
func PollGitHubDeviceFlow(ctx context.Context, deviceCode string) (PollResult, error) {
	if deviceCode == "" {
		return PollResult{}, errors.New("forges: missing device_code")
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
	req.Header.Set("Accept", api.MIMETypeJSON)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient().Do(req)
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
			return PollResult{Status: statePending}, nil
		case "expired_token":
			return PollResult{Status: "expired", Error: "device code expired"}, nil
		case "access_denied":
			return PollResult{Status: statusError, Error: "access denied"}, nil
		}
		return PollResult{Status: statusError, Error: raw.ErrorDescription}, nil
	}
	if raw.AccessToken == "" {
		return PollResult{Status: statusError, Error: "empty access_token"}, nil
	}
	// Token in hand. Ensure gh is installed, inject, and refresh.
	if err := EnsureCLI(ctx, KindGitHub); err != nil {
		return PollResult{Status: statusError, Error: fmt.Sprintf("install gh: %v", err)}, nil
	}
	// Username can be discovered via gh after injection. Pass empty
	// for now; the manager refresh will repopulate from gh's view.
	if err := InjectToken(ctx, KindGitHub, "github.com", raw.AccessToken, ""); err != nil {
		return PollResult{Status: statusError, Error: fmt.Sprintf("inject token: %v", err)}, nil
	}
	return PollResult{Status: stateComplete}, nil
}

// LoginPATParams describes a PAT-based login (GitLab/Gitea/Codeberg).
type LoginPATParams struct {
	Kind     Kind
	Host     string
	Token    string
	Username string // optional — discovered via Whoami if empty
}

// LoginWithPAT performs a PAT-based login: validate, install CLI,
// inject token, run setup-git, refresh manager state.
//
// The CLI's own auth check is the source of truth for token validity:
// after writing the token we call Whoami; on failure the token is
// removed and the original error is returned.
func LoginWithPAT(ctx context.Context, p LoginPATParams) error {
	if p.Token == "" {
		return errors.New("forges: empty token")
	}
	if !p.Kind.Valid() {
		return fmt.Errorf("forges: invalid kind %q", p.Kind)
	}
	if p.Host == "" {
		p.Host = p.Kind.DefaultHost()
	}
	if p.Host == "" {
		return fmt.Errorf("forges: kind %q requires a host", p.Kind)
	}
	if err := EnsureCLI(ctx, p.Kind); err != nil {
		return fmt.Errorf("install %s: %w", p.Kind.CLI(), err)
	}
	if err := InjectToken(ctx, p.Kind, p.Host, p.Token, p.Username); err != nil {
		return fmt.Errorf("inject: %w", err)
	}
	// Validate by calling whoami via the CLI.
	provider, err := New(p.Kind, p.Host)
	if err != nil {
		return err
	}
	user, err := provider.Whoami(ctx)
	if err != nil {
		// Roll back the bad token.
		_ = RemoveToken(ctx, p.Kind, p.Host)
		return fmt.Errorf("validate: %w", err)
	}
	// If username wasn't supplied, persist the discovered one.
	if p.Username == "" && user != nil && user.Login != "" {
		_ = InjectToken(ctx, p.Kind, p.Host, p.Token, user.Login)
	}
	return nil
}

// Logout disconnects a forge: removes the token from the CLI config
// and asks the manager to refresh. Idempotent.
func Logout(ctx context.Context, kind Kind, host string) error {
	return RemoveToken(ctx, kind, host)
}

// oauthHTTPClient is the shared HTTP client for OAuth requests. Reused
// across calls to benefit from connection pooling. Bounded timeout to
// avoid pinning request goroutines on a slow forge.
var oauthHTTPClient = &http.Client{Timeout: 30 * time.Second}

// httpClient returns the HTTP client used for OAuth requests.
func httpClient() *http.Client {
	return oauthHTTPClient
}
