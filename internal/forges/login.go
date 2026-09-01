// Forge login flows.
//
// Two paths:
//   - GitHub: OAuth device flow (vibekit handles the entire flow, then
//     hands the resulting token to `gh auth login --with-token`).
//   - GitLab/Gitea/Codeberg: PAT paste (UI prompts the user for a
//     personal access token, vibekit pipes it to the CLI's own login
//     subcommand).
//
// In both paths the result is the same: the CLI is fully authenticated
// AND git operations are configured via the CLI's credential helper.
// The CLIs discover and store the account identity themselves; vibekit
// never supplies or persists a username.

package forges

import (
	"context"
	"errors"
	"fmt"
)

// PollResult is the per-poll status of the GitHub device flow. Deliberately
// tokenless: the access token never leaves the server. pollDeviceToken carries
// it as far as `gh auth login --with-token` and no further.
type PollResult struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// PollGitHubDeviceFlow checks if the user has approved the device.
// On complete, the token is written to gh's config and the helper
// is set up.
func PollGitHubDeviceFlow(ctx context.Context, deviceCode string) (PollResult, error) {
	result, err := pollDeviceToken(ctx, deviceCode)
	if err != nil {
		return PollResult{}, err
	}
	if result.Token == "" {
		return PollResult{Status: result.Status, Error: result.Error}, nil
	}
	if err := EnsureCLI(ctx, KindGitHub); err != nil {
		return PollResult{Status: statusError, Error: fmt.Sprintf("install gh: %v", err)}, nil
	}
	if err := cliLogin(ctx, KindGitHub, "github.com", result.Token); err != nil {
		return PollResult{Status: statusError, Error: fmt.Sprintf("store token: %v", err)}, nil
	}
	return PollResult{Status: stateComplete}, nil
}

// LoginPATParams describes a PAT-based login (GitLab/Gitea/Codeberg).
// The CLIs discover the account identity from the token themselves.
type LoginPATParams struct {
	Kind  Kind
	Host  string
	Token string
}

// LoginWithPAT performs a PAT-based login: install the CLI, log it in
// natively (the CLIs validate the token against the forge at login
// time), then verify with Whoami and roll back on failure.
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
	if err := cliLogin(ctx, p.Kind, p.Host, p.Token); err != nil {
		return err
	}
	provider, err := New(p.Kind, p.Host)
	if err != nil {
		return err
	}
	if _, err := provider.Whoami(ctx); err != nil {
		_ = cliLogout(ctx, p.Kind, p.Host)
		return fmt.Errorf("validate: %w", err)
	}
	return nil
}

// Logout disconnects a forge: removes the credential from the CLI's
// own store via its logout subcommand.
func Logout(ctx context.Context, kind Kind, host string) error {
	return cliLogout(ctx, kind, host)
}
