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

	f.Fuzz(func(t *testing.T, path string) {
		ok := validateFilePath(path)
		if !ok {
			return
		}
		if strings.HasPrefix(path, "-") {
			t.Fatal("accepted path with leading dash")
		}
		if strings.Contains(path, "..") {
			t.Fatal("accepted path with ..")
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
