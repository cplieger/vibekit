// Config-root resolution for the forge CLIs' own stores.
//
// vibekit never parses or writes another program's config file (the one
// documented exception is glab's read-only discovery parser — see
// glab_config.go). The paths below are used for exactly two things:
// pointing the read-only glab parser at its file, and the stat-only
// "a configuration exists but the CLI binary is missing" probe that
// backs the cli_missing forge rows (see discover.go).

package forges

import (
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

// cliConfigPath returns the well-known config file path for a kind's
// CLI, or "" for a kind with no probe-able file. Path knowledge only —
// the file contents are never read here.
func cliConfigPath(kind Kind) string {
	root, err := configHome()
	if err != nil {
		return ""
	}
	switch kind {
	case KindGitHub:
		return filepath.Join(root, "gh", "hosts.yml")
	case KindGitLab:
		return filepath.Join(root, "glab-cli", "config.yml")
	case KindGitea, KindCodeberg:
		return filepath.Join(root, "tea", "config.yml")
	default:
		return ""
	}
}

// cliConfigExists reports whether the kind's CLI has a config file on
// disk (stat only). Used to distinguish "nothing configured" from
// "configured but the CLI binary is missing" when discovery cannot run.
func cliConfigExists(kind Kind) bool {
	p := cliConfigPath(kind)
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}
