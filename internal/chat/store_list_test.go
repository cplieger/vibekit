package chat

import (
	"context"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

func seedChats(t *testing.T, s *Store, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if err := s.Mutate(t.Context(), vibekit.ChatID(id), func(c *vibekit.Chat, _ bool) bool {
			c.Name = id
			return true
		}); err != nil {
			t.Fatalf("seed chat %s: %v", id, err)
		}
	}
}

// A scan the fan-out never finished must report itself INCOMPLETE: the session
// reaper derives its keep-list from it, so a partial list marked complete
// authorises deleting a live chat's KAS sessions.
func TestReadHeadersParallel_CancelledScanIsNotComplete(t *testing.T) {
	s, _ := newTestStore(t)
	seedChats(t, s, "a", "b", "c")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	headers, complete := readHeadersParallel(ctx, []chatEntry{
		{id: "a", path: s.dir + "/a" + chatFileSuffix},
		{id: "b", path: s.dir + "/b" + chatFileSuffix},
		{id: "c", path: s.dir + "/c" + chatFileSuffix},
	}, s.fileCap)

	if complete {
		t.Errorf("complete = true after a cancelled scan that returned %d of 3 headers; "+
			"a partial keep-list marked complete is what deletes a live chat's sessions", len(headers))
	}
}

// A cancelled caller must not truncate the SHARED scan: the client's boot fires two
// GET /api/chats reads and the second aborts the first, so the request holding the
// singleflight slot is routinely cancelled while another is waiting on its answer.
func TestListWithCompleteness_ACancelledCallerStillGetsEveryChat(t *testing.T) {
	s, _ := newTestStore(t)
	seedChats(t, s, "a", "b", "c")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	headers, complete := s.listWithCompleteness(ctx)

	if len(headers) != 3 {
		t.Errorf("headers = %d, want 3: a cancelled caller must not truncate the scan it shares", len(headers))
	}
	if !complete {
		t.Error("complete = false; every chat was readable, so the scan is complete")
	}
}
