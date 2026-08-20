package ignore

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/settings"
)

// writeIgnoreSettings writes config.json listing the given ignore files.
// Relative paths are honoured verbatim (resolved against workDir by the matcher).
func writeIgnoreSettings(t *testing.T, dir string, files []string) {
	t.Helper()
	buf := []byte(`{"agent_ignore_files":[]}`)
	if len(files) > 0 {
		b := []byte(`{"agent_ignore_files":[`)
		for i, f := range files {
			if i > 0 {
				b = append(b, ',')
			}
			b = append(b, '"')
			b = append(b, f...)
			b = append(b, '"')
		}
		b = append(b, ']', '}')
		buf = b
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), buf, 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
}

func writeIgnoreFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write ignore file: %v", err)
	}
}

// --- Matches (end-to-end with temp files) ---

func TestIgnoreMatcher_NoSettingsFileIsNoOp(t *testing.T) {
	dir := t.TempDir()
	work := t.TempDir()
	m := NewMatcher(dir, work)

	if m.Matches(t.Context(), "any/path", false) {
		t.Error("Matches on empty setup = true, want false (no-op matcher)")
	}
}

func TestIgnoreMatcher_FreshInstallDefaultFiltersGitignored(t *testing.T) {
	// The settled "agent read filter ON by default" behavior: on a fresh
	// install (config dir has NO config.json, so agent_ignore_files is unset)
	// the matcher must fall back to the seeded default ignore-file list and
	// filter a .gitignore'd secret from the agent read path out of the box.
	dir := t.TempDir()  // config dir: intentionally NO config.json written
	work := t.TempDir() // workspace root

	// The seeded default must be non-empty (otherwise a fresh install would
	// filter nothing) and drive the workspace .gitignore.
	def := settings.DefaultAgentIgnoreFiles()
	if len(def) == 0 {
		t.Fatal("DefaultAgentIgnoreFiles() is empty; fresh install would not filter agent reads")
	}
	if !slices.Contains(def, ".gitignore") {
		t.Fatalf("default %v must include .gitignore for this test to be meaningful", def)
	}
	writeIgnoreFile(t, filepath.Join(work, ".gitignore"), ".env.dec\nsecrets/\n")

	m := NewMatcher(dir, work)

	if !m.Matches(t.Context(), ".env.dec", false) {
		t.Error("fresh install: .env.dec should be filtered by the default .gitignore (agent read filter off?)")
	}
	if !m.Matches(t.Context(), "secrets/api.key", false) {
		t.Error("fresh install: files under a gitignored secrets/ dir should be filtered")
	}
	if m.Matches(t.Context(), "src/main.go", false) {
		t.Error("fresh install: a non-ignored path must still be readable")
	}
}

func TestIgnoreMatcher_FreshInstallDefaultHonorsKiroignore(t *testing.T) {
	// The seeded default also covers .kiroignore, so a workspace .kiroignore
	// filters agent reads with no config.json present.
	if !slices.Contains(settings.DefaultAgentIgnoreFiles(), ".kiroignore") {
		t.Skip("default does not seed .kiroignore")
	}
	dir := t.TempDir() // no config.json
	work := t.TempDir()
	writeIgnoreFile(t, filepath.Join(work, ".kiroignore"), "*.secret\n")

	m := NewMatcher(dir, work)
	if !m.Matches(t.Context(), "creds.secret", false) {
		t.Error("fresh install: *.secret from the default .kiroignore should be filtered")
	}
}

func TestIgnoreMatcher_EmptyListIsNoOp(t *testing.T) {
	dir := t.TempDir()
	work := t.TempDir()
	writeIgnoreSettings(t, dir, nil)
	m := NewMatcher(dir, work)

	if m.Matches(t.Context(), ".env.dec", false) {
		t.Error("Matches with empty list = true, want false")
	}
}

func TestIgnoreMatcher_BasenameRuleMatchesAnywhere(t *testing.T) {
	dir := t.TempDir()
	work := t.TempDir()
	ign := filepath.Join(work, ".gitignore")
	writeIgnoreFile(t, ign, "*.dec\nnode_modules\n")
	writeIgnoreSettings(t, dir, []string{".gitignore"}) // relative → resolves against workDir

	m := NewMatcher(dir, work)

	tests := []struct {
		path string
		name string
		want bool
	}{
		{".env.dec", "root .env.dec", true},
		{"apps/cert-convert/.env.dec", "nested .env.dec", true},
		{".env.example", "non-matching file", false},
		{"node_modules", "dir literal as file path", true},
		{"src/node_modules", "nested node_modules", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := m.Matches(t.Context(), tt.path, false); got != tt.want {
				t.Errorf("Matches(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestIgnoreMatcher_AnchoredRuleOnlyAtRoot(t *testing.T) {
	dir := t.TempDir()
	work := t.TempDir()
	ign := filepath.Join(work, ".gitignore")
	writeIgnoreFile(t, ign, "/node\n")
	writeIgnoreSettings(t, dir, []string{".gitignore"})

	m := NewMatcher(dir, work)

	if !m.Matches(t.Context(), "node", false) {
		t.Error(`anchored "/node" should match root "node"`)
	}
	if m.Matches(t.Context(), "src/node", false) {
		t.Error(`anchored "/node" should NOT match "src/node"`)
	}
}

func TestIgnoreMatcher_AnchoredDirCoversDescendants(t *testing.T) {
	// Regression for F2: a pattern like "/secrets" must block not
	// only the "secrets" entry itself but every file beneath it.
	// Standard gitignore semantics — and the whole point of ignoring
	// a directory of secrets from the agent's read view.
	dir := t.TempDir()
	work := t.TempDir()
	ign := filepath.Join(work, ".gitignore")
	writeIgnoreFile(t, ign, "/secrets\n")
	writeIgnoreSettings(t, dir, []string{".gitignore"})

	m := NewMatcher(dir, work)

	cases := []struct {
		path string
		want bool
	}{
		{"secrets", true},
		{"secrets/api.key", true},
		{"secrets/sub/deep.json", true},
		{"other", false},
		{"other/secrets.md", false}, // anchored — no float
	}
	for _, tt := range cases {
		if got := m.Matches(t.Context(), tt.path, false); got != tt.want {
			t.Errorf("Matches(%q) = %v, want %v (anchored dir covers descendants)", tt.path, got, tt.want)
		}
	}
}

func TestIgnoreMatcher_DirOnlyRuleSkipsFiles(t *testing.T) {
	dir := t.TempDir()
	work := t.TempDir()
	ign := filepath.Join(work, ".gitignore")
	writeIgnoreFile(t, ign, "build/\n")
	writeIgnoreSettings(t, dir, []string{".gitignore"})

	m := NewMatcher(dir, work)

	if m.Matches(t.Context(), "build", false) {
		t.Error(`dir-only "build/" should NOT match file "build"`)
	}
	if !m.Matches(t.Context(), "build", true) {
		t.Error(`dir-only "build/" SHOULD match directory "build"`)
	}
	if !m.Matches(t.Context(), "src/build", true) {
		t.Error(`dir-only "build/" should match nested directory`)
	}
}

func TestIgnoreMatcher_NegationResurrects(t *testing.T) {
	dir := t.TempDir()
	work := t.TempDir()
	ign := filepath.Join(work, ".gitignore")
	writeIgnoreFile(t, ign, "*.dec\n!/.env.example.dec\n")
	writeIgnoreSettings(t, dir, []string{".gitignore"})

	m := NewMatcher(dir, work)

	if !m.Matches(t.Context(), ".env.dec", false) {
		t.Error(`"*.dec" should match ".env.dec"`)
	}
	if m.Matches(t.Context(), ".env.example.dec", false) {
		t.Error(`"!.env.example.dec" should resurrect ".env.example.dec"`)
	}
}

func TestIgnoreMatcher_NegationOrderMatters(t *testing.T) {
	// Later rules override earlier ones — put the negation BEFORE
	// the broad match and it must not resurrect.
	dir := t.TempDir()
	work := t.TempDir()
	ign := filepath.Join(work, ".gitignore")
	writeIgnoreFile(t, ign, "!/.env.example\n*.env\n")
	writeIgnoreSettings(t, dir, []string{".gitignore"})

	m := NewMatcher(dir, work)
	if !m.Matches(t.Context(), ".env", false) {
		t.Error(`"*.env" after "!/.env.example" should still match ".env"`)
	}
}

func TestIgnoreMatcher_LeadingDotSlashAndSlashStripped(t *testing.T) {
	// Matches normalises leading "/" and "./" so callers can pass either.
	dir := t.TempDir()
	work := t.TempDir()
	ign := filepath.Join(work, ".gitignore")
	writeIgnoreFile(t, ign, "secret.key\n")
	writeIgnoreSettings(t, dir, []string{".gitignore"})

	m := NewMatcher(dir, work)

	for _, p := range []string{"secret.key", "./secret.key", "/secret.key"} {
		if !m.Matches(t.Context(), p, false) {
			t.Errorf("Matches(%q) = false, want true (leading-slash normalization)", p)
		}
	}
}

func TestIgnoreMatcher_CommentsAndBlankLinesIgnored(t *testing.T) {
	dir := t.TempDir()
	work := t.TempDir()
	ign := filepath.Join(work, ".gitignore")
	writeIgnoreFile(t, ign, "# ignore junk\n\n# another comment\nsecret\n\n")
	writeIgnoreSettings(t, dir, []string{".gitignore"})

	m := NewMatcher(dir, work)

	if !m.Matches(t.Context(), "secret", false) {
		t.Error(`rule after blanks/comments should still apply`)
	}
	if m.Matches(t.Context(), "comment", false) {
		t.Error(`"# comment" content should not become a rule`)
	}
}

func TestIgnoreMatcher_MissingIgnoreFileSilentlySkipped(t *testing.T) {
	dir := t.TempDir()
	work := t.TempDir()
	// List a file that doesn't exist.
	writeIgnoreSettings(t, dir, []string{filepath.Join(work, "nope.ignore")})

	m := NewMatcher(dir, work)

	if m.Matches(t.Context(), "anything", false) {
		t.Error("missing ignore file should produce a no-op matcher, not crash or match-all")
	}
}

func TestIgnoreMatcher_OversizedFileSkipped(t *testing.T) {
	// Regression for F3/Op7: a pathological ignore file (larger
	// than maxIgnoreFileSize) must be skipped entirely, not loaded
	// into memory. Verifies the matcher falls through to no-op
	// behaviour when the only ignore file is over-cap.
	dir := t.TempDir()
	work := t.TempDir()
	big := filepath.Join(work, "big.ignore")
	// Write a file slightly larger than the cap. Every line is
	// "literal", so if it were parsed it would block anything named
	// "literal".
	var sb strings.Builder
	for sb.Len() <= maxIgnoreFileSize {
		sb.WriteString("literal\n")
	}
	writeIgnoreFile(t, big, sb.String())
	writeIgnoreSettings(t, dir, []string{big})

	m := NewMatcher(dir, work)

	if m.Matches(t.Context(), "literal", false) {
		t.Error("oversized ignore file should be skipped entirely; 'literal' must not be blocked")
	}
}

func TestIgnoreMatcher_AbsoluteAndRelativePathsBothResolved(t *testing.T) {
	dir := t.TempDir()
	work := t.TempDir()
	rel := filepath.Join(work, "rel.ignore")
	writeIgnoreFile(t, rel, "relfile\n")
	absDir := t.TempDir()
	abs := filepath.Join(absDir, "abs.ignore")
	writeIgnoreFile(t, abs, "absfile\n")
	writeIgnoreSettings(t, dir, []string{"rel.ignore", abs})

	m := NewMatcher(dir, work)

	if !m.Matches(t.Context(), "relfile", false) {
		t.Error("relative ignore entry should resolve against workDir")
	}
	if !m.Matches(t.Context(), "absfile", false) {
		t.Error("absolute ignore entry should be honoured as-is")
	}
}

func TestIgnoreMatcher_ReloadOnMTimeChange(t *testing.T) {
	// Matcher re-reads when the ignore file's mtime advances.
	dir := t.TempDir()
	work := t.TempDir()
	ign := filepath.Join(work, ".gitignore")
	writeIgnoreFile(t, ign, "first\n")
	writeIgnoreSettings(t, dir, []string{".gitignore"})

	m := NewMatcher(dir, work)

	if !m.Matches(t.Context(), "first", false) {
		t.Fatal("initial rule should match")
	}
	if m.Matches(t.Context(), "second", false) {
		t.Fatal("second rule should not yet match")
	}

	// Rewrite with new rule and bump mtime past the last load.
	writeIgnoreFile(t, ign, "second\n")
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(ign, future, future); err != nil {
		t.Fatal(err)
	}

	if !m.Matches(t.Context(), "second", false) {
		t.Error("matcher did not pick up the new rule after mtime bump")
	}
	if m.Matches(t.Context(), "first", false) {
		t.Error("old rule should be gone after reload")
	}
}

func TestIgnoreMatcher_ReloadOnSettingsFileListChange(t *testing.T) {
	// Adding a new ignore file to the settings list forces a refresh.
	dir := t.TempDir()
	work := t.TempDir()
	first := filepath.Join(work, "a.ignore")
	writeIgnoreFile(t, first, "from-a\n")
	writeIgnoreSettings(t, dir, []string{"a.ignore"})

	m := NewMatcher(dir, work)
	if !m.Matches(t.Context(), "from-a", false) {
		t.Fatal("baseline match failed")
	}
	if m.Matches(t.Context(), "from-b", false) {
		t.Fatal("not yet added")
	}

	second := filepath.Join(work, "b.ignore")
	writeIgnoreFile(t, second, "from-b\n")
	writeIgnoreSettings(t, dir, []string{"a.ignore", "b.ignore"})

	if !m.Matches(t.Context(), "from-b", false) {
		t.Error("new file added to list should take effect on next Matches")
	}
	if !m.Matches(t.Context(), "from-a", false) {
		t.Error("pre-existing rules should survive list expansion")
	}
}

func TestIgnoreMatcher_InvalidSettingsSilentlyNoOp(t *testing.T) {
	dir := t.TempDir()
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"),
		[]byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := NewMatcher(dir, work)
	if m.Matches(t.Context(), "x", false) {
		t.Error("corrupt settings should degrade to no-op matcher, not match-all")
	}
}

func TestIgnoreMatcher_WrongTypeForListSilentlyNoOp(t *testing.T) {
	dir := t.TempDir()
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"),
		[]byte(`{"agent_ignore_files":"not-an-array"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := NewMatcher(dir, work)
	if m.Matches(t.Context(), "x", false) {
		t.Error("wrong-type setting should degrade to no-op matcher")
	}
}

func TestIgnoreMatcher_EmptyStringsInListDropped(t *testing.T) {
	// An empty string or whitespace-only entry must not cause the
	// matcher to fall back to a bare workdir reference (which would
	// stat the workdir itself and load it as an "ignore file").
	dir := t.TempDir()
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"),
		[]byte(`{"agent_ignore_files":["","   "]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := NewMatcher(dir, work)
	if m.Matches(t.Context(), "x", false) {
		t.Error("empty entries should not produce fallback match-all behaviour")
	}
}

func TestIgnoreMatcher_DirOnlyAnchoredCoversDescendants(t *testing.T) {
	// Regression for F1 c2: a dir-only anchored rule like "/secrets/"
	// must block descendant files (not just the directory entry).
	// Standard gitignore semantics — and the whole point of telling
	// the agent "this is a directory of secrets, stay out". The
	// cycle 1 F2 fix covered "/secrets" (no trailing slash) but
	// missed the trailing-slash form.
	dir := t.TempDir()
	work := t.TempDir()
	ign := filepath.Join(work, ".gitignore")
	writeIgnoreFile(t, ign, "/private/\n")
	writeIgnoreSettings(t, dir, []string{".gitignore"})

	m := NewMatcher(dir, work)

	cases := []struct {
		path  string
		isDir bool
		want  bool
	}{
		{"private", true, true},                // directory itself
		{"private/api.key", false, true},       // descendant file (the fix)
		{"private/sub/deep.json", false, true}, // deep descendant
		{"private/sub", true, true},            // descendant DIRECTORY (q07: ancestor walk must fire for isDir=true too)
		{"private", false, false},              // file with same name, not a dir
		{"other", true, false},                 // unrelated directory
		{"other/file", false, false},           // unrelated file
	}
	for _, tt := range cases {
		if got := m.Matches(t.Context(), tt.path, tt.isDir); got != tt.want {
			t.Errorf("Matches(%q, isDir=%v) = %v, want %v", tt.path, tt.isDir, got, tt.want)
		}
	}
}

func TestIgnoreMatcher_DirOnlyBasenameCoversDescendants(t *testing.T) {
	// Dir-only basename rules ("build/") should also block
	// descendant files of any matching directory, not just the
	// directory entry itself. Basename flavour of the F1 fix.
	dir := t.TempDir()
	work := t.TempDir()
	ign := filepath.Join(work, ".gitignore")
	writeIgnoreFile(t, ign, "build/\n")
	writeIgnoreSettings(t, dir, []string{".gitignore"})

	m := NewMatcher(dir, work)

	cases := []struct {
		path  string
		isDir bool
		want  bool
	}{
		{"build/output.txt", false, true},      // file under top-level build/
		{"src/build/artifact.so", false, true}, // nested build/ contents
		{"build", true, true},                  // the directory itself
		{"build", false, false},                // a FILE named "build"
		{"source/README.md", false, false},
	}
	for _, tt := range cases {
		if got := m.Matches(t.Context(), tt.path, tt.isDir); got != tt.want {
			t.Errorf("Matches(%q, isDir=%v) = %v, want %v", tt.path, tt.isDir, got, tt.want)
		}
	}
}

func TestIgnoreMatcher_DirOnlyRespectsNegationOrder(t *testing.T) {
	// Ancestor-walk must still honour later-overrides-earlier rule
	// order. A negation after a dir-only block lifts the block for
	// the specified file; a negation before a dir-only block is
	// overridden by the block.
	dir := t.TempDir()
	work := t.TempDir()
	ign := filepath.Join(work, ".gitignore")
	writeIgnoreFile(t, ign, "/private/\n!/private/README.md\n")
	writeIgnoreSettings(t, dir, []string{".gitignore"})

	m := NewMatcher(dir, work)

	if !m.Matches(t.Context(), "private/api.key", false) {
		t.Error("private/api.key should remain ignored under dir-only block")
	}
	if m.Matches(t.Context(), "private/README.md", false) {
		t.Error("!/private/README.md should resurrect private/README.md")
	}
}

// --- Resilience gaps for ignore matcher (test-u8c1-6) ---

func TestIgnoreMatcher_MalformedGlobPatternDoesNotCrash(t *testing.T) {
	// Regression: a malformed glob pattern (unclosed `[`, the only
	// thing filepath.Match rejects with ErrBadPattern) in a user's
	// ignore file must be silently treated as "no match" — not
	// crash, not match-all. Pins the invariant in segMatch.
	dir := t.TempDir()
	work := t.TempDir()
	ign := filepath.Join(work, ".gitignore")
	if err := os.WriteFile(ign, []byte("[unclosed\n[abc\nnormal\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeIgnoreSettings(t, dir, []string{".gitignore"})
	m := NewMatcher(dir, work)

	// Malformed patterns must not match their literals nor
	// blanket-match — filepath.Match returns ErrBadPattern, which
	// segMatch folds into no-match.
	if m.Matches(t.Context(), "[unclosed", false) {
		t.Error("Matches(\"[unclosed\") = true; malformed pattern must not match literally")
	}
	if m.Matches(t.Context(), "whatever", false) {
		t.Error("Matches(\"whatever\") = true; malformed pattern must not blanket-match")
	}
	// Well-formed patterns after the bad one must still apply.
	if !m.Matches(t.Context(), "normal", false) {
		t.Error("Matches(\"normal\") = false; valid rule after malformed one should still apply")
	}
}

func TestIgnoreMatcher_DeletedIgnoreFileTriggersReload(t *testing.T) {
	// Regression: deleting an ignore file between Matches calls
	// must trigger a reload (filesOrMTimesChanged's "stat err +
	// had previous mtime" branch) and drop the rules. Without
	// this, a user who removes .gitignore expects their agent to
	// regain access on the next read — and that contract lives in
	// a branch nothing pinned before this test.
	dir := t.TempDir()
	work := t.TempDir()
	ign := filepath.Join(work, ".gitignore")
	if err := os.WriteFile(ign, []byte("ignored\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeIgnoreSettings(t, dir, []string{".gitignore"})
	m := NewMatcher(dir, work)

	if !m.Matches(t.Context(), "ignored", false) {
		t.Fatal("baseline load failed — ignored should match")
	}

	// Delete the ignore file. Next Matches must detect the
	// disappearance and reload to an empty rule set.
	if err := os.Remove(ign); err != nil {
		t.Fatal(err)
	}

	if m.Matches(t.Context(), "ignored", false) {
		t.Error("Matches(ignored) = true after ignore file deleted; matcher did not reload")
	}
}

func TestIgnoreMatcher_ExactCapFileIsParsed(t *testing.T) {
	// Boundary companion to TestIgnoreMatcher_OversizedFileSkipped: a file
	// of EXACTLY maxIgnoreFileSize bytes is at the inclusive limit and must
	// be parsed (the cap rejects only files strictly larger).
	configDir := t.TempDir()
	workDir := t.TempDir()
	ignorePath := filepath.Join(workDir, "big.gitignore")

	// One real rule, the rest a single comment line (dropped by the parser),
	// summing to exactly maxIgnoreFileSize bytes.
	rule := "secret\n"
	body := rule + strings.Repeat("#", int(maxIgnoreFileSize)-len(rule))
	if int64(len(body)) != int64(maxIgnoreFileSize) {
		t.Fatalf("setup: body len = %d, want %d", len(body), maxIgnoreFileSize)
	}
	if err := os.WriteFile(ignorePath, []byte(body), 0o600); err != nil {
		t.Fatalf("write ignore file: %v", err)
	}
	writeIgnoreSettings(t, configDir, []string{ignorePath})

	m := NewMatcher(configDir, workDir)
	if !m.Matches(t.Context(), "secret", false) {
		t.Errorf("Matches(\"secret\") with exactly-cap ignore file = false, want true")
	}
}

func TestIgnoreMatcher_ConfigDeletedDoesNotPanic(t *testing.T) {
	// After config.json is deleted between refreshes, a cached non-zero
	// settings mtime must not lead the refresh to dereference a nil FileInfo
	// (the regression this test pins). A deleted config.json now falls back
	// to the seeded default list (settings.DefaultAgentIgnoreFiles); this test
	// uses a NON-default ignore filename, so the fallback finds no matching
	// file in workDir and the matcher degrades to no-op — the invariant under
	// test is that it must not panic.
	configDir := t.TempDir()
	workDir := t.TempDir()
	ignorePath := filepath.Join(workDir, "custom.ignore")
	writeIgnoreFile(t, ignorePath, "secret\n")
	writeIgnoreSettings(t, configDir, []string{ignorePath})

	m := NewMatcher(configDir, workDir)
	ctx := t.Context()
	// First refresh caches a non-zero settings mtime.
	if !m.Matches(ctx, "secret", false) {
		t.Fatalf("setup: first Matches(\"secret\") = false, want true")
	}
	if err := os.Remove(filepath.Join(configDir, "config.json")); err != nil {
		t.Fatalf("remove config.json: %v", err)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Matches after config.json delete panicked: %v", r)
		}
	}()
	// The default fallback looks for .gitignore/.kiroignore in workDir; neither
	// exists here, so "secret" (from the now-unreferenced custom.ignore) is no
	// longer matched.
	if got := m.Matches(ctx, "secret", false); got {
		t.Errorf("Matches(\"secret\") after config delete = true, want false (default fallback finds no default-named file)")
	}
}

func TestIgnoreMatcher_ConfigSizeChangeBypassesFastPath(t *testing.T) {
	// The config.json fast path requires BOTH mtime and size to match the
	// cached values. With mtime forced equal but size changed, the matcher
	// must re-read the new file list rather than reuse the stale one.
	configDir := t.TempDir()
	workDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.json")

	// Two ignore files with DIFFERENT path lengths so the two config.json
	// versions differ in byte size. v1 ignores "foo", v2 ignores only "bar".
	ignoreA := filepath.Join(workDir, "ia.gitignore")
	ignoreB := filepath.Join(workDir, "ib-substantially-longer-name.gitignore")
	writeIgnoreFile(t, ignoreA, "foo\n")
	writeIgnoreFile(t, ignoreB, "bar\n")

	fixed := time.Now().Add(-time.Hour).Truncate(time.Second)

	writeIgnoreSettings(t, configDir, []string{ignoreA})
	if err := os.Chtimes(configPath, fixed, fixed); err != nil {
		t.Fatalf("chtimes v1: %v", err)
	}
	info1, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat v1: %v", err)
	}
	size1 := info1.Size()

	m := NewMatcher(configDir, workDir)
	ctx := t.Context()
	if !m.Matches(ctx, "foo", false) {
		t.Fatalf("setup: Matches(\"foo\") with v1 = false, want true")
	}

	// v2 lists ignoreB only (does NOT ignore "foo"); reset mtime to the SAME
	// fixed time so ONLY the file size differs from the cached config.json.
	writeIgnoreSettings(t, configDir, []string{ignoreB})
	info2, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat v2: %v", err)
	}
	if info2.Size() == size1 {
		t.Fatalf("setup: config sizes equal (%d); need different sizes", size1)
	}
	if err := os.Chtimes(configPath, fixed, fixed); err != nil {
		t.Fatalf("chtimes v2: %v", err)
	}

	if got := m.Matches(ctx, "foo", false); got {
		t.Errorf("Matches(\"foo\") after config size change = true, want false (must re-read v2)")
	}
}

// --- Benchmark for Matcher.Matches hot path ---

func BenchmarkIgnoreMatcherMatches(b *testing.B) {
	// Exercises the per-file ignore-check hot path called once per
	// file-read request from the bridge. Sub-benchmarks vary rule
	// count (5/15/30) and path depth (shallow/medium/deep) to catch
	// O(rules×ancestors) regressions in the dirOnly ancestor walk
	// and allocation regressions from the singleflight refresh path.

	paths := map[string]string{
		"shallow": "README.md",
		"medium":  "src/pkg/file.go",
		"deep":    "a/b/c/d/e/f.go",
	}

	// buildRules generates n ignore rules mixing basename globs,
	// anchored patterns, dir-only rules, and double-star patterns.
	buildRules := func(n int) string {
		var sb strings.Builder
		for i := range n {
			switch i % 4 {
			case 0:
				sb.WriteString("*.gen" + strings.Repeat("x", i%3) + "\n")
			case 1:
				sb.WriteString("/vendor" + strings.Repeat("/sub", i%3) + "/\n")
			case 2:
				sb.WriteString("**/build" + strings.Repeat("x", i%5) + "\n")
			case 3:
				sb.WriteString("tmp" + strings.Repeat("x", i%4) + "\n")
			}
		}
		return sb.String()
	}

	ruleCounts := []int{5, 15, 30}

	for _, rc := range ruleCounts {
		for pName, pVal := range paths {
			b.Run(fmt.Sprintf("rules_%d_%s", rc, pName), func(b *testing.B) {
				dir := b.TempDir()
				work := b.TempDir()
				ign := filepath.Join(work, ".gitignore")
				if err := os.WriteFile(ign, []byte(buildRules(rc)), 0o600); err != nil {
					b.Fatal(err)
				}
				writeIgnoreSettingsB(b, dir, []string{".gitignore"})
				m := NewMatcher(dir, work)
				// Prime the cache so we benchmark rule evaluation, not I/O.
				m.Matches(b.Context(), pVal, false)

				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					m.Matches(b.Context(), pVal, false)
				}
			})
		}
	}
}

// writeIgnoreSettingsB is the benchmark variant of writeIgnoreSettings.
func writeIgnoreSettingsB(b *testing.B, dir string, files []string) {
	b.Helper()
	buf := []byte(`{"agent_ignore_files":[]}`)
	if len(files) > 0 {
		bb := []byte(`{"agent_ignore_files":[`)
		for i, f := range files {
			if i > 0 {
				bb = append(bb, ',')
			}
			bb = append(bb, '"')
			bb = append(bb, f...)
			bb = append(bb, '"')
		}
		bb = append(bb, ']', '}')
		buf = bb
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), buf, 0o600); err != nil {
		b.Fatalf("write settings: %v", err)
	}
}

// A named pipe at an ignore-file name must be REFUSED, not opened.
//
// This is the shape that makes it serious rather than exotic: the default list
// resolves `.gitignore` and `.kiroignore` against the workspace, which the agent
// writes, and the matcher's refresh runs on the agent's own fs read path. A plain
// O_RDONLY open of a reader-less FIFO waits in open(2) with no deadline that can
// rescue it, so one pipe wedged every later read of every chat against a KAS Call
// that carries no timeout.
//
// The test would HANG rather than fail if the refusal regressed, which is exactly
// why it is worth pinning: a hang is the failure mode nobody attributes correctly.
func TestIgnoreMatcher_FIFORefusedNotOpened(t *testing.T) {
	dir := t.TempDir()
	work := t.TempDir()
	fifo := filepath.Join(work, "pipe.ignore")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unsupported here: %v", err)
	}
	real := filepath.Join(work, "real.ignore")
	writeIgnoreFile(t, real, "blocked\n")
	// The FIFO first, so a blocking open would stop the loop before the real
	// file is ever parsed.
	writeIgnoreSettings(t, dir, []string{fifo, real})

	m := NewMatcher(dir, work)

	if !m.Matches(t.Context(), "blocked", false) {
		t.Error("the ignore file listed after the FIFO was never parsed; the FIFO was not skipped")
	}
	if m.Matches(t.Context(), "unrelated", false) {
		t.Error("matcher blocked an unlisted path")
	}
}

// A symlink at an ignore-file name must not be read through.
//
// An ignore file is named by CONFIGURATION and lives in a directory the agent
// writes, so the name and its contents have different owners. Reading through the
// link would let a planted link decide the matcher's rules — and, with the size
// cap taken from a separate stat, decide them from a file the cap never judged.
func TestIgnoreMatcher_SymlinkAtIgnoreNameRefused(t *testing.T) {
	dir := t.TempDir()
	work := t.TempDir()
	elsewhere := filepath.Join(t.TempDir(), "planted")
	writeIgnoreFile(t, elsewhere, "smuggled\n")
	link := filepath.Join(work, "link.ignore")
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	writeIgnoreSettings(t, dir, []string{link})

	m := NewMatcher(dir, work)

	if m.Matches(t.Context(), "smuggled", false) {
		t.Error("matcher read rules through a symlink at the ignore-file name")
	}
}

// readIgnoreFile reports the mtime of the descriptor it read, not of a second
// stat on the name.
//
// The stamp is what change detection keys on, so a mtime newer than the bytes
// makes the next refresh believe it is current and pin a stale ruleset — a
// fail-open outcome in a filter whose job is to refuse reads.
func TestReadIgnoreFile_MTimeComesFromTheDescriptorRead(t *testing.T) {
	work := t.TempDir()
	path := filepath.Join(work, "a.ignore")
	writeIgnoreFile(t, path, "one\n")

	data, modTime, err := readIgnoreFile(path)
	if err != nil {
		t.Fatalf("readIgnoreFile(%q) = %v, want nil", path, err)
	}
	if string(data) != "one\n" {
		t.Errorf("data = %q, want %q", data, "one\n")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !modTime.Equal(info.ModTime()) {
		t.Errorf("modTime = %v, want the file's %v", modTime, info.ModTime())
	}
}
