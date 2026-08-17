package git

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/gitexec"
)

// textconvMarker is what an armed textconv driver prints. It appears in a diff
// as a context line, which is why the fixture works as an assertion in both
// directions rather than only proving the flag was passed.
const textconvMarker = "TEXTCONV_DRIVER_RAN"

// textconvDriver is a diff.<driver>.textconv value that prints the marker and
// then the file. git appends the path as the command's last argument, so under
// `sh -c` the path lands in $0.
const textconvDriver = "sh -c 'echo " + textconvMarker + "; cat \"$0\"'"

// armTextconv turns dir into a repo that executes a command whenever git renders
// changed.txt for a diff, using nothing but the repo's own .git/config and
// .gitattributes — which is exactly the shape a checked-out untrusted repo has.
//
// The driver's output differs from the raw blob on both sides of the diff. A
// driver whose output happened to match the blob would make every assertion
// below pass for the wrong reason.
func armTextconv(t *testing.T, dir string) {
	t.Helper()
	initFixtureRepo(t, dir)
	writeRepoFile(t, dir, ".gitattributes", "changed.txt diff=leak\n")
	// Set on the REPO, not globally: a repo carrying config plus attributes is
	// the threat, and a global setting would not model it.
	runGit(t, dir, "config", "diff.leak.textconv", textconvDriver)
	writeCommit(t, dir, "changed.txt", "committed line\n", "add changed.txt")
	writeRepoFile(t, dir, "changed.txt", "working line\n")
}

func writeRepoFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// fileDiffBody drives the real handler and returns the diff it served.
func fileDiffBody(t *testing.T, workDir, repo, path string) string {
	t.Helper()
	h := NewHandler(workDir)
	req := httptest.NewRequest(http.MethodGet, "/api/git/file-diff?repo="+repo+"&path="+path, nil)
	rec := httptest.NewRecorder()
	h.handleFileDiff(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Diff string `json:"diff"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.Diff
}

// repoUnder makes workDir/name and returns it, matching how the handler resolves
// a repo out of the work directory.
func repoUnder(t *testing.T, workDir, name string) string {
	t.Helper()
	dir := filepath.Join(workDir, name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", name, err)
	}
	return dir
}

// The fixture's own proof, and it must hold for the tests below to mean
// anything: without --no-textconv, git really does execute the repo's command
// and its output really does reach the caller. A green pair of "the marker is
// absent" assertions over an unarmed fixture would assert nothing at all.
func TestTextconv_FixtureIsArmed(t *testing.T) {
	dir := t.TempDir()
	armTextconv(t, dir)

	out, _ := gitexec.Run(t.Context(), dir, "diff", "HEAD", "--", "changed.txt")
	if !strings.Contains(out, textconvMarker) {
		t.Fatalf("diff without --no-textconv did not run the driver; the fixture is not armed:\n%s", out)
	}
	// `git show <ref>:<path>` is a BLOB DUMP and does not apply textconv by
	// default — measured against git 2.55.0, where the bare form prints the raw
	// blob and only `--textconv` runs the driver. So the flag on that call site
	// affirms the default rather than closing an open hole; what this asserts is
	// that the driver is reachable there at all, which is what makes affirming
	// the default worth the argument.
	enabled, err := gitexec.Run(t.Context(), dir, "show", "--textconv", "HEAD:changed.txt")
	if err != nil {
		t.Fatalf("git show --textconv: %v\n%s", err, enabled)
	}
	if !strings.Contains(enabled, textconvMarker) {
		t.Fatalf("show --textconv did not run the driver; the fixture is not armed:\n%s", enabled)
	}
	bare, err := gitexec.Run(t.Context(), dir, "show", "HEAD:changed.txt")
	if err != nil {
		t.Fatalf("git show: %v\n%s", err, bare)
	}
	if strings.Contains(bare, textconvMarker) {
		t.Errorf("git's blob-dump default changed: bare `show <ref>:<path>` now runs textconv:\n%s", bare)
	}
}

// The file-diff handler is the click that renders a changed file in the git
// panel, and it is the one an untrusted repo reaches by being opened.
func TestHandleFileDiff_DoesNotRunARepoTextconvDriver(t *testing.T) {
	workDir := t.TempDir()
	armTextconv(t, repoUnder(t, workDir, "untrusted"))

	diff := fileDiffBody(t, workDir, "untrusted", "changed.txt")
	if strings.Contains(diff, textconvMarker) {
		t.Errorf("the repo's textconv driver ran:\n%s", diff)
	}
	// The real diff must still be there: --no-textconv suppresses the driver,
	// not the comparison, and a handler that returned nothing would also pass
	// the assertion above.
	if !strings.Contains(diff, "working line") {
		t.Errorf("diff lost its content:\n%s", diff)
	}
}

// An UNTRACKED file takes the --no-index fallback, and that path is the least
// obvious one: "outside the index" does not mean outside the attributes, which
// git reads from the working tree either way.
func TestHandleFileDiff_UntrackedFallbackDoesNotRunTextconv(t *testing.T) {
	workDir := t.TempDir()
	repoDir := repoUnder(t, workDir, "untrusted")
	initFixtureRepo(t, repoDir)
	writeRepoFile(t, repoDir, ".gitattributes", "fresh.txt diff=leak\n")
	runGit(t, repoDir, "config", "diff.leak.textconv", textconvDriver)
	writeRepoFile(t, repoDir, "fresh.txt", "never committed\n")

	diff := fileDiffBody(t, workDir, "untrusted", "fresh.txt")
	if strings.Contains(diff, textconvMarker) {
		t.Errorf("the --no-index fallback ran the repo's textconv driver:\n%s", diff)
	}
	if !strings.Contains(diff, "never committed") {
		t.Errorf("untracked diff lost its content:\n%s", diff)
	}
}

// gitShowCmd is the blob read behind the editor's diff-vs-HEAD pane. It accepts
// the diff option set (`--textconv` demonstrably enables the driver, see
// TestTextconv_FixtureIsArmed), so pinning the safe value there keeps the raw
// read a stated property of this call rather than a default it inherits.
func TestGitShowCmd_ReturnsTheRawBlobNotTextconvOutput(t *testing.T) {
	dir := t.TempDir()
	armTextconv(t, dir)

	out, err := gitShowCmd(t.Context(), dir, "HEAD", "changed.txt")
	if err != nil {
		t.Fatalf("gitShowCmd: %v\n%s", err, out)
	}
	if strings.Contains(out, textconvMarker) {
		t.Errorf("show ran the repo's textconv driver:\n%s", out)
	}
	if !strings.Contains(out, "committed line") {
		t.Errorf("show lost the blob content:\n%s", out)
	}
}

// core.fsmonitor is the other config-driven execution path, and it fires on the
// two subcommands the git panel uses most. It is cleared centrally in
// gitexec.Cmd, so this asserts the end-to-end consequence: a repo that sets it
// cannot get it run.
func TestGitexec_DoesNotRunARepoFsmonitorHook(t *testing.T) {
	dir := t.TempDir()
	initFixtureRepo(t, dir)
	marker := filepath.Join(dir, "fsmonitor-ran")
	runGit(t, dir, "config", "core.fsmonitor", "sh -c 'touch \""+marker+"\"'")

	for _, args := range [][]string{
		{"status", "--porcelain"},
		{"diff", "--no-textconv", "HEAD"},
	} {
		if out, err := gitexec.Run(t.Context(), dir, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Fatalf("core.fsmonitor from the repo's own config was executed by git %v", args)
		}
	}
}
