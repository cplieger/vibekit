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
	"errors"
	"fmt"

	"github.com/cplieger/vibekit/internal/forges/oauth"
)

// DeviceFlowResponse describes a started OAuth device flow.
// Re-exported from the oauth sub-package for API compatibility.
type DeviceFlowResponse = oauth.DeviceFlowResponse

// PollResult is the per-poll status during the device flow.
type PollResult struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// StartGitHubDeviceFlow initiates the OAuth device flow with GitHub.
func StartGitHubDeviceFlow(ctx context.Context) (*DeviceFlowResponse, error) {
	return oauth.StartGitHubDeviceFlow(ctx)
}

// PollGitHubDeviceFlow checks if the user has approved the device.
// On complete, the token is written to gh's config and the helper
// is set up.
func PollGitHubDeviceFlow(ctx context.Context, deviceCode string) (PollResult, error) {
	result, err := oauth.PollGitHubDeviceFlow(ctx, deviceCode)
	if err != nil {
		return PollResult{}, err
	}
	if result.Token == "" {
		return PollResult{Status: result.Status, Error: result.Error}, nil
	}
	// Token in hand. Ensure gh is installed, inject, and refresh.
	if err := EnsureCLI(ctx, KindGitHub); err != nil {
		return PollResult{Status: statusError, Error: fmt.Sprintf("install gh: %v", err)}, nil
	}
	if err := InjectToken(ctx, KindGitHub, "github.com", result.Token, ""); err != nil {
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

// Logout disconnects a forge: removes the token from the CLI config.
func Logout(ctx context.Context, kind Kind, host string) error {
	return RemoveToken(ctx, kind, host)
}
