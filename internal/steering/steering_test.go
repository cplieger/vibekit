package steering

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/workspace"
)

// ---------------------------------------------------------------------------
// writeTools
// ---------------------------------------------------------------------------

func TestWriteTools(t *testing.T) {
	data := []byte(`{
		"tools": {
			"goimports": {"installed_version": "v0.30.0", "bins": ["goimports"]},
			"go": {"installed_version": "1.26.2", "bins": ["go", "gofmt"]},
			"pyright": {"installed_version": "1.1.411", "pm_bins": ["pyright", "pyright-langserver"]}
		}
	}`)
	var b strings.Builder
	writeTools(&b, data)
	out := b.String()

	if !strings.Contains(out, "## Installed tools") {
		t.Error("missing header")
	}
	if !strings.Contains(out, "- goimports v0.30.0") {
		t.Error("missing goimports")
	}
	// Bins expand to individual entries sharing the tool's version.
	if !strings.Contains(out, "- go 1.26.2") {
		t.Error("missing go binary")
	}
	if !strings.Contains(out, "- gofmt 1.26.2") {
		t.Error("missing gofmt binary")
	}
	// pm_bins expand the same way.
	if !strings.Contains(out, "- pyright-langserver 1.1.411") {
		t.Error("missing pyright-langserver pm bin")
	}
}

func TestWriteTools_EmptyEmitsNothing(t *testing.T) {
	// No installed tools = no section at all (an empty header would
	// just waste agent context).
	var b strings.Builder
	writeTools(&b, []byte(`{}`))
	if b.Len() != 0 {
		t.Errorf("expected no output for empty state, got:\n%s", b.String())
	}
}

func TestWriteTools_InvalidJSON(t *testing.T) {
	var b strings.Builder
	writeTools(&b, []byte(`not json`))
	if b.Len() != 0 {
		t.Error("expected no output for invalid JSON")
	}
}

func TestWriteTools_Sorted(t *testing.T) {
	data := []byte(`{"tools": {
		"zebra": {"installed_version": "1"},
		"alpha": {"installed_version": "2"}
	}}`)
	var b strings.Builder
	writeTools(&b, data)
	out := b.String()
	alphaIdx := strings.Index(out, "alpha")
	zebraIdx := strings.Index(out, "zebra")
	if alphaIdx < 0 || zebraIdx < 0 {
		t.Fatal("missing entries")
	}
	if alphaIdx > zebraIdx {
		t.Error("tools should be sorted alphabetically")
	}
}

func TestWriteTools_NotInstalledSkipped(t *testing.T) {
	// A state entry without installed_version (failed install: only
	// last_error recorded) must not be listed as installed.
	data := []byte(`{"tools": {
		"ok": {"installed_version": "1.0.0"},
		"broken": {"last_error": "download failed"}
	}}`)
	var b strings.Builder
	writeTools(&b, data)
	out := b.String()
	if !strings.Contains(out, "- ok 1.0.0") {
		t.Error("missing installed tool")
	}
	if strings.Contains(out, "broken") {
		t.Errorf("uninstalled tool listed; output:\n%s", out)
	}
}

func TestWriteTools_NoBinsUsesToolName(t *testing.T) {
	// bins absent (older state / manual entry) falls back to the
	// tool name.
	data := []byte(`{"tools": {"jq": {"installed_version": "1.8.1"}}}`)
	var b strings.Builder
	writeTools(&b, data)
	if !strings.Contains(b.String(), "- jq 1.8.1") {
		t.Errorf("missing name fallback; output:\n%s", b.String())
	}
}

func TestWriteTools_DuplicateBinsDeduped(t *testing.T) {
	// Two tools shipping the same bin name list it once — the agent
	// cares about what's on PATH, not ownership.
	data := []byte(`{"tools": {
		"typescript": {"installed_version": "5.9.0", "pm_bins": ["tsc", "tsserver"]},
		"tsc-wrapper": {"installed_version": "1.0.0", "bins": ["tsc"]}
	}}`)
	var b strings.Builder
	writeTools(&b, data)
	out := b.String()
	if strings.Count(out, "- tsc ") != 1 {
		t.Errorf("duplicate bin not deduped; output:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// writeMCP
// ---------------------------------------------------------------------------

func TestWriteMCP_EmptyServersEmitsNothing(t *testing.T) {
	var b strings.Builder
	writeMCP(&b, MCPSnapshot{})
	if b.Len() != 0 {
		t.Errorf("writeMCP(empty) wrote %d bytes, want 0", b.Len())
	}
}

func TestWriteMCP_SortsServersAlphabetically(t *testing.T) {
	var b strings.Builder
	writeMCP(&b, MCPSnapshot{
		Servers: []api.MCPSnapshotServer{
			{Name: "zed"},
			{Name: "alpha"},
			{Name: "linear"},
		},
	})
	out := b.String()
	if !strings.Contains(out, "## Connected integrations") {
		t.Error("missing header")
	}
	alphaIdx := strings.Index(out, "**alpha**")
	linearIdx := strings.Index(out, "**linear**")
	zedIdx := strings.Index(out, "**zed**")
	if alphaIdx < 0 || linearIdx < 0 || zedIdx < 0 {
		t.Fatalf("missing bullets: alpha=%d linear=%d zed=%d",
			alphaIdx, linearIdx, zedIdx)
	}
	if alphaIdx >= linearIdx || linearIdx >= zedIdx {
		t.Errorf("order alpha=%d linear=%d zed=%d, want alpha < linear < zed",
			alphaIdx, linearIdx, zedIdx)
	}
}

func TestWriteMCP_InputSnapshotNotMutated(t *testing.T) {
	servers := []api.MCPSnapshotServer{{Name: "zed"}, {Name: "alpha"}}
	snap := MCPSnapshot{Servers: servers}

	var b strings.Builder
	writeMCP(&b, snap)

	if servers[0].Name != "zed" || servers[1].Name != "alpha" {
		t.Errorf("input slice was re-sorted: %v", servers)
	}
}

// ---------------------------------------------------------------------------
// writeForges
// ---------------------------------------------------------------------------

// TestWriteForges_PerProviderFields verifies a populated provider renders
// its email, CLI line, and accessible-repositories block, while a bare
// provider (no email, unknown kind, no repos) omits all three.
func TestWriteForges_PerProviderFields(t *testing.T) {
	t.Run("populated provider renders email, CLI and repo list", func(t *testing.T) {
		var b strings.Builder
		writeForges(&b, ForgeSnapshot{Providers: []ForgeProvider{{
			Kind:  "github",
			Host:  "github.com",
			User:  "alice",
			Email: "alice@example.com",
			Repos: []string{"acme/widget"},
		}}})
		out := b.String()
		if !strings.Contains(out, "alice@example.com") {
			t.Errorf("missing authenticated email:\n%s", out)
		}
		if !strings.Contains(out, "CLI: `gh`") {
			t.Errorf("missing CLI line for github:\n%s", out)
		}
		if !strings.Contains(out, "Accessible repositories") || !strings.Contains(out, "acme/widget") {
			t.Errorf("missing accessible-repositories block:\n%s", out)
		}
	})

	t.Run("bare provider omits email, CLI and repo list", func(t *testing.T) {
		var b strings.Builder
		writeForges(&b, ForgeSnapshot{Providers: []ForgeProvider{{
			Kind: "mysteryforge", // forgeCLI() returns "" -> no CLI line
			Host: "git.example.com",
			User: "bob",
			// Email empty, Repos nil.
		}}})
		out := b.String()
		if strings.Contains(out, "bob <") {
			t.Errorf("rendered an empty <email> for a provider without an email:\n%s", out)
		}
		if strings.Contains(out, "- CLI:") {
			t.Errorf("rendered a CLI line for an unknown forge kind:\n%s", out)
		}
		if strings.Contains(out, "Accessible repositories") {
			t.Errorf("rendered accessible-repositories header with zero repos:\n%s", out)
		}
	})
}

// ---------------------------------------------------------------------------
// Generate
// ---------------------------------------------------------------------------

// TestGenerate_WritesCompleteSteeringFile drives the full Generate
// flow: reads tools-state.json, inspects workDir, uses the wired MCP
// snapshot, and writes ~/.kiro/steering/environment.md.
func TestGenerate_WritesCompleteSteeringFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace.SetKiroHomeForTest(t, filepath.Join(home, ".kiro"))
	workDir := t.TempDir()
	configDir := t.TempDir()

	tools := `{"tools":{"goimports":{"installed_version":"v0.30.0","bins":["goimports"]}}}`
	if err := os.WriteFile(filepath.Join(configDir, "tools-state.json"),
		[]byte(tools), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workDir, "myrepo", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	g := New(workDir, configDir)
	g.SetMCPSnapshot(func() MCPSnapshot {
		return MCPSnapshot{Servers: []api.MCPSnapshotServer{{Name: "github"}}}
	})

	g.Generate(context.Background())

	steeringPath := filepath.Join(home, ".kiro", "steering", "environment.md")
	out, err := os.ReadFile(steeringPath)
	if err != nil {
		t.Fatalf("steering file not written: %v", err)
	}
	got := string(out)
	wants := []string{
		"# Environment",
		"## Installed tools",
		"- goimports v0.30.0",
		"## Connected integrations",
		"**github**",
		"## Workspace",
		"myrepo",
		"## Limitations",
		"## Capabilities",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("steering file missing %q", w)
		}
	}

	// File mode must be 0o600 (narrow-by-default).
	info, statErr := os.Stat(steeringPath)
	if statErr != nil {
		t.Fatalf("stat: %v", statErr)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("steering file mode = %o, want 0o600", mode)
	}
}

// TestGenerate_NoMCPSnapshotOmitsSection pins the "Connected
// integrations" omission behaviour when the snapshot callback is
// absent (the generator has no reason to emit an empty section).
func TestGenerate_NoMCPSnapshotOmitsSection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace.SetKiroHomeForTest(t, filepath.Join(home, ".kiro"))
	workDir := t.TempDir()
	configDir := t.TempDir()

	g := New(workDir, configDir)
	// Deliberately do NOT call SetMCPSnapshot.
	g.Generate(context.Background())

	out, _ := os.ReadFile(filepath.Join(home, ".kiro", "steering", "environment.md"))
	if strings.Contains(string(out), "Connected integrations") {
		t.Error("MCP section emitted when no snapshot was wired")
	}
}

// TestGenerate_EmptyMCPSnapshotOmitsSection: a wired snapshot that
// returns zero servers must also skip the section — emitting a
// header with nothing under it is noise.
func TestGenerate_EmptyMCPSnapshotOmitsSection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace.SetKiroHomeForTest(t, filepath.Join(home, ".kiro"))
	workDir := t.TempDir()
	configDir := t.TempDir()

	g := New(workDir, configDir)
	g.SetMCPSnapshot(func() MCPSnapshot { return MCPSnapshot{} })
	g.Generate(context.Background())

	out, _ := os.ReadFile(filepath.Join(home, ".kiro", "steering", "environment.md"))
	if strings.Contains(string(out), "Connected integrations") {
		t.Error("MCP section emitted for empty snapshot")
	}
}

// TestGenerate_RendersForgeSection verifies a wired forge snapshot renders
// the "## Connected forges" section with the provider host and user.
func TestGenerate_RendersForgeSection(t *testing.T) {
	steeringPath := setupKiroHome(t)
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

// TestGenerate_IdempotentSkipsRewrite verifies a second Generate with
// identical inputs does not rewrite the file. A far-past mtime is stamped
// first so the assertion is robust against coarse filesystem mtime
// granularity: a skipped write leaves the mtime in the past, while any
// rewrite bumps it to ~now.
func TestGenerate_IdempotentSkipsRewrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace.SetKiroHomeForTest(t, filepath.Join(home, ".kiro"))
	workDir := t.TempDir()
	configDir := t.TempDir()

	g := New(workDir, configDir)
	g.Generate(context.Background())

	steeringPath := filepath.Join(home, ".kiro", "steering", "environment.md")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(steeringPath, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	// Second Generate must be a no-op — content is identical, so the
	// file is never rewritten and the stamped mtime survives.
	g.Generate(context.Background())

	info, err := os.Stat(steeringPath)
	if err != nil {
		t.Fatalf("stat after second Generate: %v", err)
	}
	if cutoff := time.Now().Add(-30 * time.Minute); !info.ModTime().Before(cutoff) {
		t.Errorf("second Generate rewrote unchanged file: mtime %v is not before %v (skip-write guard broken)",
			info.ModTime(), cutoff)
	}
}

// TestGenerate_LogsWroteOnSuccess verifies a successful write logs the
// "steering: wrote" Info line.
func TestGenerate_LogsWroteOnSuccess(t *testing.T) {
	steeringPath := setupKiroHome(t)
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

// TestGenerate_ConcurrentCallsSerialise fans out concurrent Generate and
// SetMCPSnapshot calls; under -race it fails if the mutex is weakened or
// the snapshot pointer is accessed without the lock. The final file must
// be a single coherent document, not truncated or interleaved.
func TestGenerate_ConcurrentCallsSerialise(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace.SetKiroHomeForTest(t, filepath.Join(home, ".kiro"))
	workDir := t.TempDir()
	configDir := t.TempDir()

	g := New(workDir, configDir)
	g.SetMCPSnapshot(func() MCPSnapshot {
		return MCPSnapshot{Servers: []api.MCPSnapshotServer{{Name: "github"}}}
	})

	const n = 16
	var wg sync.WaitGroup
	wg.Add(2 * n)
	for range n {
		go func() { defer wg.Done(); g.Generate(context.Background()) }()
		go func() {
			defer wg.Done()
			g.SetMCPSnapshot(func() MCPSnapshot {
				return MCPSnapshot{Servers: []api.MCPSnapshotServer{{Name: "github"}}}
			})
		}()
	}
	wg.Wait()

	got, err := os.ReadFile(filepath.Join(home, ".kiro", "steering", "environment.md"))
	if err != nil {
		t.Fatalf("steering file missing after concurrent Generate: %v", err)
	}
	s := string(got)
	if !strings.HasPrefix(s, "# Environment\n") {
		t.Errorf("steering file header corrupted under concurrency: %.80q", s)
	}
	if !strings.Contains(s, "## Capabilities") {
		t.Errorf("steering file truncated under concurrency; no Capabilities section:\n%s", s)
	}
}

// ---------------------------------------------------------------------------
// CustomPath
// ---------------------------------------------------------------------------

func TestCustomPath_UsesHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace.SetKiroHomeForTest(t, filepath.Join(home, ".kiro"))
	g := New("/some/work", "/some/config")
	got := g.CustomPath()
	want := filepath.Join(home, ".kiro", "steering", "custom.md")
	if got != want {
		t.Errorf("CustomPath() = %q, want %q", got, want)
	}
}

func TestCustomPath_HomeUnsetFallback(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("KIRO_HOME", "")
	os.Unsetenv("HOME")
	os.Unsetenv("KIRO_HOME")
	workspace.SetKiroHomeForTest(t, ".kiro")
	g := New("/some/work", "/some/config")
	// With both KIRO_HOME and HOME unset, KiroHome() falls back to a
	// relative ".kiro" — same fallback kiro-cli uses internally so the
	// two stay aligned even on stripped local-dev shells.
	if got := g.CustomPath(); got != ".kiro/steering/custom.md" {
		t.Errorf("CustomPath() with no HOME = %q, want \".kiro/steering/custom.md\"", got)
	}
}

// TestWriteForges_RepoListTruncatesAtTwenty pins the "… and N more"
// overflow line on the accessible-repositories block. A provider with
// exactly 20 repos lists them all with no overflow line; 21 repos lists
// the first 20 and a single "… and 1 more" line. This guards the
// `len(p.Repos) > 20` boundary: a `>=` slip would print "… and 0 more"
// at exactly 20, and a flipped `<=` would drop the overflow line for
// genuinely-overflowing lists.
func TestWriteForges_RepoListTruncatesAtTwenty(t *testing.T) {
	mkRepos := func(n int) []string {
		repos := make([]string, n)
		for i := range repos {
			repos[i] = fmt.Sprintf("acme/repo-%02d", i)
		}
		return repos
	}
	t.Run("exactly twenty repos: no overflow line", func(t *testing.T) {
		var b strings.Builder
		writeForges(&b, ForgeSnapshot{Providers: []ForgeProvider{{
			Kind: "github", Host: "github.com", User: "alice", Repos: mkRepos(20),
		}}})
		out := b.String()
		if strings.Contains(out, "… and") {
			t.Errorf("20 repos rendered an overflow line, want none:\n%s", out)
		}
		if !strings.Contains(out, "acme/repo-19") {
			t.Errorf("20 repos: the 20th repo must still be listed:\n%s", out)
		}
	})
	t.Run("twenty-one repos: twenty listed plus one summarised", func(t *testing.T) {
		var b strings.Builder
		writeForges(&b, ForgeSnapshot{Providers: []ForgeProvider{{
			Kind: "github", Host: "github.com", User: "alice", Repos: mkRepos(21),
		}}})
		out := b.String()
		if !strings.Contains(out, "… and 1 more") {
			t.Errorf("21 repos: want an \"… and 1 more\" overflow line:\n%s", out)
		}
		if strings.Contains(out, "acme/repo-20") {
			t.Errorf("21 repos: the 21st repo must be summarised, not listed inline:\n%s", out)
		}
	})
}
