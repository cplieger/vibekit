package pending

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
)

// BenchmarkPendingAddResolve exercises the Add→Resolve round-trip under
// contention. Sub-benchmarks vary the number of concurrent pending ops
// per chat to surface O(n) regressions in the path-busy scan and slice
// removal.
func BenchmarkPendingAddResolve(b *testing.B) {
	for _, n := range []int{1, 10, 50} {
		b.Run(fmt.Sprintf("pending=%d", n), func(b *testing.B) {
			b.RunParallel(func(pb *testing.PB) {
				s := New()
				// Pre-populate n-1 pending ops so the path-busy scan
				// has realistic work.
				for i := range n - 1 {
					id := fmt.Sprintf("prefill-%d", i)
					_, _, _ = s.Add(context.Background(), &AddParams{
						ToolCallID: id,
						ChatID:     "bench-chat",
						Path:       fmt.Sprintf("/prefill/%d.go", i),
						Kind:       KindEdit,
					})
				}
				iter := 0
				for pb.Next() {
					iter++
					id := fmt.Sprintf("bench-%d", iter)
					path := fmt.Sprintf("/bench/%d.go", iter)
					ch, _, err := s.Add(context.Background(), &AddParams{
						ToolCallID: id,
						ChatID:     "bench-chat",
						Path:       path,
						Kind:       KindEdit,
					})
					if err != nil {
						b.Fatalf("Add: %v", err)
					}
					_, _ = s.Resolve(context.Background(), id, ActionAccept)
					// Drain the channel to avoid leaking.
					<-ch
				}
			})
		})
	}
}

// BenchmarkRejectAllForChat measures the bulk-flush hot path under
// varying pending-op counts. RejectAllForChat is called from
// mode-disable, cancel, and chat-delete paths. Sub-benchmarks vary
// the number of pending ops to surface O(n²) regressions in slice
// removal or map deletion.
func BenchmarkRejectAllForChat(b *testing.B) {
	for _, n := range []int{1, 10, 50, 256} {
		b.Run(fmt.Sprintf("pending=%d", n), func(b *testing.B) {
			for range b.N {
				s := New()
				for i := range n {
					id := fmt.Sprintf("tc-%d", i)
					path := fmt.Sprintf("/f/%d.go", i)
					_, _, _ = s.Add(context.Background(), &AddParams{
						ToolCallID: id,
						ChatID:     "bench-chat",
						Path:       path,
						Kind:       KindEdit,
						NewText:    "x",
					})
				}
				s.RejectAllForChat("bench-chat")
			}
		})
	}
}

// BenchmarkPendingStore_Contention exercises the concurrent Add+Resolve
// hot path with GOMAXPROCS goroutines contending on the same chatID.
// This surfaces mutex contention regressions under parallel load.
func BenchmarkPendingStore_Contention(b *testing.B) {
	s := New()
	var counter atomic.Int64
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			// Atomic counter ensures every (path, tool-call-id) pair is unique
			// across all goroutines so Add never trips "path already staged".
			n := counter.Add(1)
			id := fmt.Sprintf("contention-%d", n)
			ch, _, err := s.Add(context.Background(), &AddParams{
				ToolCallID: id,
				ChatID:     "shared-chat",
				Path:       fmt.Sprintf("/c/%d.go", n),
				Kind:       KindEdit,
				NewText:    "x",
			})
			if err != nil {
				b.Fatalf("Add: %v", err)
			}
			_, _ = s.Resolve(context.Background(), id, ActionAccept)
			<-ch
		}
	})
}
