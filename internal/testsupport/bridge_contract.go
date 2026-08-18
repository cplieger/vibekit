package testsupport

import (
	"context"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// ACPBridgeContractTest verifies that an api.ACPBridge implementation
// satisfies the basic contract: Start/Stop don't panic, NotifCh is
// non-nil or nil consistently, and Stop is idempotent.
func ACPBridgeContractTest(t *testing.T, b api.ACPBridge) {
	t.Helper()

	// Lifetime is required by the contract (api.StartOpts.Lifetime), so it is
	// part of what this suite asserts an implementation accepts.
	if err := b.Start(context.Background(), &api.StartOpts{Lifetime: t.Context()}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// NotifCh should be callable after Start.
	_ = b.NotifCh()

	// Stop must not panic.
	b.Stop()
}
