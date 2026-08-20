package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEnsureCodeIntelligence_Guards pins the three no-op paths: not
// wired, config already present, and gate closed. Each must return
// without touching the utility session (the test agent has no live
// bridge factory session — reaching acquire would fail loudly or
// hang, so returning cleanly IS the assertion).
func TestEnsureCodeIntelligence_Guards(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "lsp.json")

	t.Run("not wired is a no-op", func(t *testing.T) {
		h, _, _ := newTestHub()
		h.EnsureCodeIntelligence(t.Context())
	})

	t.Run("existing config is a no-op", func(t *testing.T) {
		h, _, _ := newTestHub()
		if err := os.WriteFile(cfgPath, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		gateCalled := false
		h.SetCodeIntelligence(cfgPath, func() bool { gateCalled = true; return true })
		h.EnsureCodeIntelligence(t.Context())
		if gateCalled {
			t.Error("gate consulted although lsp.json already exists")
		}
		if err := os.Remove(cfgPath); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("closed gate is a no-op", func(t *testing.T) {
		h, _, _ := newTestHub()
		h.SetCodeIntelligence(cfgPath, func() bool { return false })
		h.EnsureCodeIntelligence(t.Context())
		if h.ciBusy.Load() {
			t.Error("in-flight guard left set on the closed-gate path")
		}
	})
}
