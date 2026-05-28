package testsupport

import (
	"testing"

	"vibekit/internal/api"
)

// Compile-time interface assertion.
var _ api.ACPBridge = (*NopACPBridge)(nil)

func TestNopACPBridge_Contract(t *testing.T) {
	ACPBridgeContractTest(t, &NopACPBridge{})
}
