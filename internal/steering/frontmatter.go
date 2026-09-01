// The ONE YAML front-matter parser for `.kiro/**/*.md`.
//
// Replaced a line-oriented `strings.Cut(line, ":")` reader that mishandled
// YAML block scalars (`description: >`), returning the bare indicator as the
// value and dropping continuation lines — measured on this repo: 61 of 216
// `.kiro` documents used a block scalar, so the agent-facing environment.md
// rendered "— >" as a skill's description.
//
// internal/server's REST scanners and this package's environment.md generator
// both read the same fields off the same files through this one parser.
//
// Deliberately not a YAML library: handles only the subset `.kiro`
// front-matter uses (flat/block/quoted scalars, flow/block sequences), and a
// malformed header degrades to empty fields rather than an error — a bad
// front-matter block is the author's own file, not vibekit's to reject.

package steering

import (
	"strings"
)

// FrontMatterReadCap bounds how much of a document is read to find its
// front-matter. Only the head matters, so an untrusted workspace repo cannot OOM
// the container with a giant file.
//
// Exported because it used to be written FOUR times: twice inline in
// discovery.go, once as `steeringReadCap` in internal/server, and again for
// hooks. One definition, every caller.
const FrontMatterReadCap = 64 << 10

// FrontMatter is every field `.kiro` documents carry that a reader surfaces.
// A field absent from a document is the zero value.
//
// Field census over this workspace's 216 documents, which is why exactly these
// keys are parsed: description 183, inclusion 123, fileMatchPattern 99, name 60,
// tools 47, model 47, steering_override 11.
type FrontMatter struct {
	// Name is the `name` key: a skill's or agent's declared name, which may
	// differ from its filename.
	Name string
	// Description is the `description` key, block scalars included.
	Description string
	// Inclusion is `inclusion`, validated to always|fileMatch|manual|auto with
	// anything unrecognised folded to always.
	//
	// The default is the STEERING default, so a caller classifying something
	// else must consult HasInclusion before believing it.
	Inclusion string
	// FileMatch is `fileMatchPattern`, meaningful when Inclusion is fileMatch.
	FileMatch string
	// Model is an agent's `model`.
	Model string
	// Tools is an agent's `tools` sequence, flow (`[a, b]`) or block (`- a`).
	Tools []string
	// HasInclusion reports whether the document actually DECLARED an inclusion
	// key, as opposed to inheriting the default above.
	//
	// The distinction exists because "always" is the right default for a
	// steering document and a fabrication for a skill. KAS's
	// SkillFrontMatterSchema declares no `inclusion` — only
	// SteeringContextFrontMatterSchema does — but it is `.passthrough()`, and
	// `createSteeringCommandSource` reads `config?.inclusion` across skills AND
	// steering alike, so a skill that DOES declare `manual` or `auto` really
	// becomes a slash command. Both facts have to hold at once: forward a
	// declared mode, invent nothing.
	HasInclusion bool
	// SteeringOverride reports whether `steering_override` is present and
	// truthy — a skill that replaces the steering set is worth spotting.
	SteeringOverride bool
	// HasFrontMatter distinguishes "no front-matter at all" (a spec doc, which
	// opens straight on an H1) from "front-matter present but empty". Callers
	// that fall back to the first H1 need the difference.
	HasFrontMatter bool
}

// Parse extracts the front-matter of a `.kiro` markdown document.
//
// Tolerates a leading UTF-8 BOM and CRLF line endings, so an editor-saved
// document does not silently lose its classification. Pure: no I/O.
func Parse(data []byte) FrontMatter {
	fm := FrontMatter{Inclusion: inclusionAlways}
	body, ok := frontmatterBody(data)
	if !ok {
		return fm
	}
	fm.HasFrontMatter = true
	for _, f := range parseFields(body) {
		applyField(&fm, f)
	}
	return fm
}

// field is one parsed top-level key and its value, with any block-scalar or
// block-sequence continuation already folded in.
type field struct {
	key   string
	value string
	list  []string
}

// applyField folds one parsed field into fm. Split out so Parse stays flat.
func applyField(fm *FrontMatter, f field) {
	switch f.key {
	case "name":
		fm.Name = f.value
	case "description":
		fm.Description = f.value
	case "inclusion":
		fm.Inclusion = normalizeInclusion(f.value)
		fm.HasInclusion = true
	case "fileMatchPattern":
		fm.FileMatch = f.value
	case "model":
		fm.Model = f.value
	case "tools":
		fm.Tools = f.list
	case "steering_override":
		fm.SteeringOverride = isTruthy(f.value)
	}
}

// parseFields walks the front-matter body and returns its top-level fields.
//
// The whole reason this is not a per-line Cut: a key whose value is a block
// scalar (`>`/`|`, with optional chomping/keep indicators) or a block sequence
// owns every MORE-INDENTED line that follows it, and those lines carry no colon
// of their own.
func parseFields(body string) []field {
	lines := strings.Split(body, "\n")
	var out []field
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if isSkippableLine(line) || leadingSpaces(line) > 0 {
			// An indented line at top level is a continuation the block
			// handlers below already consumed, or malformed. Either way it is
			// not a key.
			continue
		}
		rawKey, rawVal, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := field{key: strings.TrimSpace(rawKey)}
		val := strings.TrimSpace(rawVal)
		switch {
		case isBlockScalarIndicator(val):
			f.value, i = readBlockScalar(lines, i+1)
		case val == "":
			// Either a block sequence (`- item` lines below) or an empty
			// value. readBlockSequence returns i unchanged when neither.
			f.list, i = readBlockSequence(lines, i+1)
		case strings.HasPrefix(val, "["):
			f.list = parseFlowSequence(val)
			f.value = unquote(val)
		default:
			f.value = unquote(val)
		}
		out = append(out, f)
	}
	return out
}

// isSkippableLine reports whether a front-matter line carries no data: blank,
// or a full-line comment.
func isSkippableLine(line string) bool {
	t := strings.TrimSpace(line)
	return t == "" || strings.HasPrefix(t, "#")
}

// isBlockScalarIndicator reports whether a value is a YAML block-scalar header:
// `>` or `|`, optionally with a chomping (`-`/`+`) or explicit-indent digit.
// The value itself lives on the following indented lines.
func isBlockScalarIndicator(val string) bool {
	if val == "" || (val[0] != '>' && val[0] != '|') {
		return false
	}
	// Only indicator characters may follow; anything else (e.g. `>foo`) is not
	// a block scalar header.
	for _, r := range val[1:] {
		if r != '-' && r != '+' && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// readBlockScalar folds the indented lines starting at `from` into one string,
// returning it and the index of the LAST line consumed (so the caller's loop
// increments past it).
//
// Folding is `>`-style — newlines become spaces — for every indicator. A `|`
// block would strictly preserve them, but every consumer here renders the
// description as one line (a table cell, a steering bullet), and a literal
// newline in either would break the line-oriented output. Blank lines separate
// paragraphs and collapse to a single space for the same reason.
func readBlockScalar(lines []string, from int) (value string, lastIdx int) {
	var parts []string
	i := from
	for ; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			// A blank line inside a block scalar is part of it; a blank line
			// after it is harmless to consume, because a following top-level
			// key is not indented and ends the loop below.
			continue
		}
		if leadingSpaces(line) == 0 {
			break // a new top-level key
		}
		parts = append(parts, strings.TrimSpace(line))
	}
	return strings.Join(parts, " "), i - 1
}

// readBlockSequence reads `- item` lines starting at `from`, returning the items
// and the index of the last line consumed. Returns (nil, from-1) when the next
// content line is not a sequence entry, leaving the caller's cursor untouched.
func readBlockSequence(lines []string, from int) (items []string, lastIdx int) {
	i := from
	for ; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			continue
		}
		t := strings.TrimSpace(line)
		if leadingSpaces(line) == 0 || !strings.HasPrefix(t, "- ") {
			break
		}
		items = append(items, unquote(strings.TrimSpace(strings.TrimPrefix(t, "- "))))
	}
	if len(items) == 0 {
		return nil, from - 1
	}
	return items, i - 1
}

// parseFlowSequence parses a single-line `[a, b, c]` sequence. Values containing
// a comma inside quotes are not supported — no `.kiro` document uses one, and
// guessing would be worse than the simple split.
func parseFlowSequence(val string) []string {
	inner := strings.TrimSuffix(strings.TrimPrefix(val, "["), "]")
	if strings.TrimSpace(inner) == "" {
		return nil
	}
	parts := strings.Split(inner, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := unquote(strings.TrimSpace(p)); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// leadingSpaces counts the indentation of a line, treating a tab as one level.
func leadingSpaces(line string) int {
	n := 0
	for _, r := range line {
		if r != ' ' && r != '\t' {
			break
		}
		n++
	}
	return n
}

// unquote strips one matching pair of surrounding single or double quotes.
// Deliberately not an escape-sequence decoder: `.kiro` front-matter uses quotes
// to protect a leading `*` or a colon, never to encode one.
func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// isTruthy reads a YAML boolean the permissive way YAML 1.1 does, because a
// hand-authored `steering_override: yes` should not read as false.
func isTruthy(v string) bool {
	switch strings.ToLower(v) {
	case "true", "yes", "on", "1":
		return true
	default:
		return false
	}
}

// normalizeText strips a leading UTF-8 BOM and folds every line-ending
// convention to "\n".
//
// A LONE "\r" is normalized too, not just "\r\n". Found by FuzzParse: without
// it, a Mac-classic line ending is ordinary text, so a heading or a description
// could carry an embedded carriage return into output every consumer renders on
// one line. One helper so Parse and FirstHeading cannot disagree about what a
// line is — they previously normalized separately, and FirstHeading then sliced
// its own string using offsets derived from the other one.
func normalizeText(data []byte) string {
	s := strings.TrimPrefix(string(data), "\ufeff")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// FirstHeading returns the text of a document's first markdown H1, or "".
//
// The fallback for a document with no front-matter: a spec doc opens directly on
// `# Requirements — …`, so the heading is its only self-description. A
// front-matter block is skipped so a `# ` comment inside one is never mistaken
// for the title.
func FirstHeading(data []byte) string {
	content := normalizeText(data)
	if _, after, ok := strings.Cut(content, "\n---"); ok && strings.HasPrefix(content, "---\n") {
		content = after
	}
	for line := range strings.SplitSeq(content, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "# "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}
