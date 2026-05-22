// Auto-install of forge CLI tools (gh / glab / tea) on first use.
//
// The container has a /config/tools.json manifest read by the
// /opt/kweb/setup-tools.sh script. We add an entry for the needed
// CLI, then run the setup script. After the script completes the
// binary is on PATH.
//
// All three CLIs distribute prebuilt binaries via GitHub Releases
// (gh, glab) or dl.gitea.com (tea). The manifest entries follow
// the standard `binary` shape with a github update method where
// possible.

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
	setupToolsPath = "/opt/kweb/setup-tools.sh"
	installTimeout = 5 * time.Minute
)

// EnsureCLI checks if the CLI for the given kind is on PATH. If not,
// it adds the CLI to /config/tools.json and runs setup-tools.sh.
//
// Returns nil if the CLI is already installed (or installation
// succeeded). The function is safe to call multiple times — the
// manifest update is idempotent and setup-tools.sh skips already-
// installed tools.
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

	if err := addToolsManifestEntry(manifestPath, cli); err != nil {
		return fmt.Errorf("update tools.json: %w", err)
	}
	if _, err := os.Stat(scriptPath); err != nil {
		// No setup script present (test/dev environment). The
		// manifest update is still useful — a container restart
		// will install it. Surface as a non-fatal warning.
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat setup script: %w", err)
	}
	cctx, cancel := context.WithTimeout(ctx, installTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "bash", scriptPath)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("run setup-tools.sh: %w (output: %s)", err, string(out))
	}
	// Verify the install worked.
	if _, lookErr := exec.LookPath(cli); lookErr != nil {
		return fmt.Errorf("forges: %s still not on PATH after install", cli)
	}
	return nil
}

// addToolsManifestEntry inserts a binary entry for cli into tools.json
// under the binary section. No-op if cli is already present.
func addToolsManifestEntry(path, cli string) error {
	data, err := os.ReadFile(path)
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
	if _, ok := binary[cli]; ok {
		return nil // already present
	}
	entry, err := defaultManifestEntry(cli)
	if err != nil {
		return err
	}
	binary[cli] = entry
	return atomicWriteJSON(path, manifest)
}

// defaultManifestEntry returns the install entry for the given CLI.
// Uses the standard manifest shape: {version, update, install}.
func defaultManifestEntry(cli string) (map[string]any, error) {
	switch cli {
	case "gh":
		return map[string]any{
			fieldVersion: versionLatest,
			fieldUpdate: map[string]any{
				fieldMethod: string(KindGitHub),
				fieldRepo:   "cli/cli",
			},
			actionInstall: "curl -fsSL https://github.com/cli/cli/releases/download/${VERSION}/gh_${VERSION#v}_linux_amd64.tar.gz | " +
				"tar -xz -C ${TOOLS} --strip-components=2 gh_${VERSION#v}_linux_amd64/bin/gh",
		}, nil
	case "glab":
		return map[string]any{
			fieldVersion: versionLatest,
			fieldUpdate: map[string]any{
				fieldMethod: string(KindGitHub),
				fieldRepo:   "gitlab-org/cli",
			},
			actionInstall: "curl -fsSL https://github.com/gitlab-org/cli/releases/download/${VERSION}/glab_${VERSION#v}_Linux_x86_64.tar.gz | " +
				"tar -xz -C ${TOOLS} --strip-components=1 bin/glab",
		}, nil
	case "tea":
		return map[string]any{
			fieldVersion: versionLatest,
			fieldUpdate: map[string]any{
				fieldMethod: string(KindGitHub),
				fieldRepo:   "https://gitea.com/gitea/tea",
			},
			actionInstall: "curl -fsSL -o ${BIN}/tea https://dl.gitea.com/tea/${VERSION#v}/tea-${VERSION#v}-linux-amd64 && chmod +x ${BIN}/tea",
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
