package testsupport

import (
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// ACPPreStartBridge is the subject of ACPBridgePreStartContractTest: the 5
// methods of a kiro-cli ACP bridge that this suite reads. There is no shared
// ACPBridge interface any more — internal/agent declares the contract at seven
// widths, up to 15 methods — and a contract suite has no business naming a
// method it does not exercise.
type ACPPreStartBridge interface {
	NotifCh() <-chan vibekit.Notification
	Stop()
	CurrentMode() string
	Modes() []vibekit.SessionMode
	Models() []vibekit.SessionModel
}

// ACPBridgePreStartContractTest verifies behavioral contracts that any ACP
// bridge implementation must satisfy without a real kiro-cli subprocess. Run
// against both the real Bridge and test fakes to catch drift at the lifecycle
// level.
//
// Assertions are limited to properties that hold universally (before Start is
// called), so fakes that pre-populate SessionID/ModelID for convenience are not
// penalized.
//
// It lives here rather than in internal/bridge so no production package has to
// import "testing".
func ACPBridgePreStartContractTest(t *testing.T, newBridge func() ACPPreStartBridge) {
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
