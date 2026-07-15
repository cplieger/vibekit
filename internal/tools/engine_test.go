package tools

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// newTestEngine builds an Engine wired to a temp config/tools dir, a
// plain HTTP client (httptest servers live on 127.0.0.1, which the
// production SSRF transport rightly blocks), and an optional catalog.
func newTestEngine(t *testing.T, cat *Catalog) *Engine {
	t.Helper()
	dir := t.TempDir()
	st := newStore(dir)
	if err := st.initFiles(); err != nil {
		t.Fatal(err)
	}
	if cat == nil {
		cat = &Catalog{Entries: map[string]CatalogEntry{}}
	}
	toolsDir := filepath.Join(dir, "tools")
	if err := os.MkdirAll(filepath.Join(toolsDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	e := &Engine{
		store:    st,
		catalog:  cat,
		versions: newVersionResolver(http.DefaultClient),
		toolsDir: toolsDir,
	}
	e.inst = &installer{toolsDir: toolsDir, client: http.DefaultClient, output: func(string) {}}
	e.queue = newJobQueue(nil, e.executeJob)
	t.Cleanup(e.Close)
	return e
}

// waitJob polls until the job reaches a terminal state.
func waitJob(t *testing.T, e *Engine, id string) *api.ToolJob {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	v, err := e.queue.Wait(ctx, id)
	if err != nil {
		t.Fatalf("wait job %s: %v", id, err)
	}
	return v
}

func TestStore_InitBacksUpV1(t *testing.T) {
	dir := t.TempDir()
	v1 := `{"runtimes":{"node":{"enabled":false,"version":"v26.5.0"}}}`
	if err := os.WriteFile(filepath.Join(dir, "tools.json"), []byte(v1), 0o644); err != nil {
		t.Fatal(err)
	}
	st := newStore(dir)
	if err := st.initFiles(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tools.json.v1.bak")); err != nil {
		t.Fatalf("v1 backup missing: %v", err)
	}
	m, err := st.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != ManifestVersion || len(m.Tools) != 0 {
		t.Fatalf("fresh manifest = %+v", m)
	}
}

func TestStore_MutateRoundtrip(t *testing.T) {
	st := newStore(t.TempDir())
	if err := st.initFiles(); err != nil {
		t.Fatal(err)
	}
	err := st.MutateManifest(func(m *Manifest) error {
		m.Tools["jq"] = Tool{Source: "aqua:jqlang/jq", Version: "1.8.1"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := st.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if m.Tools["jq"].Version != "1.8.1" {
		t.Fatalf("roundtrip lost data: %+v", m.Tools)
	}
	st.setToolStatus("jq", func(s *ToolStatus) {
		s.InstalledVersion = "1.8.1"
		s.Bins = []string{"jq"}
	})
	if got := st.State().Tools["jq"].InstalledVersion; got != "1.8.1" {
		t.Fatalf("state roundtrip = %q", got)
	}
	st.dropToolStatus("jq")
	if _, ok := st.State().Tools["jq"]; ok {
		t.Fatal("dropToolStatus left entry")
	}
}

func TestCreate_ManualInstallRuns(t *testing.T) {
	e := newTestEngine(t, nil)
	job, err := e.Create(context.Background(), &CreateRequest{
		Name:    "hello",
		Source:  SourceManual,
		Version: "1.0.0",
		Install: `printf '#!/bin/sh\necho hi\n' > "$BIN/hello" && chmod 755 "$BIN/hello"`,
	})
	if err != nil {
		t.Fatal(err)
	}
	final := waitJob(t, e, job.ID)
	if final.State != JobDone {
		t.Fatalf("job = %+v", final)
	}
	list, err := e.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != 1 || !list.Tools[0].Installed || list.Tools[0].InstalledVersion != "1.0.0" {
		t.Fatalf("list = %+v", list.Tools)
	}
}

func TestCreate_ManualProbeMissingFails(t *testing.T) {
	e := newTestEngine(t, nil)
	job, err := e.Create(context.Background(), &CreateRequest{
		Name:    "ghost",
		Source:  SourceManual,
		Version: "1.0.0",
		Install: "true", // succeeds but installs nothing
	})
	if err != nil {
		t.Fatal(err)
	}
	final := waitJob(t, e, job.ID)
	if final.State != JobFailed {
		t.Fatalf("job state = %s, want failed", final.State)
	}
	list, _ := e.List()
	if list.Tools[0].LastError == "" {
		t.Fatal("expected last_error recorded")
	}
}

func TestCreate_Validation(t *testing.T) {
	e := newTestEngine(t, nil)
	cases := []CreateRequest{
		{Name: "bad name!", Source: SourceManual, Version: "1", Install: "true"},
		{Name: "x", Source: "weird:ref", Version: "1"},
		{Name: "x", Source: SourceManual, Version: "1"},        // no install cmd
		{Name: "x", Source: "npm:pkg", Version: "1; rm -rf /"}, // bad version
		{Name: "unknown-tool-with-no-source-or-catalog", Version: "1"},
	}
	for i, req := range cases {
		if _, err := e.Create(context.Background(), &req); err == nil {
			t.Errorf("case %d: want error, got nil", i)
		}
	}
}

func TestCreate_DuplicateRejected(t *testing.T) {
	e := newTestEngine(t, nil)
	req := CreateRequest{
		Name: "dup", Source: SourceManual, Version: "1",
		Install: `printf x > "$BIN/dup" && chmod 755 "$BIN/dup"`,
	}
	if _, err := e.Create(context.Background(), &req); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Create(context.Background(), &req); err == nil {
		t.Fatal("duplicate create should fail")
	}
}

func TestPatch_PinSyncAndVersionJob(t *testing.T) {
	e := newTestEngine(t, nil)
	job, err := e.Create(context.Background(), &CreateRequest{
		Name: "t", Source: SourceManual, Version: "1.0.0",
		Install: `printf x > "$BIN/t" && chmod 755 "$BIN/t"`, Probe: "t",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, e, job.ID)

	pin := true
	jv, err := e.Patch("t", PatchRequest{Pin: &pin})
	if err != nil || jv != nil {
		t.Fatalf("pin patch: job=%v err=%v", jv, err)
	}
	m, _ := e.store.Manifest()
	if !m.Tools["t"].Pin {
		t.Fatal("pin not persisted")
	}

	v2 := "2.0.0"
	jv, err = e.Patch("t", PatchRequest{Version: &v2})
	if err != nil || jv == nil {
		t.Fatalf("version patch: job=%v err=%v", jv, err)
	}
	final := waitJob(t, e, jv.ID)
	if final.State != JobDone {
		t.Fatalf("reinstall job = %+v", final)
	}
	if got := e.store.State().Tools["t"].InstalledVersion; got != "2.0.0" {
		t.Fatalf("installed_version = %q, want 2.0.0", got)
	}

	if _, err := e.Patch("missing", PatchRequest{Pin: &pin}); !IsNotFound(err) {
		t.Fatalf("patch missing = %v, want not-found", err)
	}
}

func TestDelete_DependentsConflict(t *testing.T) {
	e := newTestEngine(t, nil)
	mk := func(name string, requires []string) {
		job, err := e.Create(context.Background(), &CreateRequest{
			Name: name, Source: SourceManual, Version: "1", Requires: requires,
			Install: fmt.Sprintf(`printf x > "$BIN/%s" && chmod 755 "$BIN/%s"`, name, name),
		})
		if err != nil {
			t.Fatal(err)
		}
		waitJob(t, e, job.ID)
	}
	mk("base", nil)
	mk("dep", []string{"base"})

	_, deps, err := e.Delete("base", false)
	if !IsHasDependents(err) {
		t.Fatalf("err = %v, want has-dependents", err)
	}
	if len(deps) != 1 || deps[0] != "dep" {
		t.Fatalf("dependents = %v", deps)
	}

	jv, _, err := e.Delete("base", true)
	if err != nil {
		t.Fatal(err)
	}
	final := waitJob(t, e, jv.ID)
	if final.State != JobDone {
		t.Fatalf("uninstall job = %+v", final)
	}
	m, _ := e.store.Manifest()
	if len(m.Tools) != 0 {
		t.Fatalf("cascade left tools: %+v", m.Tools)
	}
	if _, err := os.Stat(filepath.Join(e.toolsDir, "bin", "base")); !os.IsNotExist(err) {
		t.Fatal("base bin not removed")
	}
}

func TestInstallOrder_BackendDepFromCatalog(t *testing.T) {
	// npm-sourced tool pulls node from the catalog automatically.
	cat := &Catalog{Entries: map[string]CatalogEntry{
		"node": {
			Name: "node", Source: SourceManual, Version: "1.0.0",
			Install: `printf x > "$BIN/node" && chmod 755 "$BIN/node"`, Probe: "node",
		},
	}}
	e := newTestEngine(t, cat)
	err := e.store.MutateManifest(func(m *Manifest) error {
		m.Tools["pyright"] = Tool{Source: "npm:pyright", Version: "1.0.0"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := e.store.Manifest()
	ordered, err := e.installOrder(context.Background(), m, []string{"pyright"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ordered) != 2 || ordered[0] != "node" || ordered[1] != "pyright" {
		t.Fatalf("ordered = %v, want [node pyright]", ordered)
	}
	// The dep was adopted into the manifest.
	m2, _ := e.store.Manifest()
	if _, ok := m2.Tools["node"]; !ok {
		t.Fatal("node not adopted into manifest")
	}
}

func TestInstallOrder_CycleDetected(t *testing.T) {
	e := newTestEngine(t, nil)
	err := e.store.MutateManifest(func(m *Manifest) error {
		m.Tools["a"] = Tool{Source: SourceManual, Version: "1", Install: "true", Requires: []string{"b"}}
		m.Tools["b"] = Tool{Source: SourceManual, Version: "1", Install: "true", Requires: []string{"a"}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := e.store.Manifest()
	if _, err := e.installOrder(context.Background(), m, []string{"a"}); err == nil {
		t.Fatal("want cycle error")
	}
}

// TestInstallAqua_EndToEnd exercises the full artifact path: download a
// tar.gz from a local server, verify its sha256 against a checksums
// file, extract, link the declared binary, prune an older version.
func TestInstallAqua_EndToEnd(t *testing.T) {
	// Build a tar.gz holding tool-1.2.0/bin/mytool.
	var tarball strings.Builder
	gz := gzip.NewWriter(&nopWriteCloser{&tarball})
	tw := tar.NewWriter(gz)
	content := []byte("#!/bin/sh\necho mytool\n")
	if err := tw.WriteHeader(&tar.Header{
		Name: "mytool-1.2.0/bin/mytool", Mode: 0o755, Size: int64(len(content)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	artifact := tarball.String()
	sum := sha256.Sum256([]byte(artifact))
	checksums := hex.EncodeToString(sum[:]) + "  mytool_1.2.0_linux_" + runtime.GOARCH + ".tar.gz\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".tar.gz"):
			_, _ = w.Write([]byte(artifact))
		case strings.HasSuffix(r.URL.Path, "checksums.txt"):
			_, _ = w.Write([]byte(checksums))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	aq := &AquaPackage{
		Type: "http", RepoOwner: "o", RepoName: "mytool",
		URL:    srv.URL + "/mytool_{{trimV .Version}}_{{.OS}}_{{.Arch}}.{{.Format}}",
		Format: "tar.gz",
		Files:  []AquaFile{{Name: "mytool", Src: "mytool-{{trimV .Version}}/bin/mytool"}},
		Checksum: &AquaChecksum{
			Type: "http", URL: srv.URL + "/checksums.txt", Algorithm: "sha256",
		},
	}
	cat := &Catalog{Entries: map[string]CatalogEntry{
		"mytool": {Name: "mytool", Source: "aqua:o/mytool", Aqua: aq},
	}}
	e := newTestEngine(t, cat)

	// Simulate a stale previous version to prune.
	old := filepath.Join(e.toolsDir, "opt", "mytool", "v1.1.0")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}

	job, err := e.Create(context.Background(), &CreateRequest{Name: "mytool", Version: "v1.2.0"})
	if err != nil {
		t.Fatal(err)
	}
	final := waitJob(t, e, job.ID)
	if final.State != JobDone {
		t.Fatalf("job = %+v tail=%v", final, final.OutputTail)
	}

	link := filepath.Join(e.toolsDir, "bin", "mytool")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("bin link: %v", err)
	}
	if want := filepath.Join(e.toolsDir, "opt", "mytool", "v1.2.0", "mytool-1.2.0", "bin", "mytool"); target != want {
		t.Errorf("link target = %s, want %s", target, want)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("old version not pruned")
	}
	st := e.store.State().Tools["mytool"]
	if st.InstalledVersion != "v1.2.0" || len(st.Bins) != 1 {
		t.Errorf("state = %+v", st)
	}
}

// TestInstallAqua_ChecksumMismatch ensures a bad digest aborts before
// anything is linked.
func TestInstallAqua_ChecksumMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "checksums.txt") {
			_, _ = w.Write([]byte(strings.Repeat("0", 64) + "  tool_1.0.0_linux_" + runtime.GOARCH + ".raw\n"))
			return
		}
		_, _ = w.Write([]byte("binary-bytes"))
	}))
	defer srv.Close()

	aq := &AquaPackage{
		Type: "http", RepoOwner: "o", RepoName: "tool",
		URL:      srv.URL + "/tool_{{trimV .Version}}_{{.OS}}_{{.Arch}}.raw",
		Format:   "raw",
		Files:    []AquaFile{{Name: "tool"}},
		Checksum: &AquaChecksum{Type: "http", URL: srv.URL + "/checksums.txt", Algorithm: "sha256"},
	}
	cat := &Catalog{Entries: map[string]CatalogEntry{
		"tool": {Name: "tool", Source: "aqua:o/tool", Aqua: aq},
	}}
	e := newTestEngine(t, cat)
	job, err := e.Create(context.Background(), &CreateRequest{Name: "tool", Version: "v1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	final := waitJob(t, e, job.ID)
	if final.State != JobFailed || !strings.Contains(final.Error, "checksum mismatch") {
		t.Fatalf("job = %+v", final)
	}
	if _, err := os.Stat(filepath.Join(e.toolsDir, "bin", "tool")); !os.IsNotExist(err) {
		t.Fatal("bin linked despite checksum failure")
	}
}

func TestJobs_CancelQueued(t *testing.T) {
	e := newTestEngine(t, nil)
	// Occupy the worker with a slow manual install.
	slow, err := e.Create(context.Background(), &CreateRequest{
		Name: "slow", Source: SourceManual, Version: "1",
		Install: `sleep 5 && printf x > "$BIN/slow"`,
	})
	if err != nil {
		t.Fatal(err)
	}
	queued, err := e.UpdateAll(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !e.CancelJob(queued.ID) {
		t.Fatal("cancel queued failed")
	}
	if v := waitJob(t, e, queued.ID); v.State != JobCancelled {
		t.Fatalf("queued job state = %s", v.State)
	}
	if !e.CancelJob(slow.ID) {
		t.Fatal("cancel running failed")
	}
	if v := waitJob(t, e, slow.ID); v.State != JobCancelled && v.State != JobFailed {
		t.Fatalf("running job state = %s", v.State)
	}
	if e.CancelJob("tj-nope") {
		t.Fatal("cancel unknown succeeded")
	}
}

func TestSearch_HidesManifestEntries(t *testing.T) {
	cat := &Catalog{Entries: map[string]CatalogEntry{
		"jq":  {Name: "jq", Source: "aqua:jqlang/jq", Featured: true},
		"gh":  {Name: "gh", Source: "aqua:cli/cli", Featured: true},
		"rg":  {Name: "ripgrep", Source: "aqua:BurntSushi/ripgrep", Aliases: []string{"rg"}},
		"xyz": {Name: "xyz", Source: "npm:xyz", Description: "json tool"},
	}}
	e := newTestEngine(t, cat)
	err := e.store.MutateManifest(func(m *Manifest) error {
		m.Tools["jq"] = Tool{Source: "aqua:jqlang/jq", Version: "1.8.1"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	feat := e.Search("")
	for _, h := range feat {
		if h.Name == "jq" {
			t.Fatal("featured list should hide installed jq")
		}
	}
	if hits := e.Search("json"); len(hits) == 0 {
		t.Fatal("description search found nothing")
	}
	if hits := e.Search("rg"); len(hits) == 0 || hits[0].Name != "ripgrep" {
		t.Fatalf("alias search = %+v", hits)
	}
}

func TestList_LatestFromCache(t *testing.T) {
	e := newTestEngine(t, nil)
	job, err := e.Create(context.Background(), &CreateRequest{
		Name: "t", Source: SourceManual, Version: "1.0.0",
		Install: `printf x > "$BIN/t" && chmod 755 "$BIN/t"`,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, e, job.ID)
	// Seed the version cache as an update job would.
	e.versions.mu.Lock()
	e.versions.cache[SourceManual] = "" // manual: never cached
	e.versions.cache["npm:x"] = "9.9.9"
	e.versions.mu.Unlock()

	list, err := e.List()
	if err != nil {
		t.Fatal(err)
	}
	if list.Tools[0].Latest != "" {
		t.Fatalf("manual tool has latest = %q", list.Tools[0].Latest)
	}
	if len(list.System) == 0 {
		t.Fatal("system tools missing")
	}
}

// nopWriteCloser adapts a strings.Builder for gzip.NewWriter.
type nopWriteCloser struct{ b *strings.Builder }

func (w *nopWriteCloser) Write(p []byte) (int, error) { return w.b.Write(p) }

// --- adversarial-review regression tests ---

// TestChecksumConfigured_FailsClosed: a configured checksum whose URL
// can't resolve must fail spec resolution, never silently downgrade to
// an unverified install.
func TestChecksumConfigured_FailsClosed(t *testing.T) {
	aq := &AquaPackage{
		Type: "http", RepoOwner: "o", RepoName: "t",
		URL: "https://example.com/t.raw", Format: "raw",
		Checksum: &AquaChecksum{Type: "weird_type", Algorithm: "sha256"},
	}
	if _, err := aq.ResolveSpec("v1.0.0"); err == nil {
		t.Fatal("unsupported checksum type must fail resolution")
	}
	aq.Checksum = &AquaChecksum{Type: "github_release", Asset: "{{.Broken", Algorithm: "sha256"}
	if _, err := aq.ResolveSpec("v1.0.0"); err == nil {
		t.Fatal("checksum template error must fail resolution")
	}
	aq.Checksum = &AquaChecksum{Type: "github_release", Asset: "sums.txt"}
	if _, err := aq.ResolveSpec("v1.0.0"); err == nil {
		t.Fatal("checksum without algorithm must fail resolution")
	}
	// enabled:false is an explicit opt-out and must still resolve.
	off := false
	aq.Checksum = &AquaChecksum{Type: "github_release", Asset: "sums.txt", Enabled: &off}
	if _, err := aq.ResolveSpec("v1.0.0"); err != nil {
		t.Fatalf("disabled checksum should resolve: %v", err)
	}
}

// TestCreate_QueueFullRollsBackManifest: a create rejected by a full
// queue must not leave a phantom manifest row.
func TestCreate_QueueFullRollsBackManifest(t *testing.T) {
	e := newTestEngine(t, nil)
	// Occupy the worker, then fill the queue to its cap.
	slow, err := e.Create(context.Background(), &CreateRequest{
		Name: "slow", Source: SourceManual, Version: "1",
		Install: `sleep 3 && printf x > "$BIN/slow"`,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Wait until the worker picks the slow job up (drains it from
	// pending) so the cap-filling below is deterministic.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if a := e.queue.Active(); a != nil && a.ID == slow.ID && a.State == JobRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("slow job never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for range jobQueueCap {
		if _, err := e.UpdateAll(nil); err != nil {
			t.Fatal(err) // filling to cap must succeed
		}
	}
	if _, err := e.Create(context.Background(), &CreateRequest{
		Name: "phantom", Source: SourceManual, Version: "1", Install: "true",
	}); err == nil {
		t.Fatal("expected queue-full error")
	}
	m, _ := e.store.Manifest()
	if _, exists := m.Tools["phantom"]; exists {
		t.Fatal("rejected create left a manifest row")
	}
}

// TestUninstall_UsesRemovedDefinitions: manual uninstall commands must
// actually run on delete (they ride the job's removed map — the
// manifest row is already gone when the job executes).
func TestUninstall_UsesRemovedDefinitions(t *testing.T) {
	e := newTestEngine(t, nil)
	marker := filepath.Join(e.toolsDir, "uninstall-ran")
	job, err := e.Create(context.Background(), &CreateRequest{
		Name: "m", Source: SourceManual, Version: "1",
		Install:   `printf x > "$BIN/m" && chmod 755 "$BIN/m"`,
		Uninstall: fmt.Sprintf(`touch %q`, marker),
	})
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, e, job.ID)

	jv, _, err := e.Delete("m", false)
	if err != nil {
		t.Fatal(err)
	}
	final := waitJob(t, e, jv.ID)
	if final.State != JobDone {
		t.Fatalf("uninstall job = %+v", final)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("manual uninstall command did not run")
	}
}

// TestLinkPMBins_PreservesOwnershipAcrossUpdates: an update whose
// binDiff finds nothing new must keep the previously recorded pm bins
// (multi-bin packages would otherwise read as uninstalled).
func TestLinkPMBins_PreservesOwnershipAcrossUpdates(t *testing.T) {
	dir := t.TempDir()
	in := &installer{toolsDir: dir, output: func(string) {}}
	pmBin := filepath.Join(dir, "npm", "bin")
	if err := os.MkdirAll(pmBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(in.binDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{"tsc", "tsserver"} {
		if err := os.WriteFile(filepath.Join(pmBin, b), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Update run: nothing newly added, previous ownership known.
	owned, err := in.linkPMBins(pmBin, nil, []string{"tsc", "tsserver"}, "typescript")
	if err != nil {
		t.Fatal(err)
	}
	if len(owned) != 2 {
		t.Fatalf("owned = %v, want both prior bins", owned)
	}
	for _, b := range owned {
		if _, err := os.Lstat(filepath.Join(in.binDir(), b)); err != nil {
			t.Errorf("bin %s not linked", b)
		}
	}
}

// TestInstallAqua_SymlinkEscapeRejected: an archive member that is a
// symlink pointing outside the install tree must be rejected before
// chmod/link publishes it.
func TestInstallAqua_SymlinkEscapeRejected(t *testing.T) {
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Tarball containing bin/tool as a symlink to the victim path.
	var tarball strings.Builder
	gz := gzip.NewWriter(&nopWriteCloser{&tarball})
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: "bin/tool", Typeflag: tar.TypeSymlink, Linkname: victim, Mode: 0o777,
	}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(tarball.String()))
	}))
	defer srv.Close()

	aq := &AquaPackage{
		Type: "http", RepoOwner: "o", RepoName: "tool",
		URL: srv.URL + "/tool.tar.gz", Format: "tar.gz",
		Files: []AquaFile{{Name: "tool", Src: "bin/tool"}},
	}
	cat := &Catalog{Entries: map[string]CatalogEntry{
		"tool": {Name: "tool", Source: "aqua:o/tool", Aqua: aq},
	}}
	e := newTestEngine(t, cat)
	job, err := e.Create(context.Background(), &CreateRequest{Name: "tool", Version: "v1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	final := waitJob(t, e, job.ID)
	if final.State != JobFailed || !strings.Contains(final.Error, "escapes") {
		t.Fatalf("job = %+v, want symlink-escape failure", final)
	}
	if _, err := os.Lstat(filepath.Join(e.toolsDir, "bin", "tool")); !os.IsNotExist(err) {
		t.Fatal("escaping symlink was published to bin/")
	}
	if fi, _ := os.Stat(victim); fi.Mode().Perm() != 0o600 {
		t.Fatal("victim file permissions were changed")
	}
}

func TestValidToolName_ScopedEdgeCases(t *testing.T) {
	bad := []string{"@/x", "@x/", "x/y", "@a/b/c", "/x"}
	for _, n := range bad {
		if validToolName(n) {
			t.Errorf("validToolName(%q) = true, want false", n)
		}
	}
	if !validToolName("@scope/name") {
		t.Error("@scope/name should be valid")
	}
}
