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

	// All three sets are empty on an empty config. AllNames included: a real
	// store reads the powers block off disk for it, and an absent or
	// unattributable file must read as empty rather than as an error.
	t.Run("every_name_set_empty_when_no_servers", func(t *testing.T) {
		cfg := newConfig(t)
		ctx := context.Background()
		for _, tc := range []struct {
			names map[string]struct{}
			name  string
		}{
			{cfg.EnabledNames(ctx), "EnabledNames"},
			{cfg.ConfiguredNames(ctx), "ConfiguredNames"},
			{cfg.AllNames(ctx), "AllNames"},
		} {
			if len(tc.names) != 0 {
				t.Errorf("%s() = %v, want empty", tc.name, tc.names)
			}
		}
	})

	// The nesting is what the hub's guard reasons from: it reads the three sets
	// in order and treats each gap as a distinct verdict, so an implementation
	// that leaks a name into a narrower set than a wider one would make a
	// configured server look like a Power's.
	t.Run("name_sets_nest", func(t *testing.T) {
		cfg := newConfig(t)
		ctx := context.Background()
		configured := cfg.ConfiguredNames(ctx)
		all := cfg.AllNames(ctx)
		for name := range cfg.EnabledNames(ctx) {
			if _, ok := configured[name]; !ok {
				t.Errorf("%q is enabled but not in ConfiguredNames", name)
			}
		}
		for name := range configured {
			if _, ok := all[name]; !ok {
				t.Errorf("%q is configured but not in AllNames", name)
			}
		}
	})
}
