// Tools feature-gating probe. The tools REST surface itself is the
// cplieger/toolbelt httpapi projection, mounted in server.go; this
// file keeps the one vibekit-specific endpoint on that prefix.

package server

import (
	"net/http"
	"os/exec"

	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/webhttp"
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
func handleToolStatus(w http.ResponseWriter, r *http.Request) {
	// Gated here rather than on the ServeMux pattern: a `GET `-prefixed pattern
	// stops being an exact match for a non-GET, which hands PATCH/DELETE
	// /api/tools/status to the /api/tools/ subtree mount — toolbelt's
	// /api/tools/{name} handlers, with name="status". See ListenAndServe.
	if !httpreply.RequireMethod(w, r, http.MethodGet) {
		return
	}
	out := make(map[string]bool, len(statusBinaries))
	for _, b := range statusBinaries {
		_, err := exec.LookPath(b)
		out[b] = err == nil
	}
	webhttp.WriteJSON(w, out)
}
