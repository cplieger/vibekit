package composition

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/pinstall/v2"
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
		wantGate  bool            // a readiness verdict is published at all
		wantReady bool            // and what it says
		reason    pinstall.Reason // its reason when not ready
		rescan    bool            // the repair hook is wired
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
			reason:   pinstall.ReasonUnavailable,
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
					t.Errorf("ready() = (%v, %s), want (%v, %s)", ready, reason, tc.wantReady, tc.reason)
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
	// The required set is {kiro-cli, kiro-cli-chat}: `kiro-cli acp` re-execs the
	// chat sidecar through a PATH search, so a directory with no sidecar is NOT a
	// complete install (see kirocli.go's Require comment for the measurement, and
	// the sibling test below for the negative case). The fake answers --version
	// with the pin (what selection probes) and exits 0 for the settings calls.
	script := "#!/bin/sh\ncase \"$1\" in --version) printf 'kiro-cli " + version + "\\n' ;; esac\nexit 0\n"
	for _, name := range []string{"kiro-cli", "kiro-cli-chat"} {
		if err := os.WriteFile(filepath.Join(versionDir, name), []byte(script), 0o700); err != nil { // #nosec G306 -- a dispatcher fake must be executable
			t.Fatalf("write fake %s: %v", name, err)
		}
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
	var reason pinstall.Reason
	for {
		ok, why := kiro.ready()
		if ok {
			break
		}
		reason = why
		if time.Now().After(deadline) {
			t.Fatalf("no version became active within the deadline; last readiness reason %s", reason)
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

// TestStartKiroCLIRejectsASidecarLessVersionDirectory is the negative half of
// the required set, and it pins the defect that made the set wrong in the first
// place. `--version` is answered by the MAIN binary, so a directory holding only
// that binary passes the selection probe; before kiro-cli-chat was Required such
// a directory was published .complete, reported READY, and then failed at every
// single chat spawn because `kiro-cli acp` re-execs a sidecar that is not there.
//
// Readiness must stay WITHHELD here rather than the boot aborting: an incomplete
// directory is simply not a selection candidate, so the install retries and the
// reason names the phase. That is invariant 6's shape, not a violation of it.
func TestStartKiroCLIRejectsASidecarLessVersionDirectory(t *testing.T) {
	const version = "9.9.9"
	toolsDir := t.TempDir()
	versionDir := filepath.Join(toolsDir, "kiro-cli-versions", version)
	if err := os.MkdirAll(versionDir, 0o750); err != nil {
		t.Fatalf("create version dir: %v", err)
	}
	script := "#!/bin/sh\ncase \"$1\" in --version) printf 'kiro-cli " + version + "\\n' ;; esac\nexit 0\n"
	// Deliberately ONLY the main dispatcher.
	if err := os.WriteFile(filepath.Join(versionDir, "kiro-cli"), []byte(script), 0o700); err != nil { // #nosec G306 -- a dispatcher fake must be executable
		t.Fatalf("write fake dispatcher: %v", err)
	}
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

	// A real download cannot succeed here (the digest is a fake), so the manager
	// stays in its retry ladder. The assertion is that it never adopts the
	// sidecar-less directory as a shortcut out of that ladder.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok, why := kiro.ready(); ok {
			t.Fatalf("a sidecar-less version directory was adopted (readiness %v, reason %s); "+
				"kiro-cli acp would fail at every chat spawn", ok, why)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := kiro.cliPath(); got != "" {
		t.Errorf("cliPath() = %q, want empty: no version may be active", got)
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

// TestKiroPathEnvWithNoInheritedPATHDoesNotWidenTheSearchPath pins the degenerate
// case. Appending an empty inherited PATH would leave a trailing separator, and an
// empty PATH element resolves to the CHILD'S CWD — cfg.WorkDir, the user's own
// checkouts — so the overlay would widen the search path at exactly the moment it
// has least information, instead of narrowing it to the verified install. Only
// reachable from a bare `go run`, a test, or a deployment that clears the
// environment, since the image always sets PATH; asserted anyway because the
// symmetrical overlay in web-terminal-kiro guards it and the two must not diverge.
func TestKiroPathEnvWithNoInheritedPATHDoesNotWidenTheSearchPath(t *testing.T) {
	t.Setenv("PATH", "")

	got := kiroPathEnv("/config/tools/kiro-cli-versions/2.14.2")
	if len(got) != 1 {
		t.Fatalf("kiroPathEnv returned %v, want exactly one PATH entry", got)
	}
	want := "PATH=/config/tools/kiro-cli-versions/2.14.2"
	if got[0] != want {
		t.Errorf("overlay = %q, want %q: a trailing separator makes the child search its own cwd", got[0], want)
	}
	if strings.HasSuffix(got[0], string(os.PathListSeparator)) {
		t.Error("the overlay ends in a path separator, so an empty element resolves to the child's cwd")
	}
}

// TestKiroSettingsLeavesTheIntegrityGateToTheManager pins the one rule this list
// must obey: app.disableAutoupdates is NOT in it. kirocli.Release() declares that
// assertion Mandatory, so pinstall forces it Required and merges it in on top of
// whatever this list carries (it is what stops the binary replacing itself and
// invalidating the verified digest). Listing it here would be the one way a
// deployment could try to restate the integrity gate as a best-effort preference.
//
// It also pins that every entry speaks kiro-cli's own settings grammar. The
// library takes a full argv and knows nothing about how kiro-cli is configured,
// so a hand-built Assertion with the wrong verb would be accepted here and only
// fail at runtime, as a warn, on a container nobody is watching.
func TestKiroSettingsLeavesTheIntegrityGateToTheManager(t *testing.T) {
	settings := kiroSettings()
	if len(settings) == 0 {
		t.Fatal("no kiro-cli settings configured; the Settings UI would misreport every toggle until a second boot")
	}
	seen := map[string]bool{}
	for _, a := range settings {
		if a.Name == "app.disableAutoupdates" {
			t.Error("app.disableAutoupdates is listed as a best-effort assertion; the release profile owns it as Mandatory")
		}
		if seen[a.Name] {
			t.Errorf("setting %q is listed twice", a.Name)
		}
		seen[a.Name] = true
		if a.Required {
			t.Errorf("setting %q is marked Required; only the profile's own mandatory assertion may gate readiness", a.Name)
		}
		want := []string{"settings", a.Name}
		if len(a.Args) != 3 || !slices.Equal(a.Args[:2], want) || a.Args[2] == "" {
			t.Errorf("setting %q has argv %v, want %v plus a non-empty value", a.Name, a.Args, want)
		}
	}
}
