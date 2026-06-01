package composition

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckExecutable_BareNameUsesLookPath pins the fix for the prod
// crash where KIRO_CLI_PATH defaults to "kiro-cli" (no slash) and
// validation rejected it via os.Stat. exec.LookPath must be used for
// bare names so the validator matches os/exec's actual lookup behavior.
func TestCheckExecutable_BareNameUsesLookPath(t *testing.T) {
	tmp := t.TempDir()
	binPath := filepath.Join(tmp, "fakecli")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Bare name should resolve via PATH.
	if err := checkExecutable("fakecli", "TEST_BIN"); err != nil {
		t.Errorf("bare name on PATH should resolve: %v", err)
	}

	// Bare name not on PATH should fail with a clear message.
	err := checkExecutable("definitely-not-installed-binary-xyz", "TEST_BIN")
	if err == nil {
		t.Errorf("missing bare name should fail")
	} else if !strings.Contains(err.Error(), "not found on $PATH") {
		t.Errorf("error message should mention $PATH: %v", err)
	}
}

// TestCheckExecutable_PathStilStats verifies that paths containing a
// slash skip LookPath and use os.Stat (for absolute or relative paths).
func TestCheckExecutable_PathStilStats(t *testing.T) {
	tmp := t.TempDir()
	binPath := filepath.Join(tmp, "fakecli")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Absolute path with exec bit: pass.
	if err := checkExecutable(binPath, "TEST_BIN"); err != nil {
		t.Errorf("absolute path should pass: %v", err)
	}

	// Absolute path missing exec bit: fail.
	noExec := filepath.Join(tmp, "noexec")
	if err := os.WriteFile(noExec, []byte("not exec"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := checkExecutable(noExec, "TEST_BIN"); err == nil {
		t.Errorf("non-exec path should fail")
	}

	// Absolute path that's a directory: fail.
	if err := checkExecutable(tmp, "TEST_BIN"); err == nil {
		t.Errorf("directory path should fail")
	}
}
