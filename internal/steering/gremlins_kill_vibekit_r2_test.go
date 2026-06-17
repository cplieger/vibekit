package steering

// Round-2 mutant-killing tests for package internal/steering.
//
// These target survivors the round-1 (u4/u5) tests missed: the
// writeRepoSkills group-header guards and writeSkillEntry field guards
// (the round-1 file only exercised the sibling writeRepoSteering /
// writeSteeringEntry), plus the per-field rendering negations in
// writeRepoEntry (forge CLI line, hook trigger, hook command) and
// writeForges (auth email, CLI line, accessible-repos block).
//
// Tests-only where noted; helpers prefixed gk_vibekit_r2_ to avoid
// colliding with the u4/u5 helpers in this same package.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func gk_vibekit_r2_writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// ===========================================================================
// writeRepoSkills + writeSkillEntry (discovery.go)
// ===========================================================================

// Kills discovery.go:134:17 (BOUNDARY `len(always) > 0`), :140:18
// (BOUNDARY + NEGATION `len(matched) > 0`), :146:17 (BOUNDARY +
// NEGATION `len(manual) > 0`), and :156:17 / :159:19 (NEGATION on the
// writeSkillEntry `FileMatch != ""` / `Description != ""` guards).
//
// The round-1 suite only covered writeRepoSteering's identical guards,
// leaving the writeRepoSkills copies live.
func TestGKVibekitR2_WriteRepoSkills_HeadersAndEntryFields(t *testing.T) {
	t.Run("only fileMatch+manual: no Always header, others present, fields rendered", func(t *testing.T) {
		var b strings.Builder
		writeRepoSkills(&b, "myrepo", []Doc{
			{Filename: "match.md", Inclusion: "fileMatch", FileMatch: "internal/**", Description: "go layout"},
			{Filename: "ref.md", Inclusion: "manual"},
		})
		out := b.String()
		// 134 BOUNDARY: empty always-group must NOT emit its header.
		if strings.Contains(out, "Always-loaded skills") {
			t.Errorf("emitted Always-loaded skills header with zero always docs:\n%s", out)
		}
		// 140 / 146 NEGATION: non-empty matched/manual groups DO emit headers.
		if !strings.Contains(out, "File-match skills") {
			t.Errorf("missing File-match skills header for a fileMatch doc:\n%s", out)
		}
		if !strings.Contains(out, "Manual skills") {
			t.Errorf("missing Manual skills header for a manual doc:\n%s", out)
		}
		// 156 / 159 NEGATION: FileMatch and Description render on the entry.
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
		// 140 / 146 BOUNDARY: empty matched/manual groups must NOT emit headers.
		if strings.Contains(out, "File-match skills") {
			t.Errorf("emitted File-match skills header with zero fileMatch docs:\n%s", out)
		}
		if strings.Contains(out, "Manual skills") {
			t.Errorf("emitted Manual skills header with zero manual docs:\n%s", out)
		}
		if !strings.Contains(out, "Always-loaded skills") {
			t.Errorf("missing Always-loaded skills header:\n%s", out)
		}
		// 156 / 159 NEGATION (other direction): a doc with empty
		// FileMatch/Description must NOT render the annotations.
		if strings.Contains(out, "(matches") {
			t.Errorf("rendered FileMatch annotation for an empty FileMatch:\n%s", out)
		}
		if strings.Contains(out, "build.md` —") {
			t.Errorf("rendered Description annotation for an empty Description:\n%s", out)
		}
	})
}

// ===========================================================================
// discovery.go entry-count caps (findRepoAgents / findRepoHooks /
// findMdDocsInDir)
// ===========================================================================

// Kills discovery.go:276:15 (BOUNDARY + NEGATION `len(out) >= 10` in
// findRepoAgents), :310:15 (BOUNDARY + NEGATION `len(out) >= 10` in
// findRepoHooks), and :350:15 (BOUNDARY `len(out) >= 20` in
// findMdDocsInDir). Feeding more files than the cap and asserting the
// EXACT capped count discriminates every variant: the `> cap` boundary
// would return cap+1, and the `< cap` negation would break after the
// first append and return 1.
func TestGKVibekitR2_DiscoveryEntryCaps(t *testing.T) {
	t.Run("findRepoAgents caps at 10", func(t *testing.T) {
		repo := t.TempDir()
		for i := range 12 {
			gk_vibekit_r2_writeFile(t,
				filepath.Join(repo, ".kiro", "agents", "agent"+itoa2(i)+".json"),
				`{"name":"a`+itoa2(i)+`"}`)
		}
		if got := len(findRepoAgents(repo)); got != 10 {
			t.Errorf("findRepoAgents(12 files) returned %d, want exactly 10 (cap)", got)
		}
	})
	t.Run("findRepoHooks caps at 10", func(t *testing.T) {
		repo := t.TempDir()
		for i := range 12 {
			gk_vibekit_r2_writeFile(t,
				filepath.Join(repo, ".kiro", "hooks", "hook"+itoa2(i)+".json"),
				`{"event_type":"preToolUse","command":"x"}`)
		}
		if got := len(findRepoHooks(repo)); got != 10 {
			t.Errorf("findRepoHooks(12 files) returned %d, want exactly 10 (cap)", got)
		}
	})
	t.Run("findMdDocsInDir caps at 20", func(t *testing.T) {
		dir := t.TempDir()
		for i := range 25 {
			gk_vibekit_r2_writeFile(t, filepath.Join(dir, "doc"+itoa2(i)+".md"), "body\n")
		}
		if got := len(findMdDocsInDir(dir)); got != 20 {
			t.Errorf("findMdDocsInDir(25 files) returned %d, want exactly 20 (cap)", got)
		}
	})
}

// itoa2 renders i as a fixed-width 2-digit string so ReadDir's
// lexical ordering matches numeric ordering (not needed for the count
// assertions, but keeps the fixtures tidy).
func itoa2(i int) string {
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}

// ===========================================================================
// writeRepoEntry (workspace.go): forge CLI line + hook fields
// ===========================================================================

// Kills workspace.go:76:10 (NEGATION `cli != ""` — the "use <cli>"
// forge line), :107:15 (NEGATION `trigger == ""` — hook trigger
// label), and :111:17 (NEGATION `h.Command != ""` — hook command
// preview). Round-1's RendersAllFields test asserted only the section
// headers, not these inner field values.
func TestGKVibekitR2_WriteRepoEntry_ForgeCLIAndHookFields(t *testing.T) {
	work := t.TempDir()
	repo := filepath.Join(work, "myrepo")
	gk_vibekit_r2_writeFile(t, filepath.Join(repo, ".git", "config"),
		"[remote \"origin\"]\n\turl = https://github.com/acme/widget.git\n")
	gk_vibekit_r2_writeFile(t, filepath.Join(repo, ".kiro", "hooks", "guard.json"),
		`{"event_type":"preToolUse","command":"echo hi"}`)

	var b strings.Builder
	writeRepoEntry(&b, work, "myrepo")
	out := b.String()

	// 76: a recognised github origin renders the "use `gh`" guidance,
	// not the bare "(github.com)" fallback.
	if !strings.Contains(out, "use `gh` for PRs") {
		t.Errorf("missing forge-CLI guidance for github origin (kills :76):\n%s", out)
	}
	// 107: a hook with a known event_type renders that trigger, not "unknown".
	if !strings.Contains(out, "[preToolUse]") {
		t.Errorf("hook trigger not rendered as [preToolUse] (kills :107):\n%s", out)
	}
	if strings.Contains(out, "[unknown]") {
		t.Errorf("hook with a known trigger wrongly rendered [unknown]:\n%s", out)
	}
	// 111: a hook with a command renders the command preview.
	if !strings.Contains(out, "echo hi") {
		t.Errorf("hook command preview not rendered (kills :111):\n%s", out)
	}
}

// ===========================================================================
// writeForges (steering.go): per-provider auth/CLI/repos rendering
// ===========================================================================

// Kills steering.go:284:14 (NEGATION `p.Email != ""`), :290:10
// (NEGATION `cli != ""`), and :294:19 (BOUNDARY + NEGATION
// `len(p.Repos) > 0`). The round-1 forge test used a provider with no
// email and no repos, leaving these branches unexercised.
func TestGKVibekitR2_WriteForges_PerProviderFields(t *testing.T) {
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
		if !strings.Contains(out, "alice@example.com") { // 284
			t.Errorf("missing authenticated email:\n%s", out)
		}
		if !strings.Contains(out, "CLI: `gh`") { // 290
			t.Errorf("missing CLI line for github:\n%s", out)
		}
		if !strings.Contains(out, "Accessible repositories") || !strings.Contains(out, "acme/widget") { // 294 negation
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
		if strings.Contains(out, "bob <") { // 284 (other direction): no "<email>"
			t.Errorf("rendered an empty <email> for a provider without an email:\n%s", out)
		}
		if strings.Contains(out, "- CLI:") { // 290 (other direction)
			t.Errorf("rendered a CLI line for an unknown forge kind:\n%s", out)
		}
		if strings.Contains(out, "Accessible repositories") { // 294 boundary
			t.Errorf("rendered accessible-repositories header with zero repos:\n%s", out)
		}
	})
}
