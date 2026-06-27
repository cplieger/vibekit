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
// parseHookJSON
// ---------------------------------------------------------------------------

// TestParseHookJSON_CommandLengthBoundary pins the 80-byte command-preview
// cap: a command of exactly 80 bytes is kept verbatim, while 81 bytes is
// truncated to 80 chars ending in "...".
func TestParseHookJSON_CommandLengthBoundary(t *testing.T) {
	cmd80 := strings.Repeat("a", 80)
	h := parseHookJSON([]byte(`{"event_type":"preToolUse","command":"` + cmd80 + `"}`))
	if h.Command != cmd80 {
		t.Errorf("parseHookJSON(80-byte command).Command = %q (len %d), want the 80-byte command unchanged",
			h.Command, len(h.Command))
	}
	cmd81 := strings.Repeat("b", 81)
	h2 := parseHookJSON([]byte(`{"command":"` + cmd81 + `"}`))
	if len(h2.Command) != 80 || !strings.HasSuffix(h2.Command, "...") {
		t.Errorf("parseHookJSON(81-byte command).Command = %q (len %d), want 80 bytes ending in '...'",
			h2.Command, len(h2.Command))
	}
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

// TestFindRepoHooks_ReadableDir verifies a readable hooks dir yields the
// hook, and a missing dir yields nil.
func TestFindRepoHooks_ReadableDir(t *testing.T) {
	repo := t.TempDir()
	mustWriteFile(t, filepath.Join(repo, ".kiro", "hooks", "guard.json"),
		`{"event_type":"preToolUse","command":"echo hi"}`)

	got := findRepoHooks(repo)
	if len(got) != 1 {
		t.Fatalf("findRepoHooks(readable dir) = %+v (len %d), want 1 hook", got, len(got))
	}
	if got[0].Trigger != "preToolUse" || got[0].Filename != "guard.json" {
		t.Errorf("findRepoHooks()[0] = %+v, want {Filename: guard.json, Trigger: preToolUse}", got[0])
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
	t.Run("findRepoHooks caps at 10", func(t *testing.T) {
		repo := t.TempDir()
		for i := range 12 {
			mustWriteFile(t,
				filepath.Join(repo, ".kiro", "hooks", fmt.Sprintf("hook%02d.json", i)),
				`{"event_type":"preToolUse","command":"x"}`)
		}
		if got := len(findRepoHooks(repo)); got != 10 {
			t.Errorf("findRepoHooks(12 files) returned %d, want exactly 10 (cap)", got)
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
