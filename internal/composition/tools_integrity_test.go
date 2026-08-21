package composition

// vibekit turns toolbelt's opt-in root-integrity check ON (buildToolsEngine's
// Config literal) and answers a refusal by running TOOL-LESS rather than by
// refusing to boot. Both halves need pinning and neither shows up in a type
// signature.
//
// The verdict is a (nil, nil) return, which reads like a forgotten case rather
// than a decision, so a later reader "fixing" it into an error would brick every
// container whose volume has a group-writable npm root. And the check being on at
// all is observable ONLY as behavior: toolbelt's zero value is the pre-check
// behavior byte for byte, so a dropped field breaks nothing that compiles.
//
// Hence these tests drive buildToolsEngine itself -- the exact literal production
// constructs -- rather than a copy of the config, for the same reason
// kirocli_namespace_test.go builds its manager from kiroInstallConfig: a copy
// cannot go stale in the one way that matters. A (nil, nil) return over an unfit
// root is only reachable when the field is set; were it dropped, New would
// succeed and every case below would see an engine.
//
// The findings are read from the LOG because the degraded arm deliberately
// swallows the error. That doubles as the control on each fixture: a case that
// names exactly the paths it made unfit, and no others, proves the refusal came
// from the injected defect and not from something incidental about a temp dir.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/toolbelt/v3"
	"github.com/cplieger/vibekit/internal/agent"
	"github.com/cplieger/vibekit/internal/chat"
	"github.com/cplieger/vibekit/internal/mcp/prewarm"
)

// unfitRootMsg must match logRootIntegrityRefusal's per-finding message. The
// per-path lines ARE the operator-facing contract (the refusal reports which
// root and why, and repairs nothing), so a silent rename is a regression rather
// than a cosmetic edit.
const unfitRootMsg = "tools: managed root is not fit to execute from"

// captureDefaultLogger redirects slog's default for one call. Both the degraded
// arm and toolbelt's own refusal line land here (vibekit sets no Config.Logger,
// so the library logs to the same default), which is why readers filter by
// message instead of counting records. slog's default is process-global: no test
// in this file may run in parallel.
func captureDefaultLogger(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// loggedUnfitPaths returns the paths the refusal named, sorted, and fails the
// test for any finding logged without a reason -- a path with no reason is a
// line an operator cannot act on, which defeats the point of reporting instead
// of repairing.
func loggedUnfitPaths(t *testing.T, logs *bytes.Buffer) []string {
	t.Helper()
	var found []string
	dec := json.NewDecoder(bytes.NewReader(logs.Bytes()))
	for {
		var rec struct {
			Msg    string `json:"msg"`
			Path   string `json:"path"`
			Reason string `json:"reason"`
		}
		if err := dec.Decode(&rec); err != nil {
			break
		}
		if rec.Msg != unfitRootMsg {
			continue
		}
		if rec.Reason == "" {
			t.Errorf("finding for %q logged no reason", rec.Path)
		}
		found = append(found, rec.Path)
	}
	slices.Sort(found)
	return found
}

// fitTree builds the layout production runs -- the tools dir inside the config
// dir -- with both roots fit, so a case's plant() is the only defect present.
func fitTree(t *testing.T) (configDir, toolsDir string) {
	t.Helper()
	configDir = t.TempDir()
	toolsDir = filepath.Join(configDir, "tools")
	if err := os.MkdirAll(toolsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	return configDir, toolsDir
}

// testRuntime is a real agent rather than a nil one on purpose. Every call below
// expects New to FAIL, and both failure arms return before the runtime is touched --
// so a nil would never be dereferenced by CORRECT code. It is the incorrect code
// that matters: with the integrity check off, buildToolsEngine runs to completion
// through the job callbacks and the code-intelligence wiring, and a nil agent turns
// that regression into a SIGSEGV stack trace instead of the assertion that names
// what actually broke.
//
// The chat store is REAL. It used to be nil on the same "correct code never
// touches it" reasoning, and agent.New's own role guard refuted that: the store is
// read at construction to wire the translator, so a nil one is a runtime that cannot
// serve a single chat. Passing nil built one anyway and deferred the crash to the
// first ACP frame.
func testRuntime(t *testing.T) *agent.Runtime {
	t.Helper()
	store, err := chat.NewStore(filepath.Join(t.TempDir(), "chats"))
	if err != nil {
		t.Fatalf("chat store: %v", err)
	}
	h := agent.New(t.Context(), t.TempDir(), nil, store)
	t.Cleanup(func() {
		if err := h.Shutdown(context.Background()); err != nil {
			t.Errorf("runtime shutdown: %v", err)
		}
	})
	return h
}

// mkdirMode creates a directory at an EXACT mode. MkdirAll applies the process
// umask, so a group-writable fixture has to chmod afterwards or it silently
// lands at 0755 and the case asserts nothing.
func mkdirMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// TestBuildToolsEngineDegradesOnRootIntegrityRefusal pins the fatal-vs-warn
// decision: an unfit managed root leaves vibekit running WITHOUT the tools
// subsystem, and does not stop the boot.
//
// The condition is persistent-volume state this process neither created nor may
// repair, and the container is the operator's only way in to fix it -- so
// aborting here would strand a box that a chmod from inside would have healed.
func TestBuildToolsEngineDegradesOnRootIntegrityRefusal(t *testing.T) {
	tests := map[string]struct {
		// plant introduces the defect and returns the paths the check must
		// name, in any order.
		plant func(t *testing.T, configDir, toolsDir string) []string
	}{
		"a symlinked bin is the launcher dir the install probe executes": {
			plant: func(t *testing.T, _, toolsDir string) []string {
				bin := filepath.Join(toolsDir, "bin")
				if err := os.Symlink(t.TempDir(), bin); err != nil {
					t.Fatal(err)
				}
				return []string{bin}
			},
		},
		"a group-writable npm/bin lets a non-owner plant a launcher": {
			plant: func(t *testing.T, _, toolsDir string) []string {
				npmBin := filepath.Join(toolsDir, "npm", "bin")
				mkdirMode(t, npmBin, 0o775)
				return []string{npmBin}
			},
		},
		"a regular file where opt belongs": {
			plant: func(t *testing.T, _, toolsDir string) []string {
				opt := filepath.Join(toolsDir, "opt")
				if err := os.WriteFile(opt, []byte("not a dir"), 0o600); err != nil {
					t.Fatal(err)
				}
				return []string{opt}
			},
		},
		"a symlinked npm redirects its bin out of the tree": {
			// The cascade toolbelt reports deliberately: the parent is named
			// for being a symlink AND the leaf for resolving elsewhere, so an
			// operator sees the whole surface rather than one line at a time.
			plant: func(t *testing.T, _, toolsDir string) []string {
				outside := t.TempDir()
				if err := os.MkdirAll(filepath.Join(outside, "bin"), 0o750); err != nil {
					t.Fatal(err)
				}
				npm := filepath.Join(toolsDir, "npm")
				if err := os.Symlink(outside, npm); err != nil {
					t.Fatal(err)
				}
				return []string{npm, filepath.Join(npm, "bin")}
			},
		},
		"a group-writable tools dir": {
			plant: func(t *testing.T, _, toolsDir string) []string {
				if err := os.Chmod(toolsDir, 0o775); err != nil {
					t.Fatal(err)
				}
				return []string{toolsDir}
			},
		},
		"a group-writable config dir": {
			// ConfigDir is judged too, and it is where tools.json lives.
			plant: func(t *testing.T, configDir, _ string) []string {
				if err := os.Chmod(configDir, 0o775); err != nil {
					t.Fatal(err)
				}
				return []string{configDir}
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			configDir, toolsDir := fitTree(t)
			want := tc.plant(t, configDir, toolsDir)
			slices.Sort(want)

			logs := captureDefaultLogger(t)
			engine, err := buildToolsEngine(t.Context(), &Config{ConfigDir: configDir, ToolsDir: toolsDir}, testRuntime(t))
			if err != nil {
				t.Fatalf("an unfit root stopped the boot; a dev box whose volume drifted cannot be repaired from inside a container that will not start: %v", err)
			}
			if engine != nil {
				t.Fatal("an engine was constructed over an unfit root; the integrity check is not enabled on the real config literal")
			}
			if got := loggedUnfitPaths(t, logs); !slices.Equal(got, want) {
				t.Errorf("findings named %v, want %v; logs:\n%s", got, want, logs.String())
			}
		})
	}
}

// TestAppShutdownToleratesTheDegradedEngine pins the guard that carries the
// degraded verdict past construction. None of toolbelt's methods is
// nil-receiver safe (Close dereferences the refresh canceller, Inventory and
// EnsureInstalled the store), so Shutdown's nil check is load-bearing rather
// than defensive decoration -- and it is the consumer that runs on EVERY
// degraded boot, where the panel-driven ones only run if a user opens a panel.
//
// Driven through the real App.Shutdown, not a copy of its guard: the members
// are stubbed to the cheapest real instances that satisfy their own Stop
// contracts, so removing the check from production fails here.
func TestAppShutdownToleratesTheDegradedEngine(t *testing.T) {
	configDir, toolsDir := fitTree(t)
	if err := os.Symlink(t.TempDir(), filepath.Join(toolsDir, "bin")); err != nil {
		t.Fatal(err)
	}
	captureDefaultLogger(t)

	engine, err := buildToolsEngine(t.Context(), &Config{ConfigDir: configDir, ToolsDir: toolsDir}, testRuntime(t))
	if err != nil || engine != nil {
		t.Fatalf("buildToolsEngine = (%v, %v), want the degraded (nil, nil)", engine, err)
	}

	chatStore, err := chat.NewStore(filepath.Join(t.TempDir(), "chats"))
	if err != nil {
		t.Fatal(err)
	}
	// This agent is Shutdown by the App under test, so it is deliberately not
	// the t.Cleanup-registered testRuntime.
	app := &App{
		Runtime:        agent.New(t.Context(), t.TempDir(), nil, chatStore),
		purgeScheduler: chat.NewPurgeScheduler(chatStore, func() time.Duration { return 0 }),
		mcpPrewarm:     prewarm.NewRunner(t.Context(), nil),
		tools:          engine,
		stopKiro:       func() {},
	}
	app.Shutdown()
}

// TestBuildToolsEngineKeepsNonIntegrityFailuresFatal is the other half of the
// decision, and the one that keeps it honest: degrading on "any New error" would
// quietly turn an unrelated regression into a tool-less boot nobody notices. Only
// the root-integrity sentinel takes the new path; a manifest this engine refuses
// to guess at still stops the boot, wrapped exactly as it was before the check
// existed.
func TestBuildToolsEngineKeepsNonIntegrityFailuresFatal(t *testing.T) {
	configDir, toolsDir := fitTree(t)
	// A manifest of another schema version: toolbelt will neither guess at nor
	// rewrite user intent, and New surfaces it. Derived from the library's own
	// constant so a schema bump cannot turn this into a manifest the engine
	// happily accepts, leaving the test asserting nothing.
	doc := fmt.Sprintf(`{"version":%d,"tools":{}}`, toolbelt.ManifestVersion+1)
	if err := os.WriteFile(filepath.Join(configDir, "tools.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	logs := captureDefaultLogger(t)

	engine, err := buildToolsEngine(t.Context(), &Config{ConfigDir: configDir, ToolsDir: toolsDir}, testRuntime(t))

	if err == nil {
		t.Fatal("a manifest-version failure was absorbed into a tool-less boot; only the root-integrity refusal may degrade")
	}
	if engine != nil {
		t.Error("an engine was returned alongside a fatal error")
	}
	if errors.Is(err, toolbelt.ErrRootIntegrity) {
		t.Errorf("a manifest failure classified as a root-integrity refusal: %v", err)
	}
	if !strings.Contains(err.Error(), "tools engine:") {
		t.Errorf("error wrapping changed to %q; callers and logs read this prefix", err)
	}
	if got := loggedUnfitPaths(t, logs); len(got) != 0 {
		t.Errorf("a non-integrity failure logged root findings %v", got)
	}
}
