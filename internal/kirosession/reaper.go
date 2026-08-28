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
// located by globbing sessions/*/sess_<id>. That glob crosses every workspace
// bucket under one $KIRO_HOME, so the hash cannot be the only thing standing
// between a reap and another workspace's transcripts — see belongsToWorkspace.
package kirosession

import (
	"encoding/json"
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

// sessionRecordName is the per-session file KAS writes its own metadata into.
// Its workspacePaths list is the subject record every removal here is checked
// against.
const sessionRecordName = "session.json"

// Reaper removes on-disk KAS session state under sessionsDir, for the one
// workspace it is entitled to reap.
type Reaper struct {
	sessionsDir string
	// workspaceRoot is the workspace this reaper may delete sessions for. Every
	// removal reads the candidate's own workspacePaths and skips a session whose
	// list does not name it — see belongsToWorkspace for why doubt skips too.
	// There is no empty-root escape: an empty root names nothing, so it reaps
	// nothing, which is the same direction every other doubt takes here.
	workspaceRoot string
	guard         time.Duration
}

// New returns a Reaper rooted at sessionsDir (typically
// filepath.Join(workspace.KiroHome(), "sessions")) and confined to
// workspaceRoot (the bridge's own cwd).
func New(sessionsDir, workspaceRoot string) *Reaper {
	root := workspaceRoot
	if root != "" {
		root = filepath.Clean(root)
	}
	return &Reaper{sessionsDir: sessionsDir, workspaceRoot: root, guard: defaultGuard}
}

// belongsToWorkspace reports whether the session at sessionDir names this
// reaper's workspace root in its own workspacePaths.
//
// A MISMATCH is a skip, and so is DOUBT — an unreadable record, an absent or
// empty list, a decode error. That is this package's stated posture (see Sweep's
// empty-keep-list refusal) and it is the cheap side of an asymmetric trade: a
// wrong skip costs retained disk, a wrong removal costs somebody else's history.
// So a KAS version that stops writing workspacePaths reclaims nothing, and a
// multi-root or non-canonical entry is spared.
//
// It exists because the reap paths glob across every workspace-hash bucket under
// one $KIRO_HOME and never read the hash, so no collision is required for a reap
// to cross workspaces — a `docker exec kiro-cli` at another cwd is enough.
func (r *Reaper) belongsToWorkspace(sessionDir string) bool {
	data, err := os.ReadFile(filepath.Join(sessionDir, sessionRecordName))
	if err != nil {
		return false
	}
	var rec struct {
		WorkspacePaths []string `json:"workspacePaths"`
	}
	if json.Unmarshal(data, &rec) != nil {
		return false
	}
	for _, p := range rec.WorkspacePaths {
		if p != "" && filepath.Clean(p) == r.workspaceRoot {
			return true
		}
	}
	return false
}

// Reap removes all on-disk state for a single session id, immediately and
// regardless of age. Used on chat delete, where the caller already knows the
// chat (and therefore its session) is gone. A no-op for an empty or
// malformed id, or when the reaper is nil.
//
// The cli/ sidecars go unconditionally: they are addressed by the exact id the
// caller owns, not by a wildcard, and a sidecar whose dir is already gone must
// still be reclaimable.
func (r *Reaper) Reap(sessionID string) {
	if r == nil || !validSessionID(sessionID) {
		return
	}
	// Session dir under an untracked per-workspace-hash subdir: glob for it. The
	// glob spans every bucket in this $KIRO_HOME, so each match still has to say
	// it belongs to this workspace before it is removed.
	dirs, _ := filepath.Glob(filepath.Join(r.sessionsDir, "*", sessionID))
	for _, d := range dirs {
		if !r.belongsToWorkspace(d) {
			slog.Debug("kirosession: skipped a session dir that does not name this workspace",
				"session", sessionID, "path", d, "workspace", r.workspaceRoot)
			continue
		}
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
// the caller skips the sweep rather than narrowing the set (agent.sweepSessionsOnce).
//
// PASSING AN EMPTY SET IS REFUSED while the tree holds sessions. A zero-
// reference keep-list is indistinguishable from a misconfigured one, and the
// two outcomes are wildly asymmetric: refusing costs some disk, proceeding
// deletes every transcript on the volume. This is not hypothetical — it
// happened. A dev build was run with KIRO_CONFIG_DIR pointed at a scratch
// directory while KIRO_HOME was left unset, so the chat store was empty and
// `workspace.KiroHome()` still resolved to the real `$HOME/.kiro`; the first
// sweep at startup took ~450 sessions belonging to another application sharing
// that home. The caller's "is the keep-list complete" flag did not help,
// because an empty store is COMPLETE — it read every one of its zero files.
//
// The guard lives here rather than at the call site on purpose: this function
// holds the RemoveAll, and a check in one caller does not protect the next one.
func (r *Reaper) Sweep(referenced map[string]struct{}) int {
	if r == nil {
		return 0
	}
	if len(referenced) == 0 {
		// Only a refusal when there is something to lose. An empty keep-list
		// against an empty tree is an ordinary no-op on a fresh volume.
		if n := r.countSessions(); n > 0 {
			slog.Error("kirosession: REFUSING orphan sweep, keep-list is empty but sessions exist on disk",
				"sessions_on_disk", n,
				"sessions_dir", r.sessionsDir,
				"hint", "the chat store and the Kiro home are pointing at different volumes; set KIRO_HOME under the same root as KIRO_CONFIG_DIR")
			return 0
		}
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

// countSessions counts `sess_*` entries under every workspace-hash directory,
// without regard to age. It answers only "is there anything here to lose",
// which is what the empty-keep-list guard needs.
func (r *Reaper) countSessions() int {
	hashes, err := os.ReadDir(r.sessionsDir)
	if err != nil {
		// Unreadable root: report a non-zero count so the guard errs toward
		// refusing. A sweep could not enumerate anything to delete anyway.
		if !errors.Is(err, os.ErrNotExist) {
			return 1
		}
		return 0
	}
	n := 0
	for _, h := range hashes {
		// Skip "cli": it holds per-session SIDECARS, not sessions. Counting it
		// as a workspace hash double-counted every session (each has a dir and
		// a `sess_<id>.history`), which made the guard's log line lie about how
		// much was at stake.
		if !h.IsDir() || h.Name() == "cli" {
			continue
		}
		entries, dErr := os.ReadDir(filepath.Join(r.sessionsDir, h.Name()))
		if dErr != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), sessionPrefix) {
				n++
			}
		}
	}
	return n
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
	// The sweep enumerates every bucket, so a session belonging to another
	// workspace root under this $KIRO_HOME is unreferenced by construction. Its
	// own record is what tells the two apart.
	if !r.belongsToWorkspace(path) {
		slog.Debug("kirosession sweep: skipped a session that does not name this workspace",
			"path", path, "workspace", r.workspaceRoot)
		return false
	}
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
		if !r.sidecarReapable(id) {
			slog.Debug("kirosession sweep: skipped a cli sidecar whose session names another workspace",
				"file", name, "workspace", r.workspaceRoot)
			return false
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

// sidecarReapable reports whether the cli/ sidecars for sessionID may be removed.
//
// A sidecar carries no workspacePaths of its own, so ownership is answered by the
// session DIR it belongs to: a dir that names this workspace makes the sidecar
// ours, and a dir that does not makes it another workspace's file to keep. A
// sidecar with no dir anywhere is STRANDED — the shape a crash between the two
// removals leaves — and nothing will ever reference it again, so it stays
// reclaimable.
//
// Without this the workspace guard would be half a guard: the dir sweep would
// spare a foreign session while this loop deleted its history sidecar, which is
// the same lost transcript by a narrower route.
func (r *Reaper) sidecarReapable(sessionID string) bool {
	dirs, _ := filepath.Glob(filepath.Join(r.sessionsDir, "*", sessionID))
	stranded := true
	for _, d := range dirs {
		if info, err := os.Stat(d); err != nil || !info.IsDir() {
			continue
		}
		stranded = false
		if r.belongsToWorkspace(d) {
			return true
		}
	}
	return stranded
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
