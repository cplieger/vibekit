// Package steering generates the environment.md steering file kiro-cli
// reads on every session. Regenerated once at startup and re-run by
// the hub when the MCP runtime registry changes so the list of
// connected integrations stays fresh.
package steering

import (
	"bytes"
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
func (g *Generator) Generate() {
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
	writeWorkspace(&b, g.workDir)

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

	home, err := os.UserHomeDir()
	if err != nil {
		slog.Error("steering: home dir", "error", err)
		return
	}
	content := []byte(b.String())
	steeringFile := filepath.Join(home, ".kiro", "steering", "environment.md")

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
	// kiro-cli. api.SaveBytes is the atomic temp+rename helper so
	// a crash mid-write can't leave a truncated file. It also
	// derives the parent-dir mode from the file mode (0o700 when
	// the file has no group/world bits), so we don't MkdirAll
	// explicitly — that would widen the dir to 0o755.
	if wErr := api.SaveBytes(steeringFile, content, 0o600); wErr != nil {
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
	home, err := os.UserHomeDir()
	if err != nil {
		// HOME is a container invariant; this only fires in
		// stripped local-dev shells. Leave a breadcrumb so the
		// "why did my custom instructions vanish?" debugging
		// scenario has a log line.
		slog.Warn("steering: no HOME, using /tmp fallback", "error", err)
		return "/tmp/custom-steering.md"
	}
	return filepath.Join(home, ".kiro", "steering", "custom.md")
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

func writeWorkspace(b *strings.Builder, workDir string) {
	entries, err := os.ReadDir(workDir)
	if err != nil || len(entries) == 0 {
		b.WriteString("## Workspace\n\nEmpty.\n\n")
		return
	}
	repos, dirs := classifyEntries(entries, workDir)
	foundFiles := findNotableFiles(workDir)
	isRoot := api.IsGitRepo(workDir)
	b.WriteString("## Workspace\n\n")
	if isRoot {
		b.WriteString("The workspace root (`/workspace`) is itself a git repository.\n\n")
	}
	if len(repos) > 0 {
		b.WriteString("### Git repositories\n\n")
		b.WriteString("Multiple repos coexist under `/workspace`. ")
		b.WriteString("Use `cwd` parameter in shell commands to target a specific repo ")
		b.WriteString("(e.g. `cwd: \"myrepo\"` runs in `/workspace/myrepo/`). ")
		b.WriteString("File paths like `myrepo/src/main.go` work with readFile/readCode.\n\n")
		for _, r := range repos {
			desc := readFirstLine(filepath.Join(workDir, r, "README.md"))
			if desc != "" {
				fmt.Fprintf(b, "- `%s/` — %s\n", r, desc)
			} else {
				fmt.Fprintf(b, "- `%s/`\n", r)
			}
		}
		b.WriteString("\n")
		b.WriteString("The Git panel in the UI has a repo selector dropdown. ")
		b.WriteString("If the user asks about a different repo than the one selected, ")
		b.WriteString("remind them to switch repos in the Git panel.\n\n")
	}
	if len(foundFiles) > 0 {
		b.WriteString("### Notable files\n\n")
		for _, f := range foundFiles {
			fmt.Fprintf(b, "- `%s`\n", f)
		}
		b.WriteString("\n")
	}
	if len(dirs) > 0 && !isRoot {
		b.WriteString("### Directories\n\n")
		for _, d := range dirs {
			fmt.Fprintf(b, "- `%s/`\n", d)
		}
		b.WriteString("\n")
	}
}

func classifyEntries(entries []os.DirEntry, workDir string) (repos, dirs []string) {
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || !e.IsDir() {
			continue
		}
		if api.IsGitRepo(filepath.Join(workDir, name)) {
			repos = append(repos, name)
		} else {
			dirs = append(dirs, name)
		}
	}
	return repos, dirs
}

func findNotableFiles(workDir string) []string {
	notable := []string{
		"README.md", "readme.md", "package.json", "go.mod",
		"Cargo.toml", "pyproject.toml", "requirements.txt",
		"Makefile", "docker-compose.yml", "docker-compose.yaml",
		"compose.yaml", "compose.yml", "Dockerfile",
		".env", "tsconfig.json", "pom.xml", "build.gradle",
	}
	var found []string
	for _, f := range notable {
		if _, err := os.Stat(filepath.Join(workDir, f)); err == nil {
			found = append(found, f)
		}
	}
	return found
}

// readFirstLine returns the first non-blank non-heading line of the
// README at path, capped and sanitised so that hostile repo content
// can't inject agent instructions into the steering file.
//
// The sanitisation chain is required because the readFirstLine output
// is written verbatim into ~/.kiro/steering/environment.md which
// kiro-cli treats as authoritative agent context. An attacker with
// write access to a workspace repo (including agent-cloned upstreams
// with crafted READMEs) could otherwise inject markdown link syntax,
// HTML tags, control characters, or newline escapes that influence
// agent behaviour.
//
// Sanitisation steps (in order):
//  1. Cap the read at firstLineReadCap so a multi-GiB README can't
//     OOM the container.
//  2. Drop CR/LF/tab in the candidate line before truncation so a
//     newline-smuggled second "line" can't appear in the output.
//  3. Strip hidden Unicode codepoints (TAG chars, zero-width joiners,
//     bidi controls) via api.SanitizeUnicode.
//  4. Drop lines containing markdown link syntax (inline, reference,
//     or image), HTML tags, backticks, or bare URLs — each of which
//     the agent renders or follows.
func readFirstLine(path string) string {
	data, err := readCappedFile(path, firstLineReadCap)
	if err != nil {
		return ""
	}
	for _, line := range strings.SplitN(string(data), "\n", 10) {
		line = strings.TrimSpace(line)
		if line == "" || isMarkdownHeading(line) {
			continue
		}
		// Replace CR/LF/tab with space before truncation; the
		// 100-char cap then can't straddle an injected newline.
		line = strings.Map(func(r rune) rune {
			switch r {
			case '\n', '\r', '\t':
				return ' '
			}
			return r
		}, line)
		// Strip hidden Unicode (TAG chars, zero-width joiners,
		// bidi controls). Same helper we apply to tool output.
		line = api.SanitizeUnicode(line)
		// Drop lines that contain markdown link syntax (inline
		// `](`, reference `[`/`]`, image `![`), HTML tags,
		// backticks, or bare URLs. Safer to show no description
		// than an injected one. A README's first description
		// line has no legitimate need for any of these.
		if strings.ContainsAny(line, "[]<`") ||
			strings.Contains(line, "http://") ||
			strings.Contains(line, "https://") {
			continue
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > 100 {
			return truncateUTF8(line, 100) + "..."
		}
		return line
	}
	return ""
}

// isMarkdownHeading reports whether line is a true CommonMark ATX
// heading: one to six `#` followed by whitespace or end-of-line.
// `#hashtag` content (no space after `#`) is NOT a heading and must
// fall through so legitimate README first lines are kept.
func isMarkdownHeading(line string) bool {
	if line == "" || line[0] != '#' {
		return false
	}
	i := 0
	for i < len(line) && line[i] == '#' {
		i++
	}
	if i > 6 {
		return false
	}
	return i == len(line) || line[i] == ' ' || line[i] == '\t'
}

// truncateUTF8 returns s truncated to at most n bytes without splitting
// a multi-byte UTF-8 rune. The 100-byte cap in readFirstLine would
// otherwise slice mid-rune, producing invalid UTF-8 in the steering
// file.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Walk back from the n-byte boundary to the nearest rune start.
	// A UTF-8 continuation byte has the bit pattern 10xxxxxx; back
	// up past them to land on a leading byte.
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

// writeMCP emits the "Connected integrations" section listing every
// currently-connected MCP server. Tool lists come from a separate
// channel (kiro.dev/commands/available) that isn't threaded to the
// steering generator today; server names alone are enough for the
// agent to know which integrations exist.
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
		fmt.Fprintf(w, "- Authenticated as: %s\n", user)
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
//
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
