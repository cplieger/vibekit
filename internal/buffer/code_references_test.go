package buffer

import (
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// TestAppendCodeReferences_Dedup pins the (licenseName, repository, url)
// dedup: the KAS fan-out and repeated identical references never produce
// duplicate entries, while distinct references (same license, different repo)
// are all kept.
func TestAppendCodeReferences_Dedup(t *testing.T) {
	buf := &Buffer{}
	mit := vibekit.CodeReference{LicenseName: "MIT", Repository: "github.com/a/b", URL: "https://example.com/a"}
	gpl := vibekit.CodeReference{LicenseName: "GPL-2.0", Repository: "github.com/c/d", URL: "https://example.com/c"}

	// First append: two distinct references.
	got := buf.AppendCodeReferences([]vibekit.CodeReference{mit, gpl})
	if len(got) != 2 {
		t.Fatalf("after first append: len = %d, want 2", len(got))
	}

	// Second append: the same MIT reference again (fan-out / re-emission) plus
	// a third with the same license but a different repo.
	same := vibekit.CodeReference{LicenseName: "MIT", Repository: "github.com/e/f", URL: "https://example.com/e"}
	got = buf.AppendCodeReferences([]vibekit.CodeReference{mit, same})
	if len(got) != 3 {
		t.Fatalf("after second append: len = %d, want 3 (mit deduped, same-license/diff-repo kept)", len(got))
	}
	if len(buf.CodeReferences) != 3 {
		t.Errorf("buffer field len = %d, want 3", len(buf.CodeReferences))
	}
}

// TestAppendCodeReferences_ReturnsCopy pins that the returned slice is a copy:
// mutating it must not corrupt the buffer's backing array (the caller
// broadcasts the return value concurrently with possible later appends).
func TestAppendCodeReferences_ReturnsCopy(t *testing.T) {
	buf := &Buffer{}
	ref := vibekit.CodeReference{LicenseName: "MIT", Repository: "r", URL: "https://example.com"}
	got := buf.AppendCodeReferences([]vibekit.CodeReference{ref})
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	got[0] = vibekit.CodeReference{LicenseName: "TAMPERED"}
	if buf.CodeReferences[0].LicenseName != "MIT" {
		t.Errorf("buffer entry mutated through returned slice: %q, want MIT", buf.CodeReferences[0].LicenseName)
	}
}
