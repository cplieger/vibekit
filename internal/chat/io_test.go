package chat

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/vibekit/internal/ids"
)

// TestReadCappedFilePathGuard pins the path guard readCappedFile runs before it opens
// anything, and pins what that guard does NOT do: two refusals on the CLEANED value,
// absoluteness (filepath.IsAbs) and a ".." component (pathinside.HasDotDot).
func TestReadCappedFilePathGuard(t *testing.T) {
	dir := t.TempDir()

	// Every accepted case must find a real file, so only the guard can fail.
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
		// Relative input where an absolute store path is expected: refused by the
		// IsAbs gate, which is the half of the guard that actually fires.
		{"relative name", "c1.json", true},
		{"relative nested", "chats/c1.json", true},
		{"bare parent", "..", true},
		{"parent then name", "../escape.json", true},
		{"dot", ".", true},
		{"empty", "", true},

		// Names that merely hold or begin with two dots: a COMPONENT test reads them.
		{"dots inside the name", dotted, false},
		{"three dots", tripleDot, false},
		{"name beginning with two dots", leadingDots, false},
		{"plain chat file", plain, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := readCappedFile(tc.path, "chat test", 0)
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

// TestReadCappedFileTraversalTestIsVacuousAfterClean pins the vacuity of the guard's
// traversal half, so nobody reads pathinside.HasDotDot as containment: filepath.Clean
// collapses every ".." before the predicate can see one. Containment here is structural
// — every caller builds the path from store.Dir() plus an ids.ValidChatID-checked id,
// and that character set admits neither a separator nor a dot. The ValidChatID
// assertion lives here because it is THIS guard's missing half.
func TestReadCappedFileTraversalTestIsVacuousAfterClean(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "c1.json")
	if err := os.WriteFile(target, []byte(`{"id":"c1"}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// A written traversal that cleans back inside dir is read: the guard never saw it.
	data, err := readCappedFile(dir+"/sub/../c1.json", "chat vacuity", 0)
	if err != nil {
		t.Fatalf("readCappedFile with a written traversal error = %v, want nil (Clean defused it)", err)
	}
	if len(data) == 0 {
		t.Error("readCappedFile returned no data")
	}

	// The real gate: store.Dir() + id cannot compose a traversal in the first place.
	for _, id := range []string{"..", ".", "a/b", `a\b`, "a.json", "..extras", ""} {
		if ids.ValidChatID(id) {
			t.Errorf("ids.ValidChatID(%q) = true; readCappedFile's guard relies on this being false", id)
		}
	}
	if !ids.ValidChatID("c1") {
		t.Error("ids.ValidChatID(\"c1\") = false, want true")
	}
}

// TestReadCappedFileRejectionNamesTheInput pins that the refusal quotes the path AS THE
// CALLER PASSED IT: echoing the cleaned value would hide the spelling that caused it.
func TestReadCappedFileRejectionNamesTheInput(t *testing.T) {
	raw := "chats/../../etc/passwd"
	_, err := readCappedFile(raw, "chat probe", 0)
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

// seedFatChat writes a chat whose MESSAGES dwarf its header, each message small, so the
// property under test is the total rather than one big token.
func seedFatChat(t *testing.T, msgBytes, msgCount int) (path string, size int64) {
	t.Helper()
	var b strings.Builder
	b.WriteString(`{"id":"c1","name":"fat","created_at":11,"updated_at":22,"messages":[`)
	body := strings.Repeat("m", msgBytes)
	for i := range msgCount {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":"m%d","role":"user","ts":%d,"content":%q}`, i, i, body)
	}
	b.WriteString(`]}`)

	path = filepath.Join(t.TempDir(), "c1.json")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("Setup: write fat chat: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Setup: stat: %v", err)
	}
	return path, st.Size()
}

// TestReadChatHeader_CostDoesNotScaleWithMessageBytes is the property that makes an
// unlimited cap survivable: readHeadersParallel runs this at 8 workers per chat, so the
// header read must not hold the transcript. Measured, because a return value cannot
// show "streams": the whole-file version returned the same header.
func TestReadChatHeader_CostDoesNotScaleWithMessageBytes(t *testing.T) {
	path, size := seedFatChat(t, 4<<10, 1_100)
	const minSize = 4 << 20
	if size < minSize {
		t.Fatalf("Setup: fixture is %d bytes, needs at least %d or a whole-file read would fit under the bound",
			size, minSize)
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	h, err := readChatHeader(path, "chat c1", 0)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatalf("readChatHeader = %v, want nil", err)
	}

	allocated := after.TotalAlloc - before.TotalAlloc
	// Twice the file, and the two implementations sit either side of it: streaming
	// measured 1.04x against 4.32x for a whole-file read.
	bound := 2 * uint64(size)
	if allocated > bound {
		t.Errorf("readChatHeader over a %d-byte chat allocated %d bytes (%.2fx), want under %d: "+
			"the header read is holding message bytes",
			size, allocated, float64(allocated)/float64(size), bound)
	}
	if h.MessageCount != 1_100 {
		t.Errorf("MessageCount = %d, want 1100", h.MessageCount)
	}
	if h.ID != "c1" || h.Name != "fat" || h.UpdatedAt != 22 || h.CreatedAt != 11 {
		t.Errorf("header = %+v, want the scalar fields decoded", h)
	}
}

// TestReadChatHeader_ScanBoundAppliesWhenTheCapIsUnlimited pins the second gate:
// maxHeaderScanBytes is independent of the chat cap, so no memory limit does not mean no
// bound on one sidebar refresh. Allocating 512 MiB to cross the scan bound is not
// acceptable in a test, so what varies is the INJECTED cap and the bound is asserted
// positive instead.
func TestReadChatHeader_ScanBoundAppliesWhenTheCapIsUnlimited(t *testing.T) {
	if maxHeaderScanBytes <= 0 {
		t.Fatal("maxHeaderScanBytes must be positive or the scan is unbounded under an unlimited cap")
	}
	// Both branches must bound the read, or one of them is all that stands between
	// List() and an unbounded stream.
	path, size := seedFatChat(t, 1<<10, 8)
	for _, tc := range []struct {
		name    string
		fileCap chatFileCap
		wantErr bool
	}{
		{"under both bounds, unlimited cap", 0, false},
		{"under the scan bound but over the injected cap", chatFileCap(size - 1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readChatHeader(path, "chat c1", tc.fileCap)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("readChatHeader(cap=%d) on a %d-byte file = nil error, want a refusal",
						tc.fileCap, size)
				}
				if !errors.Is(err, atomicfile.ErrFileTooLarge) {
					t.Errorf("readChatHeader error = %v, want it to match atomicfile.ErrFileTooLarge", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("readChatHeader(cap=%d) = %v, want nil", tc.fileCap, err)
			}
		})
	}
}

// TestDecodeChatHeader_MessagesMemberIsNeverCaptured pins the one key the scan must
// recognise instead of capturing, in the case that proves it: a capitalised `messages`,
// which encoding/json matches case-insensitively.
func TestDecodeChatHeader_MessagesMemberIsNeverCaptured(t *testing.T) {
	body := `{"id":"c1","Messages":[{"id":"m1"},{"id":"m2"}],"name":"n"}`
	h, err := decodeChatHeader(strings.NewReader(body))
	if err != nil {
		t.Fatalf("decodeChatHeader = %v, want nil", err)
	}
	if h.MessageCount != 2 {
		t.Errorf("MessageCount = %d, want 2: a capitalised member was not recognised", h.MessageCount)
	}
	if h.Name != "n" || h.ID != "c1" {
		t.Errorf("header = %+v, want the members after `Messages` still decoded", h)
	}
}
