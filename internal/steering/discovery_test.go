package steering

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// parseSteeringFrontmatter + findRepoDocs
// ---------------------------------------------------------------------------

func TestParseSteeringFrontmatter(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want Doc
	}{
		{
			name: "no frontmatter defaults to always",
			in:   "# Title\n\nBody.\n",
			want: Doc{Inclusion: "always"},
		},
		{
			name: "explicit always",
			in:   "---\ninclusion: always\n---\nbody",
			want: Doc{Inclusion: "always"},
		},
		{
			name: "fileMatch with pattern",
			in:   "---\ninclusion: fileMatch\nfileMatchPattern: \"internal/**/*.go\"\ndescription: Go layout\n---\n",
			want: Doc{Inclusion: "fileMatch", FileMatch: "internal/**/*.go", Description: "Go layout"},
		},
		{
			name: "manual",
			in:   "---\ninclusion: manual\ndescription: Incident runbook\n---\n",
			want: Doc{Inclusion: "manual", Description: "Incident runbook"},
		},
		{
			name: "unknown inclusion falls back to always",
			in:   "---\ninclusion: bogus\n---\n",
			want: Doc{Inclusion: "always"},
		},
		{
			name: "single-quoted values",
			in:   "---\ninclusion: 'fileMatch'\nfileMatchPattern: 'cmd/*.go'\n---\n",
			want: Doc{Inclusion: "fileMatch", FileMatch: "cmd/*.go"},
		},
		{
			name: "missing closing fence falls back to always",
			in:   "---\ninclusion: fileMatch\nbody without closing\n",
			want: Doc{Inclusion: "always"},
		},
		{
			name: "empty file",
			in:   "",
			want: Doc{Inclusion: "always"},
		},
		{
			name: "CRLF line endings",
			in:   "---\r\ninclusion: manual\r\ndescription: runbook\r\n---\r\nbody",
			want: Doc{Inclusion: "manual", Description: "runbook"},
		},
		{
			name: "leading UTF-8 BOM",
			in:   "\ufeff---\ninclusion: fileMatch\nfileMatchPattern: \"**/*.go\"\n---\n",
			want: Doc{Inclusion: "fileMatch", FileMatch: "**/*.go"},
		},
		{
			name: "BOM and CRLF together",
			in:   "\ufeff---\r\ninclusion: manual\r\n---\r\n",
			want: Doc{Inclusion: "manual"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseSteeringFrontmatter([]byte(tc.in))
			if got != tc.want {
				t.Errorf("parseSteeringFrontmatter() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestParseInclusion covers the exported wrapper shared with the REST
// kiro-config scanner: valid values pass through, unknown/absent fold to
// "always", and a BOM+CRLF-authored file is tolerated.
func TestParseInclusion(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"absent defaults to always", "# no frontmatter", "always"},
		{"manual", "---\ninclusion: manual\n---\n", "manual"},
		{"fileMatch", "---\ninclusion: fileMatch\n---\n", "fileMatch"},
		{"unknown folds to always", "---\ninclusion: bogus\n---\n", "always"},
		{"CRLF+BOM tolerated", "\ufeff---\r\ninclusion: manual\r\n---\r\n", "manual"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseInclusion([]byte(tc.in)); got != tc.want {
				t.Errorf("ParseInclusion(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFindRepoDocs_ClassifiesByFrontmatter(t *testing.T) {
	dir := t.TempDir()
	steering := filepath.Join(dir, ".kiro", "steering")
	if err := os.MkdirAll(steering, 0o755); err != nil {
		t.Fatal(err)
	}
	// Three files, three trigger types.
	files := map[string]string{
		"architecture.md": "---\ninclusion: always\ndescription: Arch overview\n---\nbody",
		"go-layout.md":    "---\ninclusion: fileMatch\nfileMatchPattern: \"internal/**/*.go\"\n---\nbody",
		"runbook.md":      "---\ninclusion: manual\n---\nbody",
		"plain.md":        "no frontmatter\n", // defaults to always
	}
	for n, c := range files {
		if err := os.WriteFile(filepath.Join(steering, n), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	docs := findRepoDocs(dir)
	if len(docs) != 4 {
		t.Fatalf("got %d docs, want 4", len(docs))
	}
	byName := map[string]Doc{}
	for _, d := range docs {
		byName[d.Filename] = d
	}
	if d := byName["architecture.md"]; d.Inclusion != "always" || d.Description != "Arch overview" {
		t.Errorf("architecture.md = %+v", d)
	}
	if d := byName["go-layout.md"]; d.Inclusion != "fileMatch" || d.FileMatch != "internal/**/*.go" {
		t.Errorf("go-layout.md = %+v", d)
	}
	if d := byName["runbook.md"]; d.Inclusion != "manual" {
		t.Errorf("runbook.md = %+v", d)
	}
	if d := byName["plain.md"]; d.Inclusion != "always" {
		t.Errorf("plain.md = %+v (no frontmatter should default to always)", d)
	}
}

func TestFindRepoDocs_NoSteeringDir(t *testing.T) {
	dir := t.TempDir()
	docs := findRepoDocs(dir)
	if docs != nil {
		t.Errorf("expected nil, got %+v", docs)
	}
}

func TestFindRepoDocs_CapAt20(t *testing.T) {
	dir := t.TempDir()
	steering := filepath.Join(dir, ".kiro", "steering")
	if err := os.MkdirAll(steering, 0o755); err != nil {
		t.Fatal(err)
	}
	// 25 files; expect 20 returned.
	for i := range 25 {
		name := fmt.Sprintf("doc-%02d.md", i)
		if err := os.WriteFile(filepath.Join(steering, name), []byte("body"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	docs := findRepoDocs(dir)
	if len(docs) != 20 {
		t.Errorf("got %d docs, want 20 (capped)", len(docs))
	}
}

// ---------------------------------------------------------------------------
// writeRepoSteering / writeRepoSkills group headers + entry annotations
// ---------------------------------------------------------------------------

// TestWriteRepoSteering_OmitsEmptyGroupHeaders verifies each inclusion
// group's header is emitted only when that group holds at least one doc.
func TestWriteRepoSteering_OmitsEmptyGroupHeaders(t *testing.T) {
	t.Run("only fileMatch docs omit the Always-loaded header", func(t *testing.T) {
		var b strings.Builder
		writeRepoSteering(&b, "myrepo", []Doc{{Filename: "match.md", Inclusion: "fileMatch"}})
		out := b.String()
		if strings.Contains(out, "Always-loaded steering") {
			t.Errorf("writeRepoSteering(only fileMatch) emitted Always-loaded header:\n%s", out)
		}
		if !strings.Contains(out, "File-match steering") {
			t.Errorf("writeRepoSteering(only fileMatch) missing File-match header:\n%s", out)
		}
	})
	t.Run("only always docs omit the File-match and Manual headers", func(t *testing.T) {
		var b strings.Builder
		writeRepoSteering(&b, "myrepo", []Doc{{Filename: "always.md", Inclusion: "always"}})
		out := b.String()
		if strings.Contains(out, "File-match steering") {
			t.Errorf("writeRepoSteering(only always) emitted File-match header:\n%s", out)
		}
		if strings.Contains(out, "Manual steering") {
			t.Errorf("writeRepoSteering(only always) emitted Manual header:\n%s", out)
		}
		if !strings.Contains(out, "Always-loaded steering") {
			t.Errorf("writeRepoSteering(only always) missing Always-loaded header:\n%s", out)
		}
	})
}

// TestWriteRepoSkills_HeadersAndEntryFields verifies the skills inventory
// emits a group header only for non-empty groups and renders the
// FileMatch / Description annotations only when those fields are set.
func TestWriteRepoSkills_HeadersAndEntryFields(t *testing.T) {
	t.Run("fileMatch+manual: no Always header, others present, fields rendered", func(t *testing.T) {
		var b strings.Builder
		writeRepoSkills(&b, "myrepo", []Doc{
			{Filename: "match.md", Inclusion: "fileMatch", FileMatch: "internal/**", Description: "go layout"},
			{Filename: "ref.md", Inclusion: "manual"},
		})
		out := b.String()
		if strings.Contains(out, "Always-loaded skills") {
			t.Errorf("emitted Always-loaded skills header with zero always docs:\n%s", out)
		}
		if !strings.Contains(out, "File-match skills") {
			t.Errorf("missing File-match skills header for a fileMatch doc:\n%s", out)
		}
		if !strings.Contains(out, "Manual skills") {
			t.Errorf("missing Manual skills header for a manual doc:\n%s", out)
		}
		if !strings.Contains(out, "(matches `internal/**`)") {
			t.Errorf("missing FileMatch annotation:\n%s", out)
		}
		if !strings.Contains(out, "— go layout") {
			t.Errorf("missing Description annotation:\n%s", out)
		}
	})

	t.Run("only always: no File-match/Manual headers; bare entry has no annotations", func(t *testing.T) {
		var b strings.Builder
		writeRepoSkills(&b, "myrepo", []Doc{{Filename: "build.md", Inclusion: "always"}})
		out := b.String()
		if strings.Contains(out, "File-match skills") {
			t.Errorf("emitted File-match skills header with zero fileMatch docs:\n%s", out)
		}
		if strings.Contains(out, "Manual skills") {
			t.Errorf("emitted Manual skills header with zero manual docs:\n%s", out)
		}
		if !strings.Contains(out, "Always-loaded skills") {
			t.Errorf("missing Always-loaded skills header:\n%s", out)
		}
		if strings.Contains(out, "(matches") {
			t.Errorf("rendered FileMatch annotation for an empty FileMatch:\n%s", out)
		}
		if strings.Contains(out, "build.md` —") {
			t.Errorf("rendered Description annotation for an empty Description:\n%s", out)
		}
	})
}

// ---------------------------------------------------------------------------
// parseHookDoc
// ---------------------------------------------------------------------------

// hookDoc wraps hook-entry JSON objects in the v1 envelope.
func hookDoc(hooks ...string) string {
	return `{"version":"v1","hooks":[` + strings.Join(hooks, ",") + `]}`
}

// TestParseHookDoc_CommandLengthBoundary pins the 80-byte action-preview
// cap: a command of exactly 80 bytes is kept verbatim, while 81 bytes is
// truncated to 80 chars ending in "...".
func TestParseHookDoc_CommandLengthBoundary(t *testing.T) {
	cmd80 := strings.Repeat("a", 80)
	hs := parseHookDoc([]byte(hookDoc(
		`{"name":"n","trigger":"PreToolUse","action":{"type":"command","command":"` + cmd80 + `"}}`)))
	if len(hs) != 1 || hs[0].Command != cmd80 {
		t.Errorf("parseHookDoc(80-byte command) = %+v, want one entry with the 80-byte command unchanged", hs)
	}
	cmd81 := strings.Repeat("b", 81)
	hs2 := parseHookDoc([]byte(hookDoc(
		`{"action":{"type":"command","command":"` + cmd81 + `"}}`)))
	if len(hs2) != 1 || len(hs2[0].Command) != 80 || !strings.HasSuffix(hs2[0].Command, "...") {
		t.Errorf("parseHookDoc(81-byte command) = %+v, want 80 bytes ending in '...'", hs2)
	}
}

// TestParseHookDoc_Shapes covers the v1 envelope variants: multiple hooks
// per file, agent hooks previewing their prompt, malformed JSON, and the
// legacy pre-v1 shape (no hooks array) yielding nothing.
func TestParseHookDoc_Shapes(t *testing.T) {
	t.Run("multiple hooks in one document", func(t *testing.T) {
		hs := parseHookDoc([]byte(hookDoc(
			`{"name":"A","trigger":"PostFileSave","action":{"type":"command","command":"lint"}}`,
			`{"name":"B","trigger":"SessionStart","action":{"type":"agent","prompt":"load context"}}`)))
		if len(hs) != 2 {
			t.Fatalf("parseHookDoc(2 hooks) = %+v, want 2 entries", hs)
		}
		if hs[0].Name != "A" || hs[0].Trigger != "PostFileSave" || hs[0].Command != "lint" {
			t.Errorf("hs[0] = %+v, want {A PostFileSave lint}", hs[0])
		}
		// Agent hooks preview their prompt in the Command field.
		if hs[1].Name != "B" || hs[1].Trigger != "SessionStart" || hs[1].Command != "load context" {
			t.Errorf("hs[1] = %+v, want {B SessionStart load context}", hs[1])
		}
	})
	t.Run("malformed and legacy shapes yield nil", func(t *testing.T) {
		for _, data := range []string{`not json`, ``, `{"event_type":"preToolUse","command":"x"}`} {
			if hs := parseHookDoc([]byte(data)); len(hs) != 0 {
				t.Errorf("parseHookDoc(%q) = %+v, want empty", data, hs)
			}
		}
	})
	t.Run("hostile fields are sanitised", func(t *testing.T) {
		hs := parseHookDoc([]byte(hookDoc(
			`{"name":"evil\ninject","trigger":"PreToolUse","action":{"type":"command","command":"run \u0060rm\u0060\nline2"}}`)))
		if len(hs) != 1 {
			t.Fatalf("parseHookDoc = %+v, want 1 entry", hs)
		}
		if strings.ContainsAny(hs[0].Name+hs[0].Command, "\n\r\t`") {
			t.Errorf("sanitisation left newline/backtick in fields: %+v", hs[0])
		}
	})
}

// ---------------------------------------------------------------------------
// findRepoAgents / findRepoHooks / findMdDocsInDir
// ---------------------------------------------------------------------------

// TestFindRepoAgents_ReadableDir verifies a readable agents dir yields the
// agent, and a missing dir yields nil.
func TestFindRepoAgents_ReadableDir(t *testing.T) {
	repo := t.TempDir()
	mustWriteFile(t, filepath.Join(repo, ".kiro", "agents", "deploy.json"), `{"name":"deploy"}`)

	got := findRepoAgents(repo)
	if len(got) != 1 {
		t.Fatalf("findRepoAgents(readable dir) = %+v (len %d), want 1 agent", got, len(got))
	}
	if got[0].Name != "deploy" || got[0].Filename != "deploy.json" {
		t.Errorf("findRepoAgents()[0] = %+v, want {Filename: deploy.json, Name: deploy}", got[0])
	}
	if got := findRepoAgents(filepath.Join(repo, "does-not-exist")); got != nil {
		t.Errorf("findRepoAgents(missing dir) = %+v, want nil", got)
	}
}

// TestFindRepoHooks_ReadableDir verifies a readable hooks dir yields every
// hook in a multi-hook v1 document (stamped with the filename), and a
// missing dir yields nil.
func TestFindRepoHooks_ReadableDir(t *testing.T) {
	repo := t.TempDir()
	mustWriteFile(t, filepath.Join(repo, ".kiro", "hooks", "guard.json"), hookDoc(
		`{"name":"Guard","trigger":"PreToolUse","action":{"type":"command","command":"echo hi"}}`,
		`{"name":"Regen","trigger":"PostFileSave","action":{"type":"command","command":"make gen"}}`))

	got := findRepoHooks(repo)
	if len(got) != 2 {
		t.Fatalf("findRepoHooks(readable dir) = %+v (len %d), want 2 hooks from one document", got, len(got))
	}
	if got[0].Trigger != "PreToolUse" || got[0].Filename != "guard.json" || got[0].Name != "Guard" {
		t.Errorf("findRepoHooks()[0] = %+v, want {Filename: guard.json, Name: Guard, Trigger: PreToolUse}", got[0])
	}
	if got[1].Trigger != "PostFileSave" || got[1].Filename != "guard.json" {
		t.Errorf("findRepoHooks()[1] = %+v, want the second hook from the same file", got[1])
	}
	if got := findRepoHooks(filepath.Join(repo, "does-not-exist")); got != nil {
		t.Errorf("findRepoHooks(missing dir) = %+v, want nil", got)
	}
}

// TestFindMdDocsInDir_ReadableDir verifies a readable dir with a .md file
// yields the doc, and a missing dir yields nil.
func TestFindMdDocsInDir_ReadableDir(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "doc.md"), "body\n")

	got := findMdDocsInDir(dir)
	if len(got) != 1 {
		t.Fatalf("findMdDocsInDir(readable dir) = %+v (len %d), want 1 doc", got, len(got))
	}
	if got[0].Filename != "doc.md" || got[0].Inclusion != "always" {
		t.Errorf("findMdDocsInDir()[0] = %+v, want {Filename: doc.md, Inclusion: always}", got[0])
	}
	if got := findMdDocsInDir(filepath.Join(dir, "does-not-exist")); got != nil {
		t.Errorf("findMdDocsInDir(missing dir) = %+v, want nil", got)
	}
}

// TestDiscoveryEntryCaps verifies the per-directory entry caps:
// findRepoAgents and findRepoHooks cap at 10, findMdDocsInDir at 20.
// Feeding more files than the cap and asserting the EXACT capped count
// discriminates a > cap boundary (would return cap+1) from a < cap
// negation (would break after the first append and return 1).
func TestDiscoveryEntryCaps(t *testing.T) {
	t.Run("findRepoAgents caps at 10", func(t *testing.T) {
		repo := t.TempDir()
		for i := range 12 {
			mustWriteFile(t,
				filepath.Join(repo, ".kiro", "agents", fmt.Sprintf("agent%02d.json", i)),
				fmt.Sprintf(`{"name":"a%02d"}`, i))
		}
		if got := len(findRepoAgents(repo)); got != 10 {
			t.Errorf("findRepoAgents(12 files) returned %d, want exactly 10 (cap)", got)
		}
	})
	t.Run("findRepoHooks caps at 10 entries", func(t *testing.T) {
		repo := t.TempDir()
		// 6 files x 2 hooks = 12 hook entries; the cap counts ENTRIES,
		// not files.
		for i := range 6 {
			mustWriteFile(t,
				filepath.Join(repo, ".kiro", "hooks", fmt.Sprintf("hook%02d.json", i)),
				hookDoc(
					`{"name":"a","trigger":"PreToolUse","action":{"type":"command","command":"x"}}`,
					`{"name":"b","trigger":"PostToolUse","action":{"type":"command","command":"y"}}`))
		}
		if got := len(findRepoHooks(repo)); got != 10 {
			t.Errorf("findRepoHooks(12 hooks in 6 files) returned %d, want exactly 10 (cap)", got)
		}
	})
	t.Run("findMdDocsInDir caps at 20", func(t *testing.T) {
		dir := t.TempDir()
		for i := range 25 {
			mustWriteFile(t, filepath.Join(dir, fmt.Sprintf("doc%02d.md", i)), "body\n")
		}
		if got := len(findMdDocsInDir(dir)); got != 20 {
			t.Errorf("findMdDocsInDir(25 files) returned %d, want exactly 20 (cap)", got)
		}
	})
}

// ---------------------------------------------------------------------------
// findRepoSkills (skills are directories containing SKILL.md)
// ---------------------------------------------------------------------------

// TestFindRepoSkills_ScansSubdirsWithSkillMd verifies skills are scanned
// as subdirectories pointing at SKILL.md (not flat .md files), that the
// SKILL.md front-matter classifies the skill, that a subdir without a
// SKILL.md still counts (default "always", matching the REST scan), and
// that a stray flat .md file directly under skills/ is ignored.
func TestFindRepoSkills_ScansSubdirsWithSkillMd(t *testing.T) {
	repo := t.TempDir()
	mustWriteFile(t, filepath.Join(repo, ".kiro", "skills", "alpha", "SKILL.md"),
		"---\ninclusion: manual\ndescription: Alpha skill\n---\n")
	mustWriteFile(t, filepath.Join(repo, ".kiro", "skills", "beta", "SKILL.md"), "# beta\n")
	mustWriteFile(t, filepath.Join(repo, ".kiro", "skills", "gamma", "notes.txt"), "no skill.md here\n")
	mustWriteFile(t, filepath.Join(repo, ".kiro", "skills", "loose.md"), "# not a skill\n")

	skills := findRepoSkills(repo)
	// alpha, beta, gamma are directories -> 3 skills. loose.md is a flat
	// file -> ignored.
	if len(skills) != 3 {
		t.Fatalf("findRepoSkills = %+v (len %d), want 3 skill dirs (flat .md ignored)", skills, len(skills))
	}
	byFile := map[string]Doc{}
	for _, d := range skills {
		byFile[d.Filename] = d
	}
	alpha, ok := byFile["alpha/SKILL.md"]
	if !ok {
		t.Fatalf("missing alpha/SKILL.md in %+v", skills)
	}
	if alpha.Inclusion != "manual" || alpha.Description != "Alpha skill" {
		t.Errorf("alpha = %+v, want {Inclusion: manual, Description: Alpha skill}", alpha)
	}
	if beta, ok := byFile["beta/SKILL.md"]; !ok || beta.Inclusion != "always" {
		t.Errorf("beta = %+v (ok=%v), want inclusion always", beta, ok)
	}
	if gamma, ok := byFile["gamma/SKILL.md"]; !ok || gamma.Inclusion != "always" {
		t.Errorf("gamma (no SKILL.md) = %+v (ok=%v), want listed with inclusion always", gamma, ok)
	}
	if _, ok := byFile["loose.md"]; ok {
		t.Error("flat skills/loose.md should not be treated as a skill")
	}
}

func TestFindRepoSkills_NoSkillsDir(t *testing.T) {
	if got := findRepoSkills(t.TempDir()); got != nil {
		t.Errorf("findRepoSkills(no skills dir) = %+v, want nil", got)
	}
}

// TestFindRepoSkills_CapAt20 feeds 25 skill directories and asserts the
// exact 20-entry cap.
func TestFindRepoSkills_CapAt20(t *testing.T) {
	repo := t.TempDir()
	for i := range 25 {
		mustWriteFile(t,
			filepath.Join(repo, ".kiro", "skills", fmt.Sprintf("skill%02d", i), "SKILL.md"),
			"body\n")
	}
	if got := len(findRepoSkills(repo)); got != 20 {
		t.Errorf("findRepoSkills(25 skill dirs) = %d, want 20 (capped)", got)
	}
}

// ---------------------------------------------------------------------------
// findRepoAgents de-dupes paired .json + .md
// ---------------------------------------------------------------------------

// TestFindRepoAgents_DedupsPairedFiles verifies a paired reviewer.json +
// reviewer.md collapses to ONE agent preferring the .md, while a
// .json-only and a .md-only agent are each listed once.
func TestFindRepoAgents_DedupsPairedFiles(t *testing.T) {
	repo := t.TempDir()
	agentsDir := filepath.Join(repo, ".kiro", "agents")
	mustWriteFile(t, filepath.Join(agentsDir, "reviewer.json"), `{"name":"reviewer"}`)
	mustWriteFile(t, filepath.Join(agentsDir, "reviewer.md"), "# reviewer\n")
	mustWriteFile(t, filepath.Join(agentsDir, "deploy.json"), `{"name":"deploy"}`)
	mustWriteFile(t, filepath.Join(agentsDir, "notes.md"), "# notes\n")

	agents := findRepoAgents(repo)
	if len(agents) != 3 {
		t.Fatalf("findRepoAgents = %+v (len %d), want 3 (reviewer paired -> 1)", agents, len(agents))
	}
	byName := map[string]AgentEntry{}
	for _, a := range agents {
		byName[a.Name] = a
	}
	if got := byName["reviewer"].Filename; got != "reviewer.md" {
		t.Errorf("reviewer filename = %q, want reviewer.md (prefer .md over .json)", got)
	}
	if got := byName["deploy"].Filename; got != "deploy.json" {
		t.Errorf("deploy filename = %q, want deploy.json (.json-only)", got)
	}
	if got := byName["notes"].Filename; got != "notes.md" {
		t.Errorf("notes filename = %q, want notes.md", got)
	}
}
