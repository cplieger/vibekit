package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// TestEnsureCodeIntelligence_WiredAndUnconfiguredRunsTheInit is the case
// TestEnsureCodeIntelligence_Guards is the complement of, and the only one where
// the function does anything.
//
// Its three guards each return for a good reason, so a guard that fires one case
// too wide turns the whole feature off silently: lsp.json is never written, no
// language server ever starts, and code intelligence is simply absent with nothing
// logged to say why. The init's own report is the evidence it ran.
func TestEnsureCodeIntelligence_WiredAndUnconfiguredRunsTheInit(t *testing.T) {
	logs := captureLogs(t)
	h, _, br := newTestHub()
	br.callResults = map[string]json.RawMessage{
		methodKiroCodeIntel: json.RawMessage(`{"success":true,"message":"started go, typescript"}`),
	}
	// A path that does not exist yet is the whole trigger: KAS writes it.
	h.SetCodeIntelligence(filepath.Join(t.TempDir(), "lsp.json"), func() bool { return true })

	h.EnsureCodeIntelligence(t.Context())

	if !slices.Contains(br.callLog(), methodKiroCodeIntel) {
		t.Fatalf("no code-intelligence init was issued; calls were %v", br.callLog())
	}
	if h.ciBusy.Load() {
		t.Error("the in-flight guard was left set, so no later trigger can ever retry")
	}
	out := logs.String()
	if !strings.Contains(out, `"msg":"code intelligence initialized"`) {
		t.Errorf("a completed init said nothing: %s", out)
	}
	if !strings.Contains(out, `"detail":"started go, typescript"`) {
		t.Errorf("the init line drops KAS's own report of what it started: %s", out)
	}
}
