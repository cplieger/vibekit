package command

// Closing a tab is a teardown, and a teardown that depends on the thing it is
// tearing down being present is a teardown that fails exactly when it is most
// needed. So the shape pinned here is the ABSENT bridge: a chat the user never
// prompted, or one whose process is already gone, still has permissions to
// clear and state to close.
//
// The subject is closeChatTeardown rather than a command: close_chat left the
// client surface when close_tab arrived, and the teardown is now internal
// machinery the membership coordinator calls for every chat tab it closes.

import (
	"context"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// closeDeps records the teardown steps and hands out the bridge the test
// scripts, including none at all.
type closeDeps struct {
	*benchDeps
	bridge  Bridge
	cleared []vibekit.ChatID
	closed  []vibekit.ChatID
}

func (d *closeDeps) Bridge(vibekit.ChatID) Bridge { return d.bridge }

func (d *closeDeps) ClearPendingPermsForChat(id vibekit.ChatID) {
	d.cleared = append(d.cleared, id)
}

func (d *closeDeps) CloseChatState(_ context.Context, id vibekit.ChatID) {
	d.closed = append(d.closed, id)
}

// cancelBridge records the notifications the close cascade sends, which
// recordingBridge drops (it records Call, and a cancel is a Notify).
type cancelBridge struct {
	recordingBridge
	notified  []string
	notifyErr error
}

func (b *cancelBridge) Notify(_ context.Context, method string, _ any) error {
	b.notified = append(b.notified, method)
	return b.notifyErr
}

// A chat with no live bridge has no turn to cancel, and the rest of the
// teardown still has to run.
func TestCloseChatTeardown_TearsDownAChatWithNoBridge(t *testing.T) {
	deps := &closeDeps{benchDeps: newBenchDeps()}

	closeChatTeardown(t.Context(), deps, deps, deps, "c1")

	if len(deps.cleared) != 1 || deps.cleared[0] != "c1" {
		t.Errorf("cleared permissions for %v, want exactly [c1]", deps.cleared)
	}
	if len(deps.closed) != 1 || deps.closed[0] != "c1" {
		t.Errorf("closed state for %v, want exactly [c1]", deps.closed)
	}
}

// With a live bridge the turn IS cancelled, and a cancel kiro-cli accepted
// leaves no failure line behind.
func TestCloseChatTeardown_CancelsTheTurnAndLogsNoFailure(t *testing.T) {
	logs := captureLogs(t)
	bridge := &cancelBridge{}
	deps := &closeDeps{benchDeps: newBenchDeps(), bridge: bridge}

	closeChatTeardown(t.Context(), deps, deps, deps, "c1")

	if len(bridge.notified) != 1 || bridge.notified[0] != vibekit.MethodCancel {
		t.Errorf("bridge saw notifications %v, want exactly [%s]", bridge.notified, vibekit.MethodCancel)
	}
	if len(deps.closed) != 1 {
		t.Errorf("closed state for %v, want exactly [c1]", deps.closed)
	}
	if strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("an accepted cancel logged a warning: %s", logs.String())
	}
}
