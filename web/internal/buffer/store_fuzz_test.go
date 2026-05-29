package buffer

import (
	"fmt"
	"testing"

	"vibekit/internal/api"
)

func FuzzStoreLifecycle(f *testing.F) {
	f.Add([]byte{0, 1, 2, 0, 3, 1, 0, 2})

	f.Fuzz(func(t *testing.T, ops []byte) {
		store := NewStore()
		numChats := 5
		active := make(map[api.ChatID]bool)

		for _, op := range ops {
			chatID := api.ChatID(fmt.Sprintf("chat-%d", int(op>>4)%numChats))
			switch op & 0x03 {
			case 0: // GetOrInit
				buf := store.GetOrInit(chatID)
				if buf == nil {
					t.Fatal("GetOrInit returned nil")
				}
				active[chatID] = true
			case 1: // Take
				buf, ok := store.Take(chatID)
				if ok {
					if buf == nil {
						t.Fatal("Take returned ok=true but nil buffer")
					}
					delete(active, chatID)
				}
			case 2: // Get
				// May return nil if a prior Take in this iteration removed
				// the chat — that's expected; we just exercise the path.
				_ = store.Get(chatID)
			case 3: // Delete
				store.Delete(chatID)
				delete(active, chatID)
			}
		}
	})
}
