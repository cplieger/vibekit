package bridge

import (
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// TestBridge_SharedContractSuite runs the shared contract test against the real Bridge
// (without starting a subprocess — only pre-Start lifecycle assertions).
func TestBridge_SharedContractSuite(t *testing.T) {
	ACPBridgeContractTest(t, func() api.ACPBridge {
		return New("/nonexistent/kiro", t.TempDir())
	})
}
