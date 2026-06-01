// Vibekit for Kiro: ACP-based web interface for kiro-cli.
//
// One kiro-cli subprocess per active chat; multiple browsers on the same
// chat share the same bridge and context. Server is the source of truth;
// the browser projects server state via SSE + GET /api/chats.
package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"

	"vibekit/internal/api"
	"vibekit/internal/auth"
	"vibekit/internal/bridge"
	"vibekit/internal/chat"
	"vibekit/internal/composition"
	"vibekit/internal/filehandler"
	forgesPkg "vibekit/internal/forges"
	"vibekit/internal/git"
	"vibekit/internal/hub"
	mcpPkg "vibekit/internal/mcp"
	"vibekit/internal/permissions"
	pushPkg "vibekit/internal/push"
	"vibekit/internal/server"
	"vibekit/internal/steering"
	"vibekit/internal/workspace"
)

// Compile-time interface satisfaction checks.
var (
	_ api.ChatStore         = (*chat.Store)(nil)
	_ api.ACPBridge         = (*bridge.Bridge)(nil)
	_ api.Broadcaster       = (*hub.Hub)(nil)
	_ api.Hub               = (*hub.Hub)(nil)
	_ api.SteeringGenerator = (*steering.Generator)(nil)
	_ api.GitHandler        = (*git.Handler)(nil)
	_ api.FileHandler       = (*filehandler.Handler)(nil)
	_ api.AuthHandler       = (*auth.Handler)(nil)
	_ api.PushService       = (*pushPkg.Service)(nil)
	_ api.MCPConfig         = (*mcpPkg.Store)(nil)
	_ api.RouteHandler      = (*mcpPkg.Store)(nil)
	_ api.RouteHandler      = (*mcpPkg.RegistryProxy)(nil)
)

func main() {
	os.Exit(runMain())
}

// runMain performs the actual startup sequence. Isolated from main()
// so that `defer app.Shutdown()` fires on normal exit paths (os.Exit
// in main itself would skip the defer).
func runMain() int {
	cfg := composition.ConfigFromEnv()

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
	_ = forgesPkg.NewManager
	_ = permissions.Args
	_ = server.New
)
