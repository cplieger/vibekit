// vibekit never parses or writes another program's config file; the one
// exception is glab's read-only discovery parser (see glab_config.go).

package forges

import (
	"fmt"
	"os"
	"path/filepath"
)

// configHome returns $XDG_CONFIG_HOME or $HOME/.config.
func configHome() (string, error) {
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
// CLI, or "" for a kind with no probe-able file.
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
// disk. Used to distinguish "nothing configured" from "configured but
// the CLI binary is missing" when discovery cannot run.
func cliConfigExists(kind Kind) bool {
	p := cliConfigPath(kind)
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}
