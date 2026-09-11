// Connection state comes from each CLI's own machine-readable status
// output, never from parsing its config files: gh's `auth status
// --json hosts`, tea's `logins list -o json`, and glab's read-only
// config parser (glab ships no JSON status output; see
// glab_config.go).
//
// Connected derives from presence (host + login) only. gh's status
// output also carries a network-tested "state" field; consuming it
// would import network weather into the 30s-TTL forge list, so it is
// deliberately ignored — Manager.Probe is the only network-verifying,
// Connected-flipping path.

package forges

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

// ghStatusAccount is one account entry in `gh auth status --json hosts`.
// Scopes is GitHub's own answer for that account's token (gh reports
// the X-Oauth-Scopes header its check received), so it is empty when
// the check could not reach GitHub.
type ghStatusAccount struct {
	Login  string `json:"login"`
	Scopes string `json:"scopes"`
	Active bool   `json:"active"`
}

// ghAuthStatus decodes `gh auth status --json hosts`. gh exits non-zero
// when any host's check errors AND when no hosts are configured, but
// still prints the JSON in the former case, so stdout is decoded
// regardless of exit status.
func ghAuthStatus(ctx context.Context) (map[string][]ghStatusAccount, error) {
	out, err := runCmd(ctx, CmdTimeout, nil, "gh", "auth", "status", "--json", "hosts")
	if errors.Is(err, ErrNotInstalled) {
		return nil, err
	}
	var payload struct {
		Hosts map[string][]ghStatusAccount `json:"hosts"`
	}
	if jsonErr := json.Unmarshal(out, &payload); jsonErr != nil || payload.Hosts == nil {
		if err != nil && !errors.Is(err, ErrNotLoggedIn) {
			return nil, err
		}
		return map[string][]ghStatusAccount{}, nil
	}
	return payload.Hosts, nil
}

// activeAccount picks the account whose credential gh actually uses:
// the one flagged active, else the first. One selection rule so the
// login a host reports and the scopes it reports name one account.
func activeAccount(accounts []ghStatusAccount) (ghStatusAccount, bool) {
	if len(accounts) == 0 {
		return ghStatusAccount{}, false
	}
	for _, a := range accounts {
		if a.Active {
			return a, true
		}
	}
	return accounts[0], true
}

// ghAuthHosts returns host -> login for every account gh knows about.
func ghAuthHosts(ctx context.Context) (map[string]string, error) {
	byHost, err := ghAuthStatus(ctx)
	if err != nil {
		return nil, err
	}
	hosts := make(map[string]string, len(byHost))
	for host, accounts := range byHost {
		a, ok := activeAccount(accounts)
		if !ok {
			continue
		}
		hosts[host] = a.Login
	}
	return hosts, nil
}

// ghGrantedScopes reports the OAuth scopes GitHub says the token gh
// currently holds for host carries. Best-effort by design: no gh, no
// login, or a check that could not reach GitHub all answer nil, and a
// caller composing a scope request then falls back to its own floor
// rather than failing the login.
func ghGrantedScopes(ctx context.Context, host string) []string {
	byHost, err := ghAuthStatus(ctx)
	if err != nil {
		return nil
	}
	a, ok := activeAccount(byHost[host])
	if !ok {
		return nil
	}
	return parseScopeList(a.Scopes)
}

// teaLoginInfo is one entry in `tea logins list -o json`.
type teaLoginInfo struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	User string `json:"user"`
}

func (l teaLoginInfo) host() string {
	u, err := url.Parse(strings.TrimSpace(l.URL))
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// teaLogins returns every login tea has stored.
func teaLogins(ctx context.Context) ([]teaLoginInfo, error) {
	out, err := runCmd(ctx, CmdTimeout, nil, cliTea, "logins", "list", "-o", "json")
	if err != nil {
		if errors.Is(err, ErrNotLoggedIn) {
			return nil, nil
		}
		return nil, err
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" || trimmed == "[]" {
		return nil, nil
	}
	var logins []teaLoginInfo
	if jsonErr := json.Unmarshal([]byte(trimmed), &logins); jsonErr != nil {
		return nil, jsonErr
	}
	return logins, nil
}
