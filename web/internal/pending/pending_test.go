package pending

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// TestAdd_BasicAcceptFlow pins the happy path: stage, resolve accept,
// waiter unblocks with accepted=true.
func TestAdd_BasicAcceptFlow(t *testing.T) {
	t.Parallel()
	s := New()
	waitCh, accepted, err := s.Add(context.Background(), &AddParams{
		ToolCallID: "tc-1", ChatID: "c-1",
		Path: "foo.go", Kind: KindEdit,
		OldText: "old", NewText: "new",
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	done := make(chan bool, 1)
	go func() {
		<-waitCh
		done <- accepted().Accepted
	}()

	if _, err := s.Resolve(context.Background(), "tc-1", ActionAccept); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	select {
	case got := <-done:
		if !got {
			t.Fatalf("waiter saw accepted=false, want true")
		}
	case <-time.After(time.Second):
		t.Fatal("waiter blocked after resolve")
	}
	if s.CountForChat("c-1") != 0 {
		t.Errorf("CountForChat after resolve = %d, want 0", s.CountForChat("c-1"))
	}
}

// TestAdd_Reject mirrors the accept flow for reject.
func TestAdd_Reject(t *testing.T) {
	t.Parallel()
	s := New()
	waitCh, accepted, err := s.Add(context.Background(), &AddParams{
		ToolCallID: "tc-1", ChatID: "c-1",
		Path: "foo.go", Kind: KindEdit, NewText: "new",
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	done := make(chan bool, 1)
	go func() { <-waitCh; done <- accepted().Accepted }()

	if _, err := s.Resolve(context.Background(), "tc-1", ActionReject); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	select {
	case got := <-done:
		if got {
			t.Fatalf("waiter saw accepted=true, want false")
		}
	case <-time.After(time.Second):
		t.Fatal("waiter blocked after resolve")
	}
}

// TestAdd_PathBusy rejects a second op for the same (chat, path) and
// allows a second op on a different path.
func TestAdd_PathBusy(t *testing.T) {
	t.Parallel()
	s := New()
	if _, _, err := s.Add(context.Background(), &AddParams{
		ToolCallID: "tc-1", ChatID: "c-1", Path: "foo.go", Kind: KindEdit, NewText: "x",
	}); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	_, _, err := s.Add(context.Background(), &AddParams{
		ToolCallID: "tc-2", ChatID: "c-1", Path: "foo.go", Kind: KindEdit, NewText: "y",
	})
	if !errors.Is(err, ErrPathBusy) {
		t.Fatalf("same-path Add: got %v, want ErrPathBusy", err)
	}
	if _, _, err := s.Add(context.Background(), &AddParams{
		ToolCallID: "tc-3", ChatID: "c-1", Path: "bar.go", Kind: KindEdit, NewText: "z",
	}); err != nil {
		t.Fatalf("different-path Add: %v", err)
	}
}

// TestAdd_DifferentChatsSamePath is allowed: path-busy is per-chat.
func TestAdd_DifferentChatsSamePath(t *testing.T) {
	t.Parallel()
	s := New()
	if _, _, err := s.Add(context.Background(), &AddParams{
		ToolCallID: "tc-1", ChatID: "c-1", Path: "foo.go", Kind: KindEdit, NewText: "x",
	}); err != nil {
		t.Fatalf("chat1 Add: %v", err)
	}
	if _, _, err := s.Add(context.Background(), &AddParams{
		ToolCallID: "tc-2", ChatID: "c-2", Path: "foo.go", Kind: KindEdit, NewText: "y",
	}); err != nil {
		t.Fatalf("chat2 same-path Add: %v", err)
	}
}

// TestResolve_Unknown returns ErrUnknown for a never-staged id.
func TestResolve_Unknown(t *testing.T) {
	t.Parallel()
	s := New()
	if _, err := s.Resolve(context.Background(), "nope", ActionAccept); !errors.Is(err, ErrUnknown) {
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
	if _, err := s.Resolve(context.Background(), "tc-1", ActionAccept); err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	if _, err := s.Resolve(context.Background(), "tc-1", ActionReject); !errors.Is(err, ErrUnknown) {
		t.Fatalf("second Resolve: got %v, want ErrUnknown", err)
	}
}

// TestResolve_BadAction rejects anything other than accept/reject.
// TestAdd_ValidationErrors covers every empty/invalid input.
func TestAdd_ValidationErrors(t *testing.T) {
	t.Parallel()
	s := New()
	cases := []struct {
		want error
		name string
		p    AddParams
	}{
		{want: ErrEmptyID, name: "empty id", p: AddParams{ChatID: "c", Path: "p", Kind: KindEdit}},
		{want: ErrEmptyChatID, name: "empty chat_id", p: AddParams{ToolCallID: "t", Path: "p", Kind: KindEdit}},
		{want: ErrEmptyPath, name: "empty path", p: AddParams{ToolCallID: "t", ChatID: "c", Kind: KindEdit}},
		{want: ErrBadKind, name: "bad kind", p: AddParams{ToolCallID: "t", ChatID: "c", Path: "p", Kind: "chown"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := s.Add(context.Background(), &tc.p); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
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
	if s.CountForChat("c-1") != 0 {
		t.Fatalf("c-1 count after flush = %d, want 0", s.CountForChat("c-1"))
	}
	if s.CountForChat("c-2") != 1 {
		t.Fatalf("c-2 count after flush = %d, want 1 (untouched)", s.CountForChat("c-2"))
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

// TestRejectAllForChat_Empty is a no-op.
func TestRejectAllForChat_Empty(t *testing.T) {
	t.Parallel()
	s := New()
	if got := s.RejectAllForChat("nothing-here"); got != nil {
		t.Fatalf("empty RejectAllForChat returned %v, want nil", got)
	}
}

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

// TestConcurrentAddResolve exercises the lock contract with many
// goroutines racing Add/Resolve. Failure looks like a deadlock, a
// lost resume signal, or a double-close panic.
func TestConcurrentAddResolve(t *testing.T) {
	t.Parallel()
	s := New()
	const n = 50
	done := make(chan struct{}, n)
	for i := range n {
		id := string(rune('a'+i%26)) + string(rune('0'+i/26))
		waitCh, accepted, err := s.Add(context.Background(), &AddParams{
			ToolCallID: id, ChatID: "c-1",
			Path: id + ".go", Kind: KindEdit, NewText: "x",
		})
		if err != nil {
			t.Fatalf("Add %s: %v", id, err)
		}
		go func(id string) {
			<-waitCh
			_ = accepted()
			done <- struct{}{}
		}(id)
	}
	// Resolve half via Accept, half via RejectAllForChat.
	for i := range n / 2 {
		id := string(rune('a'+i%26)) + string(rune('0'+i/26))
		if _, err := s.Resolve(context.Background(), id, ActionAccept); err != nil {
			t.Fatalf("Resolve: %v", err)
		}
	}
	s.RejectAllForChat("c-1")

	timeout := time.After(5 * time.Second)
	for i := range n {
		select {
		case <-done:
		case <-timeout:
			t.Fatalf("deadlock: only %d of %d waiters released", i, n)
		}
	}
}

// --- ResolveWithText / Resolution ---

// TestResolveWithText is a table-driven test covering all
// ResolveWithText scenarios: happy-path accept, unknown id, cap
// exceeded, idempotent double-resolve, resolution carries merged
// text, delete-kind rejection, and create-kind allowance.
func TestResolveWithText(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		kind           Kind
		mergedText     string
		wantErr        error
		wantAccepted   bool
		wantMerged     string
		wantStillExist bool // op should remain in store after call
		noAdd          bool // skip Add (test unknown id)
		doubleResolve  bool // resolve once before the test call
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
				_, err := s.ResolveWithText(context.Background(), "nope", tc.mergedText)
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
				if _, err := s.ResolveWithText(context.Background(), "tc-1", "a"); err != nil {
					t.Fatalf("first ResolveWithText: %v", err)
				}
				_, err := s.ResolveWithText(context.Background(), "tc-1", tc.mergedText)
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("second ResolveWithText: got %v, want %v", err, tc.wantErr)
				}
				return
			}

			// For exceeds_cap: expect a non-nil error (not a specific sentinel).
			if tc.name == "exceeds_cap" {
				_, err := s.ResolveWithText(context.Background(), "tc-1", tc.mergedText)
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
				accepted   bool
				mergedText string
			}
			done := make(chan result, 1)
			go func() {
				<-waitCh
				r := readRes()
				done <- result{r.Accepted, r.MergedText}
			}()

			_, err = s.ResolveWithText(context.Background(), "tc-1", tc.mergedText)
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
			case <-time.After(time.Second):
				t.Fatal("waiter blocked after ResolveWithText")
			}

			if s.CountForChat("c-1") != 0 {
				t.Errorf("CountForChat after resolve = %d, want 0", s.CountForChat("c-1"))
			}
		})
	}
}

// TestAdd_CapPerChatExceeded: once MaxPendingPerChat is reached,
// further Add calls for the same chat return ErrTooManyPending.
func TestAdd_CapPerChatExceeded(t *testing.T) {
	t.Parallel()
	s := New()
	for i := range MaxPendingPerChat {
		id := "tc-" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		path := "f" + string(rune('a'+i%26)) + string(rune('0'+i/26)) + ".go"
		if _, _, err := s.Add(context.Background(), &AddParams{
			ToolCallID: id, ChatID: "c-1", Path: path,
			Kind: KindEdit, NewText: "x",
		}); err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}
	_, _, err := s.Add(context.Background(), &AddParams{
		ToolCallID: "over-cap", ChatID: "c-1", Path: "zzz.go",
		Kind: KindEdit, NewText: "x",
	})
	if !errors.Is(err, ErrTooManyPending) {
		t.Fatalf("over-cap Add: got %v, want ErrTooManyPending", err)
	}
	// Different chat should still work while cap is per-chat.
	if _, _, err := s.Add(context.Background(), &AddParams{
		ToolCallID: "other-chat", ChatID: "c-2", Path: "foo.go",
		Kind: KindEdit, NewText: "x",
	}); err != nil {
		t.Fatalf("other chat Add: %v", err)
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
	if got := s.CountForChat("c-1"); got != 0 {
		t.Errorf("CountForChat after flush = %d, want 0", got)
	}
}

// --- Context cancellation ---

// TestAdd_ContextCancellation verifies that cancelling the context
// passed to Add auto-rejects the op and unblocks the waiter.
func TestAdd_ContextCancellation(t *testing.T) {
	t.Parallel()
	s := New()
	ctx, cancel := context.WithCancel(context.Background())
	waitCh, readRes, err := s.Add(ctx, &AddParams{
		ToolCallID: "tc-ctx", ChatID: "c-1",
		Path: "foo.go", Kind: KindEdit, NewText: "x",
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Cancel the context; the op should auto-reject.
	cancel()

	select {
	case <-waitCh:
	case <-time.After(time.Second):
		t.Fatal("waiter blocked after context cancel")
	}

	res := readRes()
	if res.Accepted {
		t.Error("Resolution.Accepted = true after ctx cancel, want false")
	}
	if res.MergedText != "" {
		t.Errorf("Resolution.MergedText = %q, want empty", res.MergedText)
	}
	if s.CountForChat("c-1") != 0 {
		t.Errorf("CountForChat after ctx cancel = %d, want 0", s.CountForChat("c-1"))
	}
}

// TestAdd_ContextCancelAfterResolve verifies that cancelling the
// context after a normal resolve is a no-op (no double-close panic).
func TestAdd_ContextCancelAfterResolve(t *testing.T) {
	t.Parallel()
	s := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waitCh, _, err := s.Add(ctx, &AddParams{
		ToolCallID: "tc-ctx2", ChatID: "c-1",
		Path: "bar.go", Kind: KindEdit, NewText: "x",
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Resolve normally first.
	if _, err := s.Resolve(context.Background(), "tc-ctx2", ActionAccept); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	<-waitCh

	// Cancel after resolve — must not panic.
	cancel()
	// Give the goroutine time to observe the cancel and exit cleanly.
	time.Sleep(50 * time.Millisecond)
}

// --- removeID characterization tests ---
//
// removeID is unexported and defensively handles the not-found and
// empty cases, both of which are unreachable via the public API
// today (callers always pass an id they just located in byChat).
// These tests pin the invariants so a future refactor — e.g.
// switching to slices.DeleteFunc — cannot silently regress the
// empty-slice or not-found semantics.

// TestRemoveID_NotFound returns the slice unchanged (length + values
// preserved) when target is absent.
func TestRemoveID_NotFound(t *testing.T) {
	t.Parallel()

	in := []string{"a", "b", "c"}
	out := removeID(in, "missing")

	if len(out) != 3 {
		t.Fatalf("removeID not-found len = %d, want 3", len(out))
	}
	for i, v := range []string{"a", "b", "c"} {
		if out[i] != v {
			t.Errorf("removeID not-found [%d] = %q, want %q", i, out[i], v)
		}
	}
}

// TestRemoveID_Empty handles nil/empty input without panic.
func TestRemoveID_Empty(t *testing.T) {
	t.Parallel()

	out := removeID(nil, "anything")

	if len(out) != 0 {
		t.Errorf("removeID(nil) len = %d, want 0", len(out))
	}
}

// TestRemoveID_FirstMiddleLast covers every positional case through
// a table-driven subtest.
func TestRemoveID_FirstMiddleLast(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		target string
		in     []string
		want   []string
	}{
		{name: "first", in: []string{"a", "b", "c"}, target: "a", want: []string{"b", "c"}},
		{name: "middle", in: []string{"a", "b", "c"}, target: "b", want: []string{"a", "c"}},
		{name: "last", in: []string{"a", "b", "c"}, target: "c", want: []string{"a", "b"}},
		{name: "only", in: []string{"a"}, target: "a", want: []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := removeID(append([]string(nil), tc.in...), tc.target)
			if len(got) != len(tc.want) {
				t.Fatalf("removeID(%v, %q) len = %d, want %d", tc.in, tc.target, len(got), len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("removeID(%v, %q)[%d] = %q, want %q", tc.in, tc.target, i, got[i], tc.want[i])
				}
			}
		})
	}
}

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
