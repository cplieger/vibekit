package kirocli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// selfContained reports whether path is a dispatcher that keeps working after
// the staging tree is removed: a REGULAR executable file, not a symlink.
// os.Stat follows links and a bare executable check is also true for a
// directory, so both are checked explicitly.
func selfContained(path string) bool {
	fi, err := os.Lstat(path)
	if err != nil {
		return false
	}
	return fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

// probeVersion asks a binary for its version, bounded. Only the LAST FIELD of
// the FIRST LINE is taken, matching the shell helper this replaces, so extra
// banner lines or trailing output cannot change the answer.
func (m *Manager) probeVersion(ctx context.Context, bin string) (string, error) {
	out, err := m.run(ctx, &command{
		Path:    bin,
		Args:    []string{"--version"},
		Timeout: probeTimeout,
	})
	if err != nil {
		return "", err
	}
	version := parseVersion(string(out))
	if version == "" {
		return "", fmt.Errorf("kiro-cli --version produced no parseable version at %s", bin)
	}
	return version, nil
}

// parseVersion extracts the version from `kiro-cli --version` output: the last
// whitespace-separated field of the first line. It never panics on arbitrary
// input and returns "" when there is nothing to take.
func parseVersion(out string) string {
	line, _, _ := strings.Cut(out, "\n")
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

// versionDirComplete reports whether a version directory is a completed
// install: the sentinel is a plain file naming exactly this version, and every
// required dispatcher inside is a self-contained executable.
//
// A directory populated file-by-file at its final name is exactly the mixed-set
// bug the old promotion journal existed for, so the sentinel is what separates
// an interrupted staging tree from a finished install — and it is checked
// against the directory's own name, not against the pin, so a retained
// predecessor still reads as complete.
func (m *Manager) versionDirComplete(version string) bool {
	dir := m.versionDir(version)
	sentinel := filepath.Join(dir, sentinelName)
	fi, err := os.Lstat(sentinel)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	// #nosec G304 -- sentinel is built from ToolsDir plus a directory entry
	// name, and it was just proved to be a regular file rather than a link.
	raw, err := os.ReadFile(sentinel)
	if err != nil || strings.TrimSpace(string(raw)) != version {
		return false
	}
	for _, name := range m.cfg.Required {
		if !selfContained(filepath.Join(dir, name)) {
			return false
		}
	}
	return true
}

// completeVersions lists the completed version directories, newest first.
func (m *Manager) completeVersions() []string {
	entries, err := os.ReadDir(m.versionsRoot())
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		// Dot-prefixed entries are staging trees, never versions.
		if !e.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		if !m.versionDirComplete(name) {
			continue
		}
		out = append(out, name)
	}
	sortVersionsDesc(out)
	return out
}

// trusted reports whether a version directory may be activated. When the
// entrypoint reported that the tools tree was writable by someone else, a
// sentinel proves nothing (it is trivially forgeable, unlike a digest), so only
// a version THIS process installed from a verified archive qualifies.
func (m *Manager) trusted(version string) bool {
	if !m.cfg.Tainted {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.installed[version]
}

// selectActive picks the version to run: the pin when it is activatable, else
// the newest other complete version, else none.
//
// Directory name plus sentinel is NOT proof (finding 4). Before a candidate is
// accepted its main dispatcher is probed and must answer with the version its
// own directory claims. A mismatch — a binary replaced on the volume while the
// sentinel stayed intact — excludes that directory, falls through to another
// complete version if one exists, and leaves the pin unsatisfied so the caller
// reinstalls.
func (m *Manager) selectActive(ctx context.Context) (selection, bool) {
	for _, version := range preferPin(m.completeVersions(), m.cfg.Version) {
		if !m.trusted(version) {
			slog.Warn("ignoring an existing kiro-cli version directory because the tools tree was writable by others; only a freshly verified install may be activated",
				"version", version)
			continue
		}
		dir := m.versionDir(version)
		bin := filepath.Join(dir, mainBinary)
		got, err := m.probeVersion(ctx, bin)
		if err != nil {
			slog.Warn("excluding a kiro-cli version directory: its binary did not answer --version",
				"version", version, "path", bin, "error", err)
			continue
		}
		if got != version {
			slog.Error("excluding a kiro-cli version directory: its binary reports a different version than the directory and sentinel claim, so the install was tampered with or replaced",
				"version", version, "reported", got, "path", bin, "error", ErrVersionMismatch)
			continue
		}
		return selection{version: version, dir: dir, bin: bin}, true
	}
	return selection{}, false
}

// preferPin returns versions with pin first (when present) and the rest in the
// given order, so the pinned version is always tried before any fallback.
func preferPin(versions []string, pin string) []string {
	out := make([]string, 0, len(versions))
	for _, v := range versions {
		if v == pin {
			out = append(out, v)
		}
	}
	for _, v := range versions {
		if v != pin {
			out = append(out, v)
		}
	}
	return out
}

// sortVersionsDesc orders versions newest first, comparing numeric segments
// numerically so 2.14.2 sorts above 2.9.0.
func sortVersionsDesc(versions []string) {
	sort.Slice(versions, func(i, j int) bool {
		return compareVersions(versions[i], versions[j]) > 0
	})
}

// compareVersions returns >0 when a is newer than b, <0 when older, 0 when
// equal. Segments that are not numeric fall back to a string comparison, so an
// unexpected directory name still orders deterministically.
func compareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(as), len(bs)) {
		// A missing segment counts as zero, so 2.14 and 2.14.0 are the same
		// version rather than ordering by string.
		av, bv := "0", "0"
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		an, aerr := strconv.Atoi(av)
		bn, berr := strconv.Atoi(bv)
		if aerr == nil && berr == nil {
			if an != bn {
				return an - bn
			}
			continue
		}
		if av != bv {
			return strings.Compare(av, bv)
		}
	}
	return 0
}

// retainedVersions names the version directories pruning must KEEP: the active
// version and its immediate predecessor (the newest complete version below it),
// and nothing else. This is ruling 3 of the decisions addendum, and it is the
// reason no rollback journal is needed — the predecessor survives every switch,
// so a bad activation is recoverable by selecting it.
//
// Why per-version process leases are NOT required: a new pin arrives only by
// image rebuild, which recreates the container and therefore kills every live
// chat bridge, so no bridge can outlive two version changes and no running
// process can still hold a reference into a directory this function drops. If
// in-process LIVE upgrades are ever enabled — a new pin reaching a running
// container without a recreate — that argument dies: a bridge started on
// version A could still need to reach a sidecar out of A's directory, and
// pruning would then have to take a per-version lease from every live bridge
// and remove a directory only at zero leases. Revisit this function first if
// that changes.
func retainedVersions(complete []string, active string) []string {
	if active == "" {
		return nil
	}
	keep := []string{active}
	predecessor := ""
	for _, v := range complete {
		if compareVersions(v, active) >= 0 {
			continue
		}
		if predecessor == "" || compareVersions(v, predecessor) > 0 {
			predecessor = v
		}
	}
	if predecessor != "" {
		keep = append(keep, predecessor)
	}
	return keep
}

// versionsToPrune returns the complete version directories that are neither
// active nor its immediate predecessor. Anything newer than the active version
// is also pruned: reaching that state means the pin moved DOWN, and a stale
// higher version is not a fallback the pin wants kept.
func versionsToPrune(complete []string, active string) []string {
	if active == "" {
		return nil
	}
	keep := map[string]bool{}
	for _, v := range retainedVersions(complete, active) {
		keep[v] = true
	}
	out := make([]string, 0, len(complete))
	for _, v := range complete {
		if !keep[v] {
			out = append(out, v)
		}
	}
	return out
}

// pruneSuperseded removes every complete version directory that is neither the
// active one nor its immediate predecessor, then syncs the installation root
// again so the removals are durable (finding 2). Failures warn: disk hygiene
// must not brick a boot.
func (m *Manager) pruneSuperseded(active string) {
	victims := versionsToPrune(m.completeVersions(), active)
	if len(victims) == 0 {
		return
	}
	removed := m.removeUnderRoot(victims)
	if removed == 0 {
		return
	}
	slog.Info("pruned superseded kiro-cli versions", "removed", removed, "active", active)
	if err := m.fsync(m.versionsRoot()); err != nil {
		slog.Warn("failed to sync the kiro-cli installation root after pruning", "error", err)
	}
}

// prunePartials removes incomplete version directories and orphan staging
// trees. It runs before selection so a partial directory is never a candidate,
// and it runs while no staging tree of this manager exists — Ensure holds opMu
// and creates its stage later.
//
// Taint is deliberately NOT a delete trigger here: a foreign-writable tree
// invalidates a directory for ACTIVATION (see trusted), and turning that flag
// into a mass delete would throw away the fallback set invariant 6 depends
// on.
func (m *Manager) prunePartials() {
	entries, err := os.ReadDir(m.versionsRoot())
	if err != nil {
		return
	}
	victims := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		switch {
		case strings.HasPrefix(name, stagePrefix):
			victims = append(victims, name)
		case !e.IsDir(), strings.HasPrefix(name, "."):
			continue
		case !m.versionDirComplete(name):
			victims = append(victims, name)
		}
	}
	if removed := m.removeUnderRoot(victims); removed > 0 {
		slog.Info("removed incomplete kiro-cli install directories", "removed", removed)
	}
}

// removeUnderRoot deletes the named entries through an os.Root confined to the
// installation root, so a symlinked or otherwise redirected entry cannot make
// a delete escape the tree. It returns how many were removed.
func (m *Manager) removeUnderRoot(names []string) int {
	if len(names) == 0 {
		return 0
	}
	root, err := os.OpenRoot(m.versionsRoot())
	if err != nil {
		slog.Warn("failed to open the kiro-cli installation root for pruning", "error", err)
		return 0
	}
	defer root.Close()
	removed := 0
	for _, name := range names {
		if err := root.RemoveAll(name); err != nil {
			slog.Warn("failed to remove a kiro-cli install directory", "entry", name, "error", err)
			continue
		}
		removed++
	}
	return removed
}
