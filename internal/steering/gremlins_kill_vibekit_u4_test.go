package steering

// Mutant-killing tests for unit vibekit-u4 (package internal/steering).
//
// Targets the surviving gremlins mutants in discovery.go, steering.go,
// and workspace.go. Each test names the mutant(s) it kills and asserts
// an observable outcome that flips when the operator at that line is
// mutated. Tests-only; no production code is edited.

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/workspace"
)

// ---------------------------------------------------------------------------
// shared helpers (prefixed gk_vibekit_u4_ to avoid sibling-unit collisions)
// ---------------------------------------------------------------------------

func gk_vibekit_u4_writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// gk_vibekit_u4_setupKiroHome mirrors the existing Generate tests: it
// redirects $HOME (the path KiroHome() resolves through when no
// resolver is wired) and pins the cached kiro-home for good measure,
// then returns the resolved environment.md path.
func gk_vibekit_u4_setupKiroHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace.SetKiroHomeForTest(t, filepath.Join(home, ".kiro"))
	return filepath.Join(home, ".kiro", "steering", "environment.md")
}

// ===========================================================================
// discovery.go
// ===========================================================================

// Kills discovery.go:40:17, :46:18, :52:17 (CONDITIONALS_BOUNDARY on the
// three `if len(group) > 0` guards in writeRepoSteering). When a group is
// empty, `> 0` is false so its header is omitted; the `>= 0` mutant would
// emit the header with no entries under it.
func TestGKVibekitU4_WriteRepoSteering_GroupHeaderOmittedWhenEmpty(t *testing.T) {
	t.Run("no-always-docs omits Always-loaded header (kills :40)", func(t *testing.T) {
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
	t.Run("no-matched/no-manual omits those headers (kills :46, :52)", func(t *testing.T) {
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

// Kills discovery.go:324:14 (CONDITIONALS_BOUNDARY on `if len(cmd) > 80`).
// At exactly 80 bytes the original keeps the command verbatim; the `>= 80`
// mutant truncates to 77 chars + "...".
func TestGKVibekitU4_ParseHookJSON_CommandKeptAt80Boundary(t *testing.T) {
	cmd80 := strings.Repeat("a", 80)
	h := parseHookJSON([]byte(`{"event_type":"preToolUse","command":"` + cmd80 + `"}`))
	if h.Command != cmd80 {
		t.Errorf("parseHookJSON(80-byte command).Command = %q (len %d), want the 80-byte command unchanged",
			h.Command, len(h.Command))
	}
	// Sanity: an 81-byte command IS truncated to 80 chars ending in "...".
	cmd81 := strings.Repeat("b", 81)
	h2 := parseHookJSON([]byte(`{"command":"` + cmd81 + `"}`))
	if len(h2.Command) != 80 || !strings.HasSuffix(h2.Command, "...") {
		t.Errorf("parseHookJSON(81-byte command).Command = %q (len %d), want 80 bytes ending in '...'",
			h2.Command, len(h2.Command))
	}
}

// Kills discovery.go:263:9 (CONDITIONALS_NEGATION on `if err != nil` in
// findRepoAgents). On a readable agents dir err is nil, so the original
// proceeds and returns the agent; the `err == nil` mutant returns nil
// (it bails on success and would only proceed on a read error).
func TestGKVibekitU4_FindRepoAgents_ReturnsAgentsOnReadableDir(t *testing.T) {
	repo := t.TempDir()
	gk_vibekit_u4_writeFile(t, filepath.Join(repo, ".kiro", "agents", "deploy.json"), `{"name":"deploy"}`)

	got := findRepoAgents(repo)
	if len(got) != 1 {
		t.Fatalf("findRepoAgents(readable dir) = %+v (len %d), want 1 agent", got, len(got))
	}
	if got[0].Name != "deploy" || got[0].Filename != "deploy.json" {
		t.Errorf("findRepoAgents()[0] = %+v, want {Filename: deploy.json, Name: deploy}", got[0])
	}
	// A missing dir still yields nil (read error path).
	if got := findRepoAgents(filepath.Join(repo, "does-not-exist")); got != nil {
		t.Errorf("findRepoAgents(missing dir) = %+v, want nil", got)
	}
}

// Kills discovery.go:294:9 (CONDITIONALS_NEGATION on `if err != nil` in
// findRepoHooks). Same shape as findRepoAgents.
func TestGKVibekitU4_FindRepoHooks_ReturnsHooksOnReadableDir(t *testing.T) {
	repo := t.TempDir()
	gk_vibekit_u4_writeFile(t, filepath.Join(repo, ".kiro", "hooks", "guard.json"),
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

// Kills discovery.go:334:9 (CONDITIONALS_NEGATION on `if err != nil` in
// findMdDocsInDir). A readable dir with a .md file returns the doc; the
// `err == nil` mutant returns nil on success.
func TestGKVibekitU4_FindMdDocsInDir_ReturnsDocsOnReadableDir(t *testing.T) {
	dir := t.TempDir()
	gk_vibekit_u4_writeFile(t, filepath.Join(dir, "doc.md"), "body\n")

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

// ===========================================================================
// steering.go (Generate / writeForges path)
// ===========================================================================

// Kills steering.go:111:34 and :133:13 (both CONDITIONALS_NEGATION on
// `forgeFn != nil`). With only the forge snapshot wired, the original
// invokes forgeFn and renders the "## Connected forges" section. The :111
// mutant (`forgeFn == nil` as the second OR operand) skips the snapshot
// block so forges stays empty and the section is omitted; the :133 mutant
// (`forgeFn == nil`) skips writeForges outright. Either way the section
// disappears.
func TestGKVibekitU4_Generate_RendersForgeSectionWhenForgeFnWired(t *testing.T) {
	steeringPath := gk_vibekit_u4_setupKiroHome(t)
	workDir := t.TempDir()
	configDir := t.TempDir()

	g := New(workDir, configDir)
	g.SetForgeSnapshot(func() ForgeSnapshot {
		return ForgeSnapshot{Providers: []ForgeProvider{
			{Kind: "github", Host: "github.com", User: "alice"},
		}}
	})
	g.Generate(context.Background())

	out, err := os.ReadFile(steeringPath)
	if err != nil {
		t.Fatalf("steering file not written: %v", err)
	}
	got := string(out)
	for _, want := range []string{"## Connected forges", "github.com", "alice"} {
		if !strings.Contains(got, want) {
			t.Errorf("Generate with forge snapshot omitted %q\n--- output ---\n%s", want, got)
		}
	}
}

// Kills steering.go:157:61 (CONDITIONALS_NEGATION on `readErr == nil` in
// the skip-if-unchanged guard). When the file already holds byte-identical
// content the original skips the rewrite, so an artificially-old mtime
// survives. The `readErr != nil` mutant never takes the skip (the file
// exists, so readErr is nil), rewrites the file, and the mtime jumps to
// ~now.
func TestGKVibekitU4_Generate_SkipsRewriteWhenContentUnchanged(t *testing.T) {
	steeringPath := gk_vibekit_u4_setupKiroHome(t)
	workDir := t.TempDir()
	configDir := t.TempDir()

	g := New(workDir, configDir)
	g.Generate(context.Background())
	if _, err := os.Stat(steeringPath); err != nil {
		t.Fatalf("first Generate did not write file: %v", err)
	}

	// Stamp a far-past mtime; a skipped second write leaves it untouched.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(steeringPath, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	g.Generate(context.Background()) // identical inputs -> must skip the write

	info, err := os.Stat(steeringPath)
	if err != nil {
		t.Fatalf("stat after second Generate: %v", err)
	}
	cutoff := time.Now().Add(-30 * time.Minute)
	if !info.ModTime().Before(cutoff) {
		t.Errorf("second Generate rewrote unchanged file: mtime %v is not before %v (skip-write guard broken)",
			info.ModTime(), cutoff)
	}
}

// Kills steering.go:171:70 (CONDITIONALS_NEGATION on `wErr != nil` after
// atomicfile.WriteFile). On a successful write the original logs the Info
// line "steering: wrote"; the `wErr == nil` mutant treats success as
// failure, logs the Error line and returns before the Info line.
func TestGKVibekitU4_Generate_LogsWroteOnSuccessfulWrite(t *testing.T) {
	steeringPath := gk_vibekit_u4_setupKiroHome(t)
	workDir := t.TempDir()
	configDir := t.TempDir()

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	g := New(workDir, configDir)
	g.Generate(context.Background())

	if _, err := os.Stat(steeringPath); err != nil {
		t.Fatalf("Generate did not write file: %v", err)
	}
	logs := buf.String()
	if !strings.Contains(logs, "steering: wrote") {
		t.Errorf("successful Generate did not log the success line; logs:\n%s", logs)
	}
}

// ===========================================================================
// workspace.go
// ===========================================================================

// Kills workspace.go:27:16, :48:21, :55:15 (CONDITIONALS_BOUNDARY on the
// three section-length guards in writeWorkspace). Each `> 0` becomes
// `>= 0`, which would emit the section header even when its slice is empty.
func TestGKVibekitU4_WriteWorkspace_SectionHeaderOmittedWhenEmpty(t *testing.T) {
	t.Run("no git repos / no notable files (kills :27 and :48)", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "plaindir"), 0o755); err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		writeWorkspace(context.Background(), &b, dir)
		out := b.String()
		if strings.Contains(out, "### Git repositories") {
			t.Errorf("Git repositories header emitted with zero repos:\n%s", out)
		}
		if strings.Contains(out, "### Notable files") {
			t.Errorf("Notable files header emitted with zero notable files:\n%s", out)
		}
	})
	t.Run("no directories, not a root repo (kills :55)", func(t *testing.T) {
		dir := t.TempDir()
		// Only a README (a file, not a dir) -> dirs empty, isRoot false.
		gk_vibekit_u4_writeFile(t, filepath.Join(dir, "README.md"), "A workspace readme line\n")
		var b strings.Builder
		writeWorkspace(context.Background(), &b, dir)
		out := b.String()
		if strings.Contains(out, "### Directories") {
			t.Errorf("Directories header emitted with zero dirs:\n%s", out)
		}
		// Sanity: the notable file IS listed (its guard is true here).
		if !strings.Contains(out, "### Notable files") {
			t.Errorf("expected Notable files section for README workspace:\n%s", out)
		}
	})
}

// Kills workspace.go:70:12, :73:12, :82:10 (CONDITIONALS_NEGATION on the
// branch / origin / desc `!= ""` guards) and :91:17 + :95:17 + :103:16
// (CONDITIONALS_NEGATION on the skills / agents / hooks `len > 0` guards)
// in writeRepoEntry. A fully-populated repo renders every field; each
// negation mutant drops exactly one of them.
func TestGKVibekitU4_WriteRepoEntry_RendersAllFieldsWhenPresent(t *testing.T) {
	work := t.TempDir()
	repo := filepath.Join(work, "myrepo")
	gk_vibekit_u4_writeFile(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/feature-x\n")
	gk_vibekit_u4_writeFile(t, filepath.Join(repo, ".git", "config"),
		"[remote \"origin\"]\n\turl = https://github.com/acme/widget.git\n")
	gk_vibekit_u4_writeFile(t, filepath.Join(repo, "README.md"), "A widget toolkit\n")
	gk_vibekit_u4_writeFile(t, filepath.Join(repo, ".kiro", "skills", "build.md"), "body\n")
	gk_vibekit_u4_writeFile(t, filepath.Join(repo, ".kiro", "agents", "deploy.json"), `{"name":"deploy"}`)
	gk_vibekit_u4_writeFile(t, filepath.Join(repo, ".kiro", "hooks", "guard.json"),
		`{"event_type":"preToolUse","command":"echo hi"}`)

	var b strings.Builder
	writeRepoEntry(&b, work, "myrepo")
	out := b.String()

	checks := map[string]string{
		"feature-x":            "branch (kills :70)",
		"github.com":           "origin host (kills :73)",
		"A widget toolkit":     "README description (kills :82)",
		"Always-loaded skills": "skills section (kills :91 negation)",
		"**Custom agents**":    "agents section (kills :95 negation)",
		"**Hooks**":            "hooks section (kills :103 negation)",
		"deploy":               "agent name",
		"guard.json":           "hook filename",
	}
	for want, why := range checks {
		if !strings.Contains(out, want) {
			t.Errorf("writeRepoEntry omitted %q [%s]\n--- output ---\n%s", want, why, out)
		}
	}
}

// Kills workspace.go:95:17 and :103:16 (CONDITIONALS_BOUNDARY on the
// agents / hooks `len > 0` guards). Unlike the steering/skills blocks,
// these blocks write their header unconditionally on entry, so a `>= 0`
// mutant emits the header even with an empty slice. With zero agents and
// zero hooks the original emits neither header.
func TestGKVibekitU4_WriteRepoEntry_AgentAndHookHeadersAbsentWhenEmpty(t *testing.T) {
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, "bare"), 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	writeRepoEntry(&b, work, "bare")
	out := b.String()
	if strings.Contains(out, "**Custom agents**") {
		t.Errorf("Custom agents header emitted with zero agents:\n%s", out)
	}
	if strings.Contains(out, "**Hooks**") {
		t.Errorf("Hooks header emitted with zero hooks:\n%s", out)
	}
}

// Kills workspace.go:126:74 (ARITHMETIC_BASE on the `64*1024` read cap in
// readGitOrigin). The origin url sits beyond byte 1088, well within the
// 65536-byte cap but beyond any shrunk mutant value (`64/1024`==0,
// `64+1024`==1088, etc.), so a mutated cap reads too few bytes to reach
// the url and returns "".
func TestGKVibekitU4_ReadGitOrigin_ReadsUrlBeyondShrunkCap(t *testing.T) {
	repo := t.TempDir()
	filler := strings.Repeat("a", 2000) // pushes the url past byte 1088
	cfg := "[core]\n\tpadding = " + filler +
		"\n[remote \"origin\"]\n\turl = https://github.com/acme/widget.git\n"
	gk_vibekit_u4_writeFile(t, filepath.Join(repo, ".git", "config"), cfg)

	got := readGitOrigin(repo)
	const want = "https://github.com/acme/widget.git"
	if got != want {
		t.Errorf("readGitOrigin(padded config) = %q, want %q", got, want)
	}
}
