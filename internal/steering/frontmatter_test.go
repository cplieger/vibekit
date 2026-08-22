package steering

import (
	"slices"
	"strings"
	"testing"
)

// TestParse_FoldedScalarDescription is the regression. The line-oriented parser
// this replaced returned the literal ">" as the description of every document
// using a block scalar — all 47 agents and 14 of 28 skills in this repo — and
// for skills that value reached the AGENT-FACING environment.md as "— >".
func TestParse_FoldedScalarDescription(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "folded with >",
			in:   "---\nname: judgement\ndescription: >\n  Read-only adversarial\n  decision-support framework.\ninclusion: manual\n---\n# Body\n",
			want: "Read-only adversarial decision-support framework.",
		},
		{
			name: "literal with |",
			in:   "---\ndescription: |\n  First line.\n  Second line.\n---\n",
			want: "First line. Second line.",
		},
		{
			name: "folded with a chomping indicator",
			in:   "---\ndescription: >-\n  Chomped folded value.\n---\n",
			want: "Chomped folded value.",
		},
		{
			name: "folded with an explicit indent digit",
			in:   "---\ndescription: >2\n  Indented folded value.\n---\n",
			want: "Indented folded value.",
		},
		{
			// Both ends of the digit range are indicator characters, so neither
			// may fall through to the plain-scalar branch and leave the header
			// itself standing in for the value the author wrote below it.
			name: "folded with a zero indent digit",
			in:   "---\ndescription: >0\n  Zero-indent folded value.\n---\n",
			want: "Zero-indent folded value.",
		},
		{
			name: "literal with a nine indent digit",
			in:   "---\ndescription: |9\n  Nine-indent literal value.\n---\n",
			want: "Nine-indent literal value.",
		},
		{
			name: "blank line inside a block scalar collapses",
			in:   "---\ndescription: >\n  Para one.\n\n  Para two.\n---\n",
			want: "Para one. Para two.",
		},
		{
			// `>foo` is NOT a block-scalar header; it is a plain value.
			name: "greater-than followed by text is a plain scalar",
			in:   "---\ndescription: >foo\n---\n",
			want: ">foo",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Parse([]byte(tc.in)).Description
			if got != tc.want {
				t.Errorf("Description = %q, want %q", got, tc.want)
			}
			if got == ">" || got == "|" {
				t.Error("the block-scalar indicator leaked as the value — this is the shipped bug")
			}
		})
	}
}

// TestParse_BlockScalarDoesNotSwallowFollowingKeys pins the boundary. A folded
// value ends at the next unindented key; getting this wrong would eat the rest
// of the front-matter, which is a worse failure than the bug it replaced.
func TestParse_BlockScalarDoesNotSwallowFollowingKeys(t *testing.T) {
	in := "---\ndescription: >\n  A folded value.\ninclusion: manual\nmodel: claude-opus-5\nname: thing\n---\n"
	fm := Parse([]byte(in))
	if fm.Description != "A folded value." {
		t.Errorf("Description = %q", fm.Description)
	}
	if fm.Inclusion != "manual" {
		t.Errorf("Inclusion = %q, want manual (the key after a block scalar must survive)", fm.Inclusion)
	}
	if fm.Model != "claude-opus-5" {
		t.Errorf("Model = %q, want claude-opus-5", fm.Model)
	}
	if fm.Name != "thing" {
		t.Errorf("Name = %q, want thing", fm.Name)
	}
}

func TestParse_Fields(t *testing.T) {
	in := "---\nname: my-agent\ndescription: \"A quoted description: with a colon\"\n" +
		"inclusion: fileMatch\nfileMatchPattern: \"internal/**/*.go\"\n" +
		"model: claude-opus-5\nsteering_override: true\n---\n"
	fm := Parse([]byte(in))
	if fm.Name != "my-agent" {
		t.Errorf("Name = %q", fm.Name)
	}
	// A quoted value containing a colon must survive: the naive Cut would
	// truncate at the FIRST colon, inside the quotes.
	if fm.Description != "A quoted description: with a colon" {
		t.Errorf("Description = %q, want the full quoted value including its colon", fm.Description)
	}
	if fm.Inclusion != "fileMatch" {
		t.Errorf("Inclusion = %q", fm.Inclusion)
	}
	if fm.FileMatch != "internal/**/*.go" {
		t.Errorf("FileMatch = %q", fm.FileMatch)
	}
	if fm.Model != "claude-opus-5" {
		t.Errorf("Model = %q", fm.Model)
	}
	if !fm.SteeringOverride {
		t.Error("SteeringOverride = false, want true")
	}
	if !fm.HasFrontMatter {
		t.Error("HasFrontMatter = false, want true")
	}
}

func TestParse_Tools(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"flow sequence", "---\ntools: [read, write, execute]\n---\n", []string{"read", "write", "execute"}},
		{"flow with quotes", "---\ntools: [\"read\", 'write']\n---\n", []string{"read", "write"}},
		{"empty flow", "---\ntools: []\n---\n", nil},
		{
			name: "block sequence",
			in:   "---\ntools:\n  - read\n  - write\ndescription: after\n---\n",
			want: []string{"read", "write"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fm := Parse([]byte(tc.in))
			if !slices.Equal(fm.Tools, tc.want) {
				t.Errorf("Tools = %v, want %v", fm.Tools, tc.want)
			}
		})
	}
	// A block sequence must not swallow the key after it either.
	fm := Parse([]byte("---\ntools:\n  - read\ndescription: after\n---\n"))
	if fm.Description != "after" {
		t.Errorf("Description after a block sequence = %q, want %q", fm.Description, "after")
	}

	// And a key with NO sequence under it must leave the cursor where it found
	// it. A key that looks like the head of a sequence and is not is the one
	// case where the reader has to hand lines back unread; consuming them
	// instead drops the rest of the block on the floor, and the document then
	// classifies itself with defaults it never declared.
	t.Run("an empty key consumes none of the keys after it", func(t *testing.T) {
		fm := Parse([]byte("---\ntools:\ninclusion: manual\ndescription: kept\n---\n"))
		if fm.Tools != nil {
			t.Errorf("Tools = %v, want nil", fm.Tools)
		}
		if fm.Inclusion != "manual" {
			t.Errorf("Inclusion = %q, want manual", fm.Inclusion)
		}
		if fm.Description != "kept" {
			t.Errorf("Description = %q, want %q", fm.Description, "kept")
		}
	})
}

// TestParse_StripsOneLayerOfQuotes pins unquote's whole contract, the empty pair
// included.
//
// The quotes are there to protect a leading `*` or a colon, so they are never
// part of the value — and an explicitly empty value is the one case where
// stripping them changes whether the field counts as set at all: the renderers
// treat an empty description as "no description" and omit it, while a literal
// pair of quote characters renders as a description whose entire content is two
// quote marks.
func TestParse_StripsOneLayerOfQuotes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"double quoted", "---\ndescription: \"quoted\"\n---\n", "quoted"},
		{"single quoted", "---\ndescription: 'quoted'\n---\n", "quoted"},
		{"an explicitly empty double-quoted value", "---\ndescription: \"\"\n---\n", ""},
		{"an explicitly empty single-quoted value", "---\ndescription: ''\n---\n", ""},
		{"unquoted, left alone", "---\ndescription: plain\n---\n", "plain"},
		{"one unbalanced quote is not a pair", "---\ndescription: \"unbalanced\n---\n", "\"unbalanced"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Parse([]byte(tc.in)).Description; got != tc.want {
				t.Errorf("Description = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParse_NoFrontMatter(t *testing.T) {
	// A spec doc: opens straight on an H1.
	fm := Parse([]byte("# Requirements — HTTP security\n\nBody.\n"))
	if fm.HasFrontMatter {
		t.Error("HasFrontMatter = true for a doc with none")
	}
	if fm.Inclusion != inclusionAlways {
		t.Errorf("Inclusion = %q, want the always default", fm.Inclusion)
	}
	if fm.Description != "" {
		t.Errorf("Description = %q, want empty", fm.Description)
	}
}

// TestParse_EmptyFencePairCarriesNoFrontMatter covers the pair of fences with
// nothing between them.
//
// HasFrontMatter is what a caller checks before falling back to the document's
// first H1 for a title, so it has to mean "this document classified itself". A
// fence pair enclosing nothing classifies nothing, and reporting it as
// front-matter would suppress that fallback and render the row with no
// description at all rather than with the heading the author wrote.
func TestParse_EmptyFencePairCarriesNoFrontMatter(t *testing.T) {
	for _, in := range []string{
		"---\n\n---\n# Title\n",     // one blank line between the fences
		"---\n---\n# Title\n",       // the closing fence on the next line
		"---\r\n\r\n---\r\n# T\r\n", // the same, as an editor would save it
	} {
		t.Run(strings.ReplaceAll(strings.ReplaceAll(in, "\n", "\\n"), "\r", "\\r"), func(t *testing.T) {
			fm := Parse([]byte(in))
			if fm.HasFrontMatter {
				t.Errorf("HasFrontMatter = true for a fence pair enclosing nothing; the caller's H1 fallback is now suppressed")
			}
			if fm.Inclusion != inclusionAlways {
				t.Errorf("Inclusion = %q, want the always default", fm.Inclusion)
			}
		})
	}
}

func TestParse_BOMAndCRLF(t *testing.T) {
	in := "\ufeff---\r\ninclusion: manual\r\ndescription: >\r\n  Folded over CRLF.\r\n---\r\n"
	fm := Parse([]byte(in))
	if fm.Inclusion != "manual" {
		t.Errorf("Inclusion = %q, want manual (BOM + CRLF must not lose classification)", fm.Inclusion)
	}
	if fm.Description != "Folded over CRLF." {
		t.Errorf("Description = %q", fm.Description)
	}
}

func TestParse_Comments(t *testing.T) {
	fm := Parse([]byte("---\n# a comment\ninclusion: manual\n---\n"))
	if fm.Inclusion != "manual" {
		t.Errorf("Inclusion = %q, want manual", fm.Inclusion)
	}
}

func TestParse_UnknownInclusionFoldsToAlways(t *testing.T) {
	for _, v := range []string{"", "typo", "ALWAYS", "file-match"} {
		fm := Parse([]byte("---\ninclusion: " + v + "\n---\n"))
		if fm.Inclusion != inclusionAlways {
			t.Errorf("inclusion %q → %q, want always", v, fm.Inclusion)
		}
	}
}

// "auto" is KAS's fourth mode and it is ON-DEMAND, not always-loaded:
// emitDocumentsChanged filters `inclusion !== "auto"` out of its notification
// and createSteeringCommandSource collects manual AND auto as slash commands.
// Folding it to "always" asserted the opposite of the truth about token cost.
func TestParse_AutoIsPreservedNotFoldedToAlways(t *testing.T) {
	fm := Parse([]byte("---\ninclusion: auto\n---\n"))
	if fm.Inclusion != inclusionAuto {
		t.Errorf("inclusion auto → %q, want auto", fm.Inclusion)
	}
}

// HasInclusion separates a DECLARED mode from the inherited steering default,
// which is what lets a skill (whose schema has no inclusion key) avoid being
// badged always-loaded while still showing a mode it really did declare.
func TestParse_HasInclusionSeparatesDeclaredFromDefault(t *testing.T) {
	if fm := Parse([]byte("---\nname: thing\n---\n")); fm.HasInclusion {
		t.Error("HasInclusion = true for a document that declared none")
	}
	if fm := Parse([]byte("---\nname: thing\n---\n")); fm.Inclusion != inclusionAlways {
		t.Error("the steering default must still be reported for callers that want it")
	}
	if fm := Parse([]byte("---\ninclusion: manual\n---\n")); !fm.HasInclusion {
		t.Error("HasInclusion = false for a declared mode")
	}
	// A typo folds to always AND still counts as declared: the author said
	// something, so a caller that only forwards declared modes should forward it.
	if fm := Parse([]byte("---\ninclusion: typo\n---\n")); !fm.HasInclusion {
		t.Error("HasInclusion = false for a declared-but-invalid mode")
	}
}

func TestIsTruthy(t *testing.T) {
	for _, v := range []string{"true", "TRUE", "yes", "on", "1"} {
		if !isTruthy(v) {
			t.Errorf("isTruthy(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"false", "no", "off", "0", "", "maybe"} {
		if isTruthy(v) {
			t.Errorf("isTruthy(%q) = true, want false", v)
		}
	}
}

func TestFirstHeading(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain h1", "# Requirements — HTTP security\n\nBody\n", "Requirements — HTTP security"},
		{"h1 after prose", "Intro\n\n# The Title\n", "The Title"},
		{"no h1", "Just body text.\n", ""},
		{"h2 is not h1", "## Subheading\n", ""},
		{
			// A `# ` INSIDE front-matter (a comment) must not be read as the title.
			name: "skips front-matter comments",
			in:   "---\n# not a heading\ninclusion: manual\n---\n\n# Real Title\n",
			want: "Real Title",
		},
		{"front-matter then h1", "---\nname: x\n---\n# After FM\n", "After FM"},
		{"BOM and CRLF", "\ufeff# Windows Title\r\n", "Windows Title"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FirstHeading([]byte(tc.in)); got != tc.want {
				t.Errorf("FirstHeading() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestParse_MalformedDegradesQuietly pins the posture: front-matter that falls
// outside the supported subset yields empty fields, never a panic and never an
// error. A malformed header is the author's own file; the row renders with its
// filename and no description.
func TestParse_MalformedDegradesQuietly(t *testing.T) {
	inputs := []string{
		"---\n",                          // unterminated
		"---\n---\n",                     // empty block
		"---\nno colon here\n---\n",      // no key
		"---\n:\n---\n",                  // empty key and value
		"---\n  indented: orphan\n---\n", // indented at top level
		// An indented line is a continuation, never a key, so an indented line
		// that happens to spell a real key must not classify the document.
		"---\n  inclusion: manual\n---\n",
		"---\ndescription:\n---\n",   // key with nothing after it
		"---\ndescription: >\n---\n", // block scalar with no content
		"",                           // empty document
		"\ufeff",                     // BOM only
		"---",                        // fence only, no newline
	}
	for _, in := range inputs {
		t.Run(strings.ReplaceAll(in, "\n", "\\n"), func(t *testing.T) {
			fm := Parse([]byte(in)) // must not panic
			if fm.Inclusion != inclusionAlways {
				t.Errorf("Inclusion = %q, want the always default", fm.Inclusion)
			}
		})
	}
}

// FuzzParse checks the parser against arbitrary document heads. `.kiro` files
// are workspace content, which is attacker-controlled from vibekit's point of
// view, and the parsed description is rendered into the agent-facing
// environment.md — so the invariants are: never panic, always a valid inclusion,
// and never leak a block-scalar indicator as a value (the bug this replaced).
func FuzzParse(f *testing.F) {
	f.Add("---\ndescription: >\n  folded\n---\n")
	f.Add("---\ndescription: |\n  literal\n---\n")
	f.Add("---\ninclusion: manual\nfileMatchPattern: \"**/*.go\"\n---\n")
	f.Add("---\ntools: [a, b]\n---\n")
	f.Add("---\ntools:\n  - a\n  - b\n---\n")
	f.Add("# Just a heading\n")
	f.Add("---\n")
	f.Add("\ufeff---\r\ninclusion: manual\r\n---\r\n")
	f.Add("---\ndescription: >\n" + strings.Repeat("  x\n", 100) + "---\n")

	f.Fuzz(func(t *testing.T, doc string) {
		fm := Parse([]byte(doc))

		switch fm.Inclusion {
		case inclusionAlways, inclusionFileMatch, inclusionManual:
		default:
			t.Errorf("Inclusion = %q, outside the validated set", fm.Inclusion)
		}
		// A bare indicator as the value is the defect this parser exists to
		// fix; it must never reappear for a well-formed block scalar.
		if fm.Description == ">" || fm.Description == "|" {
			t.Errorf("Description = %q: a block-scalar indicator leaked as the value", fm.Description)
		}
		// No parsed value may carry a newline: every consumer renders these on
		// one line (a table cell, a steering bullet), so an embedded newline
		// would break out of it.
		for name, v := range map[string]string{
			"Name": fm.Name, "Description": fm.Description,
			"Inclusion": fm.Inclusion, "FileMatch": fm.FileMatch, "Model": fm.Model,
		} {
			if strings.ContainsAny(v, "\n\r") {
				t.Errorf("%s = %q contains a newline", name, v)
			}
		}
		for i, tool := range fm.Tools {
			if strings.ContainsAny(tool, "\n\r") {
				t.Errorf("Tools[%d] = %q contains a newline", i, tool)
			}
		}
		// FirstHeading over the same bytes must also be single-line and safe.
		if h := FirstHeading([]byte(doc)); strings.ContainsAny(h, "\n\r") {
			t.Errorf("FirstHeading() = %q contains a newline", h)
		}
	})
}
