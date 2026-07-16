package permissions

import (
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/cplieger/slogx/capture"
	"pgregory.net/rapid"
)

// newRulesWithEntries builds an in-memory CommandRules with the given
// entries (insertion order = evaluation order) without touching disk.
func newRulesWithEntries(entries ...Rule) *CommandRules {
	r := &CommandRules{}
	r.entries = entries
	r.entriesPtr.Store(&r.entries)
	return r
}

func TestCommandRules_AllowMatchRemove(t *testing.T) {
	dir := t.TempDir()
	r := NewCommandRules(dir)

	// Initially empty.
	if r.MatchesAllow("npm install") {
		t.Error("should not match before adding")
	}

	// Add an allow pattern.
	if err := r.Add("npm *", RuleAllow); err != nil {
		t.Fatal(err)
	}
	if !r.MatchesAllow("npm install") {
		t.Error("should match after adding")
	}
	if !r.MatchesAllow("npm run build") {
		t.Error("should match prefix")
	}
	if r.MatchesAllow("node index.js") {
		t.Error("should not match unrelated command")
	}

	// Dedup: adding the same pattern with the same mode is a no-op.
	if err := r.Add("npm *", RuleAllow); err != nil {
		t.Fatal(err)
	}
	if len(r.List()) != 1 {
		t.Errorf("expected 1 entry after dedup, got %d", len(r.List()))
	}

	// Mode toggle: re-adding with RuleDeny flips the mode in place.
	if err := r.Add("npm *", RuleDeny); err != nil {
		t.Fatal(err)
	}
	if len(r.List()) != 1 {
		t.Errorf("expected 1 entry after mode flip, got %d", len(r.List()))
	}
	if r.MatchesAllow("npm install") {
		t.Error("should NOT match allow after flip to deny")
	}
	if !r.MatchesDeny("npm install") {
		t.Error("should match deny after flip")
	}

	// Remove.
	if err := r.Remove("npm *"); err != nil {
		t.Fatal(err)
	}
	if r.MatchesDeny("npm install") {
		t.Error("should not match after removing")
	}

	// Invalid mode.
	if err := r.Add("foo", RuleMode("bogus")); err == nil {
		t.Error("expected ErrInvalidMode, got nil")
	}
}

func TestCommandRules_Persistence(t *testing.T) {
	dir := t.TempDir()
	r := NewCommandRules(dir)
	_ = r.Add("git *", RuleAllow)
	_ = r.Add("*filter-repo*", RuleDeny)

	// Verify the new-format file was written.
	path := filepath.Join(dir, "command-rules.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("rules file not created: %v", err)
	}

	// Load a new instance from the same dir.
	r2 := NewCommandRules(dir)
	if !r2.MatchesAllow("git status") {
		t.Error("persisted allow should match after reload")
	}
	if !r2.MatchesDeny("git filter-repo --path x") {
		t.Error("persisted deny should match after reload")
	}
	if len(r2.List()) != 2 {
		t.Errorf("expected 2 entries after reload, got %d", len(r2.List()))
	}
}

func TestCommandRules_LegacyMigration(t *testing.T) {
	// A pre-existing command-whitelist.json should be read once, its
	// entries converted to allow-mode rules, written to the new file,
	// and deleted. Second open sees only the new file.
	dir := t.TempDir()
	legacy := []byte(`[{"pattern":"npm *","created_at":1},{"pattern":"git *","created_at":2}]`)
	if err := os.WriteFile(filepath.Join(dir, "command-whitelist.json"), legacy, 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewCommandRules(dir)
	entries := r.List()
	if len(entries) != 2 {
		t.Fatalf("expected 2 migrated entries, got %d", len(entries))
	}
	for _, e := range entries {
		if e.Mode != RuleAllow {
			t.Errorf("migrated entry %q has mode %q, want allow", e.Pattern, e.Mode)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "command-whitelist.json")); !os.IsNotExist(err) {
		t.Errorf("legacy file should have been removed, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "command-rules.json")); err != nil {
		t.Errorf("new-format file missing after migration: %v", err)
	}
}

// TestCommandRules_Add_UpdatesPriorityForSamePatternAndMode pins that
// re-adding an existing pattern+mode with a different priority updates
// the entry in place rather than no-op'ing or duplicating it.
func TestCommandRules_Add_UpdatesPriorityForSamePatternAndMode(t *testing.T) {
	dir := t.TempDir()
	r := NewCommandRules(dir)
	if err := r.Add("npm install", RuleAllow, 5); err != nil {
		t.Fatalf("first Add = %v, want nil", err)
	}
	if err := r.Add("npm install", RuleAllow, 7); err != nil {
		t.Fatalf("second Add = %v, want nil", err)
	}
	list := r.List()
	if len(list) != 1 {
		t.Fatalf("List() len = %d, want 1", len(list))
	}
	if list[0].Priority != 7 {
		t.Errorf("Add(same pattern/mode, prio 7): Priority = %d, want 7", list[0].Priority)
	}
	if list[0].Mode != RuleAllow {
		t.Errorf("Mode = %q, want %q", list[0].Mode, RuleAllow)
	}
}

// TestCommandRules_EvaluateCommand_PriorityTieBreak pins the
// priority-ordered evaluation: a higher-priority rule wins, and at equal
// priority deny wins over allow.
func TestCommandRules_EvaluateCommand_PriorityTieBreak(t *testing.T) {
	tests := []struct {
		name        string
		command     string
		wantMode    RuleMode
		entries     []Rule
		wantMatched bool
	}{
		{
			// Equal priority, deny listed first: deny wins the tie-break
			// over the later allow.
			name: "equal_priority_deny_wins",
			entries: []Rule{
				{Pattern: "git *", Mode: RuleDeny, Priority: 5},
				{Pattern: "* --force", Mode: RuleAllow, Priority: 5},
			},
			command:     "git push --force",
			wantMode:    RuleDeny,
			wantMatched: true,
		},
		{
			// Lower-priority allow first, higher-priority deny second:
			// the higher priority deny overrides the allow.
			name: "higher_priority_deny_overrides_lower_allow",
			entries: []Rule{
				{Pattern: "git *", Mode: RuleAllow, Priority: 1},
				{Pattern: "* --force", Mode: RuleDeny, Priority: 9},
			},
			command:     "git push --force",
			wantMode:    RuleDeny,
			wantMatched: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRulesWithEntries(tt.entries...)
			gotMode, gotMatched := r.EvaluateCommand(tt.command)
			if gotMode != tt.wantMode || gotMatched != tt.wantMatched {
				t.Errorf("EvaluateCommand(%q) = (%q, %v), want (%q, %v)",
					tt.command, gotMode, gotMatched, tt.wantMode, tt.wantMatched)
			}
		})
	}
}

// --- Load corruption + migration edge cases (T4) ---

func TestCommandRules_Load_CorruptRulesFileResetsToEmpty(t *testing.T) {
	// A corrupt current-format file must not silently migrate from
	// the legacy file (which would overwrite the broken-but-
	// recoverable data) and must not match-all. Empty store is the
	// safe fallback; a warn log records the failure.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "command-rules.json"),
		[]byte("this is not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewCommandRules(dir)

	if n := len(r.List()); n != 0 {
		t.Errorf("corrupt rules file: List len = %d, want 0 (fail-closed)", n)
	}
	if r.MatchesAllow("anything") {
		t.Error("corrupt rules file must NOT match-all on load")
	}
	// File should still exist (we don't clobber a file we failed to parse).
	if _, err := os.Stat(filepath.Join(dir, "command-rules.json")); err != nil {
		t.Errorf("rules file deleted on parse failure: %v", err)
	}
}

func TestCommandRules_Load_CorruptLegacyFileLeavesStoreEmpty(t *testing.T) {
	// No new-format file; legacy file present but malformed. Must
	// not migrate, must not delete, must not create the new file.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "command-whitelist.json"),
		[]byte("this is not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewCommandRules(dir)

	if n := len(r.List()); n != 0 {
		t.Errorf("corrupt legacy file: List len = %d, want 0", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "command-whitelist.json")); err != nil {
		t.Errorf("legacy file deleted on parse failure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "command-rules.json")); !os.IsNotExist(err) {
		t.Errorf("new-format file created despite corrupt legacy: err=%v", err)
	}
}

// --- Add/Remove boundary coverage (T3, T5) ---

func TestCommandRules_Add_EmptyPatternSilentlyIgnored(t *testing.T) {
	// Regression guard: the whole-package invariant is "an empty
	// pattern never lives in the store" — matchPattern("", cmd)
	// would otherwise wildcard-grant the allow list. Any future
	// refactor that skips the TrimSpace/empty check in Add or
	// normalizeRules fails loudly here instead of silently.
	dir := t.TempDir()
	r := NewCommandRules(dir)

	if err := r.Add("", RuleAllow); err != nil {
		t.Fatalf("Add(\"\") returned error %v, want nil no-op", err)
	}
	if err := r.Add("   ", RuleDeny); err != nil {
		t.Fatalf("Add(whitespace) returned error %v, want nil no-op", err)
	}
	if n := len(r.List()); n != 0 {
		t.Errorf("after empty Add, List len = %d, want 0", n)
	}
	if r.MatchesAllow("anything at all") {
		t.Error("empty allow rule must not match anything")
	}
	if r.MatchesAllow("") {
		t.Error("empty allow rule must not match empty command")
	}
}

func TestCommandRules_NormalizeRules_DropsEmptyAndUnknownModes(t *testing.T) {
	// Covers the two normalisation branches: empty patterns and
	// unknown modes. Unknown-mode entries are DROPPED (not coerced
	// to allow) so a typo like "deni" on a safety-net rule can't
	// silently become an auto-approve.
	in := []Rule{
		{Pattern: "", Mode: RuleAllow, CreatedAt: 1},
		{Pattern: "  ", Mode: RuleDeny, CreatedAt: 2},
		{Pattern: "  trim-me  ", Mode: RuleAllow, CreatedAt: 3},
		{Pattern: "deni-typo", Mode: RuleMode("deni"), CreatedAt: 4}, // dropped
		{Pattern: "blank-mode", Mode: "", CreatedAt: 5},              // dropped
		{Pattern: "ls", Mode: RuleAllow, CreatedAt: 6},
	}

	got := normalizeRules(in)

	want := []Rule{
		{Pattern: "trim-me", Mode: RuleAllow, CreatedAt: 3},
		{Pattern: "ls", Mode: RuleAllow, CreatedAt: 6},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("normalizeRules = %v, want %v", got, want)
	}
}

func TestCommandRules_Remove_UnknownPatternIsSilentNoOp(t *testing.T) {
	dir := t.TempDir()
	r := NewCommandRules(dir)
	_ = r.Add("npm *", RuleAllow)

	before := r.List()
	if err := r.Remove("does-not-exist"); err != nil {
		t.Errorf("Remove(unknown) returned %v, want nil", err)
	}
	after := r.List()

	if len(before) != len(after) {
		t.Errorf("Remove(unknown) mutated store: before=%d after=%d", len(before), len(after))
	}
	// Reload from disk to confirm state matches expectation.
	r2 := NewCommandRules(dir)
	if len(r2.List()) != len(before) {
		t.Errorf("after unknown Remove + reload: len=%d, want %d", len(r2.List()), len(before))
	}
}

func TestCommandRules_NormalizeTrimsHandEditedPatterns(t *testing.T) {
	// A user hand-editing command-rules.json might write
	// `"pattern": "  npm *  "`. normalizeRules must trim on load so
	// the stored and Add-path representations are identical — else
	// the hand-edited rule parses OK but never fires because
	// matches() trims the command but not the stored pattern.
	dir := t.TempDir()
	path := filepath.Join(dir, "command-rules.json")
	body := `[{"pattern":"  npm *  ","mode":"allow","created_at":1}]`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	r := NewCommandRules(dir)

	if !r.MatchesAllow("npm install") {
		t.Error("hand-edited pattern with surrounding whitespace should still match after load")
	}
	list := r.List()
	if len(list) != 1 || list[0].Pattern != "npm *" {
		t.Errorf("normalize should have trimmed pattern to %q, got %v", "npm *", list)
	}
}

// --- Save-failure rollback regression tests (test-u8c1-1, test-u8c1-2) ---
// These pin the in-memory-matches-disk invariant called out in the
// Add / Remove source comments. Save failure is triggered by
// chmod'ing the configDir read-only after the first successful save,
// which makes api.SaveBytes's writeTempFile fail with EACCES.

func TestCommandRules_Add_NewEntrySaveFailureRollback(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("chmod semantics not enforced for root")
	}
	// Regression: if saveLocked fails when adding a new entry, the
	// in-memory slice MUST be rolled back to the pre-Add state so
	// List() matches disk. A broken rollback would leave the new
	// entry in memory while it was never persisted, and
	// MatchesAllow would lie about its presence.
	dir := t.TempDir()
	r := NewCommandRules(dir)
	if err := r.Add("npm *", RuleAllow); err != nil {
		t.Fatal(err)
	}

	// Lock the dir so the next Add's saveLocked fails.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := r.Add("git *", RuleDeny)

	if err == nil {
		t.Fatal("Add(git *) with read-only dir = nil error, want error")
	}
	entries := r.List()
	if len(entries) != 1 {
		t.Fatalf("after failed Add, List len = %d, want 1 (rollback)", len(entries))
	}
	if entries[0].Pattern != "npm *" {
		t.Errorf("after failed Add, entries[0].Pattern = %q, want %q (rollback preserved original)",
			entries[0].Pattern, "npm *")
	}
	if r.MatchesDeny("git push") {
		t.Error("MatchesDeny(git push) = true after failed Add; rollback should have dropped the entry")
	}
}

func TestCommandRules_Add_ModeFlipSaveFailureRollback(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("chmod semantics not enforced for root")
	}
	// Regression: if saveLocked fails when flipping the mode of an
	// existing entry, the in-memory mode MUST revert. A broken
	// rollback would leave the on-disk mode out of sync with
	// List()/MatchesAllow/MatchesDeny — breaking the wire contract
	// with the Permissions UI and with shell evaluation.
	dir := t.TempDir()
	r := NewCommandRules(dir)
	if err := r.Add("npm *", RuleAllow); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := r.Add("npm *", RuleDeny)

	if err == nil {
		t.Fatal("Add(npm *, deny) with read-only dir = nil error, want error")
	}
	entries := r.List()
	if len(entries) != 1 {
		t.Fatalf("after failed flip, List len = %d, want 1", len(entries))
	}
	if entries[0].Mode != RuleAllow {
		t.Errorf("after failed flip, Mode = %q, want %q (rollback)",
			entries[0].Mode, RuleAllow)
	}
	if !r.MatchesAllow("npm install") {
		t.Error("MatchesAllow(npm install) = false after failed flip; rollback should have restored allow")
	}
	if r.MatchesDeny("npm install") {
		t.Error("MatchesDeny(npm install) = true after failed flip; rollback should have restored allow")
	}
}

func TestCommandRules_Remove_SaveFailureRollbackPreservesIndex(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("chmod semantics not enforced for root")
	}
	// Regression: if saveLocked fails during Remove, the removed
	// entry must be re-inserted at its ORIGINAL index, not just
	// appended to the tail. Order preservation is called out in
	// the source comment; the Permissions UI renders rules in
	// insertion order.
	dir := t.TempDir()
	r := NewCommandRules(dir)
	for _, p := range []struct {
		pat  string
		mode RuleMode
	}{
		{"npm *", RuleAllow},
		{"git *", RuleDeny},
		{"docker *", RuleAllow},
	} {
		if err := r.Add(p.pat, p.mode); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	// Remove the MIDDLE entry — catches trailing-append mutants.
	err := r.Remove("git *")

	if err == nil {
		t.Fatal("Remove with read-only dir = nil error, want error")
	}
	entries := r.List()
	if len(entries) != 3 {
		t.Fatalf("after failed Remove, List len = %d, want 3 (rollback)", len(entries))
	}
	wantPatterns := []string{"npm *", "git *", "docker *"}
	for i, w := range wantPatterns {
		if entries[i].Pattern != w {
			t.Errorf("entries[%d].Pattern = %q, want %q (rollback did not preserve index)",
				i, entries[i].Pattern, w)
		}
	}
	if entries[1].Mode != RuleDeny {
		t.Errorf("entries[1].Mode after rollback = %q, want %q",
			entries[1].Mode, RuleDeny)
	}
	if !r.MatchesDeny("git push") {
		t.Error("MatchesDeny(git push) = false after failed Remove; rollback should have kept deny rule")
	}
}

// --- load() error-branch regression tests (test-u8c1-3) ---

func TestCommandRules_Load_NonENOENTCurrentDoesNotTriggerLegacyMigration(t *testing.T) {
	// Regression: a non-ENOENT error on command-rules.json (here
	// simulated by making it a directory — os.ReadFile returns
	// EISDIR, which is neither nil nor fs.ErrNotExist) must NOT
	// fall through to the legacy-migration branch. Doing so would
	// silently overwrite a broken-but-recoverable current file
	// with stale legacy data. The explicit invariant is called
	// out in the source comment.
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "command-rules.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`[{"pattern":"npm *","created_at":1}]`)
	legacyPath := filepath.Join(dir, "command-whitelist.json")
	if err := os.WriteFile(legacyPath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	r := NewCommandRules(dir)

	if n := len(r.List()); n != 0 {
		t.Errorf("after non-ENOENT current-file error, List len = %d, want 0 (no migration)", n)
	}
	if r.MatchesAllow("npm install") {
		t.Error("MatchesAllow(npm install) = true; legacy entry was illegally migrated")
	}
	if _, err := os.Stat(legacyPath); err != nil {
		t.Errorf("legacy file removed despite failed migration: %v", err)
	}
}

func TestCommandRules_Load_NonENOENTLegacyErrorDoesNotCrash(t *testing.T) {
	// The legacy-read branch also has an explicit non-ENOENT
	// log-and-return path. A directory at command-whitelist.json
	// triggers it; the constructor must still return a usable
	// (empty) store without panicking.
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "command-whitelist.json"), 0o700); err != nil {
		t.Fatal(err)
	}

	r := NewCommandRules(dir)

	if n := len(r.List()); n != 0 {
		t.Errorf("legacy dir-as-file: List len = %d, want 0", n)
	}
	if r.MatchesAllow("anything") {
		t.Error("non-ENOENT legacy error must not match-all")
	}
}

func TestCommandRules_Load_MigrationSaveFailurePreservesLegacy(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("chmod semantics not enforced for root")
	}
	// Regression: if the post-migration saveLocked fails, the
	// legacy file MUST stay on disk so the next boot re-migrates.
	// Without this, a transient disk-full or permission event
	// during first boot permanently loses the user's whitelist.
	dir := t.TempDir()
	legacy := []byte(`[{"pattern":"npm *","created_at":1},{"pattern":"git *","created_at":2}]`)
	legacyPath := filepath.Join(dir, "command-whitelist.json")
	if err := os.WriteFile(legacyPath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	r := NewCommandRules(dir)

	entries := r.List()
	if len(entries) != 2 {
		t.Errorf("expected 2 migrated entries in memory, got %d: %v", len(entries), entries)
	}
	for _, e := range entries {
		if e.Mode != RuleAllow {
			t.Errorf("migrated entry %q: mode = %q, want allow", e.Pattern, e.Mode)
		}
	}
	if _, err := os.Stat(legacyPath); err != nil {
		t.Errorf("legacy file removed despite save failure: %v (next boot would lose the whitelist)", err)
	}
}

// TestCommandRules_Migrate_LogsMigrationComplete pins that a successful
// legacy migration logs Info "migration complete" after removing the
// legacy file and never logs a removal-failure Warn.
func TestCommandRules_Migrate_LogsMigrationComplete(t *testing.T) {
	dir := t.TempDir()
	legacy := `[{"pattern":"npm install","created_at":111}]`
	if err := os.WriteFile(filepath.Join(dir, "command-whitelist.json"), []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy whitelist: %v", err)
	}
	logs := capture.Default(t)
	_ = NewCommandRules(dir)

	if !hasLog(logs, slog.LevelInfo, "migration complete") {
		t.Error("expected Info 'migration complete' after successful legacy removal")
	}
	if hasLog(logs, slog.LevelWarn, "removal failed") {
		t.Error("unexpected Warn 'removal failed' on successful legacy removal")
	}
}

func TestCommandRules_RapidAddRemoveInvariants(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		dir := t.TempDir()
		r := NewCommandRules(dir)

		// Generate a sequence of operations.
		nOps := rapid.IntRange(1, 20).Draw(rt, "nOps")
		for range nOps {
			pattern := rapid.StringMatching(`[a-z]{1,10}( \*)?`).Draw(rt, "pattern")
			op := rapid.IntRange(0, 2).Draw(rt, "op")

			switch op {
			case 0: // Add allow
				_ = r.Add(pattern, RuleAllow)
				// Invariant 1: after Add(p, allow), MatchesAllow(p) == true.
				if !r.MatchesAllow(pattern) {
					rt.Fatalf("after Add(%q, allow), MatchesAllow returned false", pattern)
				}
			case 1: // Add deny
				_ = r.Add(pattern, RuleDeny)
				// After Add(p, deny), MatchesDeny(p) == true.
				if !r.MatchesDeny(pattern) {
					rt.Fatalf("after Add(%q, deny), MatchesDeny returned false", pattern)
				}
			case 2: // Remove
				_ = r.Remove(pattern)
				// Invariant 2: after Remove(p), neither matches.
				if r.MatchesAllow(pattern) {
					rt.Fatalf("after Remove(%q), MatchesAllow returned true", pattern)
				}
				if r.MatchesDeny(pattern) {
					rt.Fatalf("after Remove(%q), MatchesDeny returned true", pattern)
				}
			}
		}

		// Invariant 3: List has no duplicate patterns.
		list := r.List()
		seen := make(map[string]int)
		for _, rule := range list {
			seen[rule.Pattern]++
			if seen[rule.Pattern] > 1 {
				rt.Fatalf("duplicate pattern in List: %q", rule.Pattern)
			}
		}
	})
}
