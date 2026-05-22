package hub

import (
	"testing"

	"vibekit/internal/api"
	"vibekit/internal/chat"
)

// TestRealChatStore_Contract runs the reusable ChatStoreContractTest suite
// against the real chat.Store backed by a temporary directory. This closes
// the contract-parity loop: both the fake and the real implementation are
// verified against the same behavioral expectations.
func TestRealChatStore_Contract(t *testing.T) {
	ChatStoreContractTest(t, func(t *testing.T) api.ChatStore {
		t.Helper()
		s, err := chat.NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("chat.NewStore: %v", err)
		}
		return s
	})
}
