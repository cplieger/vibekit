package permissions

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	cfgsettings "github.com/cplieger/vibekit/internal/settings"
)

// writeSettings writes a config.json file in a fresh temp dir and
// returns its config dir path. Keeps the tests fast and isolated.
func writeSettings(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if body != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
			t.Fatalf("write settings: %v", err)
		}
	}
	return dir
}

// --- Args() ---

func TestArgs_FailModes(t *testing.T) {
	tests := []struct {
		name     string
		settings string
		want     []string
		corrupt  bool
		noFile   bool
	}{
		{"MissingSettingsFallsOpenToTrustAll", "", []string{"--trust-all-tools"}, false, true},
		{"UnreadableSettingsFallsOpenToTrustAll", "{not json", []string{"--trust-all-tools"}, true, false},
		{"UnsetModeFallsOpenToTrustAll", `{"some_other_field":"x"}`, []string{"--trust-all-tools"}, false, false},
		{"UnknownModeFallsOpenToTrustAll", `{"permission_mode":"yolo"}`, []string{"--trust-all-tools"}, false, false},
		{"PromptModeReturnsEmpty", `{"permission_mode":"prompt"}`, []string{}, false, false},
		{"TrustAllModeReturnsFlag", `{"permission_mode":"trust-all"}`, []string{"--trust-all-tools"}, false, false},
		{"TrustListEmptyFallsToPrompt", `{"permission_mode":"trust-list","trust_tools":[]}`, []string{}, false, false},
		{"MalformedPermissionModeFallsOpenToTrustAll", `{"permission_mode":42}`, []string{"--trust-all-tools"}, false, false},
		{"MalformedTrustToolsFallsOpenToTrustAll", `{"trust_tools":"fsWrite"}`, []string{"--trust-all-tools"}, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var dir string
			switch {
			case tt.noFile:
				dir = t.TempDir()
			case tt.corrupt:
				dir = t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(tt.settings), 0o600); err != nil {
					t.Fatal(err)
				}
			default:
				dir = writeSettings(t, tt.settings)
			}
			got := Args(context.Background(), dir)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Args() = %v, want %v", got, tt.want)
			}
			// Non-nil empty slice contract for prompt-mode cases.
			if len(tt.want) == 0 && got == nil {
				t.Error("Args returned nil; contract says non-nil empty slice")
			}
		})
	}
}

func TestArgs_TrustListPopulatesToolsFlag(t *testing.T) {
	dir := writeSettings(t, `{"permission_mode":"trust-list","trust_tools":["fsWrite","strReplace","executePwsh"]}`)
	got := Args(context.Background(), dir)
	if len(got) != 2 || got[0] != "--trust-tools" {
		t.Fatalf("Args = %v, want [--trust-tools, <list>]", got)
	}
	names := strings.Split(got[1], ",")
	wantAll := []string{"fsWrite", "strReplace", "executePwsh"}
	for _, w := range wantAll {
		if !slices.Contains(names, w) {
			t.Errorf("tool list missing %q: %v", w, names)
		}
	}
}

func TestArgs_TrustListSanitisesList(t *testing.T) {
	// Trailing whitespace, duplicates, and non-alnum garbage all get cleaned.
	dir := writeSettings(t, `{"permission_mode":"trust-list","trust_tools":[" fsWrite ","fsWrite","bad name","",":evil;"]}`)
	got := Args(context.Background(), dir)
	if len(got) != 2 {
		t.Fatalf("Args = %v, want two elements", got)
	}
	names := strings.Split(got[1], ",")
	if !slices.Contains(names, "fsWrite") {
		t.Errorf("expected fsWrite in %v", names)
	}
	for _, n := range names {
		if strings.ContainsAny(n, " :;") || n == "" {
			t.Errorf("sanitise failed, tool name %q still has bad chars", n)
		}
	}
	// Dedup: fsWrite appears exactly once.
	count := 0
	for _, n := range names {
		if n == "fsWrite" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected fsWrite once after dedup, got %d in %v", count, names)
	}
}

// --- cleanList and validToolName ---

func TestCleanList_DropsEmptyAndWhitespace(t *testing.T) {
	got := cleanList([]string{"", "  ", "\t", "ok"})
	want := []string{"ok"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cleanList = %v, want %v", got, want)
	}
}

func TestCleanList_PreservesOrder(t *testing.T) {
	got := cleanList([]string{"zeta", "alpha", "beta"})
	want := []string{"zeta", "alpha", "beta"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cleanList did not preserve insertion order: %v", got)
	}
}

func TestValidToolName(t *testing.T) {
	tests := []struct {
		input string
		name  string
		want  bool
	}{
		{"fsWrite", "simple", true},
		{"strReplace", "camelCase", true},
		{"tool_with_underscore", "underscore", true},
		{"tool-with-dash", "dash", true},
		{"tool.with.dot", "dot", true},
		{"Tool123", "digits_mixed_case", true},
		{"", "empty", false},
		{"tool with space", "space", false},
		{"tool;rm", "semicolon", false},
		{"tool/path", "slash", false},
		{"tool$var", "dollar", false},
		{"tool|pipe", "pipe", false},
		{strings.Repeat("a", 128), "at_cap", true},
		{strings.Repeat("a", 129), "over_cap", false},
		// F2: leading hyphen/dot rejected as defense-in-depth against
		// tool names that could look like CLI flags or hidden-file
		// idioms to a lax downstream parser. Kiro-cli identifiers
		// are camelCase (fsWrite, executePwsh) — nothing legitimate
		// starts with `-` or `.`.
		{"-foo", "leading_hyphen_rejected", false},
		{"--version", "double_leading_hyphen_rejected", false},
		{".hidden", "leading_dot_rejected", false},
		{"..up", "double_leading_dot_rejected", false},
		{"123tool", "leading_digit_still_ok", true},
		{"_internal", "leading_underscore_still_ok", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validToolName(tt.input); got != tt.want {
				t.Errorf("validToolName(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestCleanList_EnforcesLengthCap(t *testing.T) {
	long := strings.Repeat("x", 200)
	got := cleanList([]string{"ok", long})
	if len(got) != 1 || got[0] != "ok" {
		t.Errorf("cleanList did not drop overlong name: %v", got)
	}
}

func TestValidToolName_MCPLongNamesAccepted(t *testing.T) {
	// Regression (ops-perm-03): the MCP wire format
	// `mcp__<server>__<tool>` is user-chosen on both components;
	// legitimate names easily pass 64 chars (e.g. a server like
	// "github-enterprise-read-write-replica" plus a tool like
	// "list_pull_request_review_threads" is ~76 chars). The 128-
	// char cap must accept these while still rejecting runaway
	// strings.
	mcp76 := "mcp__github-enterprise-read-write-replica__list_pull_request_review_threads"
	if !validToolName(mcp76) {
		t.Errorf("validToolName(%q) = false, want true (76-char MCP name below 128 cap)", mcp76)
	}
	if !validToolName("mcp__" + strings.Repeat("a", 123)) {
		t.Error("128-char name at cap must be accepted")
	}
	if validToolName("mcp__" + strings.Repeat("a", 124)) {
		t.Error("129-char name must be rejected (above 128 cap)")
	}
}

func TestArgs_MalformedTrustToolsInTrustListModeFallsToPrompt(t *testing.T) {
	// Invariant: when the mode is set to trust-list but trust_tools is the wrong
	// type, the field unmarshal fails, TrustTools stays nil, cleanList returns
	// empty, and the empty-list branch in trust-list mode maps to "prompt for
	// everything" (empty slice). Partial corruption in trust-list mode must NOT
	// silently escalate privileges from trust-list (restrictive) to trust-all
	// (permissive) — the user chose trust-list, we honor it.
	dir := writeSettings(t, `{"permission_mode":"trust-list","trust_tools":"fsWrite"}`)

	got := Args(context.Background(), dir)

	if len(got) != 0 {
		t.Errorf("Args with trust-list mode + malformed trust_tools = %v, want empty", got)
	}
}

func TestArgs_TrustListAllEntriesRejectedFallsToPrompt(t *testing.T) {
	// Invariant: trust-list mode with a non-empty list where every entry
	// fails cleanList's filters must return the prompt fallback (empty
	// slice), not trust-all. Partial corruption inside trust-list mode
	// never escalates privileges. Exercises the slog.Warn downgrade
	// branch in Args that existing tests don't hit (empty-list and
	// wrong-type paths skip it; sanitise-keeps-some paths skip it too).
	tests := []struct {
		name       string
		trustTools string // raw JSON array body
	}{
		{
			name:       "all entries contain invalid characters",
			trustTools: `[" bad name ",":evil;","tool/path","tool|pipe"]`,
		},
		{
			name:       "all entries exceed length cap",
			trustTools: `["` + strings.Repeat("x", 129) + `","` + strings.Repeat("y", 200) + `"]`,
		},
		{
			name:       "mix of over-length and invalid chars",
			trustTools: `["` + strings.Repeat("a", 129) + `","tool with space"]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := writeSettings(t, `{"permission_mode":"trust-list","trust_tools":`+tt.trustTools+`}`)

			got := Args(context.Background(), dir)

			if len(got) != 0 {
				t.Errorf("Args with trust-list + all-rejected entries = %v, want empty slice (downgrade to prompt)", got)
			}
			if got == nil {
				t.Errorf("Args returned nil; contract requires non-nil empty slice")
			}
		})
	}
}

// --- FuzzValidToolName ---

func FuzzValidToolName(f *testing.F) {
	// Seed corpus: representative valid and invalid names.
	f.Add("fsWrite")
	f.Add("mcp__server__tool")
	f.Add("")
	f.Add("-flag")
	f.Add(".hidden")
	f.Add(strings.Repeat("a", 128))
	f.Add(strings.Repeat("a", 129))
	f.Add("tool with space")
	f.Add("tool;rm")

	f.Fuzz(func(t *testing.T, s string) {
		got := validToolName(s)

		// Invariant 1: never panics (implicit — reaching here means no panic).

		// Invariant 2: idempotent.
		if validToolName(s) != got {
			t.Errorf("validToolName(%q) not idempotent", s)
		}

		// Invariant 3: if valid, enforce character allowlist and constraints.
		if got {
			if len(s) == 0 || len(s) > 128 {
				t.Errorf("validToolName(%q) = true but len %d outside [1,128]", s, len(s))
			}
			if s[0] == '-' || s[0] == '.' {
				t.Errorf("validToolName(%q) = true but starts with %q", s, s[0])
			}
			for _, r := range s {
				if !isToolNameRune(r) {
					t.Errorf("validToolName(%q) = true but contains invalid rune %q", s, r)
				}
			}
		}

		// Invariant 4: empty string always returns false.
		if s == "" && got {
			t.Error("validToolName(\"\") = true, want false")
		}
	})
}

// --- SupervisedDefault ---
//
// Every branch is a documented "fail-closed to false" fallback. The
// function is called on every new-chat auto-create, so missing-file,
// malformed-JSON, missing-key, wrong-type, and both true/false values
// need pinning so a future refactor that accidentally flips a branch
// to fail-open is caught immediately.

func TestSupervisedDefault(t *testing.T) {
	tests := []struct {
		name     string
		settings string // JSON body; "" means no file written
		useEmpty bool   // true → pass "" as configDir (empty-dir short-circuit)
		noFile   bool   // true → use TempDir with no config.json
		want     bool
	}{
		{"EmptyConfigDirReturnsFalse", "", true, false, false},
		{"MissingSettingsReturnsFalse", "", false, true, false},
		{"MalformedJSONReturnsFalse", `{not json`, false, false, false},
		{"MissingKeyReturnsFalse", `{"other_field":true}`, false, false, false},
		{"WrongTypeReturnsFalse", `{"supervised_default":42}`, false, false, false},
		{"TrueReturnsTrue", `{"supervised_default":true}`, false, false, true},
		{"FalseReturnsFalse", `{"supervised_default":false}`, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var dir string
			switch {
			case tt.useEmpty:
				dir = ""
			case tt.noFile:
				dir = t.TempDir()
			default:
				dir = writeSettings(t, tt.settings)
			}
			if got := SupervisedDefault(context.Background(), dir); got != tt.want {
				t.Errorf("SupervisedDefault() = %v, want %v", got, tt.want)
			}
		})
	}
}

// --- Non-ENOENT settings read-error fail-mode (test-u8c1-5) ---

func TestSettingsReaders_NonENOENTReadErrorHonoursFailMode(t *testing.T) {
	// Regression: when config.json exists but cannot be read
	// (here simulated by making it a directory, which returns
	// EISDIR — a non-ENOENT error), every reader must land on
	// its documented fail-mode. The asymmetry is deliberate:
	//   Args                → fail OPEN (--trust-all-tools)
	//   readShellPolicy     → fail CLOSED (safe_commands)
	//   SupervisedDefault   → fail CLOSED (false)
	// A regression that flipped any CLOSED branch to OPEN would
	// silently grant shell auto-approval or disable the
	// Supervised gate.
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "config.json"), 0o700); err != nil {
		t.Fatal(err)
	}

	// Args: fail OPEN → [--trust-all-tools]
	got := Args(context.Background(), dir)
	want := []string{"--trust-all-tools"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Args(dir-as-settings) = %v, want %v (fail-open)", got, want)
	}

	// readShellPolicy via EvaluateShellCommand: fail CLOSED to
	// safe_commands. `ls` auto-approves under safe_commands;
	// `rm -rf /` prompts. The pair proves we landed on Safe, not
	// All (which would allow rm) nor None (which would deny ls).
	r := NewCommandRules(dir)
	if d := EvaluateShellCommand(context.Background(), dir, "ls", r); d.Decision != "allow" {
		t.Errorf("EvaluateShellCommand(ls) with dir-as-settings = %q, want \"allow\" (safe_commands fail-closed)", d.Decision)
	}
	if d := EvaluateShellCommand(context.Background(), dir, "rm -rf /", r); d.Decision != "ask" {
		t.Errorf("EvaluateShellCommand(rm -rf /) with dir-as-settings = %q, want \"ask\" (safe_commands fail-closed)", d.Decision)
	}

	// SupervisedDefault: fail CLOSED to false.
	if SupervisedDefault(context.Background(), dir) {
		t.Error("SupervisedDefault(context.Background(), dir-as-settings) = true, want false (fail-closed)")
	}
}

func TestReadSettingsBytes_EmptyConfigDirReturnsNil(t *testing.T) {
	// Regression (ops-perm-05): empty configDir must short-circuit
	// to (nil, nil) instead of filepath.Join("", "config.json")
	// → "config.json" which would read the process's PWD. Every
	// reader built on cfgsettings.ReadBytes then picks its own
	// fallback.
	data, err := cfgsettings.ReadBytes(context.Background(), "")
	if err != nil {
		t.Errorf("cfgsettings.ReadBytes(\"\") err = %v, want nil", err)
	}
	if data != nil {
		t.Errorf("cfgsettings.ReadBytes(\"\") data = %v, want nil", data)
	}
	// SupervisedDefault, Args, readShellPolicy all inherit the
	// short-circuit; pin their fallbacks.
	if SupervisedDefault(context.Background(), "") {
		t.Error("SupervisedDefault(context.Background(), \"\") = true, want false")
	}
	if got, want := Args(context.Background(), ""), []string{"--trust-all-tools"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Args(\"\") = %v, want %v (fail-open)", got, want)
	}
}

// isToolNameRune mirrors validToolName's per-character predicate:
// ASCII letters, digits, underscore, dash, or dot. Factored out of
// the fuzz/property test to satisfy staticcheck QF1001 (the inline
// `!(A || B || C)` form is De-Morgan-equivalent to `!A && !B && !C`
// but the latter is harder to read across six clauses).
func isToolNameRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
		return true
	case r >= 'A' && r <= 'Z':
		return true
	case r >= '0' && r <= '9':
		return true
	case r == '_' || r == '-' || r == '.':
		return true
	}
	return false
}
