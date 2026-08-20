package agent

// Proof for the confinement the fs handlers gained: the check-then-act the
// lexical resolver used to leave open is closed by naming every operation
// through the workspace's os.Root.
//
// Each test here stages the race explicitly — resolve, swap an INTERMEDIATE
// directory for a symlink pointing outside the workspace, then act — and asserts
// both halves: the ambient operation the handler used to perform escapes, and the
// confined operation it performs now is refused. The ambient half is not padding;
// it is the red check, and it is what makes these tests fail loudly if anyone
// reintroduces an os call on the resolver's absolute path.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/cplieger/atomicfile/v3"
)

// escapeStage is one staged ancestor swap: a workspace containing work/sub/f.txt
// and a sibling directory outside it holding a file the agent must not reach.
type escapeStage struct {
	rt      *Runtime
	work    string
	outside string
	// abs is the path the lexical resolver returned BEFORE the swap: what the
	// deleted ambient os calls would have operated on.
	abs string
	// root/rel are what the handlers use now.
	root *os.Root
	rel  string
}

const (
	outsideSecret = "SECRET-OUTSIDE-THE-WORKSPACE"
	insideContent = "inside"
)

// stageAncestorSwap builds the workspace, resolves work/sub/f.txt, and then
// replaces the "sub" DIRECTORY with a symlink to a directory outside the
// workspace — the swap an agent with write access to the workspace can perform
// with one rename, in the window between the resolver's verdict and the
// operation.
func stageAncestorSwap(t *testing.T) escapeStage {
	t.Helper()
	// EvalSymlinks the temp dirs: on macOS /var is itself a symlink to
	// /private/var, and an unresolved base makes the containment check compare
	// two different spellings of the same directory.
	work := canonDir(t, t.TempDir())
	outside := canonDir(t, t.TempDir())
	if err := os.WriteFile(filepath.Join(outside, "f.txt"), []byte(outsideSecret), 0o600); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}
	sub := filepath.Join(work, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	target := filepath.Join(sub, "f.txt")
	if err := os.WriteFile(target, []byte(insideContent), 0o600); err != nil {
		t.Fatalf("seed inside file: %v", err)
	}

	rt, _ := hubForFSTest(t, work)

	abs, err := rt.lifecycle.resolveInsideWorkDir(target)
	if err != nil {
		t.Fatalf("resolveInsideWorkDir(%q) = %v, want nil", target, err)
	}
	root, rel, err := rt.lifecycle.confineInWorkDir(target)
	if err != nil {
		t.Fatalf("confineInWorkDir(%q) = %v, want nil", target, err)
	}
	if rel != filepath.Join("sub", "f.txt") {
		t.Fatalf("confineInWorkDir(%q) rel = %q, want %q", target, rel, filepath.Join("sub", "f.txt"))
	}

	// The swap. Everything the resolver verified about "sub" is now false.
	if err := os.Remove(target); err != nil {
		t.Fatalf("remove inside file: %v", err)
	}
	if err := os.Remove(sub); err != nil {
		t.Fatalf("remove sub dir: %v", err)
	}
	if err := os.Symlink(outside, sub); err != nil {
		t.Fatalf("symlink sub -> outside: %v", err)
	}

	return escapeStage{rt: rt, work: work, outside: outside, abs: abs, root: root, rel: rel}
}

// canonDir resolves a directory through EvalSymlinks so comparisons against it
// are against the real path.
func canonDir(t *testing.T, dir string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", dir, err)
	}
	return resolved
}

// A read named through the workspace root is refused after an ancestor swap; the
// ambient os.ReadFile the handler used to perform hands back the outside file.
func TestConfinedRead_RefusesAncestorSwap(t *testing.T) {
	st := stageAncestorSwap(t)

	// Red check: this is the deleted code path, and it escapes.
	leaked, err := os.ReadFile(st.abs)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) after ancestor swap = %v; the escape this test "+
			"exists to close is not reachable, so the confined half proves nothing", st.abs, err)
	}
	if string(leaked) != outsideSecret {
		t.Fatalf("os.ReadFile(%q) = %q, want the outside file's contents %q",
			st.abs, leaked, outsideSecret)
	}

	// The confined read the handler performs now.
	got, err := atomicfile.ReadBoundedInRoot(t.Context(), st.root, st.rel, fsReadCap)
	if err == nil {
		t.Fatalf("ReadBoundedInRoot(root, %q) = (%q, nil), want an escape error", st.rel, got)
	}
	if strings.Contains(string(got), outsideSecret) {
		t.Errorf("ReadBoundedInRoot returned the outside file's contents alongside err = %v", err)
	}
}

// A write named through the workspace root is refused after an ancestor swap.
// The control is the exact open the handler used to perform, syscall.O_NOFOLLOW
// included — the flag guards only the FINAL component, so it does not help.
func TestConfinedWrite_RefusesAncestorSwap(t *testing.T) {
	st := stageAncestorSwap(t)
	outsideFile := filepath.Join(st.outside, "f.txt")

	// Red check: the deleted open writes outside the workspace, O_NOFOLLOW and all.
	f, err := os.OpenFile(st.abs, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		t.Fatalf("os.OpenFile(%q, O_NOFOLLOW) after ancestor swap = %v; the escape this "+
			"test exists to close is not reachable", st.abs, err)
	}
	if _, err := f.WriteString("CLOBBERED"); err != nil {
		t.Fatalf("write through the swapped ancestor: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got, rErr := os.ReadFile(outsideFile); rErr != nil || string(got) != "CLOBBERED" {
		t.Fatalf("outside file after the ambient write = (%q, %v), want (%q, nil)",
			got, rErr, "CLOBBERED")
	}

	// Restore the outside file so the confined half is judged against a clean state.
	if err := os.WriteFile(outsideFile, []byte(outsideSecret), 0o600); err != nil {
		t.Fatalf("restore outside file: %v", err)
	}

	// The confined write the handler performs now.
	if _, err := atomicfile.WriteFileInRoot(t.Context(), st.root, st.rel, []byte("CONFINED"),
		atomicfile.WithMode(0o600), atomicfile.WithMkdirMode(0o755)); err == nil {
		t.Fatalf("WriteFileInRoot(root, %q) = nil, want an escape error", st.rel)
	}
	got, err := os.ReadFile(outsideFile)
	if err != nil {
		t.Fatalf("read outside file: %v", err)
	}
	if string(got) != outsideSecret {
		t.Errorf("outside file = %q, want it untouched at %q", got, outsideSecret)
	}
}

// A delete named through the pinned parent is refused after an ancestor swap;
// the ambient Lstat + Remove pair unlinks the outside file.
func TestConfinedDelete_RefusesAncestorSwap(t *testing.T) {
	st := stageAncestorSwap(t)
	outsideFile := filepath.Join(st.outside, "f.txt")

	// The confined descent FIRST here, because the red check destroys the target:
	// OpenParentInRoot refuses "sub" outright, since it Lstats each component and
	// a symlink is not a directory.
	if parent, base, err := atomicfile.OpenParentInRoot(st.root, st.rel); err == nil {
		_ = parent.Close()
		t.Fatalf("OpenParentInRoot(root, %q) = (%v, %q, nil), want a refusal at the "+
			"symlinked component", st.rel, parent.Name(), base)
	}
	if _, err := os.Lstat(outsideFile); err != nil {
		t.Fatalf("outside file after the confined delete attempt: %v, want it still there", err)
	}

	// Red check: the deleted pair unlinks it.
	info, err := os.Lstat(st.abs)
	if err != nil {
		t.Fatalf("os.Lstat(%q) after ancestor swap = %v; the escape this test exists "+
			"to close is not reachable", st.abs, err)
	}
	if info.IsDir() {
		t.Fatalf("os.Lstat(%q).IsDir() = true, want the staged regular file", st.abs)
	}
	if err := os.Remove(st.abs); err != nil {
		t.Fatalf("os.Remove(%q) through the swapped ancestor = %v", st.abs, err)
	}
	if _, err := os.Lstat(outsideFile); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("outside file after the ambient delete: err = %v, want fs.ErrNotExist "+
			"(the ambient pair should have unlinked it)", err)
	}
}

// A listing named through the workspace root is refused after an ancestor swap;
// os.ReadDir enumerates the outside directory.
func TestConfinedReadDir_RefusesAncestorSwap(t *testing.T) {
	st := stageAncestorSwap(t)
	absDir := filepath.Dir(st.abs)

	if entries, err := os.ReadDir(absDir); err != nil || len(entries) == 0 {
		t.Fatalf("os.ReadDir(%q) after ancestor swap = (%d entries, %v); the escape "+
			"this test exists to close is not reachable", absDir, len(entries), err)
	}
	if _, err := readDirInRoot(st.root, "sub"); err == nil {
		t.Fatalf("readDirInRoot(root, %q) = nil error, want an escape error", "sub")
	}
}

// A FIFO left at a read target must be REFUSED, not opened. open(2) on a
// reader-less FIFO blocks indefinitely, and this handler runs under
// lifetime.inflight against a KAS Call that carries no timeout — so a hang here
// wedges the chat's turn permanently. The test would hang rather than fail if
// the refusal regressed, which is why it is worth pinning.
func TestConfinedRead_RefusesFIFO(t *testing.T) {
	work := canonDir(t, t.TempDir())
	fifo := filepath.Join(work, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unsupported here: %v", err)
	}
	rt, _ := hubForFSTest(t, work)

	root, rel, err := rt.lifecycle.confineInWorkDir(fifo)
	if err != nil {
		t.Fatalf("confineInWorkDir(%q) = %v, want nil", fifo, err)
	}
	_, err = atomicfile.ReadBoundedInRoot(t.Context(), root, rel, fsReadCap)
	if !errors.Is(err, atomicfile.ErrNotRegular) {
		t.Errorf("ReadBoundedInRoot(fifo) err = %v, want ErrNotRegular", err)
	}
}

// A FIFO left at a write target must be refused too: the deleted
// O_WRONLY|O_CREATE|O_TRUNC open would have blocked until a reader appeared, and
// rename(2) would happily have replaced the pipe with a regular file.
func TestConfinedWrite_RefusesFIFO(t *testing.T) {
	work := canonDir(t, t.TempDir())
	fifo := filepath.Join(work, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unsupported here: %v", err)
	}
	rt, _ := hubForFSTest(t, work)

	root, rel, err := rt.lifecycle.confineInWorkDir(fifo)
	if err != nil {
		t.Fatalf("confineInWorkDir(%q) = %v, want nil", fifo, err)
	}
	_, err = atomicfile.WriteFileInRoot(t.Context(), root, rel, []byte("x"),
		atomicfile.WithMkdirMode(0o755))
	if !errors.Is(err, atomicfile.ErrNotRegular) {
		t.Errorf("WriteFileInRoot(fifo) err = %v, want ErrNotRegular", err)
	}
	fi, err := os.Lstat(fifo)
	if err != nil {
		t.Fatalf("Lstat(fifo): %v", err)
	}
	if fi.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("fifo mode after the refused write = %s, want it left as a named pipe", fi.Mode())
	}
}

// A symlink planted at the write target AFTER the path was confined must not
// carry the write through to its victim.
//
// This is the property the deleted syscall.O_NOFOLLOW open held, and it is worth
// pinning because the mechanism changed. The resolver EvalSymlinks the leaf, so a
// link that already exists is rewritten to its target and never reaches the
// write at all — the only way a symlink gets here is the race, so the race is
// what the test stages. Where the old open answered ELOOP, the rename now
// REPLACES the link with the new regular file, because rename(2) does not follow
// a final component. Different answer, same guarantee: the victim's bytes are
// untouched, and os.Root forbids the link pointing outside the workspace anyway.
func TestConfinedWrite_SymlinkPlantedAfterConfineDoesNotReachVictim(t *testing.T) {
	work := canonDir(t, t.TempDir())
	victim := filepath.Join(work, "victim.txt")
	if err := os.WriteFile(victim, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("seed victim: %v", err)
	}
	target := filepath.Join(work, "target.txt")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	rt, _ := hubForFSTest(t, work)

	root, rel, err := rt.lifecycle.confineInWorkDir(target)
	if err != nil {
		t.Fatalf("confineInWorkDir(%q) = %v, want nil", target, err)
	}

	// The race: swap the confined target for a symlink at the victim.
	if err := os.Remove(target); err != nil {
		t.Fatalf("remove target: %v", err)
	}
	if err := os.Symlink(victim, target); err != nil {
		t.Fatalf("symlink target -> victim: %v", err)
	}

	_, err = atomicfile.WriteFileInRoot(t.Context(), root, rel, []byte("clobber"),
		atomicfile.WithMode(0o600), atomicfile.WithMkdirMode(0o755))
	// Either outcome is acceptable — a refusal (the Lstat saw the link) or a
	// completed write that replaced the link. What must NOT happen is the
	// victim changing.
	if got, rErr := os.ReadFile(victim); rErr != nil || string(got) != "keep me" {
		t.Errorf("victim after write (err = %v) = (%q, %v), want (%q, nil)",
			err, got, rErr, "keep me")
	}
	if errors.Is(err, atomicfile.ErrSymlinkTarget) {
		return // refused at the check; nothing else to assert
	}
	if err != nil {
		t.Fatalf("WriteFileInRoot err = %v, want nil or ErrSymlinkTarget", err)
	}
	fi, lErr := os.Lstat(target)
	if lErr != nil {
		t.Fatalf("Lstat(target): %v", lErr)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Errorf("target is still a symlink after a nil-error write; the rename "+
			"followed the link instead of replacing it (mode %s)", fi.Mode())
	}
}

// With no workspace root the fs handlers must REFUSE rather than fall back to
// ambient os calls: a boundary with nothing behind it is worse than a withheld
// capability.
func TestConfineInWorkDir_RefusesWithoutRoot(t *testing.T) {
	lt := &lifetime{workDir: t.TempDir()}
	if _, _, err := lt.confineInWorkDir("f.txt"); !errors.Is(err, errNoWorkRoot) {
		t.Errorf("confineInWorkDir with nil workRoot err = %v, want errNoWorkRoot", err)
	}
}
