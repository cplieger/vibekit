package steering

import (
	"io/fs"
	"strings"
)

// The ONE prefer-`.md` rule for an `agents/` directory listing.
//
// An agent may ship as a `.md` doc, a `.json` ACP config, or both, sharing a
// base name. Three doors read this directory — the document-oriented and
// entity-oriented REST scans, and this package's environment.md generator —
// and each had its own copy of the collapse before this consolidation, so the
// three could disagree about which files exist.
//
// The cap and the read are deliberately NOT here: each door answers to a
// different budget and does its own thing with the chosen file.

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
