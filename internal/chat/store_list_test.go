package chat

import (
	"context"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// seedChats writes n readable chats and returns their ids.
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

// A scan the fan-out never finished must report itself INCOMPLETE, which is the
// property the session reaper's fail-closed guard rests on.
//
// parallel.Bounded abandons the items it has not started when the context ends,
// and it leaves their result slots untouched. Unless an unvisited slot reads as
// lost, a cancelled scan answers with a subset of the chats while claiming to
// have read them all — and ReferencedSessionIDs then hands the reaper a keep-list
// that omits real chats, which authorises deleting their KAS session directories.
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

// The SHARED scan is not owned by whichever caller opened the singleflight slot.
//
// The client's own boot fires two GET /api/chats reads and the second ABORTS the
// first, so the request holding the slot is routinely cancelled while a second
// request is already waiting on its answer — and both then received a truncated
// list. Measured symptom: after a forced restart some open tabs came up with no
// store row at all and stayed blank until the page was reloaded.
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
