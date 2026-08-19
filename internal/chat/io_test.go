package chat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/ids"
)

// TestReadCappedFilePathGuard pins the path guard readCappedFile runs
// before it opens anything, and pins what that guard does NOT do.
//
// The guard is two refusals: absoluteness (filepath.IsAbs) and a ".."
// component (pathinside.HasDotDot), both applied to the CLEANED value.
// The behavioural delta from the substring test this replaced is one
// direction only — names holding two adjacent dots are no longer refused.
// Nothing that was refused for traversal became acceptable.
func TestReadCappedFilePathGuard(t *testing.T) {
	dir := t.TempDir()

	// Every accepted case must find a real file, so the only thing that
	// can fail is the guard.
	write := func(name string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(`{"id":"c1"}`), 0o600); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
		return p
	}

	plain := write("c1.json")
	dotted := write("a..b.json")
	tripleDot := write("...json")
	leadingDots := write("..extras.json")

	cases := []struct {
		name   string
		path   string
		reject bool
	}{
		// Relative input, where an absolute store path is expected.
		// Refused by the IsAbs gate on the cleaned value. This is the
		// half of the guard that actually fires, and the ".."-shaped
		// inputs below land here rather than on the traversal test.
		{"relative name", "c1.json", true},
		{"relative nested", "chats/c1.json", true},
		{"bare parent", "..", true},
		{"parent then name", "../escape.json", true},
		{"dot", ".", true},
		{"empty", "", true},

		// Names that merely hold or begin with two dots. A ".." SUBSTRING
		// test refused all of these; a ".." COMPONENT test reads them.
		// This is the whole behavioural change at this site.
		{"dots inside the name", dotted, false},
		{"three dots", tripleDot, false},
		{"name beginning with two dots", leadingDots, false},
		{"plain chat file", plain, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := readCappedFile(tc.path, "chat test")
			if tc.reject {
				if err == nil {
					t.Fatalf("readCappedFile(%q) error = nil, want a rejection", tc.path)
				}
				if !strings.Contains(err.Error(), "rejected unsafe path") {
					t.Errorf("readCappedFile(%q) error = %v, want the %q refusal", tc.path, err, "rejected unsafe path")
				}
				return
			}
			if err != nil {
				t.Fatalf("readCappedFile(%q) error = %v, want nil", tc.path, err)
			}
			if len(data) == 0 {
				t.Errorf("readCappedFile(%q) returned no data", tc.path)
			}
		})
	}
}

// TestReadCappedFileTraversalTestIsVacuousAfterClean pins the documented
// vacuity of the guard's traversal half, so nobody reads the
// pathinside.HasDotDot call as containment enforcement.
//
// filepath.Clean collapses every ".." and clamps at the filesystem root,
// so an absolute path that reaches the predicate can never hold a ".."
// component and the predicate can never fire. An absolute path WRITTEN
// with a traversal therefore passes the guard and is opened at whatever
// it cleans down to — true before this adoption and unchanged by it.
//
// Containment at this boundary is structural, not lexical: every caller
// builds the path from store.Dir() plus an ids.ValidChatID-checked id,
// and that character set ([A-Za-z0-9_-]) admits neither a separator nor
// a dot, so no chat id can contribute a traversal segment. The assertion
// on ValidChatID is here rather than in internal/ids because it is
// THIS guard's missing half; if it ever loosens, this test is the one
// that should fail.
//
// A future hardening (canonicalise ConfigDir at load, then judge the raw
// path) is expected to break the first half of this test deliberately.
func TestReadCappedFileTraversalTestIsVacuousAfterClean(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "c1.json")
	if err := os.WriteFile(target, []byte(`{"id":"c1"}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// A written traversal that cleans back to a file inside dir is read,
	// not refused: the guard never saw the "..".
	data, err := readCappedFile(dir+"/sub/../c1.json", "chat vacuity")
	if err != nil {
		t.Fatalf("readCappedFile with a written traversal error = %v, want nil (Clean defused it)", err)
	}
	if len(data) == 0 {
		t.Error("readCappedFile returned no data")
	}

	// The real gate: a chat id can carry neither a separator nor a dot,
	// so store.Dir() + id cannot compose a traversal in the first place.
	for _, id := range []string{"..", ".", "a/b", `a\b`, "a.json", "..extras", ""} {
		if ids.ValidChatID(id) {
			t.Errorf("ids.ValidChatID(%q) = true; readCappedFile's guard relies on this being false", id)
		}
	}
	if !ids.ValidChatID("c1") {
		t.Error("ids.ValidChatID(\"c1\") = false, want true")
	}
}

// TestReadCappedFileRejectionNamesTheInput pins that the refusal quotes
// the path AS THE CALLER PASSED IT, not the cleaned form. An operator
// diagnosing a rejected read needs to see the string their own code
// built; echoing the cleaned value would hide the spelling that caused
// the refusal.
func TestReadCappedFileRejectionNamesTheInput(t *testing.T) {
	raw := "chats/../../etc/passwd"
	_, err := readCappedFile(raw, "chat probe")
	if err == nil {
		t.Fatal("readCappedFile error = nil, want a rejection")
	}
	if !strings.Contains(err.Error(), raw) {
		t.Errorf("error = %v, want it to quote the raw input %q", err, raw)
	}
	if !strings.HasPrefix(err.Error(), "chat probe: ") {
		t.Errorf("error = %v, want it prefixed with the caller's label", err)
	}
}
