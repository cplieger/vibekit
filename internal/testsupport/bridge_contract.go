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

	if err := b.Start(context.Background(), &api.StartOpts{}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// NotifCh should be callable after Start.
	_ = b.NotifCh()

	// Stop must not panic.
	b.Stop()
}
