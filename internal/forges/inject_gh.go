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
	var current string
	var hasCurrent bool
	for line := range strings.SplitSeq(string(data), "\n") {
		// Top-level keys (host names) start at column 0 with a colon.
		if !strings.HasPrefix(line, " ") && strings.HasSuffix(strings.TrimSpace(line), ":") {
			current = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ":"))
			if current == "" {
				hasCurrent = false
				continue
			}
			hasCurrent = true
			hosts[current] = ghHostEntry{}
			continue
		}
		if !hasCurrent {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, ":") {
			continue
		}
		parts := strings.SplitN(trimmed, ":", 2)
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		entry := hosts[current]
		switch key {
		case "oauth_token":
			entry.OAuthToken = val
		case fieldUser:
			entry.User = val
		case "git_protocol":
			entry.GitProtocol = val
		}
		hosts[current] = entry
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
