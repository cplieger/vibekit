package kascap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Golden fixtures for the two projections. Stage 1 of the capability-regression
// gate: no agent-server bundle is required, so these run in CI as they stand.
// The bundle-dependent census is census_test.go and skips when it is absent.
const (
	initializeGoldenPath = "testdata/initialize.golden"
	sessionGoldenPath    = "testdata/session.golden"
	updateGoldenCmd      = "UPDATE_GOLDEN=1 go test ./internal/kascap/ -run 'TestInitializeDeclaresExactly|TestSessionDoorDeclaresExactly'"
)

// spawnMatrix is the COMPLETE set of runtime inputs to either projection. Spawn
// has two boolean fields, so four rows is exhaustive, and exhaustive is what
// makes a golden a contract rather than a sample: a change to how either gate
// is applied cannot hide in an untested combination.
//
// The slice order IS the goldens' line order. Reordering it rewrites both
// fixtures without changing any payload.
var spawnMatrix = []struct {
	name  string
	spawn Spawn
}{
	{"gates off", Spawn{}},
	{"secret storage only", Spawn{SecretStorage: true}},
	{"hooks only", Spawn{Hooks: true}},
	{"both gates on", Spawn{SecretStorage: true, Hooks: true}},
}

// renderMatrix marshals one projection across the whole spawn matrix, one
// compact JSON object per line. encoding/json sorts map keys, so the output is
// deterministic without the test sorting anything itself.
func renderMatrix(t *testing.T, build func(Spawn) map[string]any) string {
	t.Helper()
	var out strings.Builder
	for _, tc := range spawnMatrix {
		raw, err := json.Marshal(build(tc.spawn))
		if err != nil {
			t.Fatalf("%s: marshal: %v", tc.name, err)
		}
		out.Write(raw)
		out.WriteString("\n")
	}
	return out.String()
}

// checkGolden writes the fixture under UPDATE_GOLDEN=1 and compares in every
// case, so one code path both regenerates and asserts.
func checkGolden(t *testing.T, path, got string) {
	t.Helper()
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("create testdata dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("wrote %s (%d bytes)", path, len(got))
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (regenerate with: %s): %v", path, updateGoldenCmd, err)
	}
	if string(want) == got {
		return
	}
	wantLines := strings.Split(strings.TrimSuffix(string(want), "\n"), "\n")
	gotLines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(wantLines) != len(gotLines) {
		t.Fatalf(`%s holds %d payload(s), the table produced %d.
A row or a spawnMatrix case changed. Regenerate with: %s`,
			path, len(wantLines), len(gotLines), updateGoldenCmd)
	}
	for i := range wantLines {
		if wantLines[i] == gotLines[i] {
			continue
		}
		name := "case " + strconv.Itoa(i)
		if i < len(spawnMatrix) {
			name = spawnMatrix[i].name
		}
		t.Errorf(`%s changed for %q.
A capability was added, removed, renamed, or reshaped. If that is deliberate,
regenerate with:
  %s
--- want
%s
+++ got
%s`, path, name, updateGoldenCmd, wantLines[i], gotLines[i])
	}
}

// TestInitializeDeclaresExactly pins the exact _meta.kiro payload the
// initialize handshake carries, for every spawn combination.
//
// This is the loud half of the capability-regression gate. Every failure mode
// this table exists to prevent is silent on the wire: a settings key dropped to
// a bare true resolves false, a capability renamed by a KAS bump simply never
// matches, and a key nested one level wrong is ignored. None of those produce an
// error, a log line or a -32601, so a fixture is the only thing that notices.
//
// internal/bridge holds the paired fixture over the FULL initialize request, so
// a change here that somehow did not reach the wire fails there instead.
//
// Regenerate with:
//
//	UPDATE_GOLDEN=1 go test ./internal/kascap/ -run TestInitializeDeclaresExactly
func TestInitializeDeclaresExactly(t *testing.T) {
	checkGolden(t, initializeGoldenPath, renderMatrix(t, Capabilities))
}

// TestSessionDoorDeclaresExactly pins the session door's payload, which is
// EMPTY today for every spawn.
//
// An empty fixture is the point rather than a placeholder. The session door is
// the concept T5 needs before it can move a key, and an empty golden is what
// turns that move into a visible diff: whatever lands there first shows up here
// as a line changing from {} to a payload, in the same review that adds it.
//
// Regenerate with:
//
//	UPDATE_GOLDEN=1 go test ./internal/kascap/ -run TestSessionDoorDeclaresExactly
func TestSessionDoorDeclaresExactly(t *testing.T) {
	checkGolden(t, sessionGoldenPath, renderMatrix(t, SessionMeta))
}
