package policyfile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
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
	// Relative paths are resolved to absolute first (deterministic).
	if WorkspaceHash("rel") == "" {
		t.Errorf("relative path produced empty hash")
	}
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
	up, err := PathFor(ScopeUser, home, wd)
	if err != nil || up != filepath.Join(home, ".kiro", "settings", "permissions.yaml") {
		t.Errorf("user path = %q, err = %v", up, err)
	}
	wp, err := PathFor(ScopeWorkspace, home, wd)
	wantWP := filepath.Join(home, ".kiro", "workspace-roots", WorkspaceHash(wd), "permissions.yaml")
	if err != nil || wp != wantWP {
		t.Errorf("workspace path = %q, want %q, err = %v", wp, wantWP, err)
	}
	if _, err := PathFor("agent", home, wd); err != ErrInvalidScope {
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
	if err := Save(context.Background(), path, in); err != nil {
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
	if _, err := SanitizeRule(&Rule{Capability: "nope", Effect: "ask"}); err == nil {
		t.Error("unknown capability should error")
	}
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

func TestSignatureOrderIndependent(t *testing.T) {
	a := Rule{Capability: "fs_read", Effect: "ask", Match: []string{"x", "y"}}
	b := Rule{Capability: "fs_read", Effect: "ask", Match: []string{"y", "x"}}
	if Signature(&a) != Signature(&b) {
		t.Error("signature must be order-independent for match globs")
	}
}
