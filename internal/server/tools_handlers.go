// Tools feature-gating probe. The tools REST surface itself is the
// cplieger/toolbelt httpapi projection, mounted in server.go; this
// file keeps the one vibekit-specific endpoint on that prefix.

package server

import (
	"net/http"
	"os/exec"

	"github.com/cplieger/vibekit/internal/api"
)

// statusBinaries is the set of binaries /api/tools/status probes. Each
// key is a name kiro-cli or a vibekit feature panel expects on PATH
// (the MCP add modal gates on node/npx/uv, Sources on the forge CLIs).
var statusBinaries = []string{
	"node", "npm", "npx",
	"go", "gofmt",
	"java",
	"cargo", "rustc",
	"uv", "uvx",
	"gh", "glab", "tea",
	"typescript-language-server", "tsc",
	"pyright", "pyrefly",
	"gopls", "rust-analyzer", "clangd",
	"jdtls", "kotlin-language-server",
}

// handleToolStatus: GET /api/tools/status
//
// Bare PATH presence probes for the well-known binaries feature panels
// gate on (e.g. the MCP modal's "Setting up Node..." spinner).
func (s *Server) handleToolStatus(w http.ResponseWriter, _ *http.Request) {
	out := make(map[string]bool, len(statusBinaries))
	for _, b := range statusBinaries {
		_, err := exec.LookPath(b)
		out[b] = err == nil
	}
	api.WriteJSON(w, out)
}
