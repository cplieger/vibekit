package buffer

import (
	"fmt"
	"sync"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

func BenchmarkBufferStore_Contention(b *testing.B) {
	for _, goroutines := range []int{1, 10, 50} {
		b.Run(fmt.Sprintf("goroutines=%d", goroutines), func(b *testing.B) {
			store := NewStore()
			b.ReportAllocs()
			b.ResetTimer()

			var wg sync.WaitGroup
			for g := range goroutines {
				// The remainder is distributed rather than dropped. A bare
				// b.N/goroutines loses up to goroutines-1 iterations while the
				// framework still divides the elapsed time by b.N, so every
				// count that does not divide b.N reported a per-op cost that
				// was too low by the shortfall.
				iters := b.N / goroutines
				if g < b.N%goroutines {
					iters++
				}
				wg.Go(func() {
					chatID := vibekit.ChatID(fmt.Sprintf("chat-%d", g))
					for range iters {
						buf := store.GetOrInit(chatID)
						buf.AppendTextDelta("x", "")
						store.Take(chatID)
					}
				})
			}
			wg.Wait()
		})
	}
}

func BenchmarkLineTracker_Parallel(b *testing.B) {
	lt := NewLineTracker()
	chatID := vibekit.ChatID("bench-chat")

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		turn := 0
		for pb.Next() {
			turn++
			lt.Record(chatID, fmt.Sprintf("file-%d.go", turn%100), LineRange{StartLine: 1, EndLine: 10, Turn: turn, Kind: "edit"})
			lt.Get(chatID, fmt.Sprintf("file-%d.go", turn%100))
		}
	})
}

func BenchmarkBufferTrackFileChanges(b *testing.B) {
	for _, numDiffs := range []int{1, 10, 100} {
		b.Run(fmt.Sprintf("diffs=%d", numDiffs), func(b *testing.B) {
			diffs := make([]vibekit.ToolDiff, numDiffs)
			for i := range diffs {
				diffs[i] = vibekit.ToolDiff{
					Path:    fmt.Sprintf("file-%d.go", i),
					OldText: "old line\n",
					NewText: "new line\nnew line 2\n",
				}
			}
			b.ReportAllocs()
			for b.Loop() {
				buf := &Buffer{ToolStartTimes: make(map[string]int64)}
				buf.TrackFileChanges(diffs, false)
			}
		})
	}
}

func BenchmarkLineTrackerEviction(b *testing.B) {
	lt := NewLineTracker()
	chatID := vibekit.ChatID("eviction-chat")

	// Pre-populate to capacity (500 files).
	for i := range maxFilesPerChat {
		lt.Record(chatID, fmt.Sprintf("pre-file-%d.go", i), LineRange{StartLine: 1, EndLine: 10, Turn: i, Kind: "edit"})
	}

	b.ReportAllocs()
	i := 0
	for b.Loop() {
		// Each Record triggers eviction since we're at capacity with a new file.
		lt.Record(chatID, fmt.Sprintf("new-file-%d.go", i), LineRange{StartLine: 1, EndLine: 10, Turn: maxFilesPerChat + i, Kind: "edit"})
		i++
	}
}
