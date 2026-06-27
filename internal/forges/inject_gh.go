package forges

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// writeGHHosts writes ~/.config/gh/hosts.yml with the given token.
// gh expects YAML; we hand-roll it since we only ever write a
// well-defined shape (host → {oauth_token, user, git_protocol}).
func writeGHHosts(host, token, username string) error {
	root, err := configHome()
	if err != nil {
		return err
	}
	dir := filepath.Join(root, "gh")
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir gh: %w", err)
	}
	path := filepath.Join(dir, "hosts.yml")

	hosts, err := loadGHHosts(path)
	if err != nil {
		return err
	}
	hosts[host] = ghHostEntry{
		OAuthToken:  token,
		User:        username,
		GitProtocol: protoHTTPS,
	}
	return writeYAML(path, marshalGHHosts(hosts))
}

// removeGHHost removes the host entry from gh's hosts.yml.
func removeGHHost(host string) error {
	root, err := configHome()
	if err != nil {
		return err
	}
	path := filepath.Join(root, "gh", "hosts.yml")
	if _, statErr := os.Stat(path); errors.Is(statErr, fs.ErrNotExist) {
		return nil
	}
	hosts, err := loadGHHosts(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	delete(hosts, host)
	if len(hosts) == 0 {
		return os.Remove(path)
	}
	return writeYAML(path, marshalGHHosts(hosts))
}

// ghHostEntry is what gh stores per host. Field order matches gh's
// own writer for diff cleanliness.
type ghHostEntry struct {
	OAuthToken  string
	User        string
	GitProtocol string
}

// ghHostsParser accumulates gh's host map while scanning hosts.yml.
type ghHostsParser struct {
	hosts      map[string]ghHostEntry
	current    string
	hasCurrent bool
}

// parseLine folds a single hosts.yml line into the accumulating host map.
func (p *ghHostsParser) parseLine(line string) {
	// Top-level keys (host names) start at column 0 with a colon.
	if !strings.HasPrefix(line, " ") && strings.HasSuffix(strings.TrimSpace(line), ":") {
		p.current = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ":"))
		p.hasCurrent = p.current != ""
		if p.hasCurrent {
			p.hosts[p.current] = ghHostEntry{}
		}
		return
	}
	if !p.hasCurrent {
		return
	}
	key, val, ok := strings.Cut(strings.TrimSpace(line), ":")
	if !ok {
		return
	}
	entry := p.hosts[p.current]
	entry.setField(strings.TrimSpace(key), strings.TrimSpace(val))
	p.hosts[p.current] = entry
}

// setField assigns a parsed key/value onto a host entry.
func (e *ghHostEntry) setField(key, val string) {
	switch key {
	case "oauth_token":
		e.OAuthToken = val
	case fieldUser:
		e.User = val
	case "git_protocol":
		e.GitProtocol = val
	}
}

// loadGHHosts parses a gh hosts.yml file. We don't need a full YAML
// parser — gh's format is a strict subset (host → flat key/value).
// Returns an empty map for a missing file.
func loadGHHosts(path string) (map[string]ghHostEntry, error) {
	hosts := make(map[string]ghHostEntry)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return hosts, nil
		}
		return nil, err
	}
	p := &ghHostsParser{hosts: hosts}
	for line := range strings.SplitSeq(string(data), "\n") {
		p.parseLine(line)
	}
	return hosts, nil
}

// marshalGHHosts renders the hosts map as YAML matching gh's writer.
func marshalGHHosts(hosts map[string]ghHostEntry) string {
	var b strings.Builder
	for host, e := range hosts {
		fmt.Fprintf(&b, "%s:\n", host)
		if e.OAuthToken != "" {
			fmt.Fprintf(&b, "    oauth_token: %s\n", e.OAuthToken)
		}
		if e.User != "" {
			fmt.Fprintf(&b, "    user: %s\n", e.User)
		}
		gp := e.GitProtocol
		if gp == "" {
			gp = protoHTTPS
		}
		fmt.Fprintf(&b, "    git_protocol: %s\n", gp)
	}
	return b.String()
}

// setupGitGH runs `gh auth setup-git` so git uses gh as its
// credential helper. Best-effort: a setup failure doesn't block
// login (the token is already stored).
func setupGitGH(ctx context.Context, host string) error {
	args := []string{"auth", "setup-git", flagHostname, host}
	_, err := runCmd(ctx, CmdTimeout, nil, "gh", args...)
	if err != nil && errors.Is(err, ErrNotInstalled) {
		return err
	}
	return nil
}
