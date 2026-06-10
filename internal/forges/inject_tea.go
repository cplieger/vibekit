package forges

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/cplieger/atomicfile"
)

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
		case fieldURL:
			current.URL = val
		case "token":
			current.Token = val
		case fieldUser:
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
func setupGitTea(ctx context.Context, host string) error {
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
	existing, _ := os.ReadFile(credFile)
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
	if err := atomicfile.SaveBytes(credFile, []byte(strings.Join(out, "\n")+"\n"), 0o600); err != nil {
		return fmt.Errorf("write git-credentials: %w", err)
	}
	// Ensure git uses the store helper.
	gitCfg := filepath.Join(home, ".gitconfig")
	cfgData, _ := os.ReadFile(gitCfg)
	if !strings.Contains(string(cfgData), "credential.helper = store") &&
		!strings.Contains(string(cfgData), "helper = store") {
		_, err := runCmd(ctx, CmdTimeout, nil, "git",
			"config", "--global", "credential.helper", "store")
		if err != nil {
			return fmt.Errorf("git config credential.helper: %w", err)
		}
	}
	return nil
}
