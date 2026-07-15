// Package tools is vibekit's native tool-install engine. It replaces the
// former setup-tools.sh + tools.json v1 stack with a single Go owner of
// the manifest, a job queue with SSE-streamed output, and a catalog
// compiled from the mise registry (name index) and the aqua registry
// (binary install knowledge). No external package-manager binary is
// shipped; the engine downloads/extracts binaries itself and shells out
// to the ecosystem package managers (npm, uv, cargo, go) it installs.
//
// Data files (all under the persistent config volume):
//
//	<configDir>/tools.json  — the v2 manifest: what the user wants installed.
//	                          Written ONLY by this package, under one lock.
//	<configDir>/tools-state.json — engine-owned machine state (what IS
//	                          installed, which bin symlinks each tool owns,
//	                          last install error). Never user-edited.
//	<toolsDir>/opt/<name>/<version>/ — versioned install trees.
//	<toolsDir>/bin — the single PATH dir: symlinks + shims + pm bins.
//
// The catalog (/opt/vibekit/tool-catalog.json, baked at image build by
// cmd/toolcatalog) is read-only environment data; a missing catalog
// degrades to manual installs only.
package tools

import "time"

// Source prefixes for Tool.Source. A source is "<kind>:<ref>" except
// SourceManual which stands alone.
const (
	SourceAqua   = "aqua"   // aqua:owner/repo — evaluated aqua-registry definition
	SourceNpm    = "npm"    // npm:package
	SourcePip    = "pip"    // pip:package (installed via uv)
	SourceCargo  = "cargo"  // cargo:crate
	SourceGo     = "go"     // go:module/path
	SourceManual = "manual" // user-provided install command
)

// ManifestVersion is the schema version this engine reads and writes.
// Files without this version (the retired v1 shape) are backed up and
// replaced with a fresh manifest at engine start.
const ManifestVersion = 2

// Tool is one manifest entry: the user's intent for a single tool.
type Tool struct {
	// Source locates the install definition: "aqua:cli/cli",
	// "npm:pyright", "pip:x", "cargo:x", "go:golang.org/x/tools/gopls",
	// or "manual".
	Source string `json:"source"`
	// Version is the concrete upstream version, exactly as upstream
	// tags it (may or may not carry a leading v). Never a range.
	Version string `json:"version"`
	// Pin freezes the version: update runs skip this tool.
	Pin bool `json:"pin,omitempty"`
	// Requires lists other manifest/catalog tool names that must be
	// installed before (or alongside) this one, e.g. jdtls -> java.
	// Backend-level needs (npm->node, pip->uv, cargo->rust, go->go)
	// are implied and need not be listed.
	Requires []string `json:"requires,omitempty"`
	// Shims maps extra bin names to command lines, written as wrapper
	// scripts in the bin dir (e.g. typescript-language-server ->
	// "tsc --lsp --stdio").
	Shims map[string]string `json:"shims,omitempty"`
	// Description is display text (catalog-provided or user-written).
	Description string `json:"description,omitempty"`
	// Origin records provenance for linked entries, e.g. "mcp:<name>"
	// for a tool created from the MCP add flow.
	Origin string `json:"origin,omitempty"`
	// Install is the shell command for Source == "manual". It runs via
	// bash with VERSION, BIN, TOOLS, OPT and ARCH_* in the environment.
	Install string `json:"install,omitempty"`
	// Uninstall optionally overrides cleanup for Source == "manual".
	Uninstall string `json:"uninstall,omitempty"`
	// Probe is the bin name whose presence marks the tool installed
	// (manual installs only; other sources derive it). Defaults to the
	// tool name.
	Probe string `json:"probe,omitempty"`
}

// Manifest is the tools.json v2 document.
type Manifest struct {
	Version int             `json:"version"`
	Tools   map[string]Tool `json:"tools"`
}

// ToolStatus is the engine-owned per-tool machine state.
type ToolStatus struct {
	// InstalledVersion is the version last installed successfully.
	InstalledVersion string `json:"installed_version,omitempty"`
	// Bins are the names this tool owns in the bin dir (symlinks and
	// shim wrappers), removed on uninstall.
	Bins []string `json:"bins,omitempty"`
	// PMBins are package-manager bin names discovered by diffing the
	// pm's bin dir (npm/pip), symlinked into the bin dir.
	PMBins []string `json:"pm_bins,omitempty"`
	// LastError is the failure message of the most recent install
	// attempt; cleared on success.
	LastError string `json:"last_error,omitempty"`
	// UpdatedAt is when this status last changed.
	UpdatedAt time.Time `json:"updated_at"`
}

// State is the tools-state.json document.
type State struct {
	Tools map[string]ToolStatus `json:"tools"`
}

// Job states.
const (
	JobQueued    = "queued"
	JobRunning   = "running"
	JobDone      = "done"
	JobFailed    = "failed"
	JobCancelled = "cancelled"
)

// Job kinds.
const (
	JobKindInstall   = "install"
	JobKindUninstall = "uninstall"
	JobKindUpdate    = "update"
	JobKindSync      = "sync" // boot: install missing + update unpinned
)

// The wire shapes for jobs, tool rows, and list/search responses live
// in internal/api (api.ToolJob, api.ToolInfo, api.ToolsList, ...) so
// wiregen exports them to the client alongside the SSE payloads.

// CatalogEntry is one tool in the compiled catalog: the mise-registry
// name/description joined with the preferred install source and, for
// aqua sources, the embedded aqua package definition. Overlay entries
// (vibekit-curated) may add requires/shims/manual install commands.
type CatalogEntry struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Source      string   `json:"source"`
	Aliases     []string `json:"aliases,omitempty"`
	Featured    bool     `json:"featured,omitempty"`
	// Version is the default pinned version for entries without an
	// upstream version source (manual installs).
	Version   string            `json:"version,omitempty"`
	Requires  []string          `json:"requires,omitempty"`
	Shims     map[string]string `json:"shims,omitempty"`
	Install   string            `json:"install,omitempty"`   // manual-source entries
	Uninstall string            `json:"uninstall,omitempty"` // manual-source entries
	Probe     string            `json:"probe,omitempty"`     // manual-source entries
	Aqua      *AquaPackage      `json:"aqua,omitempty"`
}

// Catalog is the compiled tool-catalog.json document.
type Catalog struct {
	// Refs records the upstream registry refs this catalog was
	// compiled from (informational).
	Refs    map[string]string       `json:"refs,omitempty"`
	Entries map[string]CatalogEntry `json:"entries"`
}
