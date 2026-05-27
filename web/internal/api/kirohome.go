// Path helpers for kiro-cli's per-user state directory.
package api

import (
	"os"
	"path/filepath"
)

// KiroHome returns the directory kiro-cli uses for per-user state
// (auth.db, sessions/cli, settings, agents, steering). Resolution
// order:
//
//  1. $KIRO_HOME if set (kiro-cli 2.3+ canonical override)
//  2. $HOME/.kiro otherwise
//
// Vibekit's entrypoint sets KIRO_HOME=/config/kiro so this resolves
// to a sibling of /config/chats and /config/mcp.json rather than
// being buried inside the user's HOME. Code that reads or writes
// kiro-cli state must go through this helper so vibekit and kiro-cli
// agree on where state lives.
func KiroHome() string {
	if h := os.Getenv("KIRO_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// Fall back to a relative .kiro — degraded but at least
		// consistent with kiro-cli's own fallback if HOME is unset.
		return ".kiro"
	}
	return filepath.Join(home, ".kiro")
}

// KiroSteeringPath returns the path to a file under KiroHome()/steering/.
func KiroSteeringPath(name string) string {
	return filepath.Join(KiroHome(), "steering", name)
}

// KiroSettingsPath returns the path to a file under KiroHome()/settings/.
func KiroSettingsPath(name string) string {
	return filepath.Join(KiroHome(), "settings", name)
}

// KiroSessionsCLIDir returns the directory kiro-cli uses for v2 ACP
// session state files (~/.kiro/sessions/cli or $KIRO_HOME/sessions/cli).
func KiroSessionsCLIDir() string {
	return filepath.Join(KiroHome(), "sessions", "cli")
}
