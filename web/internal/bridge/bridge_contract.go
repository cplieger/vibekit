package bridge

import (
	"testing"

	"vibekit/internal/api"
)

// ACPBridgeContractTest verifies behavioral contracts that any
// api.ACPBridge implementation must satisfy without a real kiro-cli
// subprocess. Run this against both the real Bridge and test fakes to
// catch drift at the lifecycle level.
//
// Assertions are limited to properties that hold universally (before
// Start is called), so fakes that pre-populate SessionID/ModelID for
// convenience are not penalized.
func ACPBridgeContractTest(t *testing.T, newBridge func() api.ACPBridge) {
	t.Helper()

	t.Run("NotifCh_non_nil", func(t *testing.T) {
		b := newBridge()
		if b.NotifCh() == nil {
			t.Error("NotifCh() returned nil, want non-nil channel")
		}
	})

	t.Run("Stop_idempotent", func(t *testing.T) {
		b := newBridge()
		// Stop must not panic when called twice.
		b.Stop()
		b.Stop()
	})

	t.Run("CurrentMode_empty_before_Start", func(t *testing.T) {
		b := newBridge()
		if got := b.CurrentMode(); got != "" {
			t.Errorf("CurrentMode() = %q before Start, want empty", got)
		}
	})
}
