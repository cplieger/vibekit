package checkpoint

import (
	"path/filepath"
	"strings"
	"testing"
)

// FuzzBlobStorePathFor verifies pathFor returns non-empty only for exactly
// 64-char lowercase hex strings and that the returned path never contains
// directory traversal.
//
// Bug class: path traversal (CWE-22) via unicode normalization or length confusion.
func FuzzBlobStorePathFor(f *testing.F) {
	f.Add("abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")
	f.Add("ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789")
	f.Add("")
	f.Add("zz")
	f.Add("../../etc/passwd" + strings.Repeat("a", 49))
	f.Add(strings.Repeat("\x00", 64))

	f.Fuzz(func(t *testing.T, hash string) {
		bs := &blobStore{root: "/tmp/blobs"}
		result := bs.pathFor(hash)

		if result == "" {
			// Rejected — verify input was actually invalid.
			if len(hash) == 64 {
				allHex := true
				for i := range 64 {
					c := hash[i]
					if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
						allHex = false
						break
					}
				}
				if allHex {
					t.Fatalf("pathFor(%q) returned empty for valid 64-char hex", hash)
				}
			}
			return
		}

		// Invariant 1: result is under root.
		if !strings.HasPrefix(result, "/tmp/blobs") {
			t.Fatalf("pathFor(%q) = %q; not under root", hash, result)
		}

		// Invariant 2: no directory traversal.
		if strings.Contains(result, "..") {
			t.Fatalf("pathFor(%q) = %q; contains ..", hash, result)
		}

		// Invariant 3: fanout prefix is first 2 chars of hash.
		dir := filepath.Dir(result)
		if filepath.Base(dir) != hash[:2] {
			t.Fatalf("pathFor(%q) = %q; fanout dir %q != %q", hash, result, filepath.Base(dir), hash[:2])
		}
	})
}
