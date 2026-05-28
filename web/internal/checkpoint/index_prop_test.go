package checkpoint

import (
	"fmt"
	"sync"
	"testing"

	"pgregory.net/rapid"
)

func TestCrossChatIndex_RapidConcurrentSafety(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		idx := newCrossChatIndex()

		nChats := rapid.IntRange(2, 5).Draw(rt, "nChats")
		nOps := rapid.IntRange(5, 30).Draw(rt, "nOps")
		nPaths := rapid.IntRange(1, 5).Draw(rt, "nPaths")

		paths := make([]string, nPaths)
		for i := range nPaths {
			paths[i] = fmt.Sprintf("path/%d.go", i)
		}

		var wg sync.WaitGroup
		for c := range nChats {
			chatID := fmt.Sprintf("chat-%d", c)
			wg.Add(1)
			go func() {
				defer wg.Done()
				for op := range nOps {
					path := paths[op%nPaths]
					ts := int64(op + c*1000)
					sha := fmt.Sprintf("sha-%d-%d", c, op)
					ev := &event{
						Kind:      kindSnapshot,
						Path:      path,
						AfterSHA:  sha,
						TS:        ts,
					}
					idx.apply(chatID, ev)

					// Concurrent read.
					idx.check(chatID, path, sha)
				}
			}()
		}
		wg.Wait()

		// No panic means the RWMutex is correctly protecting the map.
	})
}

func TestCrossChatIndex_RapidMonotonicity(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		idx := newCrossChatIndex()
		chatID := "chat-mono"

		nOps := rapid.IntRange(5, 30).Draw(rt, "nOps")
		path := "mono/file.go"

		var lastSHA string
		for i := range nOps {
			sha := fmt.Sprintf("sha-%d", i)
			ev := &event{
				Kind:     kindSnapshot,
				Path:     path,
				AfterSHA: sha,
				TS:       int64(i + 1),
			}
			idx.apply(chatID, ev)
			lastSHA = sha
		}

		// After monotonically increasing timestamps, the latest SHA
		// should be the one recorded.
		idx.mu.RLock()
		obs, ok := idx.entries[path]
		idx.mu.RUnlock()

		if !ok {
			rt.Fatalf("path %q not in index after %d applies", path, nOps)
		}
		if obs.expectedSHA != lastSHA {
			rt.Fatalf("expected SHA %q, got %q", lastSHA, obs.expectedSHA)
		}
		if obs.chatID != chatID {
			rt.Fatalf("expected chatID %q, got %q", chatID, obs.chatID)
		}
	})
}
