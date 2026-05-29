// Auto-install of forge CLI tools (gh / glab / tea) on first use.
//
// The new tools.json default ships gh, glab and tea as pre-populated
// "binary" entries with "enabled": false. Adding a forge account
// flips enabled=true on the matching entry and triggers
// /opt/vibekit/setup-tools.sh, which downloads + installs the CLI
// using the install command stored in the manifest. The frontend
// surfaces this as a spinner inside the OAuth/PAT modal so the user
// sees the install progress; this Go helper is the backend hook
// invoked from forges.LoginWithPAT / LoginWithOAuth before any CLI
// command runs.
//
// If the entry is missing from the user's tools.json (e.g. they had
// an older default), we fall back to inserting the entry with a
// minimal default so things still work — no separate migration step.

package forges

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// installPaths holds where setup-tools.sh and tools.json live.
// Tests can override.
var (
	installMu      sync.RWMutex
	toolsJSONPath  = "/config/tools.json"
	setupToolsPath = "/opt/vibekit/setup-tools.sh"
	installTimeout = 5 * time.Minute
)

// EnsureCLI checks if the CLI for the given kind is on PATH. If not,
// it flips enabled=true on the binary.<cli> entry in tools.json and
// runs setup-tools.sh. After the script completes the binary should
// be on PATH.
//
// Returns nil if the CLI is already installed (or installation
// succeeded). The function is safe to call multiple times — flipping
// enabled is idempotent and setup-tools.sh skips already-installed
// tools.
func EnsureCLI(ctx context.Context, kind Kind) error {
	cli := kind.CLI()
	if cli == "" {
		return fmt.Errorf("forges: no CLI for kind %q", kind)
	}
	if _, err := exec.LookPath(cli); err == nil {
		return nil // already on PATH
	}
	installMu.RLock()
	manifestPath := toolsJSONPath
	scriptPath := setupToolsPath
	installMu.RUnlock()

	if err := enableToolEntry(manifestPath, cli); err != nil {
		return fmt.Errorf("update tools.json: %w", err)
	}
	if _, err := os.Stat(scriptPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// No setup script present (test/dev environment). The
			// manifest update is still useful — a container restart
			// will install it. Surface as a non-fatal warning.
			return nil
		}
		return fmt.Errorf("stat setup script: %w", err)
	}
	cctx, cancel := context.WithTimeout(ctx, installTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "bash", scriptPath) //nolint:gosec // hardcoded path
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("run setup-tools.sh: %w (output: %s)", err, string(out))
	}
	if _, lookErr := exec.LookPath(cli); lookErr != nil {
		return fmt.Errorf("forges: %s still not on PATH after install", cli)
	}
	return nil
}

// enableToolEntry flips binary.<cli>.enabled to true. If the entry
// doesn't exist (older user-customized tools.json), inserts a default
// entry with the same install command shipped in the new default.
func enableToolEntry(path, cli string) error {
	data, err := os.ReadFile(path) //nolint:gosec // configDir is the trusted runtime directory
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var manifest map[string]any
	if len(data) > 0 {
		if err = json.Unmarshal(data, &manifest); err != nil {
			return fmt.Errorf("parse: %w", err)
		}
	}
	if manifest == nil {
		manifest = make(map[string]any)
	}
	binary, _ := manifest["binary"].(map[string]any)
	if binary == nil {
		binary = make(map[string]any)
		manifest["binary"] = binary
	}
	entry, ok := binary[cli].(map[string]any)
	if !ok {
		// Fallback for users with older tools.json that doesn't
		// pre-populate forge CLIs. Insert a fresh entry matching
		// the new default.
		def, derr := defaultManifestEntry(cli)
		if derr != nil {
			return derr
		}
		binary[cli] = def
		entry = def
	}
	entry["enabled"] = true
	return atomicWriteJSON(path, manifest)
}

// defaultManifestEntry returns the install entry for the given CLI.
// Mirrors the shape used in the bundled tools.json default; only
// reached when the user's tools.json is missing the pre-populated
// forge entries (older default or hand-edited).
func defaultManifestEntry(cli string) (map[string]any, error) {
	switch cli {
	case "gh":
		return map[string]any{
			fieldEnabled: true,
			fieldVersion: "v2.93.0",
			fieldUpdate: map[string]any{
				fieldMethod: string(KindGitHub),
				fieldRepo:   "cli/cli",
			},
			actionInstall: "curl -fsSL https://github.com/cli/cli/releases/download/${VERSION}/gh_${VERSION_NOPFX}_linux_${ARCH_AMD64_OR_ARM64}.tar.gz | " +
				"tar -xz -C ${TOOLS} --strip-components=2 gh_${VERSION_NOPFX}_linux_${ARCH_AMD64_OR_ARM64}/bin/gh",
		}, nil
	case "glab":
		return map[string]any{
			fieldEnabled: true,
			fieldVersion: "v1.69.0",
			fieldUpdate: map[string]any{
				fieldMethod: string(KindGitHub),
				fieldRepo:   "gitlab-org/cli",
			},
			actionInstall: "curl -fsSL https://gitlab.com/gitlab-org/cli/-/releases/${VERSION}/downloads/glab_${VERSION_NOPFX}_linux_${ARCH_AMD64_OR_ARM64}.tar.gz | " +
				"tar -xz -C ${TOOLS} --strip-components=1 bin/glab",
		}, nil
	case cliTea:
		return map[string]any{
			fieldEnabled: true,
			fieldVersion: "v0.11.0",
			fieldUpdate: map[string]any{
				fieldMethod: "url",
				"url":       "https://dl.gitea.com/tea/version.json",
			},
			actionInstall: "curl -fsSL -o ${BIN}/tea https://dl.gitea.com/tea/${VERSION_NOPFX}/tea-${VERSION_NOPFX}-linux-${ARCH_AMD64_OR_ARM64} && chmod +x ${BIN}/tea",
		}, nil
	}
	return nil, fmt.Errorf("forges: no install template for %q", cli)
}

// atomicWriteJSON writes the manifest with pretty-printing, atomically.
func atomicWriteJSON(path string, manifest map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil { //nolint:gosec // G306: user config file, not secrets
		return err
	}
	return os.Rename(tmp, path)
}
