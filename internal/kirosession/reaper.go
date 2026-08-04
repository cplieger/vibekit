// Package kirosession reaps kiro-cli / KAS on-disk session state that
// vibekit no longer needs.
//
// On v3, KAS persists per-session state under $KIRO_HOME/sessions so a chat
// can be resumed via session/load across container restarts. That state's
// ONLY purpose is to reload a chat vibekit still keeps — so it must die with
// the chat. vibekit owns this end to end: a session is reaped promptly when its
// chat is deleted, when an archived chat is purged, and a periodic orphan sweep
// removes crash residue and pre-v3 files.
//
// vibekit is the ONLY retention authority, but not for the reason previously
// recorded here. This comment used to say kiro-cli's `cleanup.periodDays` is
// "pinned to 0/never"; that key has ZERO occurrences in both the KAS bundle and
// the kiro-cli binary, and `kiro-cli settings` accepts unknown keys, so
// vibekit's 0 is stored and ignored. The pin is documentation of intent, not a
// mechanism. The real reason is that KAS's one self-initiated delete path —
// `sessionEviction`, an LRU sweep once total session bytes exceed a 500 MB
// default budget, newest 5 preserved, checked once per process at newSession —
// DEFAULTS TO DISABLED and neither layer enables it. If it were ever enabled it
// would implement a different policy from this one and delete live chains
// silently, which is why the boot conformance check asserts it stays absent.
//
// On-disk layout (verified against KAS 2.12):
//
//	$KIRO_HOME/sessions/<workspace-hash>/sess_<id>/   — the KAS session dir
//	$KIRO_HOME/sessions/cli/sess_<id>.history         — its history sidecar
//	$KIRO_HOME/sessions/cli/<uuid>.{json,jsonl,lock}  — dead v2-engine files
//
// The workspace-hash dir is not tracked by vibekit, so a single session is
// located by globbing sessions/*/sess_<id>.
package kirosession

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// sessionPrefix is the v3 KAS session-id prefix. Anything without it is
// either empty/garbage (never reaped by id) or a dead v2-engine file
// (reaped by the sweep purely on age).
const sessionPrefix = "sess_"

// defaultGuard spares session state younger than this from the orphan
// sweep. It covers the create race: KAS writes the session dir a moment
// before vibekit persists the chat file that references it, so a brand-new
// session can momentarily look unreferenced. Direct Reap (on delete) is not
// subject to the guard — there the caller knows the chat is gone.
const defaultGuard = 10 * time.Minute

// Reaper removes on-disk KAS session state under sessionsDir.
type Reaper struct {
	sessionsDir string
	guard       time.Duration
}

// New returns a Reaper rooted at sessionsDir (typically
// filepath.Join(workspace.KiroHome(), "sessions")).
func New(sessionsDir string) *Reaper {
	return &Reaper{sessionsDir: sessionsDir, guard: defaultGuard}
}

// Reap removes all on-disk state for a single session id, immediately and
// regardless of age. Used on chat delete, where the caller already knows the
// chat (and therefore its session) is gone. A no-op for an empty or
// malformed id, or when the reaper is nil.
func (r *Reaper) Reap(sessionID string) {
	if r == nil || !validSessionID(sessionID) {
		return
	}
	// Session dir under an untracked per-workspace-hash subdir: glob for it.
	dirs, _ := filepath.Glob(filepath.Join(r.sessionsDir, "*", sessionID))
	for _, d := range dirs {
		if err := os.RemoveAll(d); err != nil {
			slog.Warn("kirosession: remove session dir", "session", sessionID, "path", d, "error", err)
		}
	}
	r.removeCLISidecars(sessionID)
}

// Sweep removes orphaned session state: any session dir or cli sidecar whose
// id is not in referenced and is older than the guard window, plus dead
// v2-engine files (bare-uuid, no sess_ prefix) older than the guard. Returns
// the number of sessions reaped. A no-op when the reaper is nil or the sessions
// dir is absent.
//
// `referenced` must be the COMPLETE keep-list — every session in every active
// and archived chat's chain, unioned with every live bridge's session. Age is
// not evidence that a session is disposable: the guard below is a create-race
// cushion, not a liveness test. Passing a partial set deletes user history, so
// the caller skips the sweep rather than narrowing the set (hub.sweepSessionsOnce).
func (r *Reaper) Sweep(referenced map[string]struct{}) int {
	if r == nil {
		return 0
	}
	cutoff := time.Now().Add(-r.guard)
	reaped := r.sweepSessionDirs(referenced, cutoff)
	reaped += r.sweepCLI(referenced, cutoff)
	if reaped > 0 {
		slog.Info("kirosession: orphan sweep reaped sessions", "count", reaped)
	}
	return reaped
}

// sweepSessionDirs reaps orphaned sessions/<hash>/sess_*/ directories.
func (r *Reaper) sweepSessionDirs(referenced map[string]struct{}, cutoff time.Time) int {
	hashDirs, err := os.ReadDir(r.sessionsDir)
	if err != nil {
		return 0
	}
	reaped := 0
	for _, hd := range hashDirs {
		if !hd.IsDir() || hd.Name() == "cli" {
			continue // "cli" is handled by sweepCLI; skip non-dirs
		}
		reaped += r.sweepHashDir(filepath.Join(r.sessionsDir, hd.Name()), referenced, cutoff)
	}
	return reaped
}

// sweepHashDir reaps orphaned session dirs within one per-workspace-hash dir.
func (r *Reaper) sweepHashDir(sub string, referenced map[string]struct{}, cutoff time.Time) int {
	sessDirs, err := os.ReadDir(sub)
	if err != nil {
		return 0
	}
	reaped := 0
	for _, sd := range sessDirs {
		if r.reapOrphanSessionDir(sub, sd, referenced, cutoff) {
			reaped++
		}
	}
	return reaped
}

// reapOrphanSessionDir removes one session dir when it is a sess_* directory,
// unreferenced, and older than the guard. Reports whether it reaped.
func (r *Reaper) reapOrphanSessionDir(sub string, sd os.DirEntry, referenced map[string]struct{}, cutoff time.Time) bool {
	name := sd.Name()
	if !sd.IsDir() || !strings.HasPrefix(name, sessionPrefix) {
		return false
	}
	if _, keep := referenced[name]; keep {
		return false
	}
	info, err := sd.Info()
	if err != nil || info.ModTime().After(cutoff) {
		return false // spare young orphans (create race)
	}
	path := filepath.Join(sub, name)
	if err := os.RemoveAll(path); err != nil {
		slog.Warn("kirosession sweep: remove session dir", "path", path, "error", err)
		return false
	}
	r.removeCLISidecars(name)
	return true
}

// sweepCLI reaps orphaned/dead files under sessions/cli/: v3 sess_<id>.*
// sidecars whose id is unreferenced, and dead v2-engine files (no sess_
// prefix), both gated on the guard window.
func (r *Reaper) sweepCLI(referenced map[string]struct{}, cutoff time.Time) int {
	cliDir := filepath.Join(r.sessionsDir, "cli")
	entries, err := os.ReadDir(cliDir)
	if err != nil {
		return 0
	}
	reaped := 0
	for _, e := range entries {
		if r.reapOrphanCLIFile(cliDir, e, referenced, cutoff) {
			reaped++
		}
	}
	return reaped
}

// reapOrphanCLIFile removes one sessions/cli/ file when it is an
// unreferenced v3 sidecar or a dead v2-engine file, older than the guard.
// Reports whether a v3 SESSION was reaped (dead v2 files are incidental and
// not counted).
func (r *Reaper) reapOrphanCLIFile(cliDir string, e os.DirEntry, referenced map[string]struct{}, cutoff time.Time) bool {
	if e.IsDir() {
		return false
	}
	name := e.Name()
	isV3 := strings.HasPrefix(name, sessionPrefix)
	if isV3 {
		id, _, _ := strings.Cut(name, ".") // "sess_<id>.history" → "sess_<id>"; uuids carry no dots
		if _, keep := referenced[id]; keep {
			return false // live v3 session
		}
	}
	info, err := e.Info()
	if err != nil || info.ModTime().After(cutoff) {
		return false
	}
	if err := os.Remove(filepath.Join(cliDir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("kirosession sweep: remove cli file", "file", name, "error", err)
		return false
	}
	return isV3
}

// removeCLISidecars removes every sessions/cli/<sessionID>.* file for a
// session being reaped.
func (r *Reaper) removeCLISidecars(sessionID string) {
	sidecars, _ := filepath.Glob(filepath.Join(r.sessionsDir, "cli", sessionID+".*"))
	for _, s := range sidecars {
		if err := os.Remove(s); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("kirosession: remove cli sidecar", "session", sessionID, "path", s, "error", err)
		}
	}
}

// validSessionID reports whether id is a well-formed KAS session id safe to
// use in a filesystem glob: the sess_ prefix followed by one or more
// id-safe chars (hex, dashes, underscores — no glob metacharacters, no path
// separators, no dots).
func validSessionID(id string) bool {
	rest, ok := strings.CutPrefix(id, sessionPrefix)
	if !ok || rest == "" {
		return false
	}
	for _, c := range rest {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}
