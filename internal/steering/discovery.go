package steering

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Doc is one classified per-repo steering markdown file. The
// `Filename` is the basename (e.g. "conventions.md"); the `Inclusion`
// + `FileMatch` + `Description` are parsed from YAML frontmatter (or
// defaulted when absent). Used by writeWorkspace to render the per-repo
// steering inventory in environment.md grouped by trigger type.
type Doc struct {
	Filename    string // basename, e.g. "architecture.md"
	Inclusion   string // "always" | "fileMatch" | "manual"; defaults to "always"
	FileMatch   string // glob pattern when Inclusion == "fileMatch"; empty otherwise
	Description string // human-readable description from the description field
}

// writeRepoSteering renders the per-repo steering inventory grouped
// by inclusion trigger ("always", "fileMatch", "manual"). Indented one
// level under the repo bullet so the relationship is visually clear.
func writeRepoSteering(b *strings.Builder, repo string, docs []Doc) {
	always := make([]Doc, 0, len(docs))
	matched := make([]Doc, 0, len(docs))
	manual := make([]Doc, 0, len(docs))
	for _, d := range docs {
		switch d.Inclusion {
		case inclusionFileMatch:
			matched = append(matched, d)
		case inclusionManual:
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
	b.WriteString("- **Hooks**: don't read; kiro-cli enforces them at tool-call time. ")
	b.WriteString("If you're about to perform an action where a hook triggers, ")
	b.WriteString("proactively mention it. When the user describes a workflow pattern ")
	b.WriteString("that would benefit from a hook, suggest creating one.\n")
	b.WriteString("- One read per session is enough; subsequent prompts within the same ")
	b.WriteString("turn don't need to re-read what you already loaded.\n")
	b.WriteString("- **If a doc is added or edited during this session**: the ")
	b.WriteString("inventory above is a snapshot from when this session started and ")
	b.WriteString("won't auto-refresh. Re-read the file via `fs_read` directly if the ")
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
		case inclusionManual:
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

// findRepoDocs scans a repo's `.kiro/steering/` directory and
// returns the markdown files classified by their YAML frontmatter
// inclusion mode. Files without frontmatter default to "always".
// Returns at most 20 entries — a reasonable cap for the environment.md
// budget; repos with more steering than that are pathological and the
// agent gets a representative sample either way.
func findRepoDocs(repoDir string) []Doc {
	dir := filepath.Join(repoDir, ".kiro", "steering")
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
		// Cap each steering file read at 64 KiB; we only need the
		// frontmatter, which is at the head. Anything bigger would
		// mean a broken file or pathological YAML; treat as truncated.
		data, _ := readCappedFile(path, 64<<10)
		doc := parseSteeringFrontmatter(data)
		doc.Filename = e.Name()
		out = append(out, doc)
		if len(out) >= 20 {
			break
		}
	}
	return out
}

// parseSteeringFrontmatter extracts inclusion / fileMatchPattern /
// description from a steering markdown's YAML frontmatter. Mirrors
// the kiro-cli convention used by the IDE:
//
//	---
//	inclusion: fileMatch
//	fileMatchPattern: "internal/**/*.go"
//	description: "Go layout for the internal/ tree"
//	---
//
// Files without frontmatter default to inclusion=always with no
// pattern and no description. Pure function — testable without DOM/FS.
func parseSteeringFrontmatter(data []byte) Doc {
	doc := Doc{Inclusion: "always"}
	if len(data) == 0 {
		return doc
	}
	content := string(data)
	if !strings.HasPrefix(content, "---\n") {
		return doc
	}
	end := strings.Index(content[4:], "\n---")
	if end <= 0 {
		return doc
	}
	fm := content[4 : 4+end]
	for line := range strings.SplitSeq(fm, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key := strings.TrimSpace(k)
		val := strings.Trim(strings.TrimSpace(v), `"'`)
		switch key {
		case "inclusion":
			if val == inclusionAlways || val == inclusionFileMatch || val == inclusionManual {
				doc.Inclusion = val
			}
		case "fileMatchPattern":
			doc.FileMatch = val
		case "description":
			doc.Description = val
		}
	}
	return doc
}

// findRepoSkills scans `.kiro/skills/` with the same frontmatter
// classification as steering docs. Skills use the same inclusion model.
func findRepoSkills(repoDir string) []Doc {
	return findMdDocsInDir(filepath.Join(repoDir, ".kiro", "skills"))
}

// AgentEntry is a custom agent config found in `.kiro/agents/`.
type AgentEntry struct {
	Filename string
	Name     string // from JSON "name" field
}

// findRepoAgents scans `.kiro/agents/` for JSON agent configs.
func findRepoAgents(repoDir string) []AgentEntry {
	dir := filepath.Join(repoDir, ".kiro", "agents")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []AgentEntry
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".json") && !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimSuffix(e.Name(), ".json"), ".md")
		out = append(out, AgentEntry{Filename: e.Name(), Name: name})
		if len(out) >= 10 {
			break
		}
	}
	return out
}

// HookEntry is a hook config found in `.kiro/hooks/`.
type HookEntry struct {
	Filename string
	Trigger  string // "preToolUse", "postToolUse", "agentSpawn", "userPromptSubmit"
	Command  string // truncated command preview
}

// findRepoHooks scans `.kiro/hooks/` for JSON hook configs.
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
		h := parseHookJSON(data)
		h.Filename = e.Name()
		out = append(out, h)
		if len(out) >= 10 {
			break
		}
	}
	return out
}

func parseHookJSON(data []byte) HookEntry {
	var raw struct {
		EventType string `json:"event_type"`
		Command   string `json:"command"`
	}
	_ = json.Unmarshal(data, &raw)
	cmd := raw.Command
	if len(cmd) > 80 {
		cmd = cmd[:77] + "..."
	}
	return HookEntry{Trigger: raw.EventType, Command: cmd}
}

// findMdDocsInDir is a shared helper for scanning .md files with
// frontmatter classification (used by both steering and skills).
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
		data, _ := readCappedFile(path, 64<<10)
		doc := parseSteeringFrontmatter(data)
		doc.Filename = e.Name()
		out = append(out, doc)
		if len(out) >= 20 {
			break
		}
	}
	return out
}
