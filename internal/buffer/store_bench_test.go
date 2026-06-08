package buffer

import (
	"fmt"
	"sync"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

func BenchmarkBufferStore_Contention(b *testing.B) {
	for _, goroutines := range []int{1, 10, 50} {
		b.Run(fmt.Sprintf("goroutines=%d", goroutines), func(b *testing.B) {
			store := NewStore()
			b.ReportAllocs()
			b.ResetTimer()

			var wg sync.WaitGroup
			perG := b.N / goroutines
			for g := range goroutines {
				wg.Add(1)
				go func(id int) {
					defer wg.Done()
					chatID := api.ChatID(fmt.Sprintf("chat-%d", id))
					for range perG {
						buf := store.GetOrInit(chatID)
						buf.Content.WriteString("x")
						store.Take(chatID)
					}
				}(g)
			}
			wg.Wait()
		})
	}
}

func BenchmarkLineTracker_Parallel(b *testing.B) {
	lt := NewLineTracker()
	chatID := api.ChatID("bench-chat")

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		turn := 0
		for pb.Next() {
			turn++
			lt.Record(chatID, fmt.Sprintf("file-%d.go", turn%100), 1, 10, turn, "edit")
			lt.Get(chatID, fmt.Sprintf("file-%d.go", turn%100))
		}
	})
}

func BenchmarkBufferTrackFileChanges(b *testing.B) {
	for _, numDiffs := range []int{1, 10, 100} {
		b.Run(fmt.Sprintf("diffs=%d", numDiffs), func(b *testing.B) {
			diffs := make([]api.ToolDiff, numDiffs)
			for i := range diffs {
				diffs[i] = api.ToolDiff{
					Path:    fmt.Sprintf("file-%d.go", i),
					OldText: "old line\n",
					NewText: "new line\nnew line 2\n",
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				buf := &Buffer{ToolStartTimes: make(map[string]int64)}
				buf.TrackFileChanges(diffs, false)
			}
		})
	}
}

func BenchmarkLineTrackerEviction(b *testing.B) {
	lt := NewLineTracker()
	chatID := api.ChatID("eviction-chat")

	// Pre-populate to capacity (500 files).
	for i := range maxFilesPerChat {
		lt.Record(chatID, fmt.Sprintf("pre-file-%d.go", i), 1, 10, i, "edit")
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		// Each Record triggers eviction since we're at capacity with a new file.
		lt.Record(chatID, fmt.Sprintf("new-file-%d.go", i), 1, 10, maxFilesPerChat+i, "edit")
	}
}
