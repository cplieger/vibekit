package pending

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestResolve_Unknown returns ErrUnknown for a never-staged id.
func TestResolve_Unknown(t *testing.T) {
	t.Parallel()
	s := New()
	if _, err := s.Resolve(context.Background(), "c-1", "nope", ActionAccept); !errors.Is(err, ErrUnknown) {
		t.Fatalf("Resolve unknown: got %v, want ErrUnknown", err)
	}
}

// TestResolve_Idempotent: a second Resolve for a settled op returns
// ErrUnknown (the op is gone) and does not panic.
func TestResolve_Idempotent(t *testing.T) {
	t.Parallel()
	s := New()
	if _, _, err := s.Add(context.Background(), &AddParams{
		ToolCallID: "tc-1", ChatID: "c-1", Path: "foo.go", Kind: KindEdit, NewText: "x",
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := s.Resolve(context.Background(), "c-1", "tc-1", ActionAccept); err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	if _, err := s.Resolve(context.Background(), "c-1", "tc-1", ActionReject); !errors.Is(err, ErrUnknown) {
		t.Fatalf("second Resolve: got %v, want ErrUnknown", err)
	}
}

// TestResolve_ChatIDMismatch pins fix #7: resolving a staged op with a
// mismatched chat_id is treated as unknown, so a resolve command
// carrying the wrong chat can never settle another chat's op (and the
// op stays staged). ResolveWithText enforces the same scoping.
func TestResolve_ChatIDMismatch(t *testing.T) {
	t.Parallel()
	s := New()
	ctx := context.Background()
	if _, _, err := s.Add(ctx, &AddParams{
		ToolCallID: "tc-1", ChatID: "owner", Path: "foo.go", Kind: KindEdit, NewText: "x",
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if _, err := s.Resolve(ctx, "intruder", "tc-1", ActionAccept); !errors.Is(err, ErrUnknown) {
		t.Fatalf("Resolve with mismatched chat: got %v, want ErrUnknown", err)
	}
	if _, err := s.ResolveWithText(ctx, "intruder", "tc-1", "merged"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("ResolveWithText with mismatched chat: got %v, want ErrUnknown", err)
	}
	// The op must survive the mismatched attempts.
	if got := countForChat(s, "owner"); got != 1 {
		t.Fatalf("op count after mismatched resolves = %d, want 1 (untouched)", got)
	}
	// The rightful owner still resolves it.
	if _, err := s.Resolve(ctx, "owner", "tc-1", ActionAccept); err != nil {
		t.Fatalf("owner Resolve: %v", err)
	}
}

// TestRejectAllForChat flushes every op for the chat; other chats are
// untouched; each waiter unblocks with accepted=false.
func TestRejectAllForChat(t *testing.T) {
	t.Parallel()
	s := New()
	waiters := make([]<-chan struct{}, 0, 3)
	accepters := make([]func() Resolution, 0, 3)
	for i, path := range []string{"a.go", "b.go", "c.go"} {
		waitCh, accepted, err := s.Add(context.Background(), &AddParams{
			ToolCallID: string(rune('a' + i)), ChatID: "c-1",
			Path: path, Kind: KindEdit, NewText: "x",
		})
		if err != nil {
			t.Fatalf("Add %s: %v", path, err)
		}
		waiters = append(waiters, waitCh)
		accepters = append(accepters, accepted)
	}
	// Unrelated chat keeps its op.
	if _, _, err := s.Add(context.Background(), &AddParams{
		ToolCallID: "x", ChatID: "c-2", Path: "z.go", Kind: KindEdit, NewText: "x",
	}); err != nil {
		t.Fatalf("c-2 Add: %v", err)
	}

	snaps := s.RejectAllForChat("c-1")
	if len(snaps) != 3 {
		t.Fatalf("RejectAllForChat returned %d snapshots, want 3", len(snaps))
	}
	if countForChat(s, "c-1") != 0 {
		t.Fatalf("c-1 count after flush = %d, want 0", countForChat(s, "c-1"))
	}
	if countForChat(s, "c-2") != 1 {
		t.Fatalf("c-2 count after flush = %d, want 1 (untouched)", countForChat(s, "c-2"))
	}
	for i, w := range waiters {
		select {
		case <-w:
			if accepters[i]().Accepted {
				t.Errorf("waiter %d saw accepted=true after flush", i)
			}
		case <-time.After(time.Second):
			t.Fatalf("waiter %d blocked after flush", i)
		}
	}
}

// TestAcceptAllForChat accepts every pending op for the chat; each
// waiter unblocks with accepted=true; other chats are untouched. Covers
// the accepted=true arm of resolveAllForChat (the bulk "accept all"
// command path), the complement of TestRejectAllForChat.
func TestAcceptAllForChat(t *testing.T) {
	t.Parallel()
	s := New()
	waiters := make([]<-chan struct{}, 0, 3)
	readers := make([]func() Resolution, 0, 3)
	for i, path := range []string{"a.go", "b.go", "c.go"} {
		waitCh, readRes, err := s.Add(context.Background(), &AddParams{
			ToolCallID: string(rune('a' + i)), ChatID: "c-1",
			Path: path, Kind: KindEdit, NewText: "x",
		})
		if err != nil {
			t.Fatalf("Add %s: %v", path, err)
		}
		waiters = append(waiters, waitCh)
		readers = append(readers, readRes)
	}
	// Unrelated chat keeps its op.
	if _, _, err := s.Add(context.Background(), &AddParams{
		ToolCallID: "x", ChatID: "c-2", Path: "z.go", Kind: KindEdit, NewText: "x",
	}); err != nil {
		t.Fatalf("c-2 Add: %v", err)
	}

	snaps := s.AcceptAllForChat("c-1")
	if len(snaps) != 3 {
		t.Fatalf("AcceptAllForChat returned %d snapshots, want 3", len(snaps))
	}
	if countForChat(s, "c-1") != 0 {
		t.Fatalf("c-1 count after accept-all = %d, want 0", countForChat(s, "c-1"))
	}
	if countForChat(s, "c-2") != 1 {
		t.Fatalf("c-2 count after accept-all = %d, want 1 (untouched)", countForChat(s, "c-2"))
	}
	for i, w := range waiters {
		select {
		case <-w:
			if !readers[i]().Accepted {
				t.Errorf("waiter %d saw accepted=false after accept-all, want true", i)
			}
		case <-time.After(time.Second):
			t.Fatalf("waiter %d blocked after accept-all", i)
		}
	}
}

// TestRejectAllForChat_Empty is a no-op.
func TestRejectAllForChat_Empty(t *testing.T) {
	t.Parallel()
	s := New()
	if got := s.RejectAllForChat("nothing-here"); got != nil {
		t.Fatalf("empty RejectAllForChat returned %v, want nil", got)
	}
}

// TestResolveAllForChat_SkewedByChatEntryNoPanic pins fix #2: a byChat
// list that references an id absent from the ops map (a byChat/ops skew)
// must NOT nil-deref while holding the store mutex — that would leak the
// lock and brick staging hub-wide. resolveAllForChat skips the ghost id,
// resolves the real op, and releases the lock (verified by a follow-up
// Add succeeding without deadlock).
func TestResolveAllForChat_SkewedByChatEntryNoPanic(t *testing.T) {
	t.Parallel()
	s := New()
	ctx := context.Background()
	waitCh, _, err := s.Add(ctx, &AddParams{
		ToolCallID: "real", ChatID: "c-1", Path: "a.go", Kind: KindEdit, NewText: "x",
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Inject a skew: an id listed in byChat with no ops entry.
	s.mu.Lock()
	s.byChat["c-1"] = append(s.byChat["c-1"], "ghost")
	s.mu.Unlock()

	// Must not panic and must resolve the real op.
	snaps := s.RejectAllForChat("c-1")
	if len(snaps) != 1 || snaps[0].ToolCallID != "real" {
		t.Fatalf("RejectAllForChat over skewed byChat = %+v, want the single real op", snaps)
	}
	select {
	case <-waitCh:
	case <-time.After(time.Second):
		t.Fatal("real op's waiter not released after skewed flush")
	}

	// Lock-not-leaked: a subsequent store op completes without deadlock.
	if _, _, err := s.Add(ctx, &AddParams{
		ToolCallID: "after", ChatID: "c-2", Path: "b.go", Kind: KindEdit, NewText: "y",
	}); err != nil {
		t.Fatalf("Add after skewed flush (lock leaked?): %v", err)
	}
}

// TestResolveWithText is a table-driven test covering all
// ResolveWithText scenarios: happy-path accept, unknown id, cap
// exceeded, idempotent double-resolve, resolution carries merged
// text, delete-kind rejection, and create-kind allowance.
func TestResolveWithText(t *testing.T) {
	t.Parallel()

	cases := []struct {
		wantErr        error
		name           string
		kind           Kind
		mergedText     string
		wantMerged     string
		wantAccepted   bool
		wantStillExist bool
		noAdd          bool
		doubleResolve  bool
	}{
		{
			name:         "accepts_and_records_merged_text",
			kind:         KindEdit,
			mergedText:   "user merged",
			wantAccepted: true,
			wantMerged:   "user merged",
		},
		{
			name:    "unknown_id",
			noAdd:   true,
			wantErr: ErrUnknown,
		},
		{
			name:           "exceeds_cap",
			kind:           KindEdit,
			mergedText:     string(make([]byte, Cap+1)),
			wantErr:        nil, // non-nil error but not a specific sentinel
			wantStillExist: true,
		},
		{
			name:          "idempotent",
			kind:          KindEdit,
			mergedText:    "b",
			doubleResolve: true,
			wantErr:       ErrUnknown,
		},
		{
			name:         "resolution_carries_merged_text",
			kind:         KindEdit,
			mergedText:   "keep me",
			wantAccepted: true,
			wantMerged:   "keep me",
		},
		{
			name:           "rejects_delete_kind",
			kind:           KindDelete,
			mergedText:     "merged",
			wantErr:        ErrMergeNotApplicable,
			wantStillExist: true,
		},
		{
			name:         "allows_create_kind",
			kind:         KindCreate,
			mergedText:   "user merged",
			wantAccepted: true,
			wantMerged:   "user merged",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := New()

			if tc.noAdd {
				_, err := s.ResolveWithText(context.Background(), "c-1", "nope", tc.mergedText)
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("ResolveWithText: got %v, want %v", err, tc.wantErr)
				}
				return
			}

			waitCh, readRes, err := s.Add(context.Background(), &AddParams{
				ToolCallID: "tc-1", ChatID: "c-1",
				Path: "foo.go", Kind: tc.kind, NewText: "x",
			})
			if err != nil {
				t.Fatalf("Add: %v", err)
			}

			if tc.doubleResolve {
				if _, err := s.ResolveWithText(context.Background(), "c-1", "tc-1", "a"); err != nil {
					t.Fatalf("first ResolveWithText: %v", err)
				}
				_, err := s.ResolveWithText(context.Background(), "c-1", "tc-1", tc.mergedText)
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("second ResolveWithText: got %v, want %v", err, tc.wantErr)
				}
				return
			}

			// For exceeds_cap: expect a non-nil error (not a specific sentinel).
			if tc.name == "exceeds_cap" {
				_, err := s.ResolveWithText(context.Background(), "c-1", "tc-1", tc.mergedText)
				if err == nil {
					t.Fatal("ResolveWithText(Cap+1): want error, got nil")
				}
				if _, ok := s.Get("tc-1"); !ok {
					t.Error("op was removed on cap-exceeded failure")
				}
				select {
				case <-waitCh:
					t.Error("waiter unblocked on cap-exceeded failure")
				case <-time.After(50 * time.Millisecond):
				}
				return
			}

			// Cases that need a concurrent waiter.
			type result struct {
				mergedText string
				accepted   bool
				merged     bool
			}
			done := make(chan result, 1)
			go func() {
				<-waitCh
				r := readRes()
				done <- result{r.MergedText, r.Accepted, r.Merged}
			}()

			_, err = s.ResolveWithText(context.Background(), "c-1", "tc-1", tc.mergedText)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("ResolveWithText: got %v, want %v", err, tc.wantErr)
				}
				if tc.wantStillExist {
					if _, ok := s.Get("tc-1"); !ok {
						t.Error("op was removed despite rejected merge")
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveWithText: %v", err)
			}

			select {
			case got := <-done:
				if got.accepted != tc.wantAccepted {
					t.Errorf("accepted = %v, want %v", got.accepted, tc.wantAccepted)
				}
				if got.mergedText != tc.wantMerged {
					t.Errorf("MergedText = %q, want %q", got.mergedText, tc.wantMerged)
				}
				// A successful ResolveWithText always flags Merged so the
				// caller overrides the agent's content even on an empty merge.
				if !got.merged {
					t.Error("Resolution.Merged = false after ResolveWithText, want true")
				}
			case <-time.After(time.Second):
				t.Fatal("waiter blocked after ResolveWithText")
			}

			if countForChat(s, "c-1") != 0 {
				t.Errorf("per-chat index after resolve = %d, want 0", countForChat(s, "c-1"))
			}
		})
	}
}

// TestResolveWithText_EmptyMergeStillMerged pins fix #4 at the store
// level: an EMPTY merged text still resolves accepted with Merged=true,
// so the caller writes the empty result instead of falling back to the
// agent's content. A plain Resolve, by contrast, reports Merged=false.
func TestResolveWithText_EmptyMergeStillMerged(t *testing.T) {
	t.Parallel()
	s := New()
	ctx := context.Background()
	waitCh, readRes, err := s.Add(ctx, &AddParams{
		ToolCallID: "tc-empty", ChatID: "c-1", Path: "f.go", Kind: KindEdit, OldText: "old", NewText: "agent",
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := s.ResolveWithText(ctx, "c-1", "tc-empty", ""); err != nil {
		t.Fatalf("ResolveWithText(empty): %v", err)
	}
	<-waitCh
	res := readRes()
	if !res.Accepted {
		t.Error("Accepted = false, want true")
	}
	if !res.Merged {
		t.Error("Merged = false for an empty user merge, want true (must override agent content)")
	}
	if res.MergedText != "" {
		t.Errorf("MergedText = %q, want empty", res.MergedText)
	}
}

// TestResolve_PlainAcceptNotMerged confirms the complement: a plain
// Resolve accept leaves Merged=false so the caller keeps the agent's
// proposed content.
func TestResolve_PlainAcceptNotMerged(t *testing.T) {
	t.Parallel()
	s := New()
	ctx := context.Background()
	waitCh, readRes, err := s.Add(ctx, &AddParams{
		ToolCallID: "tc-1", ChatID: "c-1", Path: "f.go", Kind: KindEdit, NewText: "agent",
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := s.Resolve(ctx, "c-1", "tc-1", ActionAccept); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	<-waitCh
	if res := readRes(); res.Merged {
		t.Error("Merged = true after a plain Resolve accept, want false")
	}
}

// TestResolveWithText_AtCapBoundaryAllowed pins the cap check at its
// exact boundary: merged text of exactly Cap bytes is accepted (the
// reject condition is len > Cap, not >=). Complements the "exceeds_cap"
// case in TestResolveWithText, which covers Cap+1.
func TestResolveWithText_AtCapBoundaryAllowed(t *testing.T) {
	t.Parallel()
	s := New()
	ctx := context.Background()
	if _, _, err := s.Add(ctx, &AddParams{ToolCallID: "tc1", ChatID: "c-bound", Path: "f.go", Kind: KindEdit, OldText: "o", NewText: "n"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	merged := strings.Repeat("a", Cap)
	snap, err := s.ResolveWithText(ctx, "c-bound", "tc1", merged)
	if err != nil {
		t.Fatalf("ResolveWithText(len==Cap) err = %v, want nil", err)
	}
	if snap.ToolCallID != "tc1" {
		t.Errorf("snap.ToolCallID = %q, want %q", snap.ToolCallID, "tc1")
	}
}

// TestRejectAllForChat_NoMergedTextLeak: with the Resolution pattern,
// merged text lives on the op struct and is returned via the closure.
// There is no side map to leak. This test verifies that
// RejectAllForChat correctly rejects pending ops and the Resolution
// closure reports accepted=false with empty MergedText.
func TestRejectAllForChat_NoMergedTextLeak(t *testing.T) {
	t.Parallel()
	s := New()
	waitCh1, readRes1, err := s.Add(context.Background(), &AddParams{
		ToolCallID: "tc-1", ChatID: "c-1",
		Path: "foo.go", Kind: KindEdit, NewText: "x",
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	waitCh2, readRes2, err := s.Add(context.Background(), &AddParams{
		ToolCallID: "tc-2", ChatID: "c-1",
		Path: "bar.go", Kind: KindEdit, NewText: "y",
	})
	if err != nil {
		t.Fatalf("Add 2: %v", err)
	}

	s.RejectAllForChat("c-1")

	<-waitCh1
	<-waitCh2
	res1 := readRes1()
	res2 := readRes2()
	if res1.Accepted {
		t.Error("tc-1: Resolution.Accepted = true after flush, want false")
	}
	if res1.MergedText != "" {
		t.Errorf("tc-1: Resolution.MergedText = %q, want empty", res1.MergedText)
	}
	if res2.Accepted {
		t.Error("tc-2: Resolution.Accepted = true after flush, want false")
	}
	if res2.MergedText != "" {
		t.Errorf("tc-2: Resolution.MergedText = %q, want empty", res2.MergedText)
	}
	if got := countForChat(s, "c-1"); got != 0 {
		t.Errorf("per-chat index after flush = %d, want 0", got)
	}
}
