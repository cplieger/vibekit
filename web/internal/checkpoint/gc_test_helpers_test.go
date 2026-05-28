package checkpoint

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	checkpointgc "vibekit/internal/checkpoint/gc"
)

// runBlobGC sweeps unreferenced blobs. Returns the number of blobs
// removed and the number scanned (for observability).
//
// This is a convenience wrapper used by tests. Production code uses
// the gc.Coordinator which handles collection and sweeping internally.
func runBlobGC(ctx context.Context, configDir string) (removed, scanned int, err error) {
	var mu sync.RWMutex
	coord := checkpointgc.NewCoordinator(configDir, blobsRoot(configDir), chatsRoot(configDir), 1*time.Hour, &mu, func() map[string]checkpointgc.BlobRefer {
		return nil
	})
	return coord.RunOnceWithCounts(ctx)
}

// BenchmarkCollectReferencedBlobs measures GC reference-collection
// scaling with chat count. Each synthetic chat has ~20 events with
// blob references.
func BenchmarkCollectReferencedBlobs(b *testing.B) {
	for _, chatCount := range []int{1, 10, 50} {
		b.Run(fmt.Sprintf("chats_%d", chatCount), func(b *testing.B) {
			cfg := b.TempDir()
			chatsDir := chatsRoot(cfg)
			if err := os.MkdirAll(chatsDir, 0o755); err != nil {
				b.Fatal(err)
			}
			for c := range chatCount {
				chatID := fmt.Sprintf("chat-%d", c)
				log := newEventLog(cfg, chatID)
				evs := makeBenchGCEvents(20)
				for i := range evs {
					if err := log.Append(context.Background(), &evs[i]); err != nil {
						b.Fatal(err)
					}
				}
			}
			ctx := context.Background()
			var mu sync.RWMutex
			b.ResetTimer()
			b.ReportAllocs()
			for range b.N {
				coord := checkpointgc.NewCoordinator(cfg, blobsRoot(cfg), chatsRoot(cfg), 1*time.Hour, &mu, func() map[string]checkpointgc.BlobRefer {
					return nil
				})
				_, _, _ = coord.RunOnceWithCounts(ctx)
			}
		})
	}
}

// makeBenchGCEvents generates n snapshot events with unique SHAs.
func makeBenchGCEvents(n int) []event {
	evs := make([]event, 0, n)
	for i := range n {
		tag := strconv.Itoa(i)
		evs = append(evs, event{
			Kind:         kindSnapshot,
			Turn:         i,
			Tag:          tag,
			Path:         "file" + strconv.Itoa(i%5) + ".go",
			BeforeSHA:    "before" + strconv.Itoa(i),
			AfterSHA:     "after" + strconv.Itoa(i),
			MessageCount: i + 1,
			TS:           int64(i),
		})
	}
	return evs
}
