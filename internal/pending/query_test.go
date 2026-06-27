package pending

import (
	"context"
	"testing"
)

// TestListForChat_Ordering preserves insertion order.
func TestListForChat_Ordering(t *testing.T) {
	t.Parallel()
	s := New()
	ids := []string{"tc-1", "tc-2", "tc-3"}
	for i, id := range ids {
		if _, _, err := s.Add(context.Background(), &AddParams{
			ToolCallID: id, ChatID: "c-1",
			Path: "f" + string(rune('0'+i)) + ".go",
			Kind: KindEdit, NewText: "x",
		}); err != nil {
			t.Fatalf("Add %s: %v", id, err)
		}
	}
	got := s.ListForChat("c-1")
	if len(got) != 3 {
		t.Fatalf("ListForChat len = %d, want 3", len(got))
	}
	for i, snap := range got {
		if snap.ToolCallID != ids[i] {
			t.Errorf("ListForChat[%d] = %s, want %s", i, snap.ToolCallID, ids[i])
		}
	}
}

// TestListForChat_AfterResolve drops the resolved op.
func TestListForChat_AfterResolve(t *testing.T) {
	t.Parallel()
	s := New()
	for _, id := range []string{"tc-1", "tc-2"} {
		if _, _, err := s.Add(context.Background(), &AddParams{
			ToolCallID: id, ChatID: "c-1",
			Path: id + ".go", Kind: KindEdit, NewText: "x",
		}); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	if _, err := s.Resolve(context.Background(), "tc-1", ActionAccept); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got := s.ListForChat("c-1")
	if len(got) != 1 || got[0].ToolCallID != "tc-2" {
		t.Fatalf("list after resolve = %#v, want [tc-2]", got)
	}
}

// TestGet_PresentAndAbsent covers both branches.
func TestGet_PresentAndAbsent(t *testing.T) {
	t.Parallel()
	s := New()
	if _, ok := s.Get("nope"); ok {
		t.Fatal("Get on empty store: ok=true")
	}
	if _, _, err := s.Add(context.Background(), &AddParams{
		ToolCallID: "tc-1", ChatID: "c-1",
		Path: "foo.go", Kind: KindEdit, NewText: "x",
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	snap, ok := s.Get("tc-1")
	if !ok {
		t.Fatal("Get after Add: ok=false")
	}
	if snap.Path != "foo.go" {
		t.Errorf("Get.Path = %q, want foo.go", snap.Path)
	}
}

// TestChatIDs returns the set of chats with at least one pending op and
// drops a chat once all its ops are resolved. Covers the hub's SSE-replay
// path (hub/pending_replay.go), which had no direct test.
func TestChatIDs(t *testing.T) {
	t.Parallel()
	s := New()
	if got := s.ChatIDs(); len(got) != 0 {
		t.Fatalf("ChatIDs on empty store = %v, want none", got)
	}
	if _, _, err := s.Add(context.Background(), &AddParams{
		ToolCallID: "t1", ChatID: "c-1", Path: "a.go", Kind: KindEdit, NewText: "x",
	}); err != nil {
		t.Fatalf("Add c-1: %v", err)
	}
	if _, _, err := s.Add(context.Background(), &AddParams{
		ToolCallID: "t2", ChatID: "c-2", Path: "b.go", Kind: KindEdit, NewText: "x",
	}); err != nil {
		t.Fatalf("Add c-2: %v", err)
	}
	if got := s.ChatIDs(); len(got) != 2 {
		t.Fatalf("ChatIDs = %v, want 2 entries", got)
	}
	// Resolving every op in c-1 drops it from the set; c-2 remains.
	s.RejectAllForChat("c-1")
	got := s.ChatIDs()
	if len(got) != 1 || got[0] != "c-2" {
		t.Fatalf("ChatIDs after resolving c-1 = %v, want [c-2]", got)
	}
}
