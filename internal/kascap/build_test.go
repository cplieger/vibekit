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

// spawnMatrix is the COMPLETE set of runtime inputs to either projection, and
// exhaustive is what makes a golden a contract rather than a sample: a change to
// how a gate is applied cannot hide in an untested combination.
//
// It is exhaustive over the two ORIGINAL booleans and representative over the
// three gates added since, rather than the 32-line cross product five gates would
// give. The reasoning is that the added gates are INDEPENDENT of the first two —
// each keys on its own Spawn field and writes its own wire key — so a full
// product would add 25 lines that differ from a line already here by exactly one
// key. What each added gate does need is BOTH of its states plus one line where it
// coexists with the originals, and that is what the rows below give it. A gate
// whose value interacted with another's would break that argument and earn its
// product.
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
	// Presets are the third gate, and BOTH of its states need a line here. An
	// empty set withholds the policyPreset key entirely, which is the Custom
	// profile's whole implementation and the one payload shape where a missing key
	// is the correct wire — so the four cases above double as its fixture. A
	// non-empty set is the ordinary case every named profile produces, and the
	// multi-id line is what would catch the key being flattened to a bare string
	// or truncated to its first entry, neither of which errors on the wire.
	{"one preset", Spawn{Presets: []string{"read-workspace"}}},
	{"several presets", Spawn{Presets: []string{"read-workspace", "read-only-shell", "read-all"}}},
	{"presets with both gates", Spawn{
		SecretStorage: true, Hooks: true,
		Presets: []string{"allow-all"},
	}},
	// ToolSearch and Knowledge gate three keys between them and they gate DIFFERENTLY,
	// which is the whole reason both need their own lines rather than one combined row.
	//
	// ToolSearch is PRESENCE-gated: off withholds settings.toolSearch entirely,
	// because absent already resolves false at isSettingEnabled. Every line above
	// is therefore its off fixture, and the line below is the only place the key
	// appears at all.
	//
	// Knowledge is VALUE-gated across TWO keys — the knowledge capability, whose
	// resolver compares `=== true`, and settings.knowledge, read through
	// isSettingEnabled. So its off state is a payload shape rather than an absence,
	// and the lines above are that shape: `"knowledge":false` beside
	// `"knowledge":{"enabled":false}`. The line below is its on state, and it is
	// what would catch the pair drifting apart — one key flipping without the other
	// is the exact defect that shipped a knowledge UI over a store the agent had no
	// tool to query.
	{"tool search on", Spawn{ToolSearch: true}},
	{"knowledge on", Spawn{Knowledge: true}},
	{"every gate on", Spawn{
		SecretStorage: true, Hooks: true,
		Presets:    []string{"read-workspace"},
		ToolSearch: true, Knowledge: true,
	}},
}

// renderMatrix marshals one projection across the whole spawn matrix, one
// compact JSON object per line. encoding/json sorts map keys, so the output is
// deterministic without the test sorting anything itself.
//
// Spawn is no longer the only runtime input: an env-bearing row reads the
// process environment, so the ambient environment is part of this fixture and
// neutralizeEnvOverrides makes it an explicit one. Without that, a machine
// carrying VIBEKIT_AGENT_WORKFLOWS=false fails both goldens for a reason the
// diff does not explain.
func renderMatrix(t *testing.T, build func(Spawn) map[string]any) string {
	t.Helper()
	neutralizeEnvOverrides(t)
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

// TestSessionDoorDeclaresExactly pins the payload session/new and session/load
// carry, for every spawn combination.
//
// It went from four empty objects to a real payload when workflows moved here,
// and that diff is what the empty fixture existed to produce. It now guards the
// same silent failures the initialize golden does, plus one of its own: the
// session door is the only door whose payload can be EMPTY, and an empty map is
// the one value the caller must not send at all, so a row leaving this door has
// to be a visible change here rather than a call that quietly stops carrying
// _meta.
//
// internal/bridge holds the paired fixtures over the two real session requests.
//
// Regenerate with:
//
//	UPDATE_GOLDEN=1 go test ./internal/kascap/ -run TestSessionDoorDeclaresExactly
func TestSessionDoorDeclaresExactly(t *testing.T) {
	checkGolden(t, sessionGoldenPath, renderMatrix(t, SessionMeta))
}

// TestSessionDoor_WithdrawnRowsLeaveNoSettingsObject pins the one payload shape
// the goldens above cannot reach: the session door with every settings row
// withheld.
//
// The session door is the only door whose settings map can empty out, and it can
// because its single sending row is env-overridable. An empty `settings` object
// is not the same message as no `settings` key: KAS's resolvers read an absent
// key as false, which is exactly what withholding a row is asking for, so sending
// `{"settings":{}}` puts a key on the wire whose only content is the claim that
// vibekit had something to say about settings. That is the failure this guard
// exists for, and no spawn combination produces it — only the override does.
func TestSessionDoor_WithdrawnRowsLeaveNoSettingsObject(t *testing.T) {
	neutralizeEnvOverrides(t)
	t.Setenv(envWorkflows, "false")

	meta := SessionMeta(Spawn{SecretStorage: true, Hooks: true})
	if _, present := meta[settingsKey]; present {
		t.Errorf("SessionMeta()[%q] = %#v, want the key absent once every settings row is withheld",
			settingsKey, meta[settingsKey])
	}

	// The override is the only thing suppressing it, so the same call with the
	// row sending must still carry the object — otherwise this test would pass
	// against a projection that never emits settings at all.
	t.Setenv(envWorkflows, "true")
	withRow := SessionMeta(Spawn{SecretStorage: true, Hooks: true})
	settings, present := withRow[settingsKey]
	if !present {
		t.Fatalf("SessionMeta() carries no %q key with the row sending, so the case above proves nothing", settingsKey)
	}
	if got, ok := settings.(map[string]any); !ok || len(got) == 0 {
		t.Errorf("SessionMeta()[%q] = %#v, want a non-empty settings object", settingsKey, settings)
	}
}
