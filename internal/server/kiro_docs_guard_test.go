package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/filehandler"
)

// These cases need a REAL filesystem, not fstest.MapFS: the whole subject is
// symlink resolution across a root boundary, which MapFS cannot express.

// symlinkOr skips when the platform or filesystem refuses symlinks, so the suite
// stays runnable rather than failing for an unrelated reason.
func symlinkOr(t *testing.T, target, name string) {
	t.Helper()
	if err := os.Symlink(target, name); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}
}

// The scenario D113 names. `.kiro/steering` is a symlink to a directory holding
// credential files, and the scan reads the first 64 KiB of every file it walks
// into the description field of a JSON list the browser renders.
func TestKiroDocsGuard_RefusesASymlinkOutOfTheScannedTree(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o750); err != nil {
		t.Fatal(err)
	}
	secret := "---\ndescription: sk-live-0000\n---\n"
	if err := os.WriteFile(filepath.Join(outside, "creds.md"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	kiro := filepath.Join(base, "work", ".kiro")
	if err := os.MkdirAll(kiro, 0o750); err != nil {
		t.Fatal(err)
	}
	symlinkOr(t, outside, filepath.Join(kiro, "steering"))

	guard := newRootGuard(kiro, "ws/.kiro")
	if guard.allows("steering") {
		t.Error("the symlinked category directory was admitted; the walk would enumerate its target")
	}
	if guard.allows("steering/creds.md") {
		t.Error("a file outside the scanned tree was admitted")
	}
}

// The end-to-end consequence, through the real scan: no row, and none of the
// file's bytes in the output. Asserting on the row count alone would pass for a
// scan that emitted the row with an empty description.
func TestScanKiroDocs_SymlinkedCategoryContributesNothing(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "creds.md"), []byte("---\ndescription: sk-live-0000\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(base, "work")
	kiro := filepath.Join(work, ".kiro")
	if err := os.MkdirAll(filepath.Join(kiro, "agents"), 0o750); err != nil {
		t.Fatal(err)
	}
	symlinkOr(t, outside, filepath.Join(kiro, "steering"))
	// A real row beside it, so a scan that simply returned nothing would fail
	// here rather than looking like a success.
	writeFile(t, kiro, "agents/real.md", "---\nname: real\ndescription: a genuine agent\n---\n")

	srv := &Server{workDir: work, kiroDocs: &docsCache{}}
	docs := srv.collectKiroDocs(t.Context())

	if _, ok := findDoc(docs, "real"); !ok {
		t.Errorf("the genuine row is missing, so the guard refused too much: %+v", docs)
	}
	for _, d := range docs {
		if d.Category == catSteering {
			t.Errorf("a steering row came from outside the tree: %+v", d)
		}
		if strings.Contains(d.Description, "sk-live-0000") {
			t.Errorf("the outside file's content reached the docs list: %+v", d)
		}
	}
}

// leakName is planted in the escaped target and asserted absent from EVERY
// field of every row. A name is the leak here: the refusal of the guarded READ
// is what these scanners already had, and it does not stop a listing.
const leakName = "escaped-from-outside"

// The three FLAT categories (skills, agents, hooks) enumerate one directory with
// fs.ReadDir instead of walking it, so each needed the guard moved AHEAD of the
// listing. Driven end to end, because the defect was invisible at the read: the
// skills scanner turns each target subdirectory into an undescribed row and the
// agents scanner turns each target filename into one, both after the guarded
// read of the manifest has already refused.
func TestScanKiroDocs_SymlinkedFlatCategoryIsNotEnumerated(t *testing.T) {
	cases := []struct {
		category string
		plant    func(t *testing.T, outside string)
	}{
		{catSkill + "s", func(t *testing.T, outside string) {
			// A skill is a DIRECTORY, and a directory with no manifest is still
			// a row (an undescribed one), which is exactly the shape that leaked.
			writeFile(t, outside, filepath.Join(leakName, "SKILL.md"), "---\nname: "+leakName+"\n---\n")
			if err := os.MkdirAll(filepath.Join(outside, leakName+"-bare"), 0o750); err != nil {
				t.Fatal(err)
			}
		}},
		{catAgent + "s", func(t *testing.T, outside string) {
			writeFile(t, outside, leakName+".md", "---\nname: "+leakName+"\n---\n")
			writeFile(t, outside, leakName+".json", `{"name":"`+leakName+`"}`)
		}},
		{catHook + "s", func(t *testing.T, outside string) {
			writeFile(t, outside, leakName+".json",
				`{"version":"v1","hooks":[{"name":"`+leakName+`","trigger":"PostFileSave",`+
					`"action":{"type":"command","command":"`+leakName+`"}}]}`)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.category, func(t *testing.T) {
			base := t.TempDir()
			outside := filepath.Join(base, "outside")
			if err := os.MkdirAll(outside, 0o750); err != nil {
				t.Fatal(err)
			}
			tc.plant(t, outside)

			work := filepath.Join(base, "work")
			kiro := filepath.Join(work, ".kiro")
			if err := os.MkdirAll(kiro, 0o750); err != nil {
				t.Fatal(err)
			}
			symlinkOr(t, outside, filepath.Join(kiro, tc.category))
			// A real row in a DIFFERENT category, so a scan that returned
			// nothing at all would fail here rather than look like a success.
			writeFile(t, kiro, "steering/real.md", "---\nname: real\ndescription: a genuine doc\n---\n")

			srv := &Server{workDir: work, kiroDocs: &docsCache{}}
			docs := srv.collectKiroDocs(t.Context())

			if _, ok := findDoc(docs, "real"); !ok {
				t.Errorf("the genuine row is missing, so the guard refused too much: %+v", docs)
			}
			for _, d := range docs {
				if rendered := fmt.Sprintf("%+v", d); strings.Contains(rendered, leakName) {
					t.Errorf("a name from outside the tree reached the docs list: %s", rendered)
				}
			}
			// The row COUNT is the second half: an undescribed row carries no
			// planted name, so the field scan above cannot catch it alone.
			if got := len(docsByCategory(docs, strings.TrimSuffix(tc.category, "s"))); got != 0 {
				t.Errorf("%s rows = %d, want 0 from a category symlinked out of the tree", tc.category, got)
			}
		})
	}
}

// The guard-level half of the same rule: the category directory itself is
// refused, which is what makes "never enumerated" achievable rather than a
// property each scanner has to remember to honour.
func TestKiroDocsGuard_RefusesASymlinkedFlatCategoryDirectory(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o750); err != nil {
		t.Fatal(err)
	}
	kiro := filepath.Join(base, "work", ".kiro")
	if err := os.MkdirAll(kiro, 0o750); err != nil {
		t.Fatal(err)
	}
	guard := newRootGuard(kiro, "ws/.kiro")
	for _, category := range []string{"skills", "agents", "hooks"} {
		symlinkOr(t, outside, filepath.Join(kiro, category))
		if guard.allows(category) {
			t.Errorf("%q was admitted; the scanner would ReadDir its target", category)
		}
	}
}

// A symlinked FILE is the other half, and the one the walk used to follow
// silently: fs.WalkDir does not descend a nested symlinked DIRECTORY, but
// isMarkdownEntry tests the name only, so a `notes.md -> /elsewhere/secret`
// was read.
func TestKiroDocsGuard_RefusesASymlinkedFileOutOfTheTree(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "secret"), []byte("---\ndescription: leaked\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(base, "work")
	kiro := filepath.Join(work, ".kiro")
	if err := os.MkdirAll(filepath.Join(kiro, "steering"), 0o750); err != nil {
		t.Fatal(err)
	}
	symlinkOr(t, filepath.Join(base, "secret"), filepath.Join(kiro, "steering", "notes.md"))
	writeFile(t, kiro, "steering/ordinary.md", "---\ndescription: fine\n---\n")

	srv := &Server{workDir: work, kiroDocs: &docsCache{}}
	docs := srv.collectKiroDocs(t.Context())

	if len(docs) != 1 {
		t.Fatalf("got %d rows, want 1 (the symlink refused, the real file kept): %+v", len(docs), docs)
	}
	if docs[0].Name != "ordinary" {
		t.Errorf("row = %+v, want the ordinary file", docs[0])
	}
}

// A link that stays INSIDE the tree is fine, and must be: an operator symlinking
// a doc into place within their own .kiro is ordinary reshaping (invariant 6),
// and refusing it would be the guard breaking the tool.
func TestKiroDocsGuard_AdmitsALinkThatStaysInsideTheTree(t *testing.T) {
	base := t.TempDir()
	work := filepath.Join(base, "work")
	kiro := filepath.Join(work, ".kiro")
	if err := os.MkdirAll(filepath.Join(kiro, "steering"), 0o750); err != nil {
		t.Fatal(err)
	}
	writeFile(t, kiro, "shared/canonical.md", "---\nname: canonical\ndescription: shared\n---\n")
	symlinkOr(t, filepath.Join(kiro, "shared", "canonical.md"), filepath.Join(kiro, "steering", "alias.md"))

	srv := &Server{workDir: work, kiroDocs: &docsCache{}}
	docs := srv.collectKiroDocs(t.Context())

	if _, ok := findDoc(docs, "canonical"); !ok {
		t.Errorf("an in-tree symlink was refused: %+v", docs)
	}
}

// A `.kiro` that is ITSELF a symlink is followed, and its target becomes the
// boundary. That is the deliberate reading of "out of the root being scanned":
// the operator chose that root, and refusing it would make a symlinked workspace
// show an empty docs page.
func TestKiroDocsGuard_ASymlinkedRootIsItsOwnBoundary(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real-kiro")
	if err := os.MkdirAll(filepath.Join(real, "steering"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "steering", "doc.md"), []byte("---\nname: doc\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(base, "work")
	if err := os.MkdirAll(work, 0o750); err != nil {
		t.Fatal(err)
	}
	symlinkOr(t, real, filepath.Join(work, ".kiro"))

	srv := &Server{workDir: work, kiroDocs: &docsCache{}}
	if docs := srv.collectKiroDocs(t.Context()); len(docs) != 1 {
		t.Errorf("got %d rows from a symlinked .kiro, want 1: %+v", len(docs), docs)
	}
}

// The second layer, and the one that needs no symlink at all: a root that
// resolves onto the sensitive denylist is refused by the SAME predicate the
// browser file surface applies.
//
// It is driven through the predicate rather than a fixture under /config, because
// the entries are absolute container paths a test cannot create. What this pins
// is that the guard consults filehandler.IsSensitive on the RESOLVED path — the
// only form that can match — which is the half a naive "call IsSensitive on the
// walk path" implementation gets wrong while looking correct.
func TestKiroDocsGuard_ConsultsTheSharedSensitiveDenylist(t *testing.T) {
	for _, sensitive := range []string{
		"/config/home/.aws/sso/cache/token.md",
		"/config/chats/abc.md",
		"/config/mcp.json",
	} {
		if !filehandler.IsSensitive(sensitive) {
			t.Fatalf("fixture wrong: %q is not on the shared denylist", sensitive)
		}
	}
	g := &rootGuard{dir: "/config", category: "test"}
	// Inside the root, so the escape check passes and the denylist is what has
	// to refuse it. This is exactly the arrangement layer 1 does not cover.
	if g.allow("home/.aws/sso/cache/token.md") {
		t.Error("a sensitive path inside the scanned root was admitted")
	}
	if g.allow("chats/abc.md") {
		t.Error("the chat store was admitted")
	}
}

// A root the scan cannot resolve refuses everything rather than admitting it.
// The alternative — treating an unnameable root as permissive — is the failure
// mode a guard exists to prevent.
func TestKiroDocsGuard_UnresolvableRootRefusesEverything(t *testing.T) {
	guard := newRootGuard(filepath.Join(t.TempDir(), "nope"), "ws/.kiro")
	if guard.allows("steering/a.md") {
		t.Error("an unresolvable root admitted a path")
	}
}

// A nil guard admits everything. That is the fstest.MapFS seam the other scanner
// tests use, and it must stay explicit rather than accidental.
func TestKiroDocsGuard_NilAdmitsEverything(t *testing.T) {
	var guard pathGuard
	if !guard.allows("anything/at/all.md") {
		t.Error("a nil guard refused a path; the MapFS tests would silently scan nothing")
	}
}
