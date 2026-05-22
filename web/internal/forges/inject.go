// Token injection into CLI config files. Each CLI keeps its own
// auth store at a well-known path; we write tokens there directly
// after vibekit's OAuth/PAT flow completes. After injection, we
// run the CLI's "setup-git" subcommand so git push/pull/clone work
// with the same credential automatically.
//
// gh:   ~/.config/gh/hosts.yml
// glab: ~/.config/glab-cli/config.yml
// tea:  ~/.config/tea/config.yml
//
// The CLI config files are owner-readable (0600). The container is
// single-user so file permissions are sufficient — no extra crypto.

package forges

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// configHomeOverride lets tests redirect the per-CLI config paths
// to a temp dir. Empty (the default) means use $XDG_CONFIG_HOME
// or ~/.config.
var (
	configHomeMu       sync.RWMutex
	configHomeOverride string
)

// SetConfigHome overrides the config root for tests. Pass "" to
// reset to the default.
func SetConfigHome(p string) {
	configHomeMu.Lock()
	configHomeOverride = p
	configHomeMu.Unlock()
}

// configHome returns the directory the CLIs read configs from.
// Defaults to $XDG_CONFIG_HOME or $HOME/.config.
func configHome() (string, error) {
	configHomeMu.RLock()
	override := configHomeOverride
	configHomeMu.RUnlock()
	if override != "" {
		return override, nil
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return x, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config home: %w", err)
	}
	return filepath.Join(home, ".config"), nil
}

// InjectToken writes the OAuth/PAT token into the appropriate CLI
// config file for the given kind/host. After writing, it runs the
// CLI's setup-git command so git operations are also authenticated.
//
// authMethod: "oauth" or "pat" (purely informational; both flows
// produce a valid token that the CLI accepts).
func InjectToken(ctx context.Context, kind Kind, host, token, username string) error {
	if token == "" {
		return errors.New("forges: empty token")
	}
	if host == "" {
		host = kind.DefaultHost()
	}
	if host == "" {
		return fmt.Errorf("forges: kind %q requires a host for injection", kind)
	}
	switch kind {
	case KindGitHub:
		if err := writeGHHosts(host, token, username); err != nil {
			return fmt.Errorf("inject gh: %w", err)
		}
		return setupGitGH(ctx, host)
	case KindGitLab:
		if err := writeGLabConfig(host, token, username); err != nil {
			return fmt.Errorf("inject glab: %w", err)
		}
		return setupGitGLab(ctx, host)
	case KindGitea, KindCodeberg:
		if err := writeTeaConfig(host, token, username); err != nil {
			return fmt.Errorf("inject tea: %w", err)
		}
		return setupGitTea(host)
	}
	return fmt.Errorf("forges: unhandled kind %q", kind)
}

// RemoveToken deletes the credential entry for kind/host from the
// CLI's config. Used on disconnect.
func RemoveToken(ctx context.Context, kind Kind, host string) error {
	if host == "" {
		host = kind.DefaultHost()
	}
	switch kind {
	case KindGitHub:
		return removeGHHost(host)
	case KindGitLab:
		return removeGLabHost(host)
	case KindGitea, KindCodeberg:
		return removeTeaHost(host)
	}
	return nil
}

// ----- gh ----------------------------------------------------------

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
		GitProtocol: "https",
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
	for line := range strings.SplitSeq(string(data), "\n") {
		// Top-level keys (host names) start at column 0 with a colon.
		if !strings.HasPrefix(line, " ") && strings.HasSuffix(strings.TrimSpace(line), ":") {
			current = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ":"))
			hosts[current] = ghHostEntry{}
			continue
		}
		if current == "" {
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
		case "user":
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
			gp = "https"
		}
		fmt.Fprintf(&b, "    git_protocol: %s\n", gp)
	}
	return b.String()
}

// setupGitGH runs `gh auth setup-git` so git uses gh as its
// credential helper. Best-effort: a setup failure doesn't block
// login (the token is already stored).
func setupGitGH(ctx context.Context, host string) error {
	args := []string{"auth", "setup-git", "--hostname", host}
	_, err := runCmd(ctx, CmdTimeout, nil, "gh", args...)
	if err != nil && errors.Is(err, ErrNotInstalled) {
		// gh isn't on PATH; install was supposed to run before this.
		// Surface the error so the caller can show it.
		return err
	}
	return nil
}

// ----- glab --------------------------------------------------------

// writeGLabConfig writes glab's per-host config entry.
func writeGLabConfig(host, token, username string) error {
	root, err := configHome()
	if err != nil {
		return err
	}
	dir := filepath.Join(root, "glab-cli")
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir glab-cli: %w", err)
	}
	path := filepath.Join(dir, "config.yml")
	cfg, err := loadGLabConfig(path)
	if err != nil {
		return err
	}
	if cfg.Hosts == nil {
		cfg.Hosts = make(map[string]glabHostEntry)
	}
	cfg.Hosts[host] = glabHostEntry{
		Token:    token,
		User:     username,
		Protocol: "https",
		APIHost:  host,
	}
	if cfg.Editor == "" {
		cfg.Editor = "" // leave blank; glab uses $EDITOR
	}
	return writeYAML(path, marshalGLabConfig(cfg))
}

func removeGLabHost(host string) error {
	root, err := configHome()
	if err != nil {
		return err
	}
	path := filepath.Join(root, "glab-cli", "config.yml")
	if _, statErr := os.Stat(path); errors.Is(statErr, fs.ErrNotExist) {
		return nil
	}
	cfg, err := loadGLabConfig(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	delete(cfg.Hosts, host)
	if len(cfg.Hosts) == 0 {
		return os.Remove(path)
	}
	return writeYAML(path, marshalGLabConfig(cfg))
}

type glabHostEntry struct {
	Token    string
	User     string
	Protocol string
	APIHost  string
}

type glabConfig struct {
	Hosts  map[string]glabHostEntry
	Editor string
}

func loadGLabConfig(path string) (*glabConfig, error) {
	cfg := &glabConfig{Hosts: make(map[string]glabHostEntry)}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cfg, nil
		}
		return nil, err
	}
	// glab config.yml has a flat top-level + a hosts: subtree.
	var inHosts bool
	var currentHost string
	for line := range strings.SplitSeq(string(data), "\n") {
		stripped := strings.TrimRight(line, " \t")
		if stripped == "" {
			continue
		}
		// Top-level key.
		if !strings.HasPrefix(line, " ") {
			key := strings.TrimSuffix(strings.TrimSpace(line), ":")
			if key == "hosts" {
				inHosts = true
				continue
			}
			inHosts = false
			currentHost = ""
			continue
		}
		if !inHosts {
			continue
		}
		// 4-space indent → host name; 8-space indent → host fields.
		switch {
		case strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "        "):
			name := strings.TrimSuffix(strings.TrimSpace(line), ":")
			currentHost = name
			cfg.Hosts[currentHost] = glabHostEntry{}
		case strings.HasPrefix(line, "        ") && currentHost != "":
			trimmed := strings.TrimSpace(line)
			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) != 2 {
				continue
			}
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			entry := cfg.Hosts[currentHost]
			switch key {
			case "token":
				entry.Token = val
			case "user":
				entry.User = val
			case "git_protocol":
				entry.Protocol = val
			case "api_host":
				entry.APIHost = val
			}
			cfg.Hosts[currentHost] = entry
		}
	}
	return cfg, nil
}

func marshalGLabConfig(cfg *glabConfig) string {
	var b strings.Builder
	if cfg.Editor != "" {
		fmt.Fprintf(&b, "editor: %s\n", cfg.Editor)
	}
	if len(cfg.Hosts) > 0 {
		b.WriteString("hosts:\n")
		for host, e := range cfg.Hosts {
			fmt.Fprintf(&b, "    %s:\n", host)
			if e.Token != "" {
				fmt.Fprintf(&b, "        token: %s\n", e.Token)
			}
			if e.User != "" {
				fmt.Fprintf(&b, "        user: %s\n", e.User)
			}
			gp := e.Protocol
			if gp == "" {
				gp = "https"
			}
			fmt.Fprintf(&b, "        git_protocol: %s\n", gp)
			if e.APIHost != "" {
				fmt.Fprintf(&b, "        api_host: %s\n", e.APIHost)
			}
		}
	}
	return b.String()
}

func setupGitGLab(ctx context.Context, host string) error {
	args := []string{"auth", "git-credential", "configure", "--hostname", host}
	_, err := runCmd(ctx, CmdTimeout, nil, "glab", args...)
	if err != nil && errors.Is(err, ErrNotInstalled) {
		return err
	}
	return nil
}

// ----- tea ---------------------------------------------------------

// writeTeaConfig writes tea's per-host login entry.
func writeTeaConfig(host, token, username string) error {
	root, err := configHome()
	if err != nil {
		return err
	}
	dir := filepath.Join(root, "tea")
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir tea: %w", err)
	}
	path := filepath.Join(dir, "config.yml")
	cfg, err := loadTeaConfig(path)
	if err != nil {
		return err
	}
	// tea identifies logins by name. We use the host as the name so
	// it's stable and human-readable.
	cfg.upsertLogin(teaLogin{
		Name:    host,
		URL:     "https://" + host,
		Token:   token,
		User:    username,
		Default: len(cfg.Logins) == 0,
	})
	return writeYAML(path, marshalTeaConfig(cfg))
}

func removeTeaHost(host string) error {
	root, err := configHome()
	if err != nil {
		return err
	}
	path := filepath.Join(root, "tea", "config.yml")
	if _, statErr := os.Stat(path); errors.Is(statErr, fs.ErrNotExist) {
		return nil
	}
	cfg, err := loadTeaConfig(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	cfg.removeLogin(host)
	if len(cfg.Logins) == 0 {
		return os.Remove(path)
	}
	return writeYAML(path, marshalTeaConfig(cfg))
}

type teaLogin struct {
	Name    string
	URL     string
	Token   string
	User    string
	Default bool
}

type teaConfig struct {
	Logins []teaLogin
}

func (c *teaConfig) upsertLogin(l teaLogin) {
	for i := range c.Logins {
		if c.Logins[i].Name == l.Name {
			c.Logins[i] = l
			return
		}
	}
	c.Logins = append(c.Logins, l)
}

func (c *teaConfig) removeLogin(name string) {
	out := c.Logins[:0]
	for _, l := range c.Logins {
		if l.Name != name {
			out = append(out, l)
		}
	}
	c.Logins = out
}

func loadTeaConfig(path string) (*teaConfig, error) {
	cfg := &teaConfig{}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cfg, nil
		}
		return nil, err
	}
	// tea config has a top-level `logins:` array. Each entry is a
	// 2-space-indented mapping with `- name:` markers.
	var inLogins bool
	var current *teaLogin
	for line := range strings.SplitSeq(string(data), "\n") {
		stripped := strings.TrimRight(line, " \t")
		if stripped == "" {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "-") {
			key := strings.TrimSuffix(strings.TrimSpace(line), ":")
			inLogins = key == "logins"
			if current != nil {
				cfg.Logins = append(cfg.Logins, *current)
				current = nil
			}
			continue
		}
		if !inLogins {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- name:") {
			if current != nil {
				cfg.Logins = append(cfg.Logins, *current)
			}
			current = &teaLogin{}
			val := strings.TrimSpace(strings.TrimPrefix(trimmed, "- name:"))
			current.Name = val
			continue
		}
		if current == nil {
			continue
		}
		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		switch key {
		case "url":
			current.URL = val
		case "token":
			current.Token = val
		case "user":
			current.User = val
		case "default":
			current.Default = val == "true"
		}
	}
	if current != nil {
		cfg.Logins = append(cfg.Logins, *current)
	}
	return cfg, nil
}

func marshalTeaConfig(cfg *teaConfig) string {
	var b strings.Builder
	if len(cfg.Logins) > 0 {
		b.WriteString("logins:\n")
		for _, l := range cfg.Logins {
			fmt.Fprintf(&b, "    - name: %s\n", l.Name)
			fmt.Fprintf(&b, "      url: %s\n", l.URL)
			if l.Token != "" {
				fmt.Fprintf(&b, "      token: %s\n", l.Token)
			}
			if l.User != "" {
				fmt.Fprintf(&b, "      user: %s\n", l.User)
			}
			if l.Default {
				b.WriteString("      default: true\n")
			}
		}
	}
	return b.String()
}

// readTeaToken returns the token configured in tea's config.yml for
// the given host. Used by gitea.go's API-fallback paths.
func readTeaToken(host string) (string, error) {
	root, err := configHome()
	if err != nil {
		return "", err
	}
	cfg, err := loadTeaConfig(filepath.Join(root, "tea", "config.yml"))
	if err != nil {
		return "", err
	}
	for _, l := range cfg.Logins {
		if l.Name == host || strings.HasSuffix(l.URL, "//"+host) {
			return l.Token, nil
		}
	}
	return "", fmt.Errorf("forges: no tea token for host %q", host)
}

// setupGitTea writes a git credential helper that pulls the token
// from tea's config. tea has no built-in setup-git, so we install a
// small helper script.
//
// The helper reads the token via the same readTeaToken function
// when invoked by git as a credential helper.
//
// We accomplish this by writing a global git credential helper that
// runs vibekit itself with a hidden subcommand. To keep this simple
// for now, we rely on git's "store" helper after writing a one-shot
// credential entry. This is good enough — tea-based hosts use tokens
// that don't expire (PATs).
func setupGitTea(host string) error {
	token, err := readTeaToken(host)
	if err != nil {
		return err
	}
	// Write to ~/.git-credentials (the "store" helper format).
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	credFile := filepath.Join(home, ".git-credentials")
	existing, _ := os.ReadFile(credFile) //nolint:errcheck // file may not exist yet; empty bytes treated as "no entries"
	lines := strings.Split(string(existing), "\n")
	prefix := fmt.Sprintf("https://oauth2:%s@%s/", token, host)
	hostMarker := "@" + host + "/"
	var out []string
	for _, l := range lines {
		if l == "" {
			continue
		}
		if strings.Contains(l, hostMarker) {
			continue // remove old entry for this host
		}
		out = append(out, l)
	}
	out = append(out, prefix)
	if err := os.WriteFile(credFile, []byte(strings.Join(out, "\n")+"\n"), 0o600); err != nil {
		return fmt.Errorf("write git-credentials: %w", err)
	}
	// Ensure git uses the store helper.
	gitCfg := filepath.Join(home, ".gitconfig")
	cfgData, _ := os.ReadFile(gitCfg) //nolint:errcheck // missing gitconfig is fine — git config will create it
	if !strings.Contains(string(cfgData), "credential.helper = store") &&
		!strings.Contains(string(cfgData), "helper = store") {
		_, err := runCmd(context.Background(), CmdTimeout, nil, "git",
			"config", "--global", "credential.helper", "store")
		if err != nil {
			return fmt.Errorf("git config credential.helper: %w", err)
		}
	}
	return nil
}

// writeYAML writes content atomically with 0600 perms.
func writeYAML(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
