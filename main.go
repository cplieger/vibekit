// Vibekit for Kiro: ACP-based web interface for kiro-cli.
//
// One kiro-cli subprocess per active chat; multiple browsers on the same
// chat share the same bridge and context. Server is the source of truth;
// the browser projects server state via SSE + GET /api/chats.
package main

import (
	"context"
	_ "embed"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/cplieger/toolbelt/v2"
	"github.com/cplieger/vibekit/internal/composition"
	"github.com/cplieger/vibekit/internal/workspace"
)

// There are no compile-time interface assertions here any more, and no `var _ =`
// pair standing in for them either. Every assertion this file carried named a
// type the composition root already passes to the option that consumes it —
// server.WithGit, WithFiles, WithAuth, WithMCPConfig, WithMCPRegistry,
// WithSteering — so the compiler checked each satisfaction at the call site
// whether or not it was also written down. An assertion is worth keeping only
// where nothing in the build already forces the check, and after that refactor
// there is no such place left in main.
//
// What survived it was `var (_ = forges.NewManager; _ = server.New)`, under a
// comment pointing at "the compile-time var block above" that no longer existed.
// It asserted nothing — naming a function proves only that the identifier
// exists, which the compiler knows — and it was the sole reason main imported
// either package.

// requiredToolsList is the same required-tools.txt the image build
// verifies the baked catalog against, embedded so the RUNTIME catalog
// refresh applies the identical gate to every fetched catalog: one
// source of truth, two enforcement points. Parsed by
// toolbelt.ParseRequireList (the same format cmd/toolcatalog verify
// reads).
//
//go:embed required-tools.txt
var requiredToolsList string

func main() {
	os.Exit(runMain())
}

// runMain performs the actual startup sequence. Isolated from main()
// so that `defer app.Shutdown()` fires on normal exit paths (os.Exit
// in main itself would skip the defer).
func runMain() int {
	cfg := composition.ConfigFromEnv()
	cfg.ToolCatalogRequire = toolbelt.ParseRequireList(requiredToolsList)

	// Wire the kiro home resolver into the workspace package so it doesn't
	// need to read os.Getenv directly (library-composition principle).
	workspace.SetKiroHomeResolver(func() string {
		if h := os.Getenv("KIRO_HOME"); h != "" {
			return h
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return ".kiro"
		}
		return filepath.Join(home, ".kiro")
	})

	app, err := composition.Build(context.Background(), &cfg, staticFS)
	if err != nil {
		slog.Error("build", "error", err)
		return 1
	}
	defer app.Shutdown()
	if err := app.Run(); err != nil {
		return 1
	}
	return 0
}
