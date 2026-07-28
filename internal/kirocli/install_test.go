package kirocli

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestEnsureInstallsPinnedVersion pins the happy path end to end: the archive
// is fetched once, the version directory holds the required and optional
// dispatchers plus the sentinel, the sentinel names the pin, the manager is
// ready, and CLIPath/PathEntry point INSIDE the version directory rather than
// at the convenience symlink.
func TestEnsureInstallsPinnedVersion(t *testing.T) {
	env := newFakeEnv(t)
	m := env.manager()

	if err := m.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if env.fetchCount() != 1 {
		t.Errorf("fetches = %d, want 1", env.fetchCount())
	}
	dir := env.versionDir(pinnedVersion)
	for _, name := range []string{"kiro-cli", "kiro-cli-chat", "kiro-cli-term", sentinelName} {
		if !exists(filepath.Join(dir, name)) {
			t.Errorf("%s missing from the published version directory", name)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, sentinelName))
	if err != nil {
		t.Fatalf("read sentinel: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != pinnedVersion {
		t.Errorf("sentinel = %q, want %q", got, pinnedVersion)
	}
	if ready, reason := m.Ready(); !ready {
		t.Errorf("Ready() = false (%s), want true", reason)
	}
	if got, want := m.CLIPath(), filepath.Join(dir, mainBinary); got != want {
		t.Errorf("CLIPath() = %q, want the absolute version-directory path %q", got, want)
	}
	if got := m.PathEntry(); got != dir {
		t.Errorf("PathEntry() = %q, want %q", got, dir)
	}
	if m.Phase() != PhaseReady {
		t.Errorf("Phase() = %q, want %q", m.Phase(), PhaseReady)
	}
	if !exists(filepath.Join(env.tools, stateFileName)) {
		t.Error("the diagnostic state record was not written")
	}
}

// TestEnsureDigestMismatchPlacesNothing pins the refusal: a body that does not
// match the pinned digest yields a typed ErrDigestMismatch, and NOTHING is
// placed under the installation root -- no version directory, no staging tree.
// Verification happens before anything reaches the persistent volume, so a
// mismatch cannot leave a candidate behind for a later boot to trust.
func TestEnsureDigestMismatchPlacesNothing(t *testing.T) {
	env := newFakeEnv(t)
	env.onFetch = func(dst io.Writer) error {
		_, err := dst.Write([]byte("not the pinned archive"))
		return err
	}
	m := env.manager()

	err := m.Ensure(context.Background())
	if !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("Ensure error = %v, want ErrDigestMismatch", err)
	}
	if entries, rerr := os.ReadDir(env.versionsRoot()); rerr == nil && len(entries) != 0 {
		t.Errorf("installation root holds %v, want nothing placed on a digest mismatch", entries)
	}
	if env.countCalls("install.sh") != 0 {
		t.Error("the upstream installer ran despite the digest mismatch")
	}
	if ready, reason := m.Ready(); ready || reason != ReasonInstalling {
		t.Errorf("Ready() = (%v, %q), want (false, %q)", ready, reason, ReasonInstalling)
	}
}

// TestExpectedDigestPerArch pins per-arch digest selection: the aarch64 pin
// must never satisfy an x86_64 install. A swapped pair is the exact mistake a
// hand-edited Renovate PR makes, and it has to fail rather than install the
// wrong artifact.
func TestExpectedDigestPerArch(t *testing.T) {
	const amd, arm = "aaaa", "bbbb"
	tests := map[string]struct {
		arch string
		want string
		ok   bool
	}{
		"amd64 takes the x86_64 pin": {arch: archAMD64, want: amd, ok: true},
		"arm64 takes the arm64 pin":  {arch: archARM64, want: arm, ok: true},
		"unknown arch refuses":       {arch: "riscv64-linux", ok: false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := expectedDigest(&Config{Arch: tc.arch, SHA256: amd, SHA256ARM64: arm})
			switch {
			case tc.ok && err != nil:
				t.Fatalf("expectedDigest: %v", err)
			case !tc.ok && !errors.Is(err, ErrUnsupportedArch):
				t.Fatalf("error = %v, want ErrUnsupportedArch", err)
			case tc.ok && got != tc.want:
				t.Errorf("digest = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestEnsureSyncFailureAtEveryProtocolPointRetainsPreviousVersion pins the
// durability protocol (finding 2). Atomic visibility is not crash durability,
// so the install syncs every file, then the sentinel, then the staged
// directory, then -- after the rename -- the installation root. A failure at
// ANY of those points must fail the install and leave the complete version
// already on the volume active and ready.
//
// Each case names one sync point by the path the manager syncs there, so a
// reordering of the protocol that skips a sync makes the matching case fail.
func TestEnsureSyncFailureAtEveryProtocolPointRetainsPreviousVersion(t *testing.T) {
	sentinelFail := errors.New("injected sync failure")
	tests := map[string]struct {
		// match reports whether the path is the protocol point under test.
		match func(root, path string) bool
		// published is whether the pinned directory survives the failure. A
		// failure before the rename leaves nothing; a failure syncing the
		// PARENT happens after it, and the directory is durable except for its
		// parent entry, which the next boot re-probes.
		published bool
	}{
		"a required dispatcher": {
			match: func(_, path string) bool {
				return filepath.Base(path) == "kiro-cli" && strings.Contains(path, stagePrefix)
			},
		},
		"an optional dispatcher": {
			match: func(_, path string) bool {
				return filepath.Base(path) == "kiro-cli-term" && strings.Contains(path, stagePrefix)
			},
		},
		"the completion sentinel": {
			match: func(_, path string) bool {
				return filepath.Base(path) == sentinelName
			},
		},
		"the staged version directory": {
			match: func(_, path string) bool {
				return filepath.Base(path) == "v" && strings.Contains(path, stagePrefix)
			},
		},
		"the installation root": {
			match: func(root, path string) bool {
				return path == root
			},
			published: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			env := newFakeEnv(t)
			prev := env.placeVersion(prevVersion)
			root := env.versionsRoot()
			env.onSync = func(path string) error {
				if tc.match(root, path) {
					return sentinelFail
				}
				return nil
			}
			m := env.manager()

			err := m.Ensure(context.Background())
			if !errors.Is(err, sentinelFail) {
				t.Fatalf("Ensure error = %v, want the injected sync failure", err)
			}
			// The previous complete version is retained and serving: a sync
			// failure must never cost the volume its working install.
			if !exists(filepath.Join(prev, mainBinary)) {
				t.Error("the previous complete version was destroyed by a failed install")
			}
			if ready, reason := m.Ready(); !ready {
				t.Errorf("Ready() = false (%s), want true on the retained previous version", reason)
			}
			if state, ok := m.Active(); !ok || state.ActiveVersion != prevVersion {
				t.Errorf("active version = %q (ok=%v), want %q", state.ActiveVersion, ok, prevVersion)
			} else if state.LastError == "" {
				t.Error("State.LastError is empty; the failed install must be recorded for diagnosis")
			}
			if got := exists(env.versionDir(pinnedVersion)); got != tc.published {
				t.Errorf("pinned directory exists = %v, want %v", got, tc.published)
			}
			// No staging tree may survive a failed install.
			for _, entry := range env.versionDirs() {
				if strings.HasPrefix(entry, stagePrefix) {
					t.Errorf("staging tree %q survived a failed install", entry)
				}
			}
		})
	}
}

// TestEnsurePublishRenameFailureKeepsPreviousActive pins the publish boundary:
// when the single same-filesystem rename that makes a version visible fails,
// the install fails and the previously complete version stays selected. There
// is no half-published state to fall into, because the rename IS the
// publication.
func TestEnsurePublishRenameFailureKeepsPreviousActive(t *testing.T) {
	env := newFakeEnv(t)
	prev := env.placeVersion(prevVersion)
	m := env.manager()
	failure := errors.New("injected rename failure")
	target := env.versionDir(pinnedVersion)
	env.onRename = func(_, newpath string) error {
		if newpath == target {
			return failure
		}
		return nil
	}

	err := m.Ensure(context.Background())
	if !errors.Is(err, failure) {
		t.Fatalf("Ensure error = %v, want the injected rename failure", err)
	}
	if exists(target) {
		t.Error("the pinned version directory exists although the publish rename failed")
	}
	if !exists(filepath.Join(prev, mainBinary)) {
		t.Fatal("the previous complete version was destroyed by a failed publish")
	}
	if ready, reason := m.Ready(); !ready {
		t.Errorf("Ready() = false (%s), want true on the previous version", reason)
	}
	if got := m.CLIPath(); got != filepath.Join(prev, mainBinary) {
		t.Errorf("CLIPath() = %q, want the previous version's binary %q", got, filepath.Join(prev, mainBinary))
	}
}

// TestEnsureFailedInstallWithNoPreviousVersionIsUnready pins the other half of
// invariant 6's posture: with nothing on the volume to fall back to, readiness is
// withheld, the reason distinguishes the lifecycle phase, and no partial
// directory is left for a later boot to trust.
func TestEnsureFailedInstallWithNoPreviousVersionIsUnready(t *testing.T) {
	env := newFakeEnv(t)
	env.installerFails = true
	m := env.manager()

	if err := m.Ensure(context.Background()); err == nil {
		t.Fatal("Ensure returned nil although the installer produced nothing")
	}
	if ready, reason := m.Ready(); ready || reason != ReasonInstalling {
		t.Errorf("Ready() = (%v, %q), want (false, %q)", ready, reason, ReasonInstalling)
	}
	if got := m.CLIPath(); got != "" {
		t.Errorf("CLIPath() = %q, want empty when nothing is active", got)
	}
	if dirs := env.versionDirs(); len(dirs) != 0 {
		t.Errorf("installation root holds %v, want nothing", dirs)
	}
}

// TestEnsureRefusesAStagedBinaryAtTheWrongVersion pins the staged gate: a
// candidate that installs cleanly but reports a version other than the pin is
// never published, so a mismatched upstream artifact cannot become a version
// directory named after the pin.
func TestEnsureRefusesAStagedBinaryAtTheWrongVersion(t *testing.T) {
	env := newFakeEnv(t)
	env.produces["kiro-cli"] = "9.9.9"
	m := env.manager()

	err := m.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "9.9.9") {
		t.Fatalf("Ensure error = %v, want a refusal naming the staged version", err)
	}
	if exists(env.versionDir(pinnedVersion)) {
		t.Error("a wrongly versioned candidate was published under the pinned name")
	}
}

// TestEnsureRefusesToPublishWhenTheRequiredSettingFails pins the integrity
// gate: app.disableAutoupdates is what stops the binary replacing itself and
// invalidating the verified digest, so a candidate it cannot be set on is never
// published.
func TestEnsureRefusesToPublishWhenTheRequiredSettingFails(t *testing.T) {
	env := newFakeEnv(t)
	env.onSetting = func(_, key, _ string) error {
		if key == autoUpdateSetting {
			return errors.New("settings call failed")
		}
		return nil
	}
	m := env.manager()

	err := m.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), autoUpdateSetting) {
		t.Fatalf("Ensure error = %v, want a refusal naming %s", err, autoUpdateSetting)
	}
	if exists(env.versionDir(pinnedVersion)) {
		t.Error("a candidate whose auto-update could not be disabled was published")
	}
}

// TestExtractZipRefusesEscapingEntries pins the extraction guards: an absolute
// path and a traversal entry are both refused, so a hostile archive cannot
// write outside the temp tree. Refusal rather than sanitizing is deliberate --
// a legitimate archive has no such entry, so quietly rewriting one would hide
// the archive that carries it.
func TestExtractZipRefusesEscapingEntries(t *testing.T) {
	tests := map[string]string{
		"absolute path":      "/etc/cron.d/pwn",
		"parent traversal":   "../../pwn",
		"nested traversal":   "kirocli/../../pwn",
		"absolute traversal": "/../pwn",
	}
	for name, entry := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := safeJoin(dir, entry); err == nil {
				t.Errorf("safeJoin(%q) accepted an entry that escapes the extraction directory", entry)
			}
			// The same refusal must hold through the real extraction path.
			archive := filepath.Join(dir, "hostile.zip")
			writeZip(t, archive, map[string]string{entry: "pwned"})
			if err := extractZip(archive, filepath.Join(dir, "out")); err == nil {
				t.Errorf("extractZip accepted an archive holding %q", entry)
			}
			if exists(filepath.Join(dir, "pwn")) || exists("/etc/cron.d/pwn") {
				t.Fatalf("extraction of %q escaped the extraction directory", entry)
			}
		})
	}
}

// TestExtractZipUnpacksARealArchive pins the happy extraction path, including
// that the executable bit survives (install.sh has to run) and that a nested
// directory entry is created.
func TestExtractZipUnpacksARealArchive(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "a.zip")
	writeZip(t, archive, map[string]string{
		"kirocli/install.sh":    "#!/bin/sh\n",
		"kirocli/lib/README.md": "docs\n",
	})
	out := filepath.Join(dir, "out")
	if err := extractZip(archive, out); err != nil {
		t.Fatalf("extractZip: %v", err)
	}
	fi, err := os.Stat(filepath.Join(out, "kirocli", "install.sh"))
	if err != nil {
		t.Fatalf("Stat install.sh: %v", err)
	}
	if fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("install.sh mode = %v, want the owner-execute bit preserved", fi.Mode().Perm())
	}
	if !exists(filepath.Join(out, "kirocli", "lib", "README.md")) {
		t.Error("the nested archive entry was not extracted")
	}
}

// writeZip builds a zip at path from name -> body. install.sh gets the
// executable bit, everything else 0o644.
func writeZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, body := range entries {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		if strings.HasSuffix(name, ".sh") {
			h.SetMode(0o755)
		} else {
			h.SetMode(0o644)
		}
		w, cerr := zw.CreateHeader(h)
		if cerr != nil {
			t.Fatalf("CreateHeader(%s): %v", name, cerr)
		}
		if _, werr := w.Write([]byte(body)); werr != nil {
			t.Fatalf("Write(%s): %v", name, werr)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip Close: %v", err)
	}
}

// TestHTTPFetchRefusesEmptyAndNonOK pins the fetch boundary's two cheap
// refusals. An empty body is a partial download, and reporting it as such
// beats letting the digest check render it as a mismatch, which points the
// operator at the pin instead of the transfer.
func TestHTTPFetchRefusesEmptyAndNonOK(t *testing.T) {
	tests := map[string]struct {
		handler http.HandlerFunc
		want    string
	}{
		"empty body": {
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
			want:    "empty",
		},
		"not found": {
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) },
			want:    "unexpected status",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			err := httpFetch(context.Background(), srv.URL, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("httpFetch error = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

// TestCopyWithStallGuardAbortsAStalledTransfer pins the stall watchdog. A flat
// absolute deadline on a half-gigabyte download is a bandwidth floor rather
// than a hang guard, so the transfer is bounded by LACK OF PROGRESS instead.
func TestCopyWithStallGuardAbortsAStalledTransfer(t *testing.T) {
	release := make(chan struct{})
	cancelled := false
	cancel := func() {
		if !cancelled {
			cancelled = true
			close(release)
		}
	}
	first := true
	src := readerFunc(func(p []byte) (int, error) {
		if first {
			first = false
			p[0] = 'x'
			return 1, nil
		}
		<-release // unblocks only when the watchdog cancels
		return 0, errors.New("context canceled")
	})

	err := copyWithStallGuard(cancel, io.Discard, src, 20*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "no progress") {
		t.Fatalf("copyWithStallGuard error = %v, want a no-progress abort", err)
	}
	if !cancelled {
		t.Error("the watchdog did not cancel the request context")
	}
}

// TestEnsureInstallsWithNoSidecarsAtAll pins vibekit's one-file required set
// end to end. An upstream archive that stops shipping `kiro-cli-chat` /
// `kiro-cli-term` — or a rename of either — must still produce a COMPLETE,
// READY install here, because `kiro-cli acp` is served by the main binary and
// no Go path in this repo invokes `chat`. The absence is a warning, never a
// gate.
//
// The same fixture in web-terminal-kiro is an install FAILURE, which is why
// this test exists rather than being inherited: it is the assertion that stops
// a future sync from copying that repo's required set back over this one.
func TestEnsureInstallsWithNoSidecarsAtAll(t *testing.T) {
	env := newFakeEnv(t)
	env.produces = map[string]string{"kiro-cli": pinnedVersion}
	m := env.manager()

	if err := m.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure error = %v, want nil: a missing OPTIONAL sidecar must only warn", err)
	}
	dir := env.versionDir(pinnedVersion)
	for _, name := range []string{"kiro-cli", sentinelName} {
		if !exists(filepath.Join(dir, name)) {
			t.Errorf("%s missing from the published version directory", name)
		}
	}
	for _, name := range []string{"kiro-cli-chat", "kiro-cli-term"} {
		if exists(filepath.Join(dir, name)) {
			t.Errorf("%s exists although the installer produced none", name)
		}
	}
	if ready, reason := m.Ready(); !ready {
		t.Errorf("Ready() = false (%s), want true: the sidecars are not vibekit's required set", reason)
	}
	if got, want := m.CLIPath(), filepath.Join(dir, mainBinary); got != want {
		t.Errorf("CLIPath() = %q, want %q", got, want)
	}
}

// TestEnsureRefusesWhenTheMainDispatcherIsMissing pins the other side of the
// one-file set: the main binary is the whole required set, so an installer that
// produces only sidecars publishes nothing.
func TestEnsureRefusesWhenTheMainDispatcherIsMissing(t *testing.T) {
	env := newFakeEnv(t)
	env.produces = map[string]string{"kiro-cli-chat": pinnedVersion}
	m := env.manager()

	if err := m.Ensure(context.Background()); err == nil {
		t.Fatal("Ensure returned nil although install.sh produced no main dispatcher")
	}
	if exists(env.versionDir(pinnedVersion)) {
		t.Error("a version directory was published without the main dispatcher")
	}
	if ready, _ := m.Ready(); ready {
		t.Error("Ready() = true with no main dispatcher installed")
	}
}
