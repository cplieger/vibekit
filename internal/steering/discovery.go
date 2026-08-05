package steering

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cplieger/vibekit/internal/api"
)

// Doc is one classified per-repo steering markdown file. The
// `Filename` is the basename (e.g. "conventions.md"); the `Inclusion`
// + `FileMatch` + `Description` are parsed from YAML frontmatter (or
// defaulted when absent). Used by writeWorkspace to render the per-repo
// steering inventory in environment.md grouped by trigger type.
type Doc struct {
	Filename    string // basename, e.g. "architecture.md"
	Inclusion   string // "always" | "fileMatch" | "manual" | "auto"; defaults to "always"
	FileMatch   string // glob pattern when Inclusion == "fileMatch"; empty otherwise
	Description string // human-readable description from the description field
}

// writeRepoSteering renders the per-repo steering inventory grouped
// by inclusion trigger ("always", "fileMatch", "manual"/"auto"). Indented one
// level under the repo bullet so the relationship is visually clear.
func writeRepoSteering(b *strings.Builder, repo string, docs []Doc) {
	always := make([]Doc, 0, len(docs))
	matched := make([]Doc, 0, len(docs))
	manual := make([]Doc, 0, len(docs))
	for _, d := range docs {
		switch d.Inclusion {
		case inclusionFileMatch:
			matched = append(matched, d)
		case inclusionManual, inclusionAuto:
			// "auto" is on-demand like "manual" — KAS offers both as slash
			// commands and excludes both from its always-loaded set — so the
			// header below ("read on demand") already describes it.
			manual = append(manual, d)
		default:
			always = append(always, d)
		}
	}
	if len(always) > 0 {
		fmt.Fprintf(b, "  - **Always-loaded steering** (read these as soon as you start working in `%s/`):\n", repo)
		for _, d := range always {
			writeSteeringEntry(b, repo, d)
		}
	}
	if len(matched) > 0 {
		fmt.Fprintf(b, "  - **File-match steering** (read when touching matching paths in `%s/`):\n", repo)
		for _, d := range matched {
			writeSteeringEntry(b, repo, d)
		}
	}
	if len(manual) > 0 {
		fmt.Fprintf(b, "  - **Manual steering** (read on demand or when invoked via `#name`):\n")
		for _, d := range manual {
			writeSteeringEntry(b, repo, d)
		}
	}
}

// writeSteeringEntry renders one steering doc bullet under a group header.
func writeSteeringEntry(b *strings.Builder, repo string, d Doc) {
	fmt.Fprintf(b, "    - `%s/.kiro/steering/%s`", repo, d.Filename)
	if d.FileMatch != "" {
		fmt.Fprintf(b, " (matches `%s`)", d.FileMatch)
	}
	if d.Description != "" {
		fmt.Fprintf(b, " — %s", d.Description)
	}
	b.WriteString("\n")
}

// writeRepoSteeringInstructions adds an explicit directive to the main
// agent about how to consume per-repo steering. Vibekit's main agent
// boots at /workspace, so kiro-cli's auto-include logic only loads
// steering at that level — the per-repo .kiro/steering/ dirs require
// an explicit nudge.
func writeRepoSteeringInstructions(b *strings.Builder, repos []string, workDir string) {
	// Only emit this section when at least one repo actually carries
	// .kiro/ content — otherwise it's noise.
	hasAny := false
	for _, r := range repos {
		rd := filepath.Join(workDir, r)
		if len(findRepoDocs(rd)) > 0 || len(findRepoSkills(rd)) > 0 ||
			len(findRepoAgents(rd)) > 0 || len(findRepoHooks(rd)) > 0 {
			hasAny = true
			break
		}
	}
	if !hasAny {
		return
	}
	b.WriteString("### Per-repo .kiro protocol\n\n")
	b.WriteString("The per-repo `.kiro/` directories above are NOT auto-loaded ")
	b.WriteString("by kiro-cli (its auto-include only fires for the cwd it booted in, ")
	b.WriteString("which is `/workspace`). Treat the inventory above as a routing table:\n\n")
	b.WriteString("- **When you start working in a repo** (any prompt that mentions it, ")
	b.WriteString("or as soon as you cd / read / edit a file inside it): immediately ")
	b.WriteString("read every \"Always-loaded steering\" and \"Always-loaded skills\" file listed for that repo.\n")
	b.WriteString("- **When you open or edit a file** that matches a \"File-match\" ")
	b.WriteString("pattern (steering or skill): read the matching doc first, then ")
	b.WriteString("proceed with the change.\n")
	b.WriteString("- **\"Manual\" steering/skills** are reference material — read on demand if a ")
	b.WriteString("relevant question arises or the user references them by name.\n")
	b.WriteString("- **Custom agents**: don't read; just be aware they exist. If the user ")
	b.WriteString("asks to switch agents, you know what's available.\n")
	b.WriteString("- **Hooks**: don't read; kiro-cli runs them automatically on their ")
	b.WriteString("trigger events. If you're about to perform an action where a hook ")
	b.WriteString("triggers, proactively mention it. When the user describes a workflow ")
	b.WriteString("pattern that would benefit from a hook, suggest creating one.\n")
	b.WriteString("- One read per session is enough; subsequent prompts within the same ")
	b.WriteString("turn don't need to re-read what you already loaded.\n")
	b.WriteString("- **If a doc is added or edited during this session**: the ")
	b.WriteString("inventory above is a snapshot from when this session started and ")
	b.WriteString("won't auto-refresh. Re-read the file via read_file directly if the ")
	b.WriteString("user mentions it. A fresh chat will pick it up automatically.\n\n")
}

// writeRepoSkills renders the per-repo skills inventory grouped by
// inclusion trigger, same as steering.
func writeRepoSkills(b *strings.Builder, repo string, docs []Doc) {
	always := make([]Doc, 0, len(docs))
	matched := make([]Doc, 0, len(docs))
	manual := make([]Doc, 0, len(docs))
	for _, d := range docs {
		switch d.Inclusion {
		case inclusionFileMatch:
			matched = append(matched, d)
		case inclusionManual, inclusionAuto:
			// "auto" is on-demand like "manual" — KAS offers both as slash
			// commands and excludes both from its always-loaded set — so the
			// header below ("read on demand") already describes it.
			manual = append(manual, d)
		default:
			always = append(always, d)
		}
	}
	if len(always) > 0 {
		fmt.Fprintf(b, "  - **Always-loaded skills** (`%s/.kiro/skills/`):\n", repo)
		for _, d := range always {
			writeSkillEntry(b, repo, d)
		}
	}
	if len(matched) > 0 {
		fmt.Fprintf(b, "  - **File-match skills** (`%s/.kiro/skills/`):\n", repo)
		for _, d := range matched {
			writeSkillEntry(b, repo, d)
		}
	}
	if len(manual) > 0 {
		fmt.Fprintf(b, "  - **Manual skills** (invoke verbally by name):\n")
		for _, d := range manual {
			writeSkillEntry(b, repo, d)
		}
	}
}

func writeSkillEntry(b *strings.Builder, repo string, d Doc) {
	fmt.Fprintf(b, "    - `%s/.kiro/skills/%s`", repo, d.Filename)
	if d.FileMatch != "" {
		fmt.Fprintf(b, " (matches `%s`)", d.FileMatch)
	}
	if d.Description != "" {
		fmt.Fprintf(b, " — %s", d.Description)
	}
	b.WriteString("\n")
}

// findRepoDocs scans a repo's `.kiro/steering/` directory and returns
// the markdown files classified by their YAML frontmatter inclusion
// mode. Files without frontmatter default to "always". Delegates to
// findMdDocsInDir, which caps each file read at 64 KiB (only the
// frontmatter head is needed) and the result at 20 entries — a
// reasonable environment.md budget; a repo with more steering than that
// is pathological and the agent gets a representative sample either way.
func findRepoDocs(repoDir string) []Doc {
	return findMdDocsInDir(filepath.Join(repoDir, ".kiro", "steering"))
}

// parseSteeringFrontmatter adapts the shared front-matter parser (Parse, in
// frontmatter.go) onto the Doc shape this file's environment.md writers use.
//
// It used to BE the parser, line-oriented, and it returned the literal ">" as
// the description of every document using a block scalar — which was all 47
// agents and 14 of 28 skills in this repo, rendered into the agent-facing
// environment.md as "— >". Do not reintroduce a local parse here; one parser
// serves this generator and the REST scanners both.
func parseSteeringFrontmatter(data []byte) Doc {
	fm := Parse(data)
	return Doc{
		Inclusion:   fm.Inclusion,
		FileMatch:   fm.FileMatch,
		Description: fm.Description,
	}
}

// frontmatterBody returns the YAML front-matter block of a steering
// markdown file — the text between the opening and closing `---` fences —
// and whether a well-formed block was present. It strips a leading UTF-8
// BOM and normalizes CRLF to LF first, so fence detection is BOM- and
// line-ending-agnostic (a CRLF- or BOM-authored doc must not fall
// through the exact `---\n` prefix check and lose its front-matter).
func frontmatterBody(data []byte) (string, bool) {
	content := normalizeText(data)
	if !strings.HasPrefix(content, "---\n") {
		return "", false
	}
	end := strings.Index(content[4:], "\n---")
	if end <= 0 {
		return "", false
	}
	return content[4 : 4+end], true
}

// normalizeInclusion validates a steering front-matter inclusion value,
// folding any unrecognized value (typo, empty) to the default "always".
//
// FOUR values, not three. KAS's SteeringContextFrontMatterSchema declares
// `inclusion: enum(["always","fileMatch","manual","auto"])`, and "auto" is an
// ON-DEMAND mode: `emitDocumentsChanged` filters `inclusion !== "auto"` out of
// its notification, and `createSteeringCommandSource` collects `manual` and
// `auto` together as slash-command entries. Folding it to "always" therefore
// claimed the exact opposite of the truth about the one thing the inclusion
// badge exists to answer — whether a doc costs tokens on every session.
func normalizeInclusion(v string) string {
	switch v {
	case inclusionFileMatch:
		return inclusionFileMatch
	case inclusionManual:
		return inclusionManual
	case inclusionAuto:
		return inclusionAuto
	default:
		return inclusionAlways
	}
}

// ParseInclusion returns the validated inclusion mode ("always",
// "fileMatch", "manual" or "auto") from a steering markdown file's YAML
// front-matter, folding unknown or absent values to "always". It
// tolerates a leading UTF-8 BOM and CRLF line endings. Exported so the
// REST kiro-config scanner (internal/server) classifies steering docs
// through this single parser rather than a divergent copy.
func ParseInclusion(data []byte) string {
	return parseSteeringFrontmatter(data).Inclusion
}

// findRepoSkills scans `.kiro/skills/` for skill directories. A skill is
// a DIRECTORY containing SKILL.md (mirrors the authoritative REST scan
// in internal/server/kiro_config.go's scanSkills) — NOT a flat `.md`
// file. Each SKILL.md's frontmatter classifies the skill by inclusion
// mode; a subdirectory without a SKILL.md still counts as a skill
// (default "always"), matching the REST scan. Reads are capped at 64 KiB
// (only the frontmatter head is needed) and the result at 20 entries.
func findRepoSkills(repoDir string) []Doc {
	dir := filepath.Join(repoDir, ".kiro", "skills")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]Doc, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		// The skill's classification lives in its SKILL.md frontmatter;
		// a missing SKILL.md yields empty data -> default "always".
		data, _ := readCappedFile(filepath.Join(dir, e.Name(), "SKILL.md"), FrontMatterReadCap)
		doc := parseSteeringFrontmatter(data)
		doc.Filename = e.Name() + "/SKILL.md"
		out = append(out, doc)
		if len(out) >= 20 {
			break
		}
	}
	return out
}

// AgentEntry is a custom agent config found in `.kiro/agents/`.
type AgentEntry struct {
	Filename string
	Name     string // from JSON "name" field
}

// findRepoAgents scans `.kiro/agents/` for agent configs. An agent may
// ship as a `.json` config, a `.md` doc, or both; paired files share a
// base name and count as ONE agent (preferring the `.md`, mirroring the
// REST scan's scanAgents in internal/server/kiro_config.go). Capped at
// 10 distinct agents.
func findRepoAgents(repoDir string) []AgentEntry {
	dir := filepath.Join(repoDir, ".kiro", "agents")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	// De-dupe by base name; prefer the .md file when both .md and .json
	// exist for the same agent.
	chosen := make(map[string]string) // base name -> chosen filename
	var order []string
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		name, ok := agentBaseName(e.Name())
		if !ok {
			continue
		}
		existing, seen := chosen[name]
		switch {
		case !seen:
			chosen[name] = e.Name()
			order = append(order, name)
		case strings.HasSuffix(e.Name(), ".md") && strings.HasSuffix(existing, ".json"):
			chosen[name] = e.Name()
		}
	}
	out := make([]AgentEntry, 0, min(len(order), 10))
	for _, name := range order {
		if len(out) >= 10 {
			break
		}
		out = append(out, AgentEntry{Filename: chosen[name], Name: name})
	}
	return out
}

// agentBaseName returns the base name of an agent config file (stripping
// a `.md` or `.json` extension) and whether the file is an agent config.
func agentBaseName(filename string) (string, bool) {
	switch {
	case strings.HasSuffix(filename, ".md"):
		return strings.TrimSuffix(filename, ".md"), true
	case strings.HasSuffix(filename, ".json"):
		return strings.TrimSuffix(filename, ".json"), true
	default:
		return "", false
	}
}

// HookEntry is one hook from a `.kiro/hooks/*.json` document.
type HookEntry struct {
	Filename string
	Name     string // hook name from the v1 envelope
	Trigger  string // PascalCase trigger: "SessionStart", "PreToolUse", "PostFileSave", …
	Command  string // truncated action preview (command, or prompt for agent hooks)
}

// findRepoHooks scans `.kiro/hooks/` for JSON hook documents. Each file
// is a v1 envelope carrying one or more hooks; every hook renders as
// its own entry. Capped at 10 entries total.
func findRepoHooks(repoDir string) []HookEntry {
	dir := filepath.Join(repoDir, ".kiro", "hooks")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []HookEntry
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, _ := readCappedFile(path, 16<<10)
		for _, h := range parseHookDoc(data) {
			h.Filename = e.Name()
			out = append(out, h)
			if len(out) >= 10 {
				return out
			}
		}
	}
	return out
}

// ParseHooks parses a v1 hook document into its entries, with every field
// sanitized. Exported so the REST docs scanner reuses this parser rather than
// re-deriving one: hook files are workspace content, and sanitizeHookField is
// what keeps a raw newline or backtick from breaking out of the code span these
// values are rendered into.
func ParseHooks(data []byte) []HookEntry {
	return parseHookDoc(data)
}

// parseHookDoc parses a v1 hook document:
//
//	{"version":"v1","hooks":[{name, trigger, matcher?,
//	  action:{type:"command"|"agent", command|prompt}, timeout?}]}
//
// This is the on-disk format Kiro's createHook tool and vibekit's own
// create_hook command write (internal/command/hooks.go buildHookDoc);
// triggers are PascalCase (SessionStart, PreToolUse, PostFileSave, …).
// One file may carry multiple hooks; each becomes its own entry. The
// action preview prefers command hooks' command and falls back to agent
// hooks' prompt. Malformed JSON or an empty hooks array yields nil.
func parseHookDoc(data []byte) []HookEntry {
	var doc struct {
		Hooks []struct {
			Name    string `json:"name"`
			Trigger string `json:"trigger"`
			Action  struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Prompt  string `json:"prompt"`
			} `json:"action"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}
	out := make([]HookEntry, 0, len(doc.Hooks))
	for _, h := range doc.Hooks {
		preview := h.Action.Command
		if h.Action.Type == "agent" {
			preview = h.Action.Prompt
		}
		preview = sanitizeHookField(preview)
		if len(preview) > 80 {
			preview = truncateUTF8(preview, 77) + "..."
		}
		out = append(out, HookEntry{
			Name:    sanitizeHookField(h.Name),
			Trigger: sanitizeHookField(h.Trigger),
			Command: preview,
		})
	}
	return out
}

// sanitizeHookField flattens control characters, strips hidden Unicode,
// and swaps backticks for quotes. Hook files are workspace repo content
// (attacker-controlled from vibekit's point of view, same threat model
// as readFirstLine), and these fields are written into the steering
// file inside code spans — a raw newline or backtick would break out of
// the span and inject agent-visible steering lines.
func sanitizeHookField(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t':
			return ' '
		case '`':
			return '\''
		}
		return r
	}, s)
	return api.SanitizeUnicode(s)
}

// findMdDocsInDir scans a flat directory of `.md` files, classifying
// each by its YAML frontmatter (inclusion / fileMatchPattern /
// description) and defaulting to "always" when absent. Reads are capped
// at 64 KiB (frontmatter is at the head) and the result at 20 entries.
// Backs findRepoDocs (per-repo steering); skills use findRepoSkills,
// which scans subdirectories for SKILL.md instead.
func findMdDocsInDir(dir string) []Doc {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]Doc, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, _ := readCappedFile(path, FrontMatterReadCap)
		doc := parseSteeringFrontmatter(data)
		doc.Filename = e.Name()
		out = append(out, doc)
		if len(out) >= 20 {
			break
		}
	}
	return out
}
