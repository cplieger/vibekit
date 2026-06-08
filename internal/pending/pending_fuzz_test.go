package pending

import (
	"context"
	"fmt"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// FuzzStoreInvariants exercises random sequences of Add/Resolve/RejectAllForChat
// operations and asserts structural invariants after each step:
//
//  1. CountForChat monotonically decreases after each Resolve/RejectAllForChat.
//  2. ListForChat preserves insertion order.
//  3. Resolve is idempotent (second call returns ErrUnknown).
//  4. RejectAllForChat on an empty chat returns nil.
//  5. Add→Resolve round-trip always unblocks the waiter.
//
// The seed corpus encodes operation sequences as a byte slice where each byte
// selects an operation type and parameters are derived from subsequent bytes.
func FuzzStoreInvariants(f *testing.F) {
	// Seed corpus: representative operation sequences.
	f.Add([]byte{0, 1, 2, 3, 4, 5}) // add, resolve, rejectAll, then add
	f.Add([]byte{0, 0, 0, 2, 2, 2}) // three adds then three resolves
	f.Add([]byte{4, 4, 4})          // rejectAll on empty chats
	f.Add([]byte{0, 4, 0, 4})       // add then flush, repeat
	f.Add([]byte{0, 0, 0, 0, 4})    // many adds then bulk flush

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}

		s := New()
		ctx := context.Background()

		// Track state for invariant checking.
		type pendingOp struct {
			waitCh     <-chan struct{}
			toolCallID string
			chatID     api.ChatID
		}

		var (
			pending   []pendingOp // ops that haven't been resolved yet
			nextID    int
			chatIDs   = []api.ChatID{"chat-a", "chat-b", "chat-c"}
			pathNames = []string{"a.go", "b.go", "c.go", "d.go", "e.go"}
		)

		for _, b := range data {
			op := b % 5 // 5 operation types

			switch op {
			case 0: // Add
				chatID := chatIDs[int(b/5)%len(chatIDs)]
				path := pathNames[nextID%len(pathNames)]
				toolCallID := fmt.Sprintf("tc-%d", nextID)
				nextID++

				waitCh, _, err := s.Add(ctx, &AddParams{
					ToolCallID: toolCallID,
					ChatID:     chatID,
					Path:       path,
					Kind:       KindEdit,
					OldText:    "old",
					NewText:    "new",
				})
				if err != nil {
					// Expected errors: ErrPathBusy, ErrTooManyPending.
					continue
				}
				pending = append(pending, pendingOp{
					toolCallID: toolCallID,
					chatID:     chatID,
					waitCh:     waitCh,
				})

			case 1: // Resolve accept (pick from pending)
				if len(pending) == 0 {
					continue
				}
				idx := int(b/5) % len(pending)
				p := pending[idx]

				countBefore := s.CountForChat(p.chatID)
				_, err := s.Resolve(context.Background(), p.toolCallID, ActionAccept)
				if err != nil {
					t.Fatalf("Resolve(%q, accept): %v", p.toolCallID, err)
				}

				// Invariant 1: count decreases after resolve.
				countAfter := s.CountForChat(p.chatID)
				if countAfter > countBefore {
					t.Fatalf("CountForChat increased after Resolve: %d -> %d", countBefore, countAfter)
				}

				// Invariant 5: waiter unblocked.
				select {
				case <-p.waitCh:
				default:
					t.Fatalf("waiter not unblocked after Resolve(%q)", p.toolCallID)
				}

				// Invariant 3: second resolve returns ErrUnknown.
				_, err = s.Resolve(context.Background(), p.toolCallID, ActionAccept)
				if err != ErrUnknown {
					t.Fatalf("second Resolve(%q) = %v, want ErrUnknown", p.toolCallID, err)
				}

				pending = append(pending[:idx], pending[idx+1:]...)

			case 2: // Resolve reject (pick from pending)
				if len(pending) == 0 {
					continue
				}
				idx := int(b/5) % len(pending)
				p := pending[idx]

				countBefore := s.CountForChat(p.chatID)
				_, err := s.Resolve(context.Background(), p.toolCallID, ActionReject)
				if err != nil {
					t.Fatalf("Resolve(%q, reject): %v", p.toolCallID, err)
				}

				// Invariant 1: count decreases.
				countAfter := s.CountForChat(p.chatID)
				if countAfter > countBefore {
					t.Fatalf("CountForChat increased after Resolve: %d -> %d", countBefore, countAfter)
				}

				// Invariant 5: waiter unblocked.
				select {
				case <-p.waitCh:
				default:
					t.Fatalf("waiter not unblocked after Resolve(%q)", p.toolCallID)
				}

				// Invariant 3: idempotent.
				_, err = s.Resolve(context.Background(), p.toolCallID, ActionReject)
				if err != ErrUnknown {
					t.Fatalf("second Resolve(%q) = %v, want ErrUnknown", p.toolCallID, err)
				}

				pending = append(pending[:idx], pending[idx+1:]...)

			case 3: // RejectAllForChat (pick a chat)
				chatID := chatIDs[int(b/5)%len(chatIDs)]

				countBefore := s.CountForChat(chatID)
				snaps := s.RejectAllForChat(chatID)

				// Invariant 1: count goes to zero.
				countAfter := s.CountForChat(chatID)
				if countAfter != 0 {
					t.Fatalf("CountForChat(%q) after RejectAllForChat = %d, want 0", chatID, countAfter)
				}

				// Invariant 4: empty chat returns nil.
				if countBefore == 0 && snaps != nil {
					t.Fatalf("RejectAllForChat on empty chat returned non-nil: %v", snaps)
				}

				// Invariant 5: all waiters for this chat unblocked.
				var remaining []pendingOp
				for _, p := range pending {
					if p.chatID == chatID {
						select {
						case <-p.waitCh:
						default:
							t.Fatalf("waiter %q not unblocked after RejectAllForChat(%q)", p.toolCallID, chatID)
						}
					} else {
						remaining = append(remaining, p)
					}
				}
				pending = remaining

			case 4: // ListForChat order check
				chatID := chatIDs[int(b/5)%len(chatIDs)]
				list := s.ListForChat(chatID)

				// Invariant 2: insertion order preserved. Verify tool call IDs
				// are in the same relative order as our pending tracker.
				var expectedOrder []string
				for _, p := range pending {
					if p.chatID == chatID {
						expectedOrder = append(expectedOrder, p.toolCallID)
					}
				}
				if len(list) != len(expectedOrder) {
					t.Fatalf("ListForChat(%q) len=%d, want %d", chatID, len(list), len(expectedOrder))
				}
				for i, snap := range list {
					if snap.ToolCallID != expectedOrder[i] {
						t.Fatalf("ListForChat(%q)[%d].ToolCallID = %q, want %q",
							chatID, i, snap.ToolCallID, expectedOrder[i])
					}
				}
			}
		}
	})
}
