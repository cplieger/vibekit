package translate

import (
	"sync"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// FuzzCrewCacheConcurrent exercises concurrent lookup/remember/ClearChat
// operations with arbitrary chat IDs and group keys. Asserts that the
// cache never panics and that remember followed by lookup is consistent
// in a single-goroutine view.
func FuzzCrewCacheConcurrent(f *testing.F) {
	f.Add("chat-1", "group-a", "msg-001")
	f.Add("chat-2", "group-b", "msg-002")
	f.Add("", "", "")
	f.Add("chat-1", "", "msg-003")
	f.Add("", "group-a", "")
	f.Add("chat-\x00null", "grp-\xff", "id-\t\n")

	f.Fuzz(func(t *testing.T, chatID, groupKey, messageID string) {
		cc := newCrewCache()
		cid := api.ChatID(chatID)

		// Concurrent writers + readers.
		var wg sync.WaitGroup
		wg.Add(3)

		go func() {
			defer wg.Done()
			cc.remember(cid, groupKey, messageID)
		}()

		go func() {
			defer wg.Done()
			cc.lookup(cid, groupKey)
		}()

		go func() {
			defer wg.Done()
			cc.ClearChat(cid)
		}()

		wg.Wait()

		// After all concurrent ops, a fresh remember+lookup must be consistent.
		cc.remember(cid, groupKey, messageID)
		got, ok := cc.lookup(cid, groupKey)
		if !ok {
			t.Fatalf("lookup after remember returned not-ok for chatID=%q group=%q", chatID, groupKey)
		}
		if got != messageID {
			t.Fatalf("lookup returned %q, want %q", got, messageID)
		}

		// ClearChat must remove all entries for the chatID.
		cc.ClearChat(cid)
		_, ok = cc.lookup(cid, groupKey)
		if ok {
			t.Fatalf("lookup after ClearChat returned ok for chatID=%q group=%q", chatID, groupKey)
		}
	})
}
