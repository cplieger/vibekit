package git

import (
	"strings"
	"testing"
)

func FuzzValidateFilePath(f *testing.F) {
	f.Add("src/main.go")
	f.Add("-flag")
	f.Add("../escape")
	f.Add("/absolute")
	f.Add("has\x00null")
	f.Add("has\x01ctrl")
	f.Add("")
	f.Add("normal/path/file.txt")
	// The component-vs-substring boundary: names with two adjacent dots
	// that traverse nothing, against traversals the cleaned-name axis
	// (RelEscapes) would collapse.
	f.Add("v1..v2.txt")
	f.Add("a..b/main.go")
	f.Add("..extras/movie.mkv")
	f.Add("...")
	f.Add("a/../b")
	f.Add("a/..")

	f.Fuzz(func(t *testing.T, path string) {
		ok := validateFilePath(path)
		// The traversal invariant, both directions in one statement: an
		// accepted path never carries a ".." COMPONENT, and any path that
		// does is refused. Spelled independently of the production rule
		// (which splits filepath.ToSlash(path)) so it is a check and not
		// a restatement; on the Linux CI target the two coincide.
		for comp := range strings.SplitSeq(path, "/") {
			if comp == ".." && ok {
				t.Fatalf("accepted path with a .. component: %q", path)
			}
		}
		if !ok {
			return
		}
		if strings.HasPrefix(path, "-") {
			t.Fatal("accepted path with leading dash")
		}
		if strings.HasPrefix(path, "/") {
			t.Fatal("accepted absolute path")
		}
		for _, r := range path {
			if r < 0x20 || r == 0x7f {
				t.Fatalf("accepted path with control byte: %q", path)
			}
		}
	})
}
