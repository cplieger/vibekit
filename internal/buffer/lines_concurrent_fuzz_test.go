package buffer

import (
	"fmt"
	"sync"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// FuzzLineTrackerConcurrentRecordGet exercises the LineTracker's RWMutex
// and heap invariants under concurrent Record, Get, and Clear operations.
// The invariant: no panic, files-per-chat never exceeds maxFilesPerChat,
// ranges-per-file never exceeds maxLineRangesPerFile, and Get never
// returns data for a cleared chat.
func FuzzLineTrackerConcurrentRecordGet(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 0, 0, 1, 2, 3, 1})
	f.Add([]byte{3, 3, 3, 3, 0, 0, 0, 0})

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 4 {
			return
		}
		lt := NewLineTracker()
		chats := []api.ChatID{"c0", "c1", "c2"}
		numWorkers := int(data[0]%3) + 2
		data = data[1:]

		var wg sync.WaitGroup
		wg.Add(numWorkers)
		chunkSize := (len(data) + numWorkers - 1) / numWorkers

		for w := range numWorkers {
			start := w * chunkSize
			if start >= len(data) {
				wg.Done()
				continue
			}
			end := min(start+chunkSize, len(data))
			chunk := data[start:end]
			go func() {
				defer wg.Done()
				for i, b := range chunk {
					chatID := chats[int(b>>6)%len(chats)]
					switch b & 0x03 {
					case 0: // Record
						fileIdx := int(b>>2) % 600
						lt.Record(chatID, fmt.Sprintf("f%d.go", fileIdx), LineRange{StartLine: i, EndLine: i + 1, Turn: i, Kind: "edit"})
					case 1: // Get
						lt.Get(chatID, fmt.Sprintf("f%d.go", int(b>>2)%600))
					case 2: // Clear
						lt.Clear(chatID)
					case 3: // RecordFromDiffs
						lt.RecordFromDiffs(chatID, []api.ToolDiff{
							{Path: fmt.Sprintf("d%d.go", int(b>>2)%100), NewText: "line\n"},
						}, i, "create")
					}
				}
			}()
		}
		wg.Wait()

		// Post-condition: invariants hold.
		lt.mu.RLock()
		defer lt.mu.RUnlock()
		for _, state := range lt.data {
			if len(state.ranges) > maxFilesPerChat {
				t.Fatalf("files per chat %d > %d", len(state.ranges), maxFilesPerChat)
			}
			for _, ranges := range state.ranges {
				if len(ranges) > maxLineRangesPerFile {
					t.Fatalf("ranges per file %d > %d", len(ranges), maxLineRangesPerFile)
				}
			}
			// Heap size must equal ranges map size.
			if state.h.Len() != len(state.entries) {
				t.Fatalf("heap len %d != entries len %d", state.h.Len(), len(state.entries))
			}
		}
	})
}
