package policyfile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestWorkspaceHash(t *testing.T) {
	// Mirrors KAS computeWorkspaceHash(normalizePath(abs)) on Linux:
	// hex(sha256(abs))[:16].
	dir := "/tmp/vk-ws"
	want := hex.EncodeToString(func() []byte { s := sha256.Sum256([]byte(dir)); return s[:] }())[:16]
	if got := WorkspaceHash(dir); got != want {
		t.Errorf("WorkspaceHash(%q) = %q, want %q", dir, got, want)
	}
	if len(WorkspaceHash("/x")) != 16 {
		t.Errorf("hash length != 16")
	}
	// A relative KIRO_WORK_DIR is resolved against the process cwd BEFORE it is
	// hashed, so it lands on the same directory KAS computes for the absolute
	// form. Asserting only that the hash is non-empty would pass for a hash of
	// the relative text itself, which is the divergence that writes
	// workspace-scope rules where KAS never reads them.
	t.Run("a relative work dir hashes as its absolute form", func(t *testing.T) {
		base := t.TempDir()
		t.Chdir(base)
		want := WorkspaceHash(filepath.Join(base, "rel"))
		if got := WorkspaceHash("rel"); got != want {
			t.Errorf("WorkspaceHash(%q) = %q, want %q (the hash of %q)",
				"rel", got, want, filepath.Join(base, "rel"))
		}
	})
}

// TestWorkspaceHashGolden pins WorkspaceHash to a hardcoded value for the real
// default workspace root (/workspace, KIRO_WORK_DIR's default). This is the
// KAS-agreement contract: vibekit must write workspace-scope permissions.yaml
// under workspace-roots/<this hash>/ or KAS silently never reads the rules
// (they persist but are never enforced). The literal is hex(sha256("/workspace"))[:16];
// any change to the canonicalization or the hash breaks this test loudly
// rather than diverging from KAS in silence.
func TestWorkspaceHashGolden(t *testing.T) {
	const golden = "c52ddf65534b7b46" // hex(sha256("/workspace"))[:16]
	if got := WorkspaceHash("/workspace"); got != golden {
		t.Errorf("WorkspaceHash(%q) = %q, want golden %q", "/workspace", got, golden)
	}
	// Non-canonical spellings of the SAME root must canonicalize (absolute,
	// cleaned, no trailing slash) to the identical hash — otherwise a stray
	// trailing slash or "/a/../b" form in KIRO_WORK_DIR would strand
	// workspace-scope rules in a directory KAS never reads.
	for _, variant := range []string{
		"/workspace/",       // trailing slash
		"/workspace/.",      // "." segment
		"/workspace//",      // duplicate + trailing slash
		"/tmp/../workspace", // ".." segment
	} {
		if got := WorkspaceHash(variant); got != golden {
			t.Errorf("WorkspaceHash(%q) = %q, want %q (must canonicalize to /workspace)", variant, got, golden)
		}
	}
}

func TestPathFor(t *testing.T) {
	home := "/home/u"
	wd := "/work/space"
	up, err := PathFor(ScopeUser, Roots{Home: home, WorkDir: wd})
	if err != nil || up != filepath.Join(home, ".kiro", "settings", "permissions.yaml") {
		t.Errorf("user path = %q, err = %v", up, err)
	}
	wp, err := PathFor(ScopeWorkspace, Roots{Home: home, WorkDir: wd})
	wantWP := filepath.Join(home, ".kiro", "workspace-roots", WorkspaceHash(wd), "permissions.yaml")
	if err != nil || wp != wantWP {
		t.Errorf("workspace path = %q, want %q, err = %v", wp, wantWP, err)
	}
	if _, err := PathFor("agent", Roots{Home: home, WorkDir: wd}); err != ErrInvalidScope {
		t.Errorf("agent scope err = %v, want ErrInvalidScope", err)
	}
}

func TestLoadMissingIsEmpty(t *testing.T) {
	f, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if f == nil || len(f.Rules) != 0 {
		t.Errorf("missing file should yield empty File, got %+v", f)
	}
}

func TestLoadBlockYAML(t *testing.T) {
	// The format a hand-editing user (or KAS) writes.
	path := filepath.Join(t.TempDir(), "permissions.yaml")
	body := "rules:\n  - capability: fs_write\n    effect: allow\n    match:\n      - \"src/**\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(f.Rules) != 1 || f.Rules[0].Capability != "fs_write" || f.Rules[0].Effect != "allow" ||
		len(f.Rules[0].Match) != 1 || f.Rules[0].Match[0] != "src/**" {
		t.Errorf("parsed rules = %+v", f.Rules)
	}
}

func TestLoadJSONInYAML(t *testing.T) {
	// JSON is valid YAML 1.2 — Load must accept vibekit's own writes even if
	// they were emitted as JSON.
	path := filepath.Join(t.TempDir(), "permissions.yaml")
	if err := os.WriteFile(path, []byte(`{"rules":[{"capability":"shell","effect":"ask"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(f.Rules) != 1 || f.Rules[0].Capability != "shell" || f.Rules[0].Effect != "ask" {
		t.Errorf("parsed rules = %+v", f.Rules)
	}
}

func TestLoadEmptyIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "permissions.yaml")
	if err := os.WriteFile(path, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := Load(path)
	if err != nil || len(f.Rules) != 0 {
		t.Errorf("empty file: rules=%+v err=%v", f, err)
	}
}

func TestLoadMalformedErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "permissions.yaml")
	if err := os.WriteFile(path, []byte("rules: [ this is : not : valid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Errorf("malformed file should error (never silently clobber)")
	}
}

func TestSaveRoundtripAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "permissions.yaml") // sub dir auto-created
	in := &File{Rules: []Rule{
		{Capability: "fs_write", Effect: "ask", Match: []string{"src/**"}},
		{Capability: "shell", Effect: "deny", Match: []string{"rm -rf *"}},
	}}
	if err := Save(t.Context(), path, in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %o, want 0600 (policy is sensitive)", fi.Mode().Perm())
	}
	out, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(out.Rules) != 2 || out.Rules[0].Capability != "fs_write" || out.Rules[1].Effect != "deny" {
		t.Errorf("roundtrip rules = %+v", out.Rules)
	}
}

func TestSanitizeRule(t *testing.T) {
	if _, err := SanitizeRule(&Rule{Capability: "shell", Effect: "yolo"}); err == nil {
		t.Error("invalid effect should error")
	}
	if _, err := SanitizeRule(&Rule{Capability: "shell", Effect: "ask", Match: []string{strings.Repeat("a", maxPatternLen+1)}}); err == nil {
		t.Error("over-long pattern should error")
	}
	if _, err := SanitizeRule(&Rule{Capability: "shell", Effect: "ask", Match: []string{"a\x00b"}}); err == nil {
		t.Error("control char should error")
	}
	// Trims + de-dups + drops empties.
	got, err := SanitizeRule(&Rule{Capability: "fs_read", Effect: "allow", Match: []string{" a ", "a", "", "b"}})
	if err != nil {
		t.Fatalf("SanitizeRule: %v", err)
	}
	if len(got.Match) != 2 || got.Match[0] != "a" || got.Match[1] != "b" {
		t.Errorf("sanitized match = %v, want [a b]", got.Match)
	}
}

// TestSanitizeRule_RefusesAnAllEmptyPatternList closes the widening side of the
// shape check. Every entry trimming away used to return nil, and a nil Match
// serialises with no `match` key (yaml:"match,omitempty"), which KAS reads as
// `**` — so `match:[""]`, whose literal reading is "matches nothing", wrote the
// broadest grant the file can express. The same function already refuses an
// empty CAPABILITY, so the lenient side was the widening one.
func TestSanitizeRule_RefusesAnAllEmptyPatternList(t *testing.T) {
	cases := map[string][]string{
		"one empty string":       {""},
		"whitespace only":        {"   "},
		"several, all blank":     {"", " ", "\t"},
		"a comma-split leftover": {"", ""},
	}
	for name, patterns := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := SanitizeRule(&Rule{Capability: "all", Effect: EffectAllow, Match: patterns}); !errors.Is(err, ErrPatternEmpty) {
				t.Errorf("match %q: err = %v, want ErrPatternEmpty", patterns, err)
			}
			if _, err := SanitizeRule(&Rule{Capability: "all", Effect: EffectAllow, Exclude: patterns}); !errors.Is(err, ErrPatternEmpty) {
				t.Errorf("exclude %q: err = %v, want ErrPatternEmpty", patterns, err)
			}
		})
	}
}

// TestSanitizeRule_BareRuleStaysWritable is the other half of the condition, and
// it is the assertion that fails if the refusal is ever keyed on the OUTPUT being
// empty instead of on the caller having supplied something. An ABSENT match list
// is legitimate: relaxRules constructs exactly this shape for the loosest
// profile rung, and it is the intended broad grant.
func TestSanitizeRule_BareRuleStaysWritable(t *testing.T) {
	got, err := SanitizeRule(&Rule{Capability: "all", Effect: EffectAllow})
	if err != nil {
		t.Fatalf("SanitizeRule(bare rule) = %v, want it written", err)
	}
	if got.Match != nil || got.Exclude != nil {
		t.Errorf("sanitized = %+v, want nil Match and nil Exclude", got)
	}
	// The in-repo producer of bare rules, so the assertion tracks the real caller
	// rather than a hand-built shape that resembles it.
	for _, r := range relaxRules() {
		if _, err := SanitizeRule(&r); err != nil {
			t.Errorf("SanitizeRule(%+v) = %v, want it written", r, err)
		}
	}
}

// TestSanitizeRule_ForwardsUnrecognisedCapability is the T67 inversion. This
// used to be a 400. The capability vocabulary is KAS's — it validates on load and
// SKIPS an unrecognised rule as non-fatal, reporting it on
// _kiro/policy/changed's errors array rather than on _kiro/policy/error (which is
// fatal-only); see SanitizeRule's doc comment. So refusing here only meant
// vibekit could not write a rule for any capability newer than its own
// hand-copied list, which is exactly the rule a new capability exists for.
func TestSanitizeRule_ForwardsUnrecognisedCapability(t *testing.T) {
	// "hooks" is the concrete case: an upstream security report asked for it, and
	// under the old check vibekit would have refused the rule that uses it.
	for _, capability := range []string{"hooks", "some_future_capability", "nope"} {
		got, err := SanitizeRule(&Rule{Capability: capability, Effect: "deny"})
		if err != nil {
			t.Errorf("SanitizeRule(%q) = %v, want it forwarded to KAS", capability, err)
			continue
		}
		if got.Capability != capability {
			t.Errorf("capability = %q, want %q verbatim", got.Capability, capability)
		}
	}
}

// TestSanitizeRule_RejectsMalformedCapability pins the line the T67 change did
// NOT cross. A vocabulary check is KAS's; a SHAPE check is vibekit's, same class
// as the pattern checks. None of these is a capability KAS could ever have, so
// writing one only puts a rule in a security policy file that the user has to
// hand-edit out.
func TestSanitizeRule_RejectsMalformedCapability(t *testing.T) {
	cases := map[string]string{
		"empty":                  "",
		"whitespace only":        "   ",
		"a control character":    "fs_\x00write",
		"a newline (YAML break)": "fs_write\nshell",
		"over the length cap":    strings.Repeat("a", maxCapabilityLen+1),
	}
	for name, capability := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := SanitizeRule(&Rule{Capability: capability, Effect: "ask"}); !errors.Is(err, ErrCapabilityShape) {
				t.Errorf("err = %v, want ErrCapabilityShape", err)
			}
		})
	}
}

// TestSanitizeRule_TrimsCapability: the trim has to happen before the rule is
// written, because Signature is a byte comparison — " shell" and "shell" would
// otherwise be two distinct rules the user cannot tell apart in the editor.
func TestSanitizeRule_TrimsCapability(t *testing.T) {
	got, err := SanitizeRule(&Rule{Capability: "  shell  ", Effect: "ask"})
	if err != nil {
		t.Fatalf("SanitizeRule: %v", err)
	}
	if got.Capability != "shell" {
		t.Errorf("capability = %q, want %q", got.Capability, "shell")
	}
}

// TestCapabilities_IsASuggestionNotAnAllowlist: the two are now decoupled, and a
// test that only checked membership would not notice them being re-coupled.
func TestCapabilities_IsASuggestionNotAnAllowlist(t *testing.T) {
	suggested := Capabilities()
	if !slices.IsSorted(suggested) {
		t.Errorf("Capabilities() not sorted: %v", suggested)
	}
	if slices.Contains(suggested, "hooks") {
		t.Fatal("test assumes 'hooks' is NOT in the suggested set; pick another absent capability")
	}
	if _, err := SanitizeRule(&Rule{Capability: "hooks", Effect: "deny"}); err != nil {
		t.Errorf("a capability outside the suggested set must still be writable: %v", err)
	}
}

// TestSanitizeRule_AcceptsEveryBoundAtItsEdge states the near side of each shape
// bound the cases above only state the far side of.
//
// All three caps are inclusive, and the difference matters in one direction only:
// a rule refused here is a rule the editor cannot write, so a bound that is one
// short silently caps what the user is allowed to express in their own security
// policy, with an error naming a limit their input did not actually exceed.
func TestSanitizeRule_AcceptsEveryBoundAtItsEdge(t *testing.T) {
	t.Run("a capability exactly at the length cap", func(t *testing.T) {
		capability := strings.Repeat("a", maxCapabilityLen)
		got, err := SanitizeRule(&Rule{Capability: capability, Effect: "ask"})
		if err != nil {
			t.Fatalf("SanitizeRule(capability of %d bytes) = %v, want nil", maxCapabilityLen, err)
		}
		if got.Capability != capability {
			t.Errorf("capability = %q, want it verbatim", got.Capability)
		}
	})

	t.Run("a pattern exactly at the length cap", func(t *testing.T) {
		pattern := strings.Repeat("a", maxPatternLen)
		got, err := SanitizeRule(&Rule{Capability: "shell", Effect: "ask", Match: []string{pattern}})
		if err != nil {
			t.Fatalf("SanitizeRule(pattern of %d bytes) = %v, want nil", maxPatternLen, err)
		}
		if len(got.Match) != 1 || got.Match[0] != pattern {
			t.Errorf("match = %v, want the one %d-byte pattern verbatim", got.Match, maxPatternLen)
		}
	})

	t.Run("exactly the maximum number of match entries", func(t *testing.T) {
		match := make([]string, 0, maxMatchEntries)
		for i := range maxMatchEntries {
			match = append(match, "p"+strconv.Itoa(i))
		}
		got, err := SanitizeRule(&Rule{Capability: "shell", Effect: "ask", Match: match})
		if err != nil {
			t.Fatalf("SanitizeRule(%d match entries) = %v, want nil", maxMatchEntries, err)
		}
		if len(got.Match) != maxMatchEntries {
			t.Errorf("match entries = %d, want %d", len(got.Match), maxMatchEntries)
		}
	})
}

func TestUpsertDedupAndRemove(t *testing.T) {
	f := &File{Rules: []Rule{}}
	r := Rule{Capability: "web_fetch", Effect: "allow"}
	changed, err := f.Upsert(&r)
	if err != nil || !changed || len(f.Rules) != 1 {
		t.Fatalf("first upsert: changed=%v err=%v rules=%d", changed, err, len(f.Rules))
	}
	// Identical rule (by signature) → no-op.
	changed, _ = f.Upsert(&Rule{Capability: "web_fetch", Effect: "allow"})
	if changed || len(f.Rules) != 1 {
		t.Errorf("dup upsert changed=%v rules=%d, want no-op", changed, len(f.Rules))
	}
	// Different effect → distinct rule.
	changed, _ = f.Upsert(&Rule{Capability: "web_fetch", Effect: "deny"})
	if !changed || len(f.Rules) != 2 {
		t.Errorf("distinct upsert changed=%v rules=%d", changed, len(f.Rules))
	}
	if !f.Remove(&Rule{Capability: "web_fetch", Effect: "allow"}) || len(f.Rules) != 1 {
		t.Errorf("remove failed; rules=%d", len(f.Rules))
	}
	if f.Remove(&Rule{Capability: "web_fetch", Effect: "allow"}) {
		t.Error("removing an absent rule should return false")
	}
}

// TestUpsert_RefusesAFullFile pins the per-file rule ceiling at the count it
// names.
//
// The cap is on the file as it STANDS, so a file already holding the maximum is
// full and the next append is refused — off by one here and the ceiling is a
// number the file can always exceed by exactly one rule, which is not a ceiling
// anyone can reason about when the file is a security policy KAS loads.
func TestUpsert_RefusesAFullFile(t *testing.T) {
	full := &File{Rules: make([]Rule, 0, maxRulesPerFile)}
	for i := range maxRulesPerFile {
		full.Rules = append(full.Rules, Rule{Capability: "shell", Effect: "ask", Match: []string{"c" + strconv.Itoa(i)}})
	}

	changed, err := full.Upsert(&Rule{Capability: "shell", Effect: "deny", Match: []string{"one-too-many"}})
	if !errors.Is(err, ErrTooManyRules) {
		t.Errorf("Upsert() into a file of %d rules error = %v, want ErrTooManyRules", maxRulesPerFile, err)
	}
	if changed || len(full.Rules) != maxRulesPerFile {
		t.Errorf("Upsert() into a full file changed=%v rules=%d, want false and %d",
			changed, len(full.Rules), maxRulesPerFile)
	}

	// And the last slot below the cap is still usable, so the refusal above is
	// the ceiling rather than an off-by-one below it.
	nearlyFull := &File{Rules: full.Rules[:maxRulesPerFile-1]}
	changed, err = nearlyFull.Upsert(&Rule{Capability: "shell", Effect: "deny", Match: []string{"last-slot"}})
	if err != nil || !changed || len(nearlyFull.Rules) != maxRulesPerFile {
		t.Errorf("Upsert() into a file of %d rules changed=%v err=%v rules=%d, want true, nil and %d",
			maxRulesPerFile-1, changed, err, len(nearlyFull.Rules), maxRulesPerFile)
	}
}

func TestSignatureOrderIndependent(t *testing.T) {
	a := Rule{Capability: "fs_read", Effect: "ask", Match: []string{"x", "y"}}
	b := Rule{Capability: "fs_read", Effect: "ask", Match: []string{"y", "x"}}
	if Signature(&a) != Signature(&b) {
		t.Error("signature must be order-independent for match globs")
	}
}

// --- ReplaceEffect (in-place effect editing) ---

func TestReplaceEffect(t *testing.T) {
	base := func() *File {
		return &File{Rules: []Rule{
			{Capability: "fs_write", Effect: "deny", Match: []string{"**/.git/**"}},
			{Capability: "shell", Effect: "ask", Match: []string{"rm *"}},
			{Capability: "web_fetch", Effect: "allow"},
		}}
	}

	t.Run("changes_effect_in_place_preserving_position", func(t *testing.T) {
		f := base()
		old := Rule{Capability: "shell", Effect: "ask", Match: []string{"rm *"}}
		if !f.ReplaceEffect(&old, "deny") {
			t.Fatal("ReplaceEffect = false, want true")
		}
		if len(f.Rules) != 3 {
			t.Fatalf("rules = %d, want 3 (in-place, not remove+append)", len(f.Rules))
		}
		if f.Rules[1].Effect != "deny" || f.Rules[1].Match[0] != "rm *" {
			t.Errorf("rules[1] = %+v, want the shell rule updated at index 1", f.Rules[1])
		}
	})

	t.Run("absent_rule_returns_false", func(t *testing.T) {
		f := base()
		old := Rule{Capability: "shell", Effect: "allow", Match: []string{"rm *"}}
		if f.ReplaceEffect(&old, "deny") {
			t.Error("ReplaceEffect on absent rule = true, want false")
		}
	})

	t.Run("same_effect_is_noop", func(t *testing.T) {
		f := base()
		old := Rule{Capability: "web_fetch", Effect: "allow"}
		if f.ReplaceEffect(&old, "allow") {
			t.Error("ReplaceEffect to same effect = true, want false")
		}
	})

	t.Run("collapses_into_existing_duplicate", func(t *testing.T) {
		f := &File{Rules: []Rule{
			{Capability: "shell", Effect: "ask", Match: []string{"rm *"}},
			{Capability: "shell", Effect: "deny", Match: []string{"rm *"}},
		}}
		old := Rule{Capability: "shell", Effect: "ask", Match: []string{"rm *"}}
		if !f.ReplaceEffect(&old, "deny") {
			t.Fatal("ReplaceEffect = false, want true (old removed, duplicate kept)")
		}
		if len(f.Rules) != 1 || f.Rules[0].Effect != "deny" {
			t.Errorf("rules = %+v, want single deny rule (no duplicate)", f.Rules)
		}
	})
}
