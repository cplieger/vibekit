package hub

import (
	"sync"
	"testing"

	"vibekit/internal/api"
)

func FuzzReplayRing_AppendReplay_Concurrent(f *testing.F) {
	f.Add(uint64(1), uint64(5), uint64(10), "chat1")

	f.Fuzz(func(t *testing.T, id1, id2, id3 uint64, chatFilter string) {
		r := newReplayRing(8)
		var wg sync.WaitGroup

		// Writer goroutine.
		wg.Go(func() {
			for _, id := range []uint64{id1, id2, id3} {
				r.Append(sseEvent{eventID: id, chatID: api.ChatID(chatFilter)})
			}
		})

		// Reader goroutine.
		wg.Go(func() {
			r.Replay(0, "")
			r.Bounds()
			_ = r.Len()
		})

		wg.Wait()

		// Post-condition: Len never exceeds capacity.
		if r.Len() > 8 {
			t.Fatalf("Len() %d exceeds capacity 8", r.Len())
		}
	})
}
