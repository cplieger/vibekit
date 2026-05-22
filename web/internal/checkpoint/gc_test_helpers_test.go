package checkpoint

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
)

// runBlobGC sweeps unreferenced blobs. Returns the number of blobs
// removed and the number scanned (for observability). Best-effort;
// per-entry failures are logged and skipped so a single bad file
// doesn't stall the whole sweep.
//
// If collectReferencedBlobs returns an error we abort without
// removing anything: a partial referenced-set would delete blobs
// owned by the unreadable chat, which is unrecoverable data loss.
// Better to miss a sweep than corrupt history.
//
// This is a convenience wrapper used by tests. Production code calls
// collectReferencedBlobs + sweepBlobs directly (see Store.runGCOnce)
// to pass a live cached-manager map for the fast path and hold
// gcLock only around sweepBlobs.
func runBlobGC(ctx context.Context, configDir string) (removed, scanned int, err error) {
	referenced, err := collectReferencedBlobs(ctx, configDir, nil)
	if err != nil {
		return 0, 0, err
	}
	var mu sync.RWMutex
	return sweepBlobs(ctx, configDir, referenced, &mu)
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
			// Create synthetic event logs for each chat.
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
			b.ResetTimer()
			b.ReportAllocs()
			for range b.N {
				_, err := collectReferencedBlobs(ctx, cfg, nil)
				if err != nil {
					b.Fatal(err)
				}
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
