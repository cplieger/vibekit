package buffer

import (
	"fmt"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
	"pgregory.net/rapid"
)

func TestLineTracker_RapidEviction(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		lt := NewLineTracker()

		// Use 1-3 chat IDs.
		numChats := rapid.IntRange(1, 3).Draw(rt, "numChats")
		chatIDs := make([]vibekit.ChatID, numChats)
		for i := range numChats {
			chatIDs[i] = vibekit.ChatID(fmt.Sprintf("chat-%d", i))
		}

		// Generate 0-600 unique paths.
		numPaths := rapid.IntRange(0, 600).Draw(rt, "numPaths")
		paths := make([]string, numPaths)
		for i := range numPaths {
			paths[i] = fmt.Sprintf("file-%d.go", i)
		}

		// Record arbitrary sequences.
		numOps := rapid.IntRange(0, 200).Draw(rt, "numOps")
		for turn := 1; turn <= numOps; turn++ {
			chatIdx := rapid.IntRange(0, numChats-1).Draw(rt, fmt.Sprintf("chat_%d", turn))
			if numPaths == 0 {
				continue
			}
			pathIdx := rapid.IntRange(0, numPaths-1).Draw(rt, fmt.Sprintf("path_%d", turn))
			startLine := rapid.IntRange(0, 1000).Draw(rt, fmt.Sprintf("start_%d", turn))
			endLine := startLine + rapid.IntRange(0, 50).Draw(rt, fmt.Sprintf("span_%d", turn))

			lt.Record(chatIDs[chatIdx], paths[pathIdx], LineRange{StartLine: startLine, EndLine: endLine, Turn: turn, Kind: "edit"})
		}

		// Assert invariants.
		for _, chatID := range chatIDs {
			lt.mu.RLock()
			state := lt.data[chatID]
			lt.mu.RUnlock()
			if state == nil {
				continue
			}

			// Files per chat <= maxFilesPerChat.
			if len(state.ranges) > maxFilesPerChat {
				rt.Fatalf("chat %s has %d files, want <= %d", chatID, len(state.ranges), maxFilesPerChat)
			}

			// Ranges per file <= maxLineRangesPerFile.
			for path, ranges := range state.ranges {
				if len(ranges) > maxLineRangesPerFile {
					rt.Fatalf("chat %s file %s has %d ranges, want <= %d", chatID, path, len(ranges), maxLineRangesPerFile)
				}
			}

			// All surviving files are accessible via Get.
			for path := range state.ranges {
				if got := lt.Get(chatID, path); got == nil {
					rt.Fatalf("Get(%s, %s) returned nil for surviving file", chatID, path)
				}
			}
		}
	})
}
