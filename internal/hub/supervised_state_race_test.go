package hub

import (
	"context"
	"sync"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// TestSupervisedState_ConcurrentSetClearHas exercises the race between
// SetTrust (which broadcasts), ClearTrust (which broadcasts), and
// HasTrust (read-only). The key invariant: after ClearTrust returns,
// HasTrust must return false for that chatID.
func TestSupervisedState_ConcurrentSetClearHas(t *testing.T) {
	var broadcasts sync.WaitGroup
	ss := newSupervisedState(func(_ context.Context, _ api.ServerEvent) {
		// simulate broadcast latency
	})

	const chats = 10
	const ops = 100

	var wg sync.WaitGroup

	// Setters.
	wg.Go(func() {
		for i := range ops {
			chatID := api.ChatID("chat-" + string(rune('A'+i%chats)))
			ss.SetTrust(chatID)
		}
	})

	// Clearers.
	wg.Go(func() {
		for i := range ops {
			chatID := api.ChatID("chat-" + string(rune('A'+i%chats)))
			ss.ClearTrust(chatID, api.ClearReasonTurnEnded)
		}
	})

	// Readers.
	wg.Go(func() {
		for i := range ops {
			chatID := api.ChatID("chat-" + string(rune('A'+i%chats)))
			_ = ss.HasTrust(chatID)
		}
	})

	// TrustedChatIDs reader.
	wg.Go(func() {
		for range ops {
			_ = ss.TrustedChatIDs("")
			_ = ss.TrustedChatIDs("chat-A")
		}
	})

	wg.Wait()
	broadcasts.Wait()
}

// TestSupervisedState_ClearTrustLinearization verifies that after
// ClearTrust returns, HasTrust reports false — no stale reads.
func TestSupervisedState_ClearTrustLinearization(t *testing.T) {
	ss := newSupervisedState(func(_ context.Context, _ api.ServerEvent) {})

	chatID := api.ChatID("linearize-test")
	ss.SetTrust(chatID)
	if !ss.HasTrust(chatID) {
		t.Fatal("expected HasTrust true after SetTrust")
	}
	ss.ClearTrust(chatID, api.ClearReasonCancelled)
	if ss.HasTrust(chatID) {
		t.Fatal("expected HasTrust false after ClearTrust")
	}
}
