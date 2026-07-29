package testsupport

import (
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// ACPBridgePreStartContractTest verifies behavioral contracts that any
// api.ACPBridge implementation must satisfy without a real kiro-cli
// subprocess. Run this against both the real Bridge and test fakes to
// catch drift at the lifecycle level.
//
// Assertions are limited to properties that hold universally (before
// Start is called), so fakes that pre-populate SessionID/ModelID for
// convenience are not penalized. The post-Start smoke variant is
// ACPBridgeContractTest.
//
// It lives here rather than in internal/bridge so no production package
// has to import "testing".
func ACPBridgePreStartContractTest(t *testing.T, newBridge func() api.ACPBridge) {
	t.Helper()

	t.Run("NotifCh_non_nil", func(t *testing.T) {
		b := newBridge()
		if b.NotifCh() == nil {
			t.Error("NotifCh() returned nil, want non-nil channel")
		}
	})

	t.Run("Stop_idempotent", func(_ *testing.T) {
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

	t.Run("Modes_nil_before_Start", func(t *testing.T) {
		b := newBridge()
		if got := b.Modes(); got != nil {
			t.Errorf("Modes() = %v before Start, want nil", got)
		}
	})

	t.Run("Models_nil_before_Start", func(t *testing.T) {
		b := newBridge()
		if got := b.Models(); got != nil {
			t.Errorf("Models() = %v before Start, want nil", got)
		}
	})
}
