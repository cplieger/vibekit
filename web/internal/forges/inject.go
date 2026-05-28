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
//
// Per-CLI logic lives in inject_gh.go, inject_glab.go, inject_tea.go.

package forges

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
func RemoveToken(_ context.Context, kind Kind, host string) error {
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
