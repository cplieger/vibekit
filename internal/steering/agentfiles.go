package steering

import (
	"io/fs"
	"strings"
)

// The ONE prefer-`.md` rule for an `agents/` directory listing.
//
// # Why this is shared
//
// An agent may ship as a `.md` doc, a `.json` ACP config, or both, and the pair
// shares a base name. Three doors read that directory for three different
// audiences — the DOCUMENT-oriented REST scan, the ENTITY-oriented REST scan,
// and this package's environment.md generator — and each had its own copy of the
// collapse, the last of them carrying a comment naming the second as the one it
// mirrored. A rule that says in its own comment which copy it is mirroring is a
// rule with no owner: the three cannot be checked against each other, and a
// caller reading one door cannot tell whether the other two agree. This is the
// same consolidation frontmatter.go describes for the front-matter parser, and
// it lands in the same package for the same reason — `.kiro` file semantics are
// this package's, and internal/server already depends on it.
//
// # What the doors keep
//
// The CAP is not here. The document scan, the entity scan and the generator cap
// at three different numbers because they answer to three different surfaces,
// and a cap is a budget rather than a fact about the directory. Nor is the read:
// what each door does with the file it was handed is its own business.
//
// # The union of the guards, not the intersection
//
// Each copy screened something the others did not: two refused a NUL in the name
// (they build a path the client is handed), one refused a dotfile (an
// AppleDouble `._agent.json` or a hidden draft is not an authored agent, and its
// base name renders as junk in an inventory). Both belong in one rule — the
// alternative is three doors that disagree about which files exist, which is the
// state this replaces.

// AgentFile is one agent as an `agents/` listing resolves it.
type AgentFile struct {
	// Base is the agent's name: the filename with its extension stripped, which
	// is what both spellings of a pair share.
	Base string
	// File is the basename of the file chosen to represent it.
	File string
}

// DedupeAgentFiles collapses an `agents/` directory listing to one file per
// agent, preferring the `.md` over the `.json` when both exist (the markdown is
// what carries the front-matter, so it is the spelling that can describe itself).
// A `.json`-only agent is listed under its JSON.
//
// Entries are returned in first-seen order, so the caller's order is the
// directory's and a cap applied at the call site takes a stable prefix.
func DedupeAgentFiles(entries []fs.DirEntry) []AgentFile {
	chosen := make(map[string]int, len(entries)) // base name -> index in out
	out := make([]AgentFile, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		// A dot-prefixed name also covers a bare ".md" / ".json", whose base would
		// be empty.
		if e.IsDir() || strings.HasPrefix(name, ".") || strings.ContainsRune(name, 0) {
			continue
		}
		base, ok := agentBaseName(name)
		if !ok {
			continue
		}
		i, seen := chosen[base]
		switch {
		case !seen:
			chosen[base] = len(out)
			out = append(out, AgentFile{Base: base, File: name})
		case strings.HasSuffix(name, ".md") && strings.HasSuffix(out[i].File, ".json"):
			out[i].File = name
		}
	}
	return out
}

// agentBaseName returns the base name of an agent config file (stripping a `.md`
// or `.json` extension) and whether the file is an agent config at all.
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
