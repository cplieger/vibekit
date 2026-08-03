package testsupport

import (
	"context"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// MCPConfigContractTest exercises the behavioral expectations of any
// api.MCPConfig implementation. Run against both fakes and real
// implementations to catch drift.
func MCPConfigContractTest(t *testing.T, newConfig func(t *testing.T) api.MCPConfig) {
	t.Helper()

	t.Run("EnabledNames_empty_when_no_servers", func(t *testing.T) {
		cfg := newConfig(t)
		names := cfg.EnabledNames(context.Background())
		if len(names) != 0 {
			t.Errorf("EnabledNames() = %v, want empty", names)
		}
	})
}
