package kirocli

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// defaultBaseURL is the AWS-hosted release host the kiro-cli docs publish.
const defaultBaseURL = "https://desktop-release.q.us-east-1.amazonaws.com"

// Archive and extraction ceilings. The pinned archive is ~528 MB, so these are
// sanity bounds against a hostile or corrupt response, not tight fits.
const (
	maxArchiveBytes   int64 = 2 << 30
	maxExtractBytes   int64 = 3 << 30
	maxExtractEntries       = 20000
	// stallWindow aborts a transfer that makes no progress for this long. A
	// flat absolute deadline on a half-gigabyte download is a BANDWIDTH FLOOR
	// rather than a hang guard, which is why the shell installer used curl's
	// --speed-limit/--speed-time pair instead; this is the same idea.
	stallWindow = 60 * time.Second
	// connectTimeout bounds only the handshake, never the body.
	connectTimeout = 20 * time.Second
)

// command is one bounded external command execution. It is the manager's only
// path to a subprocess, so replacing the runner removes every process
// dependency from the tests.
type command struct {
	// Path is the absolute program path.
	Path string
	// Env holds extra variables appended to the process environment. The
	// upstream installer runs with HOME pointed at the private staging tree.
	Env []string
	// Args are passed as separate elements, never through a shell.
	Args []string
	// Timeout bounds the run; every call site sets one.
	Timeout time.Duration
	// CaptureStderr folds stderr into the returned output, for commands whose
	// diagnostics are worth surfacing (the upstream installer).
	CaptureStderr bool
}

// runCommand is the manager's process-execution seam.
type runCommand func(ctx context.Context, c *command) ([]byte, error)

// execRunner is the production runner: one bounded, context-cancellable
// subprocess with no shell in the path.
func execRunner(ctx context.Context, c *command) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	// #nosec G204 -- the path and args are built from this package's own
	// constants and the operator-supplied pin, never from request data, and
	// they are passed as separate argv elements so no shell parses them.
	cmd := exec.CommandContext(ctx, c.Path, c.Args...)
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	if c.CaptureStderr {
		return cmd.CombinedOutput()
	}
	return cmd.Output()
}

// httpFetch is the production archive fetcher: HTTPS only, bounded body, and a
// stall watchdog instead of an absolute deadline.
func httpFetch(ctx context.Context, url string, dst io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return fmt.Errorf("building the archive request: %w", err)
	}
	client := &http.Client{
		Transport: &http.Transport{
			TLSHandshakeTimeout:   connectTimeout,
			ResponseHeaderTimeout: connectTimeout,
			ForceAttemptHTTP2:     true,
		},
	}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetching the archive: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("fetching the archive: unexpected status %s", resp.Status)
	}
	return copyWithStallGuard(cancel, dst, io.LimitReader(resp.Body, maxArchiveBytes), stallWindow)
}

// copyWithStallGuard streams src into dst, cancelling the transfer when no
// bytes arrive for window. It refuses an empty body: a zero-length "success"
// is a partial download, and the digest check would otherwise report it as a
// mismatch and hide the real cause.
func copyWithStallGuard(cancel context.CancelFunc, dst io.Writer, src io.Reader, window time.Duration) error {
	progress := make(chan struct{}, 1)
	done := make(chan struct{})
	stalled := make(chan struct{})
	go func() {
		timer := time.NewTimer(window)
		defer timer.Stop()
		for {
			select {
			case <-done:
				return
			case <-progress:
				timer.Reset(window)
			case <-timer.C:
				close(stalled)
				cancel()
				return
			}
		}
	}()

	written, err := io.Copy(dst, readerFunc(func(p []byte) (int, error) {
		n, rerr := src.Read(p)
		if n > 0 {
			select {
			case progress <- struct{}{}:
			default:
			}
		}
		return n, rerr
	}))
	close(done)

	select {
	case <-stalled:
		return fmt.Errorf("archive transfer made no progress for %s and was aborted", window)
	default:
	}
	if err != nil {
		return fmt.Errorf("reading the archive body: %w", err)
	}
	if written == 0 {
		return errors.New("archive body was empty (partial download?)")
	}
	return nil
}

// readerFunc adapts a function to io.Reader so the copy can observe progress
// without a bespoke type.
type readerFunc func(p []byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

// archTarget maps a GOARCH to the archive's architecture token.
func archTarget(goarch string) (string, error) {
	switch goarch {
	case "amd64":
		return archAMD64, nil
	case "arm64":
		return archARM64, nil
	}
	return "", fmt.Errorf("%w: %s", ErrUnsupportedArch, goarch)
}

// The two published architecture tokens.
const (
	archAMD64 = "x86_64-linux"
	archARM64 = "aarch64-linux"
)

// expectedDigest selects the pinned digest for cfg.Arch.
func expectedDigest(cfg *Config) (string, error) {
	switch cfg.Arch {
	case archAMD64:
		return cfg.SHA256, nil
	case archARM64:
		return cfg.SHA256ARM64, nil
	}
	return "", fmt.Errorf("%w: %s", ErrUnsupportedArch, cfg.Arch)
}

// archiveURL is the pinned archive's absolute URL. The version is pinned
// rather than /latest/ so a given image tag is reproducible and the digest
// check is meaningful.
func (m *Manager) archiveURL() string {
	return fmt.Sprintf("%s/%s/kirocli-%s.zip", m.cfg.BaseURL, m.cfg.Version, m.cfg.Arch)
}

// stageTree is one in-progress installation: a private HOME for the upstream
// installer and the version directory that will be renamed into place. Both
// live under the installation root so the publish stays a same-filesystem
// rename rather than a copy.
type stageTree struct {
	root       string
	home       string
	versionDir string
}

// install performs one complete installation attempt. On every failure path
// nothing outside the staging tree is left behind and the versions already on
// the volume keep serving.
//
// The archive is downloaded and verified into a CONTAINER-LOCAL temp dir, so
// on a digest mismatch nothing has been created under the installation root at
// all — not even an empty staging directory.
func (m *Manager) install(ctx context.Context) error {
	slog.Info("installing kiro-cli", "version", m.cfg.Version, "arch", m.cfg.Arch)
	slog.Info("kiro-cli is proprietary AWS Content; by installing you accept the AWS Customer Agreement",
		"license", "https://kiro.dev/license/")

	work, err := os.MkdirTemp("", "kirocli-*")
	if err != nil {
		return fmt.Errorf("creating the download temp dir: %w", err)
	}
	defer os.RemoveAll(work)

	archive := filepath.Join(work, "kirocli.zip")
	if derr := m.downloadArchive(ctx, archive); derr != nil {
		return derr
	}
	if xerr := extractZip(archive, work); xerr != nil {
		return xerr
	}

	stage, err := m.newStage()
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage.root)

	m.runUpstreamInstaller(ctx, work, stage.home)
	staged := filepath.Join(stage.home, ".local", "bin", mainBinary)
	if err := m.gateStaged(ctx, staged); err != nil {
		return err
	}
	if err := m.assemble(stage); err != nil {
		return err
	}
	if err := m.publish(stage); err != nil {
		return err
	}
	m.mu.Lock()
	m.installed[m.cfg.Version] = true
	m.mu.Unlock()
	slog.Info("kiro-cli installed", "version", m.cfg.Version, "dir", m.versionDir(m.cfg.Version))
	return nil
}

// downloadArchive fetches the pinned archive and proves it is the artifact the
// pin names. Verification is the whole point: nothing downstream re-checks the
// digest, and nothing is placed on the persistent volume until it passes.
func (m *Manager) downloadArchive(ctx context.Context, dst string) error {
	want, err := expectedDigest(&m.cfg)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fileMode)
	if err != nil {
		return fmt.Errorf("creating the archive file: %w", err)
	}
	sum := sha256.New()
	// Digest the bytes as they land, so the ~528 MB archive is read once.
	fetchErr := m.fetch(ctx, m.archiveURL(), io.MultiWriter(f, sum))
	closeErr := f.Close()
	switch {
	case fetchErr != nil:
		return fetchErr
	case closeErr != nil:
		return fmt.Errorf("closing the archive file: %w", closeErr)
	}
	got := hex.EncodeToString(sum.Sum(nil))
	if got != want {
		return fmt.Errorf("%w: arch=%s expected=%s actual=%s (bump the version and BOTH digest literals together)",
			ErrDigestMismatch, m.cfg.Arch, want, got)
	}
	slog.Info("kiro-cli archive SHA-256 verified against the pinned digest", "arch", m.cfg.Arch, "sha256", got)
	return nil
}

// extractZip unpacks archive into dir with traversal, count and size guards. It
// uses archive/zip rather than shelling out to unzip so the guards are ours.
func extractZip(archive, dir string) error {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("opening the archive: %w", err)
	}
	defer r.Close()
	if len(r.File) > maxExtractEntries {
		return fmt.Errorf("archive holds %d entries, over the %d limit", len(r.File), maxExtractEntries)
	}
	var total int64
	for _, entry := range r.File {
		dst, joinErr := safeJoin(dir, entry.Name)
		if joinErr != nil {
			return joinErr
		}
		if entry.FileInfo().IsDir() {
			if mkErr := os.MkdirAll(dst, dirMode); mkErr != nil {
				return fmt.Errorf("creating %s: %w", entry.Name, mkErr)
			}
			continue
		}
		if mkErr := os.MkdirAll(filepath.Dir(dst), dirMode); mkErr != nil {
			return fmt.Errorf("creating the parent of %s: %w", entry.Name, mkErr)
		}
		written, exErr := extractFile(entry, dst, maxExtractBytes-total)
		if exErr != nil {
			return exErr
		}
		total += written
	}
	return nil
}

// extractFile writes one archive entry, refusing to exceed budget bytes.
func extractFile(entry *zip.File, dst string, budget int64) (int64, error) {
	if budget <= 0 {
		return 0, fmt.Errorf("archive exceeds the %d byte extraction limit", maxExtractBytes)
	}
	src, err := entry.Open()
	if err != nil {
		return 0, fmt.Errorf("opening %s in the archive: %w", entry.Name, err)
	}
	defer src.Close()
	// Keep the executable bit (install.sh must run) but never anything wider
	// than owner-write: the tree lands on the persistent volume.
	mode := entry.Mode().Perm() & 0o755
	if mode&0o100 != 0 {
		mode |= 0o700
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return 0, fmt.Errorf("creating %s: %w", entry.Name, err)
	}
	written, copyErr := io.Copy(out, io.LimitReader(src, budget+1))
	closeErr := out.Close()
	switch {
	case copyErr != nil:
		return written, fmt.Errorf("extracting %s: %w", entry.Name, copyErr)
	case closeErr != nil:
		return written, fmt.Errorf("closing %s: %w", entry.Name, closeErr)
	case written > budget:
		return written, fmt.Errorf("archive exceeds the %d byte extraction limit", maxExtractBytes)
	}
	return written, nil
}

// safeJoin resolves name under dir. It REFUSES an absolute path or any
// traversal rather than sanitizing one: a legitimate archive has no such
// entries, so quietly rewriting `../../x` into `x` would hide a hostile
// archive instead of reporting it.
func safeJoin(dir, name string) (string, error) {
	if name == "" {
		return "", errors.New("archive holds an entry with an empty name")
	}
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("archive entry %q is an absolute path", name)
	}
	clean := filepath.Clean(name)
	if clean == "." {
		// Some archives carry an explicit "./" root entry; it resolves to the
		// extraction directory itself, which already exists.
		return dir, nil
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry %q escapes the extraction directory", name)
	}
	dst := filepath.Join(dir, clean)
	rel, err := filepath.Rel(dir, dst)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry %q escapes the extraction directory", name)
	}
	return dst, nil
}

// newStage creates the staging tree under the installation root.
func (m *Manager) newStage() (*stageTree, error) {
	if err := os.MkdirAll(m.versionsRoot(), dirMode); err != nil {
		return nil, fmt.Errorf("creating the kiro-cli installation root: %w", err)
	}
	root, err := os.MkdirTemp(m.versionsRoot(), stagePrefix+"*")
	if err != nil {
		return nil, fmt.Errorf("creating the staging tree: %w", err)
	}
	stage := &stageTree{
		root:       root,
		home:       filepath.Join(root, "home"),
		versionDir: filepath.Join(root, "v"),
	}
	if err := os.MkdirAll(stage.home, dirMode); err != nil {
		return nil, fmt.Errorf("creating the staging HOME: %w", err)
	}
	return stage, nil
}

// runUpstreamInstaller runs the archive's install.sh against the PRIVATE
// staging HOME, so the candidate it drops in $HOME/.local/bin never lands on
// PATH or on the real home before it is verified.
//
// Its exit code is deliberately ignored: the upstream installer touches shell
// profiles and other surfaces that legitimately fail in a minimal root
// container. What matters is whether the binary it produced passes gateStaged.
func (m *Manager) runUpstreamInstaller(ctx context.Context, work, stageHome string) {
	out, err := m.run(ctx, &command{
		Env:           []string{"HOME=" + stageHome},
		Args:          []string{"--no-confirm"},
		Path:          filepath.Join(work, "kirocli", "install.sh"),
		Timeout:       installerTimeout,
		CaptureStderr: true,
	})
	if err == nil {
		return
	}
	slog.Warn("the upstream kiro-cli install.sh reported a failure; continuing to the staged-binary gates, which decide",
		"error", err, "output", truncate(string(out), 2000))
}

// gateStaged refuses a staged candidate that is not the pinned, self-contained
// binary, and refuses one whose self-update could not be switched off.
func (m *Manager) gateStaged(ctx context.Context, staged string) error {
	if !selfContained(staged) {
		return fmt.Errorf("install.sh produced no self-contained executable at %s (absent, not executable, or a symlink whose target dies with the staging cleanup)", staged)
	}
	got, err := m.probeVersion(ctx, staged)
	if err != nil {
		return fmt.Errorf("probing the staged kiro-cli: %w", err)
	}
	if got != m.cfg.Version {
		return fmt.Errorf("staged kiro-cli reports version %q, want %q", got, m.cfg.Version)
	}
	// The pin-enforcing settings are asserted against the STAGED binary
	// before publication, so a candidate whose auto-update could not be
	// turned off never becomes a version directory. This is not a substitute
	// for the every-call reassertion in finish: the setting lives in the
	// mutable Kiro home, so it has to be re-proved on every boot too.
	return m.applyRequiredSettings(ctx, staged)
}

// applyRequiredSettings asserts only the required settings, for the staged-gate
// path where the best-effort preferences are applied later against the
// published binary.
func (m *Manager) applyRequiredSettings(ctx context.Context, bin string) error {
	for _, s := range m.cfg.Settings {
		if !s.Required {
			continue
		}
		if _, err := m.run(ctx, &command{
			Path:    bin,
			Args:    []string{"settings", s.Key, s.Value},
			Timeout: settingTimeout,
		}); err != nil {
			return fmt.Errorf("refusing to publish a kiro-cli whose %s could not be set (it may replace itself and invalidate the pinned digest): %w", s.Key, err)
		}
	}
	return nil
}

// assemble moves the dispatchers out of the staging HOME into the staged
// version directory and makes the result DURABLE, in the order finding 2
// requires: every file is written and Synced, then the `.complete` sentinel is
// written and Synced LAST, then the directory itself is Synced. Only after
// that may publish rename it into place.
//
// Atomic visibility is not crash durability: a successful rename proves no
// concurrent lookup sees a half-populated directory, it does not prove the new
// directory entry or the file data reached stable storage. Any sync failure —
// ENOSPC included — fails the install, which leaves every complete version
// already on the volume untouched.
func (m *Manager) assemble(stage *stageTree) error {
	src := filepath.Join(stage.home, ".local", "bin")
	if err := os.MkdirAll(stage.versionDir, dirMode); err != nil {
		return fmt.Errorf("creating the staged version directory: %w", err)
	}
	moved := make([]string, 0, len(m.cfg.Required)+len(m.cfg.Optional))
	for _, name := range m.cfg.Required {
		dst, err := m.moveDispatcher(src, stage.versionDir, name)
		if err != nil {
			return fmt.Errorf("required kiro-cli dispatcher %s: %w", name, err)
		}
		moved = append(moved, dst)
	}
	for _, name := range m.cfg.Optional {
		dst, err := m.moveDispatcher(src, stage.versionDir, name)
		if err != nil {
			slog.Warn("optional kiro-cli dispatcher not installed", "dispatcher", name, "error", err)
			continue
		}
		moved = append(moved, dst)
	}
	for _, path := range moved {
		if err := m.fsync(path); err != nil {
			return fmt.Errorf("syncing %s: %w", filepath.Base(path), err)
		}
	}
	if err := m.writeSentinel(stage.versionDir); err != nil {
		return err
	}
	if err := m.fsync(stage.versionDir); err != nil {
		return fmt.Errorf("syncing the staged version directory: %w", err)
	}
	return nil
}

// moveDispatcher moves one dispatcher from the staging HOME into dst, refusing
// anything that is not a self-contained executable: a symlink would pass -f/-x
// and then dangle the moment the staging tree is removed.
func (m *Manager) moveDispatcher(src, dst, name string) (string, error) {
	from := filepath.Join(src, name)
	if !selfContained(from) {
		return "", errors.New("absent, not executable, or a symlink into the staging tree")
	}
	to := filepath.Join(dst, name)
	if err := m.rename(from, to); err != nil {
		return "", err
	}
	return to, nil
}

// writeSentinel writes the `.complete` marker LAST, holding the version whose
// full dispatcher set the directory contains. It lives inside the directory it
// describes, so it cannot drift from those binaries.
func (m *Manager) writeSentinel(dir string) error {
	path := filepath.Join(dir, sentinelName)
	if err := os.WriteFile(path, []byte(m.cfg.Version+"\n"), fileMode); err != nil {
		return fmt.Errorf("writing the completion sentinel: %w", err)
	}
	if err := m.fsync(path); err != nil {
		return fmt.Errorf("syncing the completion sentinel: %w", err)
	}
	return nil
}

// publish renames the staged version directory to its final name and syncs the
// parent, completing the durability protocol. Pruning may only run after this
// returns nil.
func (m *Manager) publish(stage *stageTree) error {
	dst := m.versionDir(m.cfg.Version)
	// The destination can exist here for exactly one reason: the pinned
	// directory was rejected by the version probe (a tampered binary under an
	// intact sentinel, finding 4) and is being replaced. It is untrusted, so
	// removing it is correct, and the retained predecessor is what covers the
	// crash window between the remove and the rename.
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("clearing the previous %s directory: %w", m.cfg.Version, err)
	}
	if err := m.rename(stage.versionDir, dst); err != nil {
		return fmt.Errorf("publishing the version directory: %w", err)
	}
	if err := m.fsync(m.versionsRoot()); err != nil {
		return fmt.Errorf("syncing the kiro-cli installation root: %w", err)
	}
	return nil
}

// writeFileDurably writes data to path through a temp file in the same
// directory: write, sync, rename, sync the parent.
func (m *Manager) writeFileDurably(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp := filepath.Join(dir, "."+filepath.Base(path)+".tmp")
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := m.fsync(tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := m.rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return m.fsync(dir)
}

// fsyncPath commits the file or directory at path to stable storage. fsync on
// a read-only descriptor is valid on Linux, so one helper covers both.
func fsyncPath(path string) error {
	f, err := os.Open(path) // #nosec G304 -- path is always built from ToolsDir and this package's own constants.
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return fmt.Errorf("fsync %s: %w", path, err)
	}
	return nil
}

// truncate bounds a diagnostic string so a runaway installer log cannot flood
// the log pipeline.
func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "...(truncated)"
}
