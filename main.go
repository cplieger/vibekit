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
	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/auth"
	"github.com/cplieger/vibekit/internal/bridge"
	"github.com/cplieger/vibekit/internal/chat"
	"github.com/cplieger/vibekit/internal/composition"
	"github.com/cplieger/vibekit/internal/filebrowse"
	"github.com/cplieger/vibekit/internal/forges"
	"github.com/cplieger/vibekit/internal/git"
	"github.com/cplieger/vibekit/internal/hub"
	"github.com/cplieger/vibekit/internal/mcp"
	"github.com/cplieger/vibekit/internal/push"
	"github.com/cplieger/vibekit/internal/server"
	"github.com/cplieger/vibekit/internal/steering"
	"github.com/cplieger/vibekit/internal/workspace"
)

// Compile-time interface satisfaction checks.
var (
	_ api.ChatStore            = (*chat.Store)(nil)
	_ api.ACPBridge            = (*bridge.Bridge)(nil)
	_ api.Broadcaster          = (*hub.Hub)(nil)
	_ api.Hub                  = (*hub.Hub)(nil)
	_ server.SteeringGenerator = (*steering.Generator)(nil)
	_ api.RouteHandler         = (*git.Handler)(nil)
	_ api.RouteHandler         = (*filebrowse.Handler)(nil)
	_ api.RouteHandler         = (*auth.Handler)(nil)
	_ api.PushService          = (*push.Service)(nil)
	_ api.MCPConfig            = (*mcp.Store)(nil)
	_ api.RouteHandler         = (*mcp.Store)(nil)
	_ api.RouteHandler         = (*mcp.RegistryProxy)(nil)
)

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

// Keep interface satisfaction checks reachable. These imports are used
// only by the compile-time var block above.
var (
	_ = forges.NewManager
	_ = server.New
)
