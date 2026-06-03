// Package steering generates the environment.md steering file kiro-cli
// reads on every session. Regenerated once at startup and re-run by
// the hub when the MCP runtime registry changes so the list of
// connected integrations stays fresh.
package steering

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"vibekit/internal/api"
	"github.com/cplieger/atomicfile"
	"vibekit/internal/workspace"
)

// Read caps bound untrusted workspace input so a crafted repo can't
// OOM the container by committing a multi-GiB README or tools.json.
// The workspace hosts agent-cloned upstreams whose contents are
// attacker-controlled from our point of view; everywhere else in
// vibekit (bridge_fs, checkpoint blobs, forges, filehandler) clamps
// reads via io.LimitReader for the same reason.
const (
	firstLineReadCap = 4 << 10 // README first non-heading line fits easily in 4 KiB
	toolsManifestCap = 1 << 20 // any realistic tools.json stays well under 1 MiB

	inclusionAlways    = "always"
	inclusionFileMatch = "fileMatch"
	inclusionManual    = "manual"
)

// MCPSnapshot is the subset of the MCP runtime registry the steering
// generator uses. Returned by the snapshot function wired at construct
// time; steering has no direct dependency on hub internals.
type MCPSnapshot struct {
	Servers []api.MCPSnapshotServer
}

// ForgeSnapshot describes connected forge providers for the steering file.
type ForgeSnapshot struct {
	Providers []ForgeProvider
}

// ForgeProvider is one connected forge with its repos.
type ForgeProvider struct {
	Kind  string   // "github", "gitlab", etc.
	Host  string   // "github.com", "gitlab.company.com"
	User  string   // authenticated username
	Email string   // authenticated email (best-effort, may be "")
	Repos []string // top repo names (capped for brevity)
}

// Generator produces steering files for kiro-cli.
type Generator struct {
	mcpSnapshot   func() MCPSnapshot
	forgeSnapshot func() ForgeSnapshot
	workDir       string
	configDir     string
	mu            sync.Mutex
}

func New(workDir, configDir string) *Generator {
	return &Generator{workDir: workDir, configDir: configDir}
}

// SetMCPSnapshot wires a snapshot callback. Called once after
// construction. If unset, the generator omits the MCP section entirely.
// The callback runs OUTSIDE the generator's mutex (so it may safely
// take hub locks without re-entry risk); only the pointer assignment
// and read are lock-guarded. Generate enforces this by snapshotting
// g.mcpSnapshot under g.mu, then releasing the lock around the call.
func (g *Generator) SetMCPSnapshot(fn func() MCPSnapshot) {
	g.mu.Lock()
	g.mcpSnapshot = fn
	g.mu.Unlock()
}

// SetForgeSnapshot wires a callback that returns connected forge info.
// Called once after construction. If unset, the forge section is omitted.
func (g *Generator) SetForgeSnapshot(fn func() ForgeSnapshot) {
	g.mu.Lock()
	g.forgeSnapshot = fn
	g.mu.Unlock()
}

// Generate renders environment.md and writes it atomically. Holds
// g.mu across the full write so concurrent Generate calls serialise
// rather than racing. Skips the write when the rendered content is
// byte-identical to the existing file (avoids mtime bumps from
// frequent MCP-event triggered regenerations).
func (g *Generator) Generate(ctx context.Context) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Snapshot the callback pointer under g.mu, then invoke it
	// outside the read-only section. Matches the contract the
	// SetMCPSnapshot doc promises: the callback may safely take
	// hub locks without re-entry risk.
	snapshotFn := g.mcpSnapshot
	forgeFn := g.forgeSnapshot
	var mcp MCPSnapshot
	var forges ForgeSnapshot
	if snapshotFn != nil || forgeFn != nil {
		g.mu.Unlock()
		if snapshotFn != nil {
			mcp = snapshotFn()
		}
		if forgeFn != nil {
			forges = forgeFn()
		}
		g.mu.Lock()
	}

	var b strings.Builder
	b.WriteString("# Environment\n\n")
	b.WriteString("Kiro Web container. Workspace: `" + g.workDir + "`\n\n")

	manifest := filepath.Join(g.configDir, "tools.json")
	if data, err := readCappedFile(manifest, toolsManifestCap); err == nil {
		writeTools(&b, data)
	}
	if snapshotFn != nil {
		writeMCP(&b, mcp)
	}
	if forgeFn != nil {
		writeForges(&b, forges)
	}
	writeWorkspace(ctx, &b, g.workDir)

	b.WriteString("## Limitations\n\n")
	b.WriteString("- Shell commands run in the container, not on the host\n")
	b.WriteString("- No GUI or browser; web_fetch and web_search work\n")
	b.WriteString("- Container has no Docker socket\n\n")

	b.WriteString("## Capabilities\n\n")
	b.WriteString("- Chat history is stored in `/config/chats/*.json` (one file per session)\n")
	b.WriteString("- Conversations are searchable via the chat files\n")
	b.WriteString("- File browser and editor are available in the UI for reading and writing files\n")
	b.WriteString("- You can read, write, and edit files in the workspace directly\n")
	b.WriteString("- Shell commands execute in the workspace directory\n")
	b.WriteString("- Git operations are available via the Git panel\n")

	content := []byte(b.String())
	steeringFile := workspace.KiroSteeringPath("environment.md")

	// Skip if the rendered content is byte-identical. MCP event
	// storms fire Generate multiple times per second; rewriting
	// the same bytes bumps mtime and wastes disk I/O.
	if existing, readErr := os.ReadFile(steeringFile); readErr == nil && bytes.Equal(existing, content) {
		slog.Debug("steering: content unchanged, skipping write", "path", steeringFile)
		return
	}

	// Mode 0o600 matches the narrow-by-default stance of other
	// vibekit writes: this file lists the workspace layout and
	// MCP server names which, while not secrets, are information
	// that should stay scoped to the single user that runs
	// kiro-cli. atomicfile.SaveBytes is the atomic temp+rename helper so
	// a crash mid-write can't leave a truncated file. It also
	// derives the parent-dir mode from the file mode (0o700 when
	// the file has no group/world bits), so we don't MkdirAll
	// explicitly — that would widen the dir to 0o755.
	if wErr := atomicfile.SaveBytes(steeringFile, content, 0o600); wErr != nil {
		slog.Error("steering: write", "path", steeringFile, "error", wErr)
		return
	}
	slog.Info("steering: wrote", "path", steeringFile, "bytes", len(content))
}

// readCappedFile reads at most limit bytes from path. Used for untrusted
// workspace input (README, tools.json) so a crafted large file can't
// OOM the container. Returns the (possibly truncated) bytes and any
// error from open/read; callers treat errors the same way os.ReadFile
// did (log-and-omit, not a fatal).
func readCappedFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit))
}

func (g *Generator) CustomPath() string {
	return workspace.KiroSteeringPath("custom.md")
}

type toolEntry struct {
	Version  string   `json:"version"`
	Binaries []string `json:"binaries,omitempty"`
}

func writeTools(b *strings.Builder, data []byte) {
	var tools struct {
		Runtimes map[string]toolEntry `json:"runtimes"`
		Binary   map[string]toolEntry `json:"binary"`
		Go       map[string]toolEntry `json:"go"`
		NPM      map[string]toolEntry `json:"npm"`
		PIP      map[string]toolEntry `json:"pip"`
		Custom   map[string]toolEntry `json:"custom"`
		Cargo    map[string]toolEntry `json:"cargo"`
		Apt      map[string]toolEntry `json:"apt"`
	}
	if err := json.Unmarshal(data, &tools); err != nil {
		slog.Error("steering: parse tools.json", "error", err)
		return
	}
	type tool struct{ name, version string }
	var all []tool
	collect := func(entries map[string]toolEntry) {
		for name, e := range entries {
			if len(e.Binaries) > 0 {
				for _, bin := range e.Binaries {
					all = append(all, tool{bin, e.Version})
				}
			} else {
				all = append(all, tool{name, e.Version})
			}
		}
	}
	collect(tools.Runtimes)
	collect(tools.Binary)
	collect(tools.Go)
	collect(tools.NPM)
	collect(tools.PIP)
	collect(tools.Custom)
	collect(tools.Cargo)
	collect(tools.Apt)
	slices.SortFunc(all, func(a, b tool) int { return strings.Compare(a.name, b.name) })
	b.WriteString("## Installed tools\n\n")
	for _, t := range all {
		fmt.Fprintf(b, "- %s %s\n", t.name, t.version)
	}
	b.WriteString("\n")
}

// writeMCP emits the "Connected integrations" section listing every
// currently-connected MCP server.
func writeMCP(b *strings.Builder, snap MCPSnapshot) {
	if len(snap.Servers) == 0 {
		return
	}
	servers := slices.Clone(snap.Servers)
	slices.SortFunc(servers, func(a, b api.MCPSnapshotServer) int {
		return strings.Compare(a.Name, b.Name)
	})

	b.WriteString("## Connected integrations\n\n")
	b.WriteString("External systems the user has connected via MCP. ")
	b.WriteString("Tool names follow the `mcp_<server>_<tool>` convention.\n\n")
	for _, s := range servers {
		fmt.Fprintf(b, "- **%s**\n", s.Name)
	}
	b.WriteString("\n")
}

// writeForges renders the connected forge providers section with
// actionable context for the agent.
func writeForges(w io.Writer, snap ForgeSnapshot) {
	if len(snap.Providers) == 0 {
		return
	}
	fmt.Fprintf(w, "## Connected forges\n\n")
	fmt.Fprintf(w, "Git operations (clone, push, pull, fetch) are pre-authenticated for all configured forges via the CLI's credential helper. Just use plain `git` commands.\n\n")
	fmt.Fprintf(w, "For PRs, issues, releases, and CI status, prefer the official CLI tools — they expose the richest feature set per forge:\n\n")
	fmt.Fprintf(w, "- GitHub: `gh pr|issue|release|run` (e.g. `gh pr create --title ... --body ...`)\n")
	fmt.Fprintf(w, "- GitLab: `glab mr|issue|release|ci`\n")
	fmt.Fprintf(w, "- Gitea / Codeberg: `tea pulls|issues|releases`\n\n")
	fmt.Fprintf(w, "All CLIs are pre-authenticated; no `gh auth login` or token setup needed.\n\n")
	for _, p := range snap.Providers {
		user := p.User
		if user == "" {
			user = "(authenticated)"
		}
		fmt.Fprintf(w, "### %s (%s)\n\n", p.Kind, p.Host)
		if p.Email != "" {
			fmt.Fprintf(w, "- Authenticated as: %s <%s>\n", user, p.Email)
		} else {
			fmt.Fprintf(w, "- Authenticated as: %s\n", user)
		}
		cli := forgeCLI(p.Kind)
		if cli != "" {
			fmt.Fprintf(w, "- CLI: `%s`\n", cli)
		}
		fmt.Fprintf(w, "- Clone via: `git clone https://%s/<owner>/<repo>.git`\n", p.Host)
		if len(p.Repos) > 0 {
			fmt.Fprintf(w, "- Accessible repositories:\n")
			n := min(len(p.Repos), 20)
			for _, r := range p.Repos[:n] {
				fmt.Fprintf(w, "  - %s\n", r)
			}
			if len(p.Repos) > 20 {
				fmt.Fprintf(w, "  - … and %d more\n", len(p.Repos)-20)
			}
		}
		fmt.Fprintln(w)
	}
}

// forgeCLI returns the CLI tool for the kind. Mirrors forges.Kind.CLI()
// without requiring a dependency on the forges package.
func forgeCLI(kind string) string {
	switch kind {
	case "github":
		return "gh"
	case "gitlab":
		return "glab"
	case "gitea", "codeberg":
		return "tea"
	}
	return ""
}
