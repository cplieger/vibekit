package pending

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// operations.go:51 CONDITIONALS_BOUNDARY (`len(s.ops) >= MaxPendingTotal` -> `>`):
// at exactly the total cap Add must reject with ErrTooManyPending. The `>`
// mutant would accept one more op. The store maps are populated directly (in
// internal test package) to reach the boundary without thousands of Add calls;
// the target chat has zero per-chat ops so the per-chat cap check passes first.
func Test_gk_vibekit_u29_AddTotalCapBoundary(t *testing.T) {
	s := New()
	for i := 0; i < MaxPendingTotal; i++ {
		s.ops[fmt.Sprintf("gk_vibekit_u29_dummy_%d", i)] = &op{}
	}
	_, _, err := s.Add(context.Background(), &AddParams{
		ToolCallID: "gk_vibekit_u29_probe",
		ChatID:     "gk-c-29",
		Path:       "gkprobe.go",
		Kind:       KindEdit,
	})
	if err != ErrTooManyPending {
		t.Errorf("Add at total cap (%d ops) err = %v, want ErrTooManyPending", MaxPendingTotal, err)
	}
}

// operations.go:114 CONDITIONALS_NEGATION (`len(paths) == 0` -> `!=`): unlinkOp
// deletes the per-chat path index only when it becomes empty. With two staged
// paths for one chat, resolving the first must leave the second still busy. The
// `!= 0` mutant drops the whole (still non-empty) index, so the second path is
// no longer reported busy.
func Test_gk_vibekit_u29_unlinkOpKeepsOtherPath(t *testing.T) {
	s := New()
	ctx := context.Background()
	const chat = "gk-c-29b"

	if _, _, err := s.Add(ctx, &AddParams{ToolCallID: "gktc1", ChatID: chat, Path: "gkP1.go", Kind: KindEdit, NewText: "a"}); err != nil {
		t.Fatalf("Add op1: %v", err)
	}
	if _, _, err := s.Add(ctx, &AddParams{ToolCallID: "gktc2", ChatID: chat, Path: "gkP2.go", Kind: KindEdit, NewText: "b"}); err != nil {
		t.Fatalf("Add op2: %v", err)
	}
	if _, err := s.Resolve(ctx, "gktc1", ActionAccept); err != nil {
		t.Fatalf("Resolve op1: %v", err)
	}

	// P2 is still pending, so a fresh Add on it must be rejected as busy.
	_, _, err := s.Add(ctx, &AddParams{ToolCallID: "gktc3", ChatID: chat, Path: "gkP2.go", Kind: KindEdit, NewText: "c"})
	if err != ErrPathBusy {
		t.Errorf("Add on still-pending path gkP2.go err = %v, want ErrPathBusy", err)
	}
}

// resolve.go:31 CONDITIONALS_BOUNDARY (`len(mergedText) > Cap` -> `>=`): merged
// text of exactly Cap bytes is allowed. The `>=` mutant rejects it at the
// boundary with the "exceeds cap" error.
func Test_gk_vibekit_u29_ResolveWithTextCapBoundary(t *testing.T) {
	s := New()
	ctx := context.Background()
	if _, _, err := s.Add(ctx, &AddParams{ToolCallID: "gktc1", ChatID: "gk-c-29c", Path: "gkf.go", Kind: KindEdit, OldText: "o", NewText: "n"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	merged := strings.Repeat("a", Cap)
	snap, err := s.ResolveWithText(ctx, "gktc1", merged)
	if err != nil {
		t.Fatalf("ResolveWithText(len==Cap) err = %v, want nil", err)
	}
	if snap.ToolCallID != "gktc1" {
		t.Errorf("snap.ToolCallID = %q, want %q", snap.ToolCallID, "gktc1")
	}
}
