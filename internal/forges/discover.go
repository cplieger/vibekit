// CLI-native forge discovery (the read verb of auth.go's model).
//
// Connection state comes from each CLI's own machine-readable status
// output — never from parsing its config files:
//
//	gh:   gh auth status --json hosts   (storage-agnostic: sees keyring
//	      tokens the old hosts.yml parser could not)
//	tea:  tea logins list -o json
//	glab: read-only config parser (glab ships no JSON status output;
//	      see glab_config.go — the one documented exception)
//
// Connected derives from PRESENCE (host + login) only. gh's status
// output also carries a network-tested "state" field per account;
// consuming it would import network weather into the 30s-TTL forge
// list, so it is deliberately ignored — Manager.Probe remains the only
// network-verification, Connected-flipping path.

package forges

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

// ghStatusAccount is one account entry in `gh auth status --json hosts`.
// The full shape carries state/tokenSource/scopes/gitProtocol too; only
// presence fields are decoded (see the package comment).
type ghStatusAccount struct {
	Login  string `json:"login"`
	Active bool   `json:"active"`
}

// ghAuthHosts returns host → login for every account gh knows about.
// gh exits non-zero when any host's check errors AND when no hosts are
// configured, but still prints the JSON in the former case — so the
// stdout is decoded regardless of the exit status, and only an
// undecodable output with a real error propagates.
func ghAuthHosts(ctx context.Context) (map[string]string, error) {
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
		return map[string]string{}, nil
	}
	hosts := make(map[string]string, len(payload.Hosts))
	for host, accounts := range payload.Hosts {
		if len(accounts) == 0 {
			continue
		}
		login := accounts[0].Login
		for _, a := range accounts {
			if a.Active {
				login = a.Login
				break
			}
		}
		hosts[host] = login
	}
	return hosts, nil
}

// teaLoginInfo is one entry in `tea logins list -o json`. tea also
// emits ssh_host and a STRING "default" field; neither is needed.
type teaLoginInfo struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	User string `json:"user"`
}

// host returns the login's URL hostname, or "" when the URL is
// unparseable.
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
