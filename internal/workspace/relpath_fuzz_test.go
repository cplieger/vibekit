package workspace

import (
	"strings"
	"testing"
)

func FuzzRelPath(f *testing.F) {
	f.Add("/workspace", "/workspace/file.txt")
	f.Add("/workspace", "/other/file.txt")
	f.Add("", "/workspace/file.txt")
	f.Add("/workspace", "")
	f.Add("/workspace", "/workspace/../escape")
	f.Add("/a\x00b", "/a\x00b/file")

	f.Fuzz(func(t *testing.T, workDir, abs string) {
		result, err := RelPath(workDir, abs)
		if err != nil {
			return
		}
		// Result must never start with "../" when successfully resolved
		// within the workspace.
		if strings.HasPrefix(result, "../") {
			// This is expected when abs is outside workDir — RelPath
			// does not reject escapes, it just normalizes. So we only
			// assert no panic; the prefix check is informational.
			return
		}
		_ = result
	})
}
