package kirocli

import (
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// The legacy layout this purge deletes is exactly what vibekit's shell
// installer produced: the promoted dispatchers in $TOOLS/bin (`kiro-cli` plus
// the two sidecars that pin carried) and the orphan staging trees its EXIT trap
// missed on a SIGKILL.
//
// There is no journal, no `.prev` backup, no `.absent` tombstone and no
// install/readiness marker in the list, and that is not an omission: vibekit
// never had them. Its promotion was single-commit-point by design (the main
// binary renamed LAST, the drift check keyed on its version), so the in-place
// transaction web-terminal-kiro needed — and the artifacts it left on a volume —
// never existed here.
//
// Deleting the rest outright is the decisions addendum's ruling 1: there is no
// backwards compatibility to keep, so an inherited layout is resolved by
// DELETION rather than by a decoder or a legacy ready-fallback. Nothing in this
// package reads any of these paths, so once they are gone they cannot influence
// a readiness or integrity decision.
//
// $HOME/.local/bin/kiro-cli* is deliberately NOT purged here. It is outside
// $TOOLS — this package's filesystem root — and the entrypoint still sweeps it,
// where the real HOME is already resolved.

// legacyTarget is one artifact the migration sweep may remove: its path
// relative to the tools dir, and the file SHAPE the shell installer left there.
//
// The shape is a gate, not decoration. $TOOLS/bin is co-owned by the toolbelt
// engine, which publishes a SYMLINK at bin/<tool> into its own
// opt/<tool>/<version>/ tree, and its tool names are unconstrained enough to
// collide with every name in this file (its validator accepts `kiro-cli`). The
// shell installer, by contrast, only ever `mv`'d real binaries into that
// directory, and only ever `mktemp -d`'d its staging trees — so "regular file"
// and "directory" are exactly what this sweep is entitled to delete, and
// anything else at the same path belongs to someone else.
type legacyTarget struct {
	// path is relative to the tools dir.
	path string
	// dir marks a target the shell installer created as a directory; every
	// other target must be a regular file.
	dir bool
}

// legacyDispatcherNames are the ONLY dispatcher names the shell installer ever
// promoted into $TOOLS/bin, taken from the promotion sequence itself (the main
// binary plus the chat and term sidecars it moved out of $HOME/.local/bin). It
// replaces a `bin/kiro-cli*` prefix sweep.
//
// The prefix sweep was unsound in the co-owned bin directory: it ran
// unconditionally on every boot and deleted EVERY matching entry, so a
// toolbelt-owned bin/kiro-cli symlink was unlinked while the engine's state row
// still claimed it. An unknown retired name is now left in place — inert,
// dot-free residue costing disk — which is the correct trade against deleting
// another owner's live symlink.
var legacyDispatcherNames = []string{
	mainBinary,
	mainBinary + "-chat",
	mainBinary + "-term",
}

// legacyStagePrefix prefixed the shell installer's staging trees directly under
// the tools dir; the managed staging trees live under the installation root
// instead, so any of these are orphans.
const legacyStagePrefix = ".kiro-cli-stage."

// legacyPurgeMarker records, on the volume, that the one-time migration sweep
// completed. It is what makes the sweep run ONCE instead of on every boot: the
// layout it deletes cannot come back (no code writes it any more), so a second
// pass can only ever find artifacts someone ELSE put there.
//
// It is dot-prefixed and lives directly under the tools dir, where the toolbelt
// engine never looks (it enumerates only bin/, opt/, npm/ and python/) and
// where this file's own stage sweep (prefix ".kiro-cli-stage.") cannot match
// it.
const legacyPurgeMarker = ".kiro-cli-legacy-purged"

// purgeLegacyOnce runs the migration sweep at most once per process AND at most
// once per volume. The in-process latch protects the convenience symlink this
// manager publishes at one of the swept paths; the on-disk marker is what stops
// a boot that has nothing left to migrate from walking the co-owned bin
// directory at all.
func (m *Manager) purgeLegacyOnce() {
	m.mu.Lock()
	already := m.purged
	m.purged = true
	m.mu.Unlock()
	if already {
		return
	}
	if m.legacyPurgeRecorded() {
		slog.Debug("skipping the legacy kiro-cli migration sweep: it is already recorded as complete on this volume",
			"marker", legacyPurgeMarker)
		return
	}
	m.purgeLegacy()
}

// purgeLegacy deletes the legacy kiro-cli layout: the retired dispatchers the
// shell installer promoted into $TOOLS/bin and every orphan staging tree — and
// NOTHING else. Each target is removed only when what is on disk has the shape
// the shell installer left there (see legacyTarget); a mismatch is another
// owner's file and is refused.
//
// It is idempotent and interruption-safe by construction: each step is an
// independent RemoveAll of a path that nothing reads afterwards, so a kill
// leaves a prefix done and the next boot repeats the sequence, and on an
// already-purged volume every step is a no-op. Failures warn and continue — an
// undeletable artifact is inert, and bricking boot over disk hygiene is exactly
// what invariant 6 forbids — but they also withhold the completion marker, so
// the next boot retries instead of recording a job it did not finish.
func (m *Manager) purgeLegacy() {
	root, err := os.OpenRoot(m.cfg.ToolsDir)
	if err != nil {
		// A missing tools dir means there is nothing to purge; anything else
		// is worth a line.
		if !os.IsNotExist(err) {
			slog.Warn("failed to open the tools dir to purge the legacy kiro-cli layout", "error", err)
		}
		return
	}
	defer root.Close()

	targets := make([]legacyTarget, 0, len(legacyDispatcherNames)+8)
	targets = append(targets, legacyDispatchers()...)
	targets = append(targets, legacyStages(root)...)

	removed, failed := 0, 0
	for _, t := range targets {
		present, matches := legacyShapeMatches(root, t)
		if !present {
			continue
		}
		if !matches {
			slog.Warn("refusing to remove an entry the legacy kiro-cli layout never had this shape at; it belongs to another owner of this tree (the toolbelt engine publishes symlinks into the shared bin dir) and its owner's state still claims it",
				"entry", t.path, "want_shape", shapeName(t.dir))
			continue
		}
		// RemoveAll through the root confines the delete to the tools tree: a
		// symlinked artifact cannot redirect it at the credential-bearing home
		// next door.
		if err := root.RemoveAll(t.path); err != nil {
			failed++
			slog.Warn("failed to remove a legacy kiro-cli artifact; it is inert, so boot continues",
				"entry", t.path, "error", err)
			continue
		}
		removed++
	}
	if removed == 0 {
		slog.Debug("no legacy kiro-cli layout to purge")
	} else {
		slog.Info("purged the legacy kiro-cli layout; the pinned version is installed fresh into its own version directory, so this boot downloads the archive once",
			"removed", removed)
	}
	if failed > 0 {
		slog.Warn("not recording the legacy kiro-cli migration as complete: some artifacts could not be removed, so the next boot retries the sweep",
			"failed", failed)
		return
	}
	m.recordLegacyPurge()
}

// legacyShapeMatches reports whether anything is at t.path, and whether it has
// the shape the shell installer left there. Lstat, never Stat: a symlink at a
// swept path is precisely the foreign artifact this gate exists to spare, so it
// must not be resolved to whatever it points at.
func legacyShapeMatches(root *os.Root, t legacyTarget) (present, matches bool) {
	fi, err := root.Lstat(t.path)
	if err != nil {
		return false, false
	}
	if t.dir {
		return true, fi.IsDir()
	}
	return true, fi.Mode().IsRegular()
}

// shapeName names a target's expected shape for the refusal log line.
func shapeName(dir bool) string {
	if dir {
		return "directory"
	}
	return "regular file"
}

// legacyDispatchers returns the retired $TOOLS/bin dispatchers as targets. The
// set is fixed because the shell installer's promotion sequence was fixed;
// there is no scan, so nothing another owner added to the shared bin directory
// is ever considered.
func legacyDispatchers() []legacyTarget {
	out := make([]legacyTarget, 0, len(legacyDispatcherNames))
	for _, name := range legacyDispatcherNames {
		out = append(out, legacyTarget{path: binSubdir + "/" + name})
	}
	return out
}

// legacyStages lists orphan shell-era staging trees directly under the tools
// dir. The prefix is dot-leading and ends in a dot, so it cannot match the
// installation root (versionsSubdir) that now sits in the same directory, and
// it cannot match the completion marker either.
func legacyStages(root *os.Root) []legacyTarget {
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil
	}
	out := make([]legacyTarget, 0, len(entries))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), legacyStagePrefix) {
			out = append(out, legacyTarget{path: e.Name(), dir: true})
		}
	}
	return out
}

// legacyPurgeRecorded reports whether the completion marker is on the volume.
// A non-regular file at that path is NOT a marker: the sweep would rather run
// again (it is idempotent) than skip on evidence it did not write.
func (m *Manager) legacyPurgeRecorded() bool {
	fi, err := os.Lstat(filepath.Join(m.cfg.ToolsDir, legacyPurgeMarker))
	return err == nil && fi.Mode().IsRegular()
}

// recordLegacyPurge writes the completion marker durably. A failure only warns:
// the only consequence is that the next boot repeats a sweep which is by then a
// no-op on every path it looks at.
func (m *Manager) recordLegacyPurge() {
	path := filepath.Join(m.cfg.ToolsDir, legacyPurgeMarker)
	stamp := m.now().UTC().Format("2006-01-02T15:04:05Z") + "\n"
	if err := m.writeFileDurably(path, []byte(stamp), fileMode); err != nil {
		slog.Warn("failed to record that the legacy kiro-cli migration completed; the sweep repeats on the next boot, which is a no-op on a migrated volume",
			"path", path, "error", err)
		return
	}
	slog.Debug("recorded the legacy kiro-cli migration as complete", "path", path)
}

// publishConvenienceLink republishes $TOOLS/bin/kiro-cli as a symlink at the
// active version's binary, for `docker exec … kiro-cli --version` and the
// documented operator path (finding 8).
//
// It is explicitly NON-AUTHORITATIVE: nothing in this package reads it, no
// integrity or readiness decision consults it, and the product always runs the
// absolute version-directory path from CLIPath. Publication is atomic (write a
// temp name, rename over the old one) with the parent synced and the target
// validated, and every failure is a warning — a missing convenience pointer
// must never withhold readiness from a correctly installed CLI.
func (m *Manager) publishConvenienceLink(target string) {
	if !selfContained(target) {
		slog.Warn("not publishing the kiro-cli convenience symlink: the active binary is not a self-contained executable",
			"target", target)
		return
	}
	binDir := filepath.Join(m.cfg.ToolsDir, binSubdir)
	if err := os.MkdirAll(binDir, dirMode); err != nil {
		slog.Warn("failed to create the tools bin dir for the kiro-cli convenience symlink", "path", binDir, "error", err)
		return
	}
	link := filepath.Join(binDir, mainBinary)
	tmp := filepath.Join(binDir, "."+mainBinary+".newlink")
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		slog.Warn("failed to stage the kiro-cli convenience symlink", "path", tmp, "error", err)
		return
	}
	if err := m.rename(tmp, link); err != nil {
		_ = os.Remove(tmp)
		slog.Warn("failed to publish the kiro-cli convenience symlink; docker exec kiro-cli will not resolve, the product path is unaffected",
			"path", link, "error", err)
		return
	}
	if err := m.fsync(binDir); err != nil {
		slog.Warn("failed to sync the tools bin dir after publishing the kiro-cli convenience symlink", "error", err)
	}
	slog.Debug("published the kiro-cli convenience symlink", "path", link, "target", target)
}
