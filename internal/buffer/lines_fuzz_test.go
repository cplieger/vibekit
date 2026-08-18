package buffer

import (
	"fmt"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

func FuzzLineTrackerRecord(f *testing.F) {
	// Seed: sequence that triggers eviction (>500 files)
	f.Add([]byte{0, 0, 1, 10, 1, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		lt := NewLineTracker()
		chats := []api.ChatID{"chat-0", "chat-1", "chat-2"}
		kinds := []string{"edit", "create", "delete"}

		// Each record needs 6 bytes: chatIdx, fileIdx(2), startLine, endLine, turn
		for i := 0; i+5 < len(data); i += 6 {
			chatIdx := int(data[i]) % len(chats)
			fileIdx := int(data[i+1])<<8 | int(data[i+2])
			startLine := int(data[i+3])
			endLine := int(data[i+4])
			turn := int(data[i+5])
			kindIdx := turn % len(kinds)

			filePath := fmt.Sprintf("file-%d.go", fileIdx%600)
			lt.Record(chats[chatIdx], filePath, LineRange{StartLine: startLine, EndLine: endLine, Turn: turn, Kind: kinds[kindIdx]})
		}

		// Verify invariants
		lt.mu.RLock()
		defer lt.mu.RUnlock()
		for _, state := range lt.data {
			if len(state.ranges) > maxFilesPerChat {
				t.Fatalf("files per chat %d > max %d", len(state.ranges), maxFilesPerChat)
			}
			for _, ranges := range state.ranges {
				if len(ranges) > maxLineRangesPerFile {
					t.Fatalf("ranges per file %d > max %d", len(ranges), maxLineRangesPerFile)
				}
			}
		}
	})
}
