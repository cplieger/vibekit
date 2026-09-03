package buffer

import (
	"encoding/binary"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

func FuzzBufferTrackFileChanges(f *testing.F) {
	f.Add([]byte("file.go\x00old\x00new\x00"))
	f.Add([]byte(""))
	f.Add([]byte("\x00\x00\x00"))

	f.Fuzz(func(t *testing.T, data []byte) {
		// Decode fuzz bytes into 1-10 ToolDiff structs.
		if len(data) < 2 {
			return
		}
		count := int(data[0]%10) + 1
		data = data[1:]

		diffs := make([]vibekit.ToolDiff, 0, count)
		for i := range count {
			_ = i
			if len(data) < 3 {
				break
			}
			pathLen := int(data[0] % 64)
			data = data[1:]
			if len(data) < pathLen+2 {
				break
			}
			path := string(data[:pathLen])
			data = data[pathLen:]

			oldLen := int(binary.LittleEndian.Uint16([]byte{data[0], 0})) % 128
			data = data[1:]
			if len(data) < oldLen+1 {
				break
			}
			oldText := string(data[:oldLen])
			data = data[oldLen:]

			newLen := int(data[0]) % 128
			data = data[1:]
			if len(data) < newLen {
				break
			}
			newText := string(data[:newLen])
			data = data[newLen:]

			diffs = append(diffs, vibekit.ToolDiff{Path: path, OldText: oldText, NewText: newText})
		}

		buf := &Buffer{}
		// Must not panic.
		buf.TrackFileChanges(diffs, false)

		// ToolStartTimes is unrelated; verify ChangedFiles keys are non-empty.
		for k := range buf.ChangedFiles {
			if k == "" {
				t.Fatal("ChangedFiles contains empty-string key")
			}
		}

		// Oracle for the counts: each side's line count bounds its own tally,
		// nothing is negative, and identical texts change nothing. The last one
		// is the property the newline count could never hold — it reported a
		// whole file removed and re-added for a write that changed nothing.
		bound := make(map[string][2]int, len(diffs))
		unchanged := make(map[string]bool, len(diffs))
		for _, d := range diffs {
			if d.Path == "" {
				continue
			}
			b := bound[d.Path]
			b[0] += len(splitDiffLines(d.NewText))
			b[1] += len(splitDiffLines(d.OldText))
			bound[d.Path] = b
			if _, seen := unchanged[d.Path]; !seen {
				unchanged[d.Path] = true
			}
			if d.OldText != d.NewText {
				unchanged[d.Path] = false
			}
		}
		for path, fc := range buf.ChangedFiles {
			if fc.LinesAdded < 0 || fc.LinesRemoved < 0 {
				t.Fatalf("%s: negative counts +%d/-%d", path, fc.LinesAdded, fc.LinesRemoved)
			}
			b := bound[path]
			if fc.LinesAdded > b[0] || fc.LinesRemoved > b[1] {
				t.Fatalf("%s: counts +%d/-%d exceed line counts +%d/-%d", path, fc.LinesAdded, fc.LinesRemoved, b[0], b[1])
			}
			if unchanged[path] && (fc.LinesAdded != 0 || fc.LinesRemoved != 0) {
				t.Fatalf("%s: unchanged text reported +%d/-%d, want +0/-0", path, fc.LinesAdded, fc.LinesRemoved)
			}
		}
	})
}
