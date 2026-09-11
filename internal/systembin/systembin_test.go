package systembin

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// withDirs points the resolver at a test-owned candidate set for one test.
func withDirs(t *testing.T, dirs ...string) {
	t.Helper()
	prev := systemDirs
	systemDirs = dirs
	t.Cleanup(func() { systemDirs = prev })
}

func writeExecutable(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestResolve_ReadsTheCandidateSetAndNotPATH is the whole point of the package:
// a name present on PATH but absent from the candidate set must miss.
func TestResolve_ReadsTheCandidateSetAndNotPATH(t *testing.T) {
	trusted := t.TempDir()
	onPath := t.TempDir()
	want := writeExecutable(t, trusted, "thing")
	writeExecutable(t, onPath, "other")
	withDirs(t, trusted)
	t.Setenv("PATH", onPath)

	if got, ok := Resolve("thing"); !ok || got != want {
		t.Errorf("Resolve(%q) = %q, %v; want %q, true", "thing", got, ok, want)
	}
	if got, ok := Resolve("other"); ok {
		t.Errorf("Resolve(%q) = %q, true; want a miss — it is on PATH, not in the set", "other", got)
	}
}

func TestResolve_FirstCandidateWins(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	want := writeExecutable(t, first, "thing")
	writeExecutable(t, second, "thing")
	withDirs(t, first, second)

	if got, _ := Resolve("thing"); got != want {
		t.Errorf("Resolve(%q) = %q, want the first candidate %q", "thing", got, want)
	}
}

func TestResolve_RequiresARegularExecutableFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "afifo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notexec"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, dir, "ok")
	withDirs(t, dir)

	for _, name := range []string{"adir", "afifo", "notexec"} {
		if got, ok := Resolve(name); ok {
			t.Errorf("Resolve(%q) = %q, true; want a miss", name, got)
		}
	}
	if _, ok := Resolve("ok"); !ok {
		t.Error("Resolve(\"ok\") missed a regular executable file")
	}
}

func TestResolve_RefusesAPathSeparator(t *testing.T) {
	outer := t.TempDir()
	trusted := filepath.Join(outer, "trusted")
	if err := os.Mkdir(trusted, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, outer, "evil")
	withDirs(t, trusted)

	for _, name := range []string{"../evil", "/bin/sh", "a/b", ""} {
		if got, ok := Resolve(name); ok {
			t.Errorf("Resolve(%q) = %q, true; want a refusal", name, got)
		}
	}
}

// TestResolve_MissReturnsNoPath pins the refusal contract callers depend on:
// there is no fallback to the bare name for them to accidentally inherit.
func TestResolve_MissReturnsNoPath(t *testing.T) {
	withDirs(t, t.TempDir())
	if got, ok := Resolve("absent"); ok || got != "" {
		t.Errorf("Resolve(%q) = %q, %v; want \"\", false", "absent", got, ok)
	}
}
