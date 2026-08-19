package push

import (
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
	"pgregory.net/rapid"
)

// TestSubscriptionLifecycle_RapidInvariants exercises arbitrary sequences of
// Subscribe/Unsubscribe operations and asserts lifecycle invariants.
func TestSubscriptionLifecycle_RapidInvariants(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		dir := t.TempDir()
		svc := New(t.Context(), dir, "mailto:test@example.com")
		defer svc.Close()

		// Track expected state.
		expected := make(map[string]bool)

		numOps := rapid.IntRange(1, 50).Draw(rt, "numOps")
		endpoints := rapid.SliceOfN(rapid.StringMatching(`https://push\.example\.com/[a-z0-9]{8}`), 1, 10).Draw(rt, "endpoints")

		for range numOps {
			op := rapid.IntRange(0, 1).Draw(rt, "op")
			idx := rapid.IntRange(0, len(endpoints)-1).Draw(rt, "idx")
			endpoint := endpoints[idx]

			switch op {
			case 0: // Subscribe
				svc.Subscribe(vibekit.PushSubscription{
					Endpoint: endpoint,
					Keys: vibekit.PushSubscriptionKeys{
						P256dh: "dGVzdA==",
						Auth:   "dGVzdA==",
					},
				})
				expected[endpoint] = true

			case 1: // Unsubscribe (idempotent)
				svc.Unsubscribe(endpoint)
				delete(expected, endpoint)
			}
		}

		// Invariant: HasSubscribers matches expected state.
		wantHas := len(expected) > 0
		if got := svc.HasSubscribers(); got != wantHas {
			rt.Fatalf("HasSubscribers() = %v, want %v (expected %d subs)", got, wantHas, len(expected))
		}
	})
}
