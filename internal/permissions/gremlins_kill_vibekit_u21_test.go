package permissions

// Mutant-killing tests for unit vibekit-u21 (internal/permissions).
// Targets surviving gremlins mutants in command_rules.go,
// command_rules_eval.go, permissions_args.go, permissions_shell.go.
//
// Several targeted lines have purely-observational (log-only) effects,
// so a few tests intercept the default slog logger to assert on the
// emitted record. The capture helpers and every new identifier are
// prefixed gk_vibekit_u21_ to avoid colliding with sibling units that
// share this package.

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// --- slog capture (for log-only mutants) ---

// gk_vibekit_u21_logCapture is an in-memory slog.Handler that records
// every emitted record at every level.
type gk_vibekit_u21_logCapture struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *gk_vibekit_u21_logCapture) Enabled(context.Context, slog.Level) bool { return true }

func (h *gk_vibekit_u21_logCapture) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *gk_vibekit_u21_logCapture) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *gk_vibekit_u21_logCapture) WithGroup(string) slog.Handler      { return h }

// has reports whether any captured record at the given level contains
// msgSub in its message.
func (h *gk_vibekit_u21_logCapture) has(level slog.Level, msgSub string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.Level == level && strings.Contains(r.Message, msgSub) {
			return true
		}
	}
	return false
}

// attrInt returns the int64 value of the named attr on the first
// matching record (level + message substring), and whether it was found.
func (h *gk_vibekit_u21_logCapture) attrInt(level slog.Level, msgSub, key string) (int64, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.Level != level || !strings.Contains(r.Message, msgSub) {
			continue
		}
		var v int64
		var ok bool
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == key && a.Value.Kind() == slog.KindInt64 {
				v = a.Value.Int64()
				ok = true
				return false
			}
			return true
		})
		if ok {
			return v, true
		}
	}
	return 0, false
}

// gk_vibekit_u21_captureLogs installs an in-memory slog default logger
// and restores the previous default at test end. These tests must not
// run in parallel (global default logger).
func gk_vibekit_u21_captureLogs(t *testing.T) *gk_vibekit_u21_logCapture {
	t.Helper()
	h := &gk_vibekit_u21_logCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}

// gk_vibekit_u21_rules builds an in-memory CommandRules with the given
// entries (insertion order = evaluation order) without touching disk.
func gk_vibekit_u21_rules(entries ...Rule) *CommandRules {
	r := &CommandRules{}
	r.entries = entries
	r.entriesPtr.Store(&r.entries)
	return r
}

// === command_rules.go:178:35 — CONDITIONALS_NEGATION (e.Priority == prio) ===

func TestGk_vibekit_u21_AddUpdatesPriorityForSamePatternAndMode(t *testing.T) {
	dir := t.TempDir()
	r := NewCommandRules(dir)
	if err := r.Add("npm install", RuleAllow, 5); err != nil {
		t.Fatalf("first Add = %v, want nil", err)
	}
	// Same pattern + same mode but a DIFFERENT priority must update in
	// place. Original `e.Priority == prio` is false (5 == 7 → false), so the
	// no-op early-return is skipped and priority becomes 7. The mutant
	// `e.Priority != prio` is true (5 != 7), takes the early return, and
	// silently keeps priority 5.
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

// === command_rules.go:320:43 — CONDITIONALS_NEGATION (err != nil on legacy remove) ===

func TestGk_vibekit_u21_MigrateLegacyLogsMigrationComplete(t *testing.T) {
	dir := t.TempDir()
	legacy := `[{"pattern":"npm install","created_at":111}]`
	if err := os.WriteFile(filepath.Join(dir, "command-whitelist.json"), []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy whitelist: %v", err)
	}
	logs := gk_vibekit_u21_captureLogs(t)
	// load() finds no command-rules.json, migrates the legacy whitelist,
	// saves the new file, then removes the legacy file (succeeds → err==nil).
	_ = NewCommandRules(dir)

	// Original `err != nil && ...` is false on success, so the final Info
	// "migration complete" fires and no "removal failed" Warn appears. The
	// mutant `err == nil && ...` is true on success: it logs the Warn and
	// returns before the Info.
	if !logs.has(slog.LevelInfo, "migration complete") {
		t.Error("expected Info 'migration complete' after successful legacy removal")
	}
	if logs.has(slog.LevelWarn, "removal failed") {
		t.Error("unexpected Warn 'removal failed' on successful legacy removal")
	}
}

// === command_rules_eval.go:20:29 — CONDITIONALS_BOUNDARY & CONDITIONALS_NEGATION ===
// (e.Priority > bestPriority)

func TestGk_vibekit_u21_EvaluateCommandPriorityTieBreak(t *testing.T) {
	tests := []struct {
		name        string
		entries     []Rule
		command     string
		wantMode    RuleMode
		wantMatched bool
	}{
		{
			// Equal priority, deny listed first: with `>` the later allow does
			// NOT override (tie-break keeps deny). BOUNDARY `>=` and NEGATION
			// `<=` both let the later allow win → allow.
			name: "equal_priority_deny_first_deny_wins",
			entries: []Rule{
				{Pattern: "git *", Mode: RuleDeny, Priority: 5},
				{Pattern: "* --force", Mode: RuleAllow, Priority: 5},
			},
			command:     "git push --force",
			wantMode:    RuleDeny,
			wantMatched: true,
		},
		{
			// Lower-priority allow first, higher-priority deny second. Original
			// `9 > 1` true → deny wins. NEGATION `9 <= 1` false → allow stays.
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
			r := gk_vibekit_u21_rules(tt.entries...)
			gotMode, gotMatched := r.EvaluateCommand(tt.command)
			if gotMode != tt.wantMode || gotMatched != tt.wantMatched {
				t.Errorf("EvaluateCommand(%q) = (%q, %v), want (%q, %v)",
					tt.command, gotMode, gotMatched, tt.wantMode, tt.wantMatched)
			}
		})
	}
}

// === permissions_args.go:112:25 — CONDITIONALS_BOUNDARY & CONDITIONALS_NEGATION ===
// (len(s.TrustTools) > 0)

func TestGk_vibekit_u21_ArgsNoRejectWarnWhenTrustToolsEmpty(t *testing.T) {
	// trust-list mode, empty trust_tools: clean empty AND raw empty, so
	// `len(s.TrustTools) > 0` is false → no "entries rejected" Warn.
	// BOUNDARY `>= 0` and NEGATION `<= 0` both make `0 ? 0` true → wrong Warn.
	dir := writeSettings(t, `{"permission_mode":"trust-list","trust_tools":[]}`)
	logs := gk_vibekit_u21_captureLogs(t)
	_ = Args(context.Background(), dir)
	if logs.has(slog.LevelWarn, "all trust_tools entries rejected") {
		t.Error("unexpected 'entries rejected' Warn when trust_tools is empty")
	}
}

func TestGk_vibekit_u21_ArgsRejectWarnWhenAllTrustToolsInvalid(t *testing.T) {
	// trust-list mode, non-empty list of all-invalid names: clean empty but
	// raw > 0, so original `> 0` fires the Warn. NEGATION `<= 0` suppresses it.
	dir := writeSettings(t, `{"permission_mode":"trust-list","trust_tools":["bad name",":evil;"]}`)
	logs := gk_vibekit_u21_captureLogs(t)
	_ = Args(context.Background(), dir)
	if !logs.has(slog.LevelWarn, "all trust_tools entries rejected") {
		t.Error("expected 'entries rejected' Warn when all trust_tools entries are invalid")
	}
}

// === permissions_args.go:118:35 — ARITHMETIC_BASE & INVERT_NEGATIVES ===
// (len(s.TrustTools) - len(clean)) AND :118:57 NEGATION (dropped > 0)

func TestGk_vibekit_u21_ArgsDroppedDebugReportsRawMinusKept(t *testing.T) {
	// 3 raw entries, 1 invalid → kept 2, dropped = 3 - 2 = 1.
	// The Debug "dropped by sanitiser" line must fire (1 > 0) and report
	// dropped == 1. ARITHMETIC `+` logs 5; INVERT_NEGATIVES changes the
	// value (or sign, suppressing the line); CONDITIONALS_NEGATION on
	// `dropped > 0` (→ `<= 0`) suppresses the line entirely.
	dir := writeSettings(t, `{"permission_mode":"trust-list","trust_tools":["fsWrite","strReplace","bad name"]}`)
	logs := gk_vibekit_u21_captureLogs(t)
	got := Args(context.Background(), dir)
	if len(got) != 2 || got[0] != "--trust-tools" {
		t.Fatalf("Args = %v, want [--trust-tools <list>]", got)
	}
	v, ok := logs.attrInt(slog.LevelDebug, "dropped by sanitiser", "dropped")
	if !ok {
		t.Fatal("expected Debug 'dropped by sanitiser' with an int 'dropped' attr")
	}
	if v != 1 {
		t.Errorf("dropped attr = %d, want 1 (raw 3 - kept 2)", v)
	}
}

func TestGk_vibekit_u21_ArgsNoDroppedDebugWhenNothingDropped(t *testing.T) {
	// 2 raw entries, both valid → kept 2, dropped = 0. The Debug line must
	// NOT fire (original `0 > 0` false). BOUNDARY `>= 0` makes `0 >= 0` true
	// → wrong Debug line.
	dir := writeSettings(t, `{"permission_mode":"trust-list","trust_tools":["fsWrite","strReplace"]}`)
	logs := gk_vibekit_u21_captureLogs(t)
	got := Args(context.Background(), dir)
	if len(got) != 2 || got[0] != "--trust-tools" {
		t.Fatalf("Args = %v, want [--trust-tools <list>]", got)
	}
	if logs.has(slog.LevelDebug, "dropped by sanitiser") {
		t.Error("unexpected 'dropped by sanitiser' Debug when nothing was dropped")
	}
}

// === permissions_args.go:145:13 — CONDITIONALS_NEGATION (s.Mode != "") ===

func TestGk_vibekit_u21_ReadCoercesUnknownModeToTrustAll(t *testing.T) {
	// A present-but-invalid permission_mode must be coerced to trust-all via
	// `s.Mode != "" && !s.Mode.Valid()`. The mutant `s.Mode == ""` leaves a
	// non-empty unknown mode uncoerced. Observed via read() directly: Args'
	// own default branch also returns trust-all and would mask this.
	dir := writeSettings(t, `{"permission_mode":"bogus"}`)
	s := read(context.Background(), dir)
	if s.Mode != modeTrustAll {
		t.Errorf("read() Mode for unknown permission_mode = %q, want %q", s.Mode, modeTrustAll)
	}
}

// === permissions_args.go:151:51 — CONDITIONALS_NEGATION (trust_tools unmarshal err != nil) ===

func TestGk_vibekit_u21_ReadTrustToolsParseWarn(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantWarn bool
	}{
		{
			// Valid array → unmarshal succeeds (err == nil) → original does NOT
			// warn. Mutant `err == nil` wrongly warns on success.
			name:     "valid_array_no_warn",
			body:     `{"permission_mode":"trust-list","trust_tools":["fsWrite"]}`,
			wantWarn: false,
		},
		{
			// Wrong JSON type → unmarshal fails (err != nil) → original warns.
			// Mutant `err == nil` stays silent on failure.
			name:     "wrong_type_warns",
			body:     `{"permission_mode":"trust-list","trust_tools":"notarray"}`,
			wantWarn: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := writeSettings(t, tt.body)
			logs := gk_vibekit_u21_captureLogs(t)
			_ = read(context.Background(), dir)
			if got := logs.has(slog.LevelWarn, "parse trust_tools"); got != tt.wantWarn {
				t.Errorf("read() 'parse trust_tools' Warn = %v, want %v", got, tt.wantWarn)
			}
		})
	}
}

// === permissions_args.go:199:38,199:50,200:6,200:18 — CONDITIONALS_BOUNDARY ===
// (isToolNameIdentStart range comparisons)

func TestGk_vibekit_u21_IsToolNameIdentStartBoundaries(t *testing.T) {
	// Each boundary rune must be a valid identifier-start. The matching
	// CONDITIONALS_BOUNDARY flip turns the inclusive comparison exclusive at
	// exactly these runes, making the function return false.
	tests := []struct {
		name string
		r    rune
	}{
		{"upper_A_boundary", 'A'}, // r >= 'A'  (199:38)
		{"upper_Z_boundary", 'Z'}, // r <= 'Z'  (199:50)
		{"digit_0_boundary", '0'}, // r >= '0'  (200:6)
		{"digit_9_boundary", '9'}, // r <= '9'  (200:18)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !isToolNameIdentStart(tt.r) {
				t.Errorf("isToolNameIdentStart(%q) = false, want true", tt.r)
			}
		})
	}
}

// === permissions_shell.go:37:11 — CONDITIONALS_NEGATION (rules != nil) ===

func TestGk_vibekit_u21_EvaluateShellCommandReportsRuleDeny(t *testing.T) {
	// A non-nil rule set with a matching deny rule must yield Reason
	// "rule:deny". The first `if rules != nil` populates ruleMode/ruleMatched
	// from rules.Evaluate; the mutant `if rules == nil` skips it for a
	// non-nil rule set, leaving ruleMatched false so shellEvalReason can
	// never return "rule:deny".
	cfgDir := t.TempDir() // no config.json → safe_commands default policy
	rules := NewCommandRules(t.TempDir())
	if err := rules.Add("rm -rf /", RuleDeny); err != nil {
		t.Fatalf("Add deny rule: %v", err)
	}
	res := EvaluateShellCommand(context.Background(), cfgDir, "rm -rf /", rules)
	if res.Reason != "rule:deny" {
		t.Errorf("EvaluateShellCommand reason = %q, want \"rule:deny\"", res.Reason)
	}
}
