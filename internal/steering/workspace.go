package steering

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cplieger/vibekit/internal/git"
	"github.com/cplieger/vibekit/internal/sanitize"
)

func writeWorkspace(ctx context.Context, b *strings.Builder, workDir string, forgeKinds map[string]bool) {
	entries, err := os.ReadDir(workDir)
	if err != nil || len(entries) == 0 {
		b.WriteString("## Workspace\n\nEmpty.\n\n")
		return
	}
	repos, dirs := classifyEntries(ctx, entries, workDir)
	foundFiles := findNotableFiles(workDir)
	isRoot := git.IsRepo(ctx, workDir)
	b.WriteString("## Workspace\n\n")
	if isRoot {
		b.WriteString("The workspace root (`/workspace`) is itself a git repository.\n\n")
	}
	if len(repos) > 0 {
		b.WriteString("### Git repositories\n\n")
		b.WriteString("Multiple repos coexist under `/workspace`. ")
		b.WriteString("Use the `cwd` parameter in shell commands to target a specific repo ")
		b.WriteString("(e.g. `cwd: \"myrepo\"` runs in `/workspace/myrepo/`). ")
		b.WriteString("File paths like `myrepo/src/main.go` work with the file tools ")
		b.WriteString("(read_file, read_code, grep_search).\n\n")
		for _, r := range repos {
			writeRepoEntry(b, workDir, r, forgeKinds)
		}
		b.WriteString("\n")
		b.WriteString("The Git panel in the UI presents these repositories as collapsible ")
		b.WriteString("sections under the **Changes** tab (uncommitted work + commit + push), ")
		b.WriteString("the **Pull requests** tab (open PRs per repo + create new), and the ")
		b.WriteString("**Sources** tab (forge accounts + cloneable remote repos).\n\n")
		// Add a top-level instruction so the agent has unambiguous
		// guidance about how to consume the per-repo steering it just
		// saw above. Without this, kiro-cli would only auto-load
		// steering for the cwd it boots in (the workspace root); the
		// per-repo `.kiro/steering/` dirs would otherwise sit unused.
		writeRepoSteeringInstructions(b, repos, workDir)
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
			fmt.Fprintf(b, "- `%s/`\n", defuse(d))
		}
		b.WriteString("\n")
	}
	// Only when repos sit UNDER the workspace root. If the root is itself a
	// repo there is no sibling path to suggest — everything under workDir
	// would be inside that repo — and with no repos there is nothing to stay
	// out of. Same guard shape as Directories above.
	if len(repos) > 0 && !isRoot {
		writeScratchGuidance(b, workDir)
	}
}

// writeScratchGuidance suggests a scratch location outside every repo.
//
// The agent already holds bundled workflow-orchestration guidance naming
// in-repo artifact paths for plans and review output; measured 2026-08-26, 42
// scratch files were written into working trees this way across two repos in
// one afternoon. A canary probe the same day confirmed a workflow STEP session
// receives only the always-on set (this file plus always-loaded workspace
// steering), so this generated doc is the only place a rule reaches one.
//
// Deliberately a preference, not a prohibition — the enforcement lever is a
// permissions.yaml fs_write deny rule, not prose.
func writeScratchGuidance(b *strings.Builder, workDir string) {
	b.WriteString("### Scratch files\n\n")
	b.WriteString("Prefer a directory OUTSIDE every repo for files that are not headed for a ")
	b.WriteString("commit: plans, investigation notes, probe output, draft commit messages, ")
	b.WriteString("review verdicts, run state. ")
	fmt.Fprintf(b, "`%s/_scratch/<task>/` is a good default; any path outside a repo working "+
		"tree works.\n\n", workDir)
	b.WriteString("This applies to a path a workflow or skill suggests, not just to one you ")
	b.WriteString("pick. Where the guidance you were given names an in-repo artifact path ")
	b.WriteString("(`<repo>/.agents/tasks/...` is the common one), a `_scratch` path can be ")
	b.WriteString("substituted, including in artifact maps and `fileCheck` stop-condition ")
	b.WriteString("paths. Scratch left in a working tree is work for whoever reads ")
	b.WriteString("`git status` next, and it can end up in a commit.\n\n")
}

func writeRepoEntry(b *strings.Builder, workDir, r string, forgeKinds map[string]bool) {
	repoDir := filepath.Join(workDir, r)
	// One defusal per repo, threaded into every writer below, rather than one
	// per interpolation: a directory name is arbitrary bytes (the agent creates
	// directories and clones into them), and defusing at each `%s` is the shape
	// that already lost a channel. `r` itself stays raw because it is also a
	// path component.
	label := defuse(r)
	origin := readGitOrigin(repoDir)
	branch := readGitBranch(repoDir)
	desc := readFirstLine(filepath.Join(repoDir, "README.md"))
	fmt.Fprintf(b, "- `%s/`", label)
	if branch != "" {
		fmt.Fprintf(b, " on `%s`", branch)
	}
	if origin != "" {
		host := hostFromGitURL(origin)
		kind := kindFromHost(host)
		// Only advertise the forge CLI when that forge kind is
		// connected — the CLI binary is installed and authenticated
		// at forge login, so an unconnected kind's CLI isn't on PATH.
		cli := forgeCLI(kind)
		if cli != "" && cliKindConnected(forgeKinds, kind) {
			fmt.Fprintf(b, " (%s — use `%s` for PRs/issues/CI)", host, cli)
		} else if host != "" {
			fmt.Fprintf(b, " (%s)", host)
		}
	}
	if desc != "" {
		fmt.Fprintf(b, " — %s", desc)
	}
	b.WriteString("\n")
	docs := findRepoDocs(repoDir)
	if len(docs) > 0 {
		writeRepoSteering(b, label, docs)
	}
	skills := findRepoSkills(repoDir)
	if len(skills) > 0 {
		writeRepoSkills(b, label, skills)
	}
	agents := findRepoAgents(repoDir)
	if len(agents) > 0 {
		writeRepoAgents(b, label, agents)
	}
	hooks := findRepoHooks(repoDir)
	if len(hooks) > 0 {
		writeRepoHooks(b, label, hooks)
	}
}

// writeRepoAgents renders the custom-agents inventory line for a repo.
func writeRepoAgents(b *strings.Builder, repo string, agents []AgentEntry) {
	fmt.Fprintf(b, "  - **Custom agents** (`%s/.kiro/agents/`):", repo)
	for _, a := range agents {
		fmt.Fprintf(b, " `%s`", a.Name)
	}
	b.WriteString("\n")
}

// writeRepoHooks renders the hooks inventory for a repo, one bullet per
// hook with its name, trigger label, and (when present) an action
// preview. Fields are pre-sanitised by parseHookDoc.
func writeRepoHooks(b *strings.Builder, repo string, hooks []HookEntry) {
	fmt.Fprintf(b, "  - **Hooks** (`%s/.kiro/hooks/`):\n", repo)
	for _, h := range hooks {
		trigger := cmp.Or(h.Trigger, "unknown")
		fmt.Fprintf(b, "    - `%s`", h.Filename)
		if h.Name != "" {
			fmt.Fprintf(b, " %s", h.Name)
		}
		fmt.Fprintf(b, " [%s]", trigger)
		if h.Command != "" {
			fmt.Fprintf(b, " → `%s`", h.Command)
		}
		b.WriteString("\n")
	}
}

// readGitOrigin returns the origin URL of a git repo by reading its
// `.git/config` directly. We avoid shelling out to `git remote get-url`
// because steering generation runs synchronously on every event and
// must not block on a wedged subprocess. The format is well-defined
// (`[remote "origin"]` block with a `url = ...` line); a tiny line
// scanner handles 99% of real-world configs without parsing INI fully.
func readGitOrigin(repoDir string) string {
	data, err := readCappedFile(filepath.Join(repoDir, ".git", "config"), 64*1024)
	if err != nil {
		return ""
	}
	inOrigin := false
	for line := range strings.SplitSeq(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			inOrigin = trimmed == `[remote "origin"]`
			continue
		}
		if !inOrigin {
			continue
		}
		if k, v, ok := strings.Cut(trimmed, "="); ok {
			if strings.TrimSpace(k) == "url" {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}

// readGitBranch returns the current branch name of a git repo by reading
// `.git/HEAD` directly (no subprocess — steering generation runs synchronously
// and must not block on a wedged one). Detached-HEAD repos return "".
//
// Cut at the FIRST line and defused: `.git/HEAD` is workspace content, not a
// name git validated, and a crafted second line ("## Capabilities") was once
// rendered as a real steering section — measured. A branch is one line by
// definition, so the cut costs nothing real.
func readGitBranch(repoDir string) string {
	data, err := readCappedFile(filepath.Join(repoDir, ".git", "HEAD"), 1024)
	if err != nil {
		return ""
	}
	first, _, _ := strings.Cut(string(data), "\n")
	s := strings.TrimSpace(first)
	const refsPrefix = "ref: refs/heads/"
	if branch, ok := strings.CutPrefix(s, refsPrefix); ok {
		return defuse(branch)
	}
	return ""
}

// hostFromGitURL extracts the host from a git remote URL. Handles
// both https:// and scp-style git@host:path forms. Returns "" for
// shapes we don't recognise (file://, ext::, etc) and for anything that is not
// SHAPED like a host — see isHostShaped, which is what makes the two callers'
// downstream reasoning sound.
func hostFromGitURL(url string) string {
	url = strings.TrimSpace(url)
	var host string
	if strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://") {
		host = hostFromHTTPURL(url)
	} else {
		host = hostFromSCPURL(url)
	}
	if !isHostShaped(host) {
		return ""
	}
	return host
}

// isHostShaped reports whether s could be a DNS host or an IPv4 literal with an
// optional port: ASCII letters, digits, dot, dash, underscore and colon only.
//
// Two reasons this refusal is load-bearing. A `.git/config` url comes from a
// file the agent writes, so without this gate a backtick or bracket could
// reach environment.md inside the host annotation. And kindFromHost lowercases
// this value before matching against `github`/`gitlab`/`gitea` — a fold that
// fails open under Unicode simple case mapping (U+0130/U+212A lowercase to
// ASCII `i`/`k`, measured to make `g\u0130thub.com` match `github.com`).
// Restricting the alphabet upstream makes strings.ToLower provably an ASCII
// fold, which is why the order matters (TestHostGateGuardsTheFold pins it).
//
// Cost: an IPv6-literal remote loses its host annotation (`[`/`]` are
// markdown-significant); the repo still renders, without the parenthesis.
func isHostShaped(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == '-', c == '_', c == ':':
		default:
			return false
		}
	}
	return true
}

// hostFromHTTPURL extracts the host from an http(s):// git URL, stripping
// any user[:pass]@ credentials that appear before the first path slash.
// Returns "" when the resulting host still carries an "@" or "/".
func hostFromHTTPURL(url string) string {
	_, rest, _ := strings.Cut(url, "://")
	// Strip credentials if present (https://user:pwd@host/...).
	// Use the first @ only if it appears before the first /.
	slash := strings.Index(rest, "/")
	if at := strings.Index(rest, "@"); at >= 0 && (slash < 0 || at < slash) {
		rest = rest[at+1:]
	}
	if i := strings.Index(rest, "/"); i > 0 {
		host := rest[:i]
		if strings.ContainsAny(host, "@/") {
			return ""
		}
		return host
	}
	if strings.ContainsAny(rest, "@/") {
		return ""
	}
	return rest
}

// hostFromSCPURL extracts the host from an scp-style git@host:path URL.
// Returns "" for any other shape (no "@", a leading "@", or no ":"
// separator), and for a host that still contains an "@" or "/".
func hostFromSCPURL(url string) string {
	at := strings.Index(url, "@")
	if at <= 0 {
		return ""
	}
	rest := url[at+1:]
	colon := strings.Index(rest, ":")
	if colon <= 0 {
		return ""
	}
	host := rest[:colon]
	if strings.ContainsAny(host, "@/") {
		return ""
	}
	return host
}

// kindFromHost maps a git host to its forge kind. Uses suffix
// matching for self-hosted variants: gitlab.example.com → gitlab,
// gitea.example.com → gitea. Returns "" for unrecognised hosts.
func kindFromHost(host string) string {
	host = strings.ToLower(host)
	if host == "" {
		return ""
	}
	if host == "github.com" || strings.HasSuffix(host, ".github.com") || strings.HasPrefix(host, "github.") {
		return kindGitHub
	}
	if host == "gitlab.com" || strings.HasSuffix(host, ".gitlab.com") || strings.Contains(host, "gitlab") {
		return kindGitLab
	}
	if host == "codeberg.org" {
		return kindCodeberg
	}
	if strings.Contains(host, "gitea") || strings.Contains(host, "forgejo") {
		return kindGitea
	}
	return ""
}

// cliKindConnected reports whether the forge CLI serving `kind` is
// available: the kind itself is connected, or — for the shared tea
// CLI — its sibling kind is (codeberg is a named gitea shortcut; one
// login makes `tea` available for both).
func cliKindConnected(forgeKinds map[string]bool, kind string) bool {
	if forgeKinds[kind] {
		return true
	}
	switch kind {
	case kindGitea:
		return forgeKinds[kindCodeberg]
	case kindCodeberg:
		return forgeKinds[kindGitea]
	}
	return false
}

// classifyEntries splits workspace entries into git repos and plain
// directories. Dot-NAMED git repos (".kiro", ".github") are legitimate
// clone targets and must be listed — the Git panel's repo scanner
// (internal/git/repos.go) learned this the hard way; skipping every
// dot-dir made such clones invisible while their steering inventories
// sat unused. Dot-named NON-repos (.cache, .venv) stay hidden: they are
// tool state, not workspace content the agent should be pointed at.
func classifyEntries(ctx context.Context, entries []os.DirEntry, workDir string) (repos, dirs []string) {
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || name == ".git" {
			continue
		}
		if git.IsRepo(ctx, filepath.Join(workDir, name)) {
			repos = append(repos, name)
		} else if !strings.HasPrefix(name, ".") {
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

// readFirstLine returns the first non-blank non-heading line of the README at
// path, capped and sanitised so hostile repo content cannot inject agent
// instructions into environment.md (kiro-cli's authoritative agent context).
//
// Order: cap the read, strip CR/LF/tab before truncation (so a smuggled
// newline can't produce a second "line"), strip hidden Unicode, then drop any
// line containing markdown link syntax, HTML tags, backticks, or a bare URL —
// a README description line has no legitimate need for any of those.
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
		line = sanitize.Unicode(line)
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
