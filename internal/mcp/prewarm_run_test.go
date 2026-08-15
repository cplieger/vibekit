package mcp

// The Store side of the prewarm seam. Production wires the store in as
// the lister directly (composition: prewarm.NewRunner(ctx, mcpStore)),
// so what this package owns is the EnabledServers projection: enabled
// records in, prewarm.ServerInfo out, fields intact. Runner.Run's own
// orchestration (dedup, npm-missing, disabled) is tested in-package at
// internal/mcp/prewarm/run_test.go.

import (
	"slices"
	"testing"

	"github.com/cplieger/vibekit/internal/mcp/prewarm"
)

// TestEnabledServers_FeedsPrewarmLister pins the Store→prewarm contract:
// the store satisfies prewarm.ServerLister, drops disabled records, and
// carries transport/command/args through unchanged (those three are what
// ExtractNpxPackage gates on, so a dropped field silently disables
// pre-warming).
func TestEnabledServers_FeedsPrewarmLister(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()

	if _, err := store.Create(ctx, &Server{
		Transport: TransportStdio, Name: "on", Command: "npx",
		Args: []string{"-y", "@scope/pkg"}, Enabled: true, Prewarm: true,
	}); err != nil {
		t.Fatalf("Create on: %v", err)
	}
	if _, err := store.Create(ctx, &Server{
		Transport: TransportStdio, Name: "off", Command: "npx",
		Args: []string{"-y", "@off/pkg"}, Enabled: false, Prewarm: true,
	}); err != nil {
		t.Fatalf("Create off: %v", err)
	}

	// Compile-time: the store IS the lister production passes to NewRunner.
	var lister prewarm.ServerLister = store

	got := lister.EnabledServers(ctx)
	if len(got) != 1 {
		t.Fatalf("EnabledServers returned %d entries, want 1 (disabled must be filtered): %+v", len(got), got)
	}
	want := prewarm.ServerInfo{
		Transport: string(TransportStdio), Command: "npx",
		Args: []string{"-y", "@scope/pkg"}, Enabled: true, Prewarm: true,
	}
	if got[0].Transport != want.Transport || got[0].Command != want.Command ||
		got[0].Enabled != want.Enabled || got[0].Prewarm != want.Prewarm ||
		!slices.Equal(got[0].Args, want.Args) {
		t.Errorf("EnabledServers()[0] = %+v, want %+v", got[0], want)
	}

	// And the projection is what prewarm actually keys on.
	if pkg := prewarm.ExtractNpxPackage(got[0]); pkg != "@scope/pkg" {
		t.Errorf("ExtractNpxPackage(projection) = %q, want %q", pkg, "@scope/pkg")
	}
}
