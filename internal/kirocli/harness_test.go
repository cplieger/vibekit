package kirocli

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The pin under test and the two versions used as "already on the volume".
const (
	pinnedVersion = "2.14.2"
	prevVersion   = "2.14.1"
	oldVersion    = "2.14.0"
)

// TestMain silences the manager's slog output for the whole package: every
// install path logs, and the volume drowns real failures in CI output. A test
// that needs to assert on a log line installs its own handler.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

// fakeEnv is the test double for every boundary the manager crosses: the
// archive fetch, subprocess execution, fsync, rename, the clock and the sleep.
// Together they make an install run end to end with no network, no real
// archive and no real kiro-cli.
type fakeEnv struct {
	t     *testing.T
	tools string

	archive []byte
	digest  string

	// produces maps the dispatcher names the fake install.sh writes into the
	// staging HOME to the version each one reports.
	produces map[string]string
	// installerFails makes install.sh report a failure AND write nothing, the
	// shape that fails the staged gates.
	installerFails bool

	onFetch   func(dst io.Writer) error
	onProbe   func(bin string) ([]byte, error)
	onSetting func(bin, key, value string) error
	onSync    func(path string) error
	onRename  func(oldpath, newpath string) error

	mu      sync.Mutex
	calls   []string
	fetches int
	slept   []time.Duration
}

func newFakeEnv(t *testing.T) *fakeEnv {
	t.Helper()
	archive := buildArchive(t)
	sum := sha256.Sum256(archive)
	return &fakeEnv{
		t:       t,
		tools:   t.TempDir(),
		archive: archive,
		digest:  hex.EncodeToString(sum[:]),
		produces: map[string]string{
			"kiro-cli":      pinnedVersion,
			"kiro-cli-chat": pinnedVersion,
			"kiro-cli-term": pinnedVersion,
		},
	}
}

// buildArchive builds a real zip whose kirocli/install.sh is the path the
// manager execs, so extraction is exercised for real while the runner seam
// stands in for running it.
func buildArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range []struct {
		name string
		mode os.FileMode
		body string
	}{
		{"kirocli/install.sh", 0o755, "#!/bin/sh\nexit 0\n"},
		{"kirocli/README.md", 0o644, "kiro-cli\n"},
	} {
		h := &zip.FileHeader{Name: f.name, Method: zip.Deflate}
		h.SetMode(f.mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatalf("zip CreateHeader(%s): %v", f.name, err)
		}
		if _, err := w.Write([]byte(f.body)); err != nil {
			t.Fatalf("zip Write(%s): %v", f.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip Close: %v", err)
	}
	return buf.Bytes()
}

// manager builds a manager wired to this env, with every seam replaced.
func (e *fakeEnv) manager(mutate ...func(*Config)) *Manager {
	e.t.Helper()
	cfg := Config{
		// Both sidecars are Optional here: vibekit's required set is the main
		// dispatcher alone (see withMainBinary).
		Optional:     []string{"kiro-cli-chat", "kiro-cli-term"},
		Version:      pinnedVersion,
		SHA256:       e.digest,
		SHA256ARM64:  e.digest,
		ToolsDir:     e.tools,
		Arch:         archAMD64,
		BaseURL:      "https://example.invalid",
		RetryBackoff: time.Millisecond,
		MaxAttempts:  1,
	}
	for _, f := range mutate {
		f(&cfg)
	}
	m, err := New(&cfg)
	if err != nil {
		e.t.Fatalf("New: %v", err)
	}
	m.fetch = e.fetch
	m.run = e.run
	m.fsync = e.fsync
	m.rename = e.rename
	m.now = func() time.Time { return time.Unix(1700000000, 0).UTC() }
	m.sleep = e.sleep
	return m
}

func (e *fakeEnv) record(format string, args ...any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, fmt.Sprintf(format, args...))
}

func (e *fakeEnv) called() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}

func (e *fakeEnv) countCalls(substr string) int {
	n := 0
	for _, c := range e.called() {
		if strings.Contains(c, substr) {
			n++
		}
	}
	return n
}

func (e *fakeEnv) fetch(_ context.Context, url string, dst io.Writer) error {
	e.mu.Lock()
	e.fetches++
	e.mu.Unlock()
	e.record("fetch %s", url)
	if e.onFetch != nil {
		return e.onFetch(dst)
	}
	_, err := dst.Write(e.archive)
	return err
}

func (e *fakeEnv) fetchCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.fetches
}

func (e *fakeEnv) run(_ context.Context, c *command) ([]byte, error) {
	switch {
	case strings.HasSuffix(c.Path, "install.sh"):
		e.record("install.sh")
		return e.runInstaller(c)
	case len(c.Args) == 1 && c.Args[0] == "--version":
		e.record("probe %s", c.Path)
		if e.onProbe != nil {
			return e.onProbe(c.Path)
		}
		return probeFromFile(c.Path)
	case len(c.Args) == 3 && c.Args[0] == "settings":
		e.record("settings %s=%s on %s", c.Args[1], c.Args[2], c.Path)
		if e.onSetting != nil {
			return nil, e.onSetting(c.Path, c.Args[1], c.Args[2])
		}
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected command %s %v", c.Path, c.Args)
}

// runInstaller stands in for the upstream install.sh: it writes the configured
// dispatchers into the private staging HOME the manager handed it.
func (e *fakeEnv) runInstaller(c *command) ([]byte, error) {
	home := ""
	for _, kv := range c.Env {
		if rest, ok := strings.CutPrefix(kv, "HOME="); ok {
			home = rest
		}
	}
	if home == "" {
		return nil, fmt.Errorf("install.sh ran without a private HOME: %v", c.Env)
	}
	if e.installerFails {
		return []byte("boom\n"), fmt.Errorf("install.sh failed")
	}
	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return nil, err
	}
	for name, version := range e.produces {
		if err := writeFakeBinary(filepath.Join(binDir, name), version); err != nil {
			return nil, err
		}
	}
	return []byte("installed\n"), nil
}

func (e *fakeEnv) fsync(path string) error {
	e.record("fsync %s", path)
	if e.onSync != nil {
		if err := e.onSync(path); err != nil {
			return err
		}
	}
	return fsyncPath(path)
}

func (e *fakeEnv) rename(oldpath, newpath string) error {
	e.record("rename %s -> %s", oldpath, newpath)
	if e.onRename != nil {
		if err := e.onRename(oldpath, newpath); err != nil {
			return err
		}
	}
	return os.Rename(oldpath, newpath)
}

func (e *fakeEnv) sleep(_ context.Context, d time.Duration) error {
	e.mu.Lock()
	e.slept = append(e.slept, d)
	e.mu.Unlock()
	return nil
}

func (e *fakeEnv) sleeps() []time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]time.Duration(nil), e.slept...)
}

// writeFakeBinary writes an executable file that records the version it
// answers --version with, so a test can "tamper" with an installed binary by
// rewriting its content.
func writeFakeBinary(path, version string) error {
	return os.WriteFile(path, []byte("kiro-cli fake\nversion="+version+"\n"), 0o755)
}

// probeFromFile reads the version a fake binary encodes and answers in the
// shape kiro-cli --version uses (version as the last field of line 1).
func probeFromFile(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		if v, ok := strings.CutPrefix(line, "version="); ok {
			return []byte("kiro-cli " + v + "\n"), nil
		}
	}
	return nil, fmt.Errorf("%s is not a fake kiro-cli", path)
}

// placeVersion writes a COMPLETE version directory straight onto the volume,
// standing in for an install a previous boot finished.
func (e *fakeEnv) placeVersion(version string, names ...string) string {
	e.t.Helper()
	dir := e.versionDir(version)
	if len(names) == 0 {
		// vibekit's required set: the main dispatcher alone. A directory with
		// no sidecar IS a complete install for this app.
		names = []string{"kiro-cli"}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatalf("MkdirAll(%s): %v", dir, err)
	}
	for _, n := range names {
		if err := writeFakeBinary(filepath.Join(dir, n), version); err != nil {
			e.t.Fatalf("writeFakeBinary(%s): %v", n, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, sentinelName), []byte(version+"\n"), 0o600); err != nil {
		e.t.Fatalf("write sentinel: %v", err)
	}
	return dir
}

// placePartial writes a version directory with the binaries but NO sentinel:
// the shape an interrupted install leaves behind.
func (e *fakeEnv) placePartial(version string) string {
	e.t.Helper()
	dir := e.versionDir(version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatalf("MkdirAll(%s): %v", dir, err)
	}
	for _, n := range []string{"kiro-cli", "kiro-cli-chat"} {
		if err := writeFakeBinary(filepath.Join(dir, n), version); err != nil {
			e.t.Fatalf("writeFakeBinary(%s): %v", n, err)
		}
	}
	return dir
}

func (e *fakeEnv) versionsRoot() string { return filepath.Join(e.tools, versionsSubdir) }

func (e *fakeEnv) versionDir(version string) string {
	return filepath.Join(e.versionsRoot(), version)
}

// versionDirs lists the non-staging entries under the installation root.
func (e *fakeEnv) versionDirs() []string {
	entries, err := os.ReadDir(e.versionsRoot())
	if err != nil {
		return nil
	}
	out := []string{}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".") {
			out = append(out, entry.Name())
		}
	}
	return out
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
