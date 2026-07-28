package composition

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/kirocli"
)

// TestStartKiroCLIShapes pins the runtimes startKiroCLI can return for a
// configuration it cannot install from, because each one answers /api/health
// differently and the wrong choice is silent: a pin-less `go run` that gated
// readiness would report a container unready forever, and unusable pins that did
// not gate it would report healthy while installing nothing.
//
// The managed path is TestStartKiroCLIAdoptsACompleteVersionDirectory's, which
// drives it end to end without a download.
func TestStartKiroCLIShapes(t *testing.T) {
	const goodDigest = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	tests := map[string]struct {
		cfg       Config
		wantPath  string
		wantGate  bool   // a readiness verdict is published at all
		wantReady bool   // and what it says
		reason    string // its reason when not ready
		rescan    bool   // the repair hook is wired
	}{
		"no pins resolves the bare name and installs nothing": {
			cfg:      Config{ToolsDir: t.TempDir()},
			wantPath: "kiro-cli",
		},
		"pins present but no tools dir falls back to the bare name": {
			cfg:      Config{KiroCLIVersion: "2.14.2", KiroCLISHA256: goodDigest},
			wantPath: "kiro-cli",
		},
		"unusable pins report unready rather than pretending": {
			cfg:      Config{KiroCLIVersion: "2.14.2", KiroCLISHA256: "not-a-digest", ToolsDir: t.TempDir()},
			wantPath: "",
			wantGate: true,
			reason:   kirocli.ReasonUnavailable,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := tc.cfg
			kiro := startKiroCLI(context.Background(), &cfg)
			defer kiro.stop()

			if got := kiro.cliPath(); got != tc.wantPath {
				t.Errorf("cliPath() = %q, want %q", got, tc.wantPath)
			}
			// Called unconditionally by the bridge factory on every spawn, so a
			// shape that left it nil would panic on the first chat rather than
			// anywhere near this wiring. cliPath and stop are the same contract.
			if got := kiro.env(); tc.wantPath == "" && got != nil {
				t.Errorf("env() = %v, want nil when no version is active", got)
			}
			switch {
			case tc.wantGate && kiro.ready == nil:
				t.Fatal("no readiness verdict published; /api/health would report healthy with no usable kiro-cli")
			case !tc.wantGate && kiro.ready != nil:
				t.Fatal("a readiness verdict was published for an install this server does not own")
			}
			if tc.wantGate {
				ready, reason := kiro.ready()
				if ready != tc.wantReady || reason != tc.reason {
					t.Errorf("ready() = (%v, %q), want (%v, %q)", ready, reason, tc.wantReady, tc.reason)
				}
			}
			if (kiro.rescan != nil) != tc.rescan {
				t.Errorf("rescan wired = %v, want %v", kiro.rescan != nil, tc.rescan)
			}
		})
	}
}

// TestStartKiroCLIAdoptsACompleteVersionDirectory drives the MANAGED path end to
// end with the pinned version directory already complete on the volume, so
// nothing is downloaded. That is both the ordinary restart path in production and
// the only way to exercise this wiring locally now that no env var can hand the
// server a binary: populate $TOOLS/kiro-cli-versions/<version>/ and the manager adopts
// it.
//
// Three properties only this test can see. Bind-first: startKiroCLI RETURNS while
// the install work is still in flight, so Build reaches Listen instead of blocking
// behind a download, and the poll below is what proves the work continued in the
// background. The manager is the SOURCE of the path: cliPath resolves INSIDE the
// activated version directory rather than to a bare name. And the repair hook is
// wired, which only a managed run may do.
func TestStartKiroCLIAdoptsACompleteVersionDirectory(t *testing.T) {
	const version = "9.9.9"
	toolsDir := t.TempDir()
	versionDir := filepath.Join(toolsDir, "kiro-cli-versions", version)
	if err := os.MkdirAll(versionDir, 0o750); err != nil {
		t.Fatalf("create version dir: %v", err)
	}
	// vibekit's REQUIRED set has cardinality one: `kiro-cli acp` is served by the
	// main dispatcher, so a directory with no sidecar is a complete install here.
	// The fake answers --version with the pin (what selection probes) and exits 0
	// for the settings calls.
	script := "#!/bin/sh\ncase \"$1\" in --version) printf 'kiro-cli " + version + "\\n' ;; esac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(versionDir, "kiro-cli"), []byte(script), 0o700); err != nil { // #nosec G306 -- a dispatcher fake must be executable
		t.Fatalf("write fake dispatcher: %v", err)
	}
	// Written LAST, exactly as the install order requires: it is the sentinel
	// that makes the directory a selection candidate at all.
	if err := os.WriteFile(filepath.Join(versionDir, ".complete"), []byte(version+"\n"), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	cfg := Config{
		KiroCLIVersion:     version,
		KiroCLISHA256:      strings.Repeat("a", 64),
		KiroCLISHA256ARM64: strings.Repeat("b", 64),
		ToolsDir:           toolsDir,
	}
	kiro := startKiroCLI(context.Background(), &cfg)
	t.Cleanup(kiro.stop)

	if kiro.ready == nil || kiro.rescan == nil {
		t.Fatalf("managed runtime is missing wiring: ready=%v rescan=%v",
			kiro.ready != nil, kiro.rescan != nil)
	}
	// Activation happens in the background, so poll rather than sleep.
	deadline := time.Now().Add(20 * time.Second)
	var reason string
	for {
		ok, why := kiro.ready()
		if ok {
			break
		}
		reason = why
		if time.Now().After(deadline) {
			t.Fatalf("no version became active within the deadline; last readiness reason %q", reason)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if got, want := kiro.cliPath(), filepath.Join(versionDir, "kiro-cli"); got != want {
		t.Errorf("cliPath() = %q, want the absolute version-directory path %q", got, want)
	}
	env := kiro.env()
	if len(env) != 1 || !strings.HasPrefix(env[0], "PATH="+versionDir+string(os.PathListSeparator)) {
		t.Errorf("env() = %v, want a single PATH entry leading with %q", env, versionDir)
	}
}

// TestKiroPathEnvLeadsWithTheVersionDirectory pins the environment overlay every
// spawned kiro-cli gets. Leading is the whole point: $TOOLS/bin is co-owned by
// the toolbelt engine and $TOOLS/go/bin is GOPATH/bin, so a stale kiro-cli in
// either would win bare-name resolution if the version directory came second.
func TestKiroPathEnvLeadsWithTheVersionDirectory(t *testing.T) {
	t.Setenv("PATH", "/config/tools/bin:/usr/bin")

	if got := kiroPathEnv(""); got != nil {
		t.Errorf("kiroPathEnv(\"\") = %v, want nil: no active version means no overlay", got)
	}

	got := kiroPathEnv("/config/tools/kiro-cli-versions/2.14.2")
	if len(got) != 1 {
		t.Fatalf("kiroPathEnv returned %v, want exactly one PATH entry", got)
	}
	want := "PATH=/config/tools/kiro-cli-versions/2.14.2" + string(os.PathListSeparator) + "/config/tools/bin:/usr/bin"
	if got[0] != want {
		t.Errorf("overlay = %q, want %q", got[0], want)
	}
	if !strings.HasPrefix(got[0], "PATH=/config/tools/kiro-cli-versions/2.14.2"+string(os.PathListSeparator)) {
		t.Error("the version directory is not FIRST on PATH")
	}
}

// TestKiroSettingsLeavesTheIntegrityGateToTheManager pins the one rule this list
// must obey: app.disableAutoupdates is NOT in it. The manager adds that setting
// itself as REQUIRED (it is what stops the binary replacing itself and
// invalidating the verified digest), so listing it here as a best-effort
// preference would be the one way to configure the integrity gate away.
func TestKiroSettingsLeavesTheIntegrityGateToTheManager(t *testing.T) {
	settings := kiroSettings()
	if len(settings) == 0 {
		t.Fatal("no kiro-cli settings configured; the Settings UI would misreport every toggle until a second boot")
	}
	seen := map[string]bool{}
	for _, s := range settings {
		if s.Key == "app.disableAutoupdates" {
			t.Error("app.disableAutoupdates is listed as a best-effort setting; the manager owns it as Required")
		}
		if s.Value == "" {
			t.Errorf("setting %q has an empty value", s.Key)
		}
		if seen[s.Key] {
			t.Errorf("setting %q is listed twice", s.Key)
		}
		seen[s.Key] = true
		if s.Required {
			t.Errorf("setting %q is marked Required; only the manager's own integrity setting may gate readiness", s.Key)
		}
	}
}
