// Auto-install of forge CLI tools (gh / glab / tea) on first use.
//
// Adding a forge account requires its CLI on PATH before any command
// runs. The tools engine owns installation: main.go wires EnsureTool to
// tools.Engine.EnsureInstalled, which adds the CLI from the catalog
// (the compiled mise/aqua registry data ships gh, glab and tea) and
// blocks until its install job finishes. The frontend surfaces this as
// a spinner inside the OAuth/PAT modal.

package forges

import (
	"context"
	"fmt"
	"os/exec"
)

// EnsureTool is the tools-engine hook that installs a tool by name and
// waits for it. Wired from main.go; nil in tests/dev, where a missing
// CLI simply surfaces as an error.
var EnsureTool func(ctx context.Context, name string) error

// EnsureCLI checks if the CLI for the given kind is on PATH, and if
// not, installs it through the tools engine. Safe to call repeatedly.
func EnsureCLI(ctx context.Context, kind Kind) error {
	cli := kind.CLI()
	if cli == "" {
		return fmt.Errorf("forges: no CLI for kind %q", kind)
	}
	if _, err := exec.LookPath(cli); err == nil {
		return nil // already on PATH
	}
	if EnsureTool == nil {
		return fmt.Errorf("forges: %s is not installed (tools engine unavailable)", cli)
	}
	if err := EnsureTool(ctx, cli); err != nil {
		return fmt.Errorf("forges: install %s: %w", cli, err)
	}
	if _, err := exec.LookPath(cli); err != nil {
		return fmt.Errorf("forges: %s still not on PATH after install", cli)
	}
	return nil
}
