package pending

import (
	"context"
	"errors"
	"fmt"
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

	if _, err := s.Resolve(context.Background(), "c-1", "tc-1", ActionAccept); err != nil {
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
	if countForChat(s, "c-1") != 0 {
		t.Errorf("per-chat index after resolve = %d, want 0", countForChat(s, "c-1"))
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

	if _, err := s.Resolve(context.Background(), "c-1", "tc-1", ActionReject); err != nil {
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

// TestResolve_KeepsOtherStagedPathBusy verifies that resolving one op
// does not drop the per-chat path index for a different still-pending
// path: with two staged paths in one chat, resolving the first must
// leave the second reported busy (a fresh Add on it returns ErrPathBusy).
func TestResolve_KeepsOtherStagedPathBusy(t *testing.T) {
	t.Parallel()
	s := New()
	ctx := context.Background()
	const chat = "c-keep"

	if _, _, err := s.Add(ctx, &AddParams{ToolCallID: "tc1", ChatID: chat, Path: "p1.go", Kind: KindEdit, NewText: "a"}); err != nil {
		t.Fatalf("Add op1: %v", err)
	}
	if _, _, err := s.Add(ctx, &AddParams{ToolCallID: "tc2", ChatID: chat, Path: "p2.go", Kind: KindEdit, NewText: "b"}); err != nil {
		t.Fatalf("Add op2: %v", err)
	}
	if _, err := s.Resolve(ctx, chat, "tc1", ActionAccept); err != nil {
		t.Fatalf("Resolve op1: %v", err)
	}

	// p2 is still pending, so a fresh Add on it must be rejected as busy.
	_, _, err := s.Add(ctx, &AddParams{ToolCallID: "tc3", ChatID: chat, Path: "p2.go", Kind: KindEdit, NewText: "c"})
	if !errors.Is(err, ErrPathBusy) {
		t.Errorf("Add on still-pending path p2.go err = %v, want ErrPathBusy", err)
	}
}

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

// TestAdd_TotalCapBoundary pins the total-cap check at its exact
// boundary: at MaxPendingTotal staged ops, the next Add must reject
// with ErrTooManyPending (the reject condition is len >= cap, not >).
// The ops map is populated directly so the boundary is reached without
// thousands of Add calls; the probe chat has zero per-chat ops, so the
// per-chat cap check passes and the total-cap check is what fires.
func TestAdd_TotalCapBoundary(t *testing.T) {
	t.Parallel()
	s := New()
	for i := range MaxPendingTotal {
		s.ops[fmt.Sprintf("dummy-%d", i)] = &op{}
	}
	_, _, err := s.Add(context.Background(), &AddParams{
		ToolCallID: "probe",
		ChatID:     "c-cap",
		Path:       "probe.go",
		Kind:       KindEdit,
	})
	if !errors.Is(err, ErrTooManyPending) {
		t.Errorf("Add at total cap (%d ops) err = %v, want ErrTooManyPending", MaxPendingTotal, err)
	}
}

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
	if countForChat(s, "c-1") != 0 {
		t.Errorf("per-chat index after ctx cancel = %d, want 0", countForChat(s, "c-1"))
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
	if _, err := s.Resolve(context.Background(), "c-1", "tc-ctx2", ActionAccept); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	<-waitCh

	// Cancel after resolve — must not panic.
	cancel()
	// Give the goroutine time to observe the cancel and exit cleanly.
	time.Sleep(50 * time.Millisecond)
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
		if _, err := s.Resolve(context.Background(), "c-1", id, ActionAccept); err != nil {
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
