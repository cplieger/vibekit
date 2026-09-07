// Package kirosession reaps the kiro-cli / KAS on-disk session state that backs
// session/load resume: it exists only to reload a chat vibekit still keeps, so it
// dies with that chat. vibekit is the sole retention authority — KAS's own
// sessionEviction defaults to disabled and the boot conformance check keeps it so.
//
// Layout (KAS 2.12):
//
//	$KIRO_HOME/sessions/<workspace-hash>/sess_<id>/   — the KAS session dir
//	$KIRO_HOME/sessions/<workspace-hash>/workflows/<workflowId>/ — a run's state
//	$KIRO_HOME/sessions/cli/sess_<id>.history         — its history sidecar
//	$KIRO_HOME/sessions/cli/<uuid>.{json,jsonl,lock}  — dead v2-engine files
//
// The workspace-hash dir is untracked, so a session is located by globbing
// sessions/*/sess_<id>: that crosses every bucket under one $KIRO_HOME, so each
// match must still name this workspace (belongsToWorkspace).
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

// sessionPrefix is the v3 KAS session-id prefix. Anything without it is empty,
// garbage, or a dead v2-engine file the sweep reaps purely on age.
const sessionPrefix = "sess_"

// defaultGuard spares session state younger than this from the orphan sweep: it
// covers the create race, since KAS writes the session dir a moment before vibekit
// persists the chat file that references it. Direct Reap is not subject to it —
// there the caller knows the chat is gone.
const defaultGuard = 10 * time.Minute

// sessionRecordName is the per-session file KAS writes its own metadata into. Its
// workspacePaths list is the subject record every removal here is checked against.
const sessionRecordName = "session.json"

// workflowsDirName is the per-bucket directory KAS keeps a workflow RUN's state in:
// sessions/<workspace-hash>/workflows/<workflowId>/.
const workflowsDirName = "workflows"

// Reaper removes on-disk KAS session state under sessionsDir, for the one
// workspace it is entitled to reap.
type Reaper struct {
	sessionsDir string
	// workspaceRoot is the workspace this reaper may delete sessions for. Every
	// removal reads the candidate's own workspacePaths and skips a session whose
	// list does not name it; an empty root names nothing, so it reaps nothing.
	workspaceRoot string
	guard         time.Duration
}

// New returns a Reaper rooted at sessionsDir (typically
// filepath.Join(workspace.KiroHome(), "sessions")) and confined to workspaceRoot
// (the bridge's own cwd).
func New(sessionsDir, workspaceRoot string) *Reaper {
	root := workspaceRoot
	if root != "" {
		root = filepath.Clean(root)
	}
	return &Reaper{sessionsDir: sessionsDir, workspaceRoot: root, guard: defaultGuard}
}

// belongsToWorkspace reports whether the session at sessionDir names this reaper's
// workspace root in its own workspacePaths.
//
// A mismatch is a skip and so is DOUBT (unreadable record, absent or empty list,
// decode error): a wrong skip costs disk, a wrong removal costs another
// workspace's history. The reap paths glob across every bucket under one
// $KIRO_HOME and never read the hash, so no collision is needed for a reap to
// cross workspaces.
func (r *Reaper) belongsToWorkspace(sessionDir string) bool {
	paths, _, ok := readSessionRecord(sessionDir)
	if !ok {
		return false
	}
	return namesRoot(paths, r.workspaceRoot)
}

// namesRoot reports whether a decoded workspacePaths list names root. Split out so
// the sweep's one-read path applies the same rule rather than a second copy.
func namesRoot(paths []string, root string) bool {
	for _, p := range paths {
		if p != "" && filepath.Clean(p) == root {
			return true
		}
	}
	return false
}

// readSessionRecord decodes the two facts this package reads out of a session's own
// session.json: the workspace roots it claims, and the workflow RUN it was created
// for (empty for every non-step session). ok is false when the file is absent,
// unreadable or undecodable.
//
// ONE reader for both, because two reads of one file could disagree while a KAS
// write lands between them.
func readSessionRecord(sessionDir string) (workspacePaths []string, workflowID string, ok bool) {
	data, err := os.ReadFile(filepath.Join(sessionDir, sessionRecordName))
	if err != nil {
		return nil, "", false
	}
	var rec struct {
		Meta struct {
			Kiro struct {
				Workflow struct {
					WorkflowID string `json:"workflowId"`
				} `json:"workflow"`
			} `json:"kiro"`
		} `json:"_meta"`
		WorkspacePaths []string `json:"workspacePaths"`
	}
	if json.Unmarshal(data, &rec) != nil {
		return nil, "", false
	}
	return rec.WorkspacePaths, rec.Meta.Kiro.Workflow.WorkflowID, true
}

// Reap removes all on-disk state for a single session id, immediately and
// regardless of age; used on chat delete. A no-op for an empty or malformed id, or
// a nil reaper. The cli/ sidecars go unconditionally: they are addressed by the
// exact id the caller owns, and a sidecar whose dir is already gone must still be
// reclaimable.
func (r *Reaper) Reap(sessionID string) {
	if r == nil || !validSessionID(sessionID) {
		return
	}
	// Session dir under an untracked per-workspace-hash subdir: glob for it. Each
	// match still has to say it belongs to this workspace before it is removed.
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

// Sweep removes orphaned session state: any session dir or cli sidecar whose id is
// not in referenced and is older than the guard, plus dead v2-engine files
// (bare-uuid, no sess_ prefix). Returns the number of sessions reaped. `referenced`
// must be the COMPLETE keep-list — every chat's chain unioned with every live
// bridge's session — because age is not evidence a session is disposable, so a
// caller holding a partial set skips the sweep instead. An EMPTY set is refused
// while the tree holds sessions: it is indistinguishable from a misconfigured one
// and would delete every transcript on the volume. Step sessions: orphanReapable.
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
// answering only "is there anything here to lose" for the empty-keep-list guard.
func (r *Reaper) countSessions() int {
	hashes, err := os.ReadDir(r.sessionsDir)
	if err != nil {
		// Unreadable root: report a non-zero count so the guard errs toward refusing.
		// A sweep could not enumerate anything to delete anyway.
		if !errors.Is(err, os.ErrNotExist) {
			return 1
		}
		return 0
	}
	n := 0
	for _, h := range hashes {
		// Skip "cli": it holds per-session SIDECARS, so counting it as a workspace
		// hash double-counts every session and overstates what is at stake.
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
	switch r.spareReason(sub, path) {
	case spareForeignWorkspace:
		slog.Debug("kirosession sweep: skipped a session that does not name this workspace",
			"path", path, "workspace", r.workspaceRoot)
		return false
	case spareLiveRun:
		slog.Debug("kirosession sweep: spared a workflow step session whose run still exists",
			"path", path)
		return false
	case spareNone:
	}
	if err := os.RemoveAll(path); err != nil {
		slog.Warn("kirosession sweep: remove session dir", "path", path, "error", err)
		return false
	}
	r.removeCLISidecars(name)
	return true
}

// sweepSpare names why the orphan sweep must leave a session alone, or spareNone
// when it may be reaped. Typed rather than a bool because two callers report it
// differently, and constant rather than a string for attribute-keyed log filters.
type sweepSpare int

const (
	// spareNone: the sweep may reap this session.
	spareNone sweepSpare = iota
	// spareForeignWorkspace: its own record does not name this workspace, or
	// could not be read at all (doubt retains, this package's posture).
	spareForeignWorkspace
	// spareLiveRun: it is a workflow step of a run whose state is still on disk.
	spareLiveRun
)

// spareReason answers both questions the sweep asks of an aged, unreferenced
// candidate off ONE read of its session.json: does it name this workspace, and is
// it a workflow STEP whose run is still on disk. A step session is referenced by no
// chat, so without the second question the sweep reaps it mid-run and the step's
// transcript — readable only out of that directory — goes with it. The bound is the
// run's own retention: KAS prunes the workflows tree for nobody, so what reclaims a
// spared step session is DELETE /api/runs/{id}. The run dir is looked for in the
// candidate's OWN bucket, and the workspace question is asked first.
func (r *Reaper) spareReason(sub, path string) sweepSpare {
	paths, workflowID, ok := readSessionRecord(path)
	// The sweep enumerates every bucket, so a session belonging to another workspace
	// root under this $KIRO_HOME is unreferenced by construction.
	if !ok || !namesRoot(paths, r.workspaceRoot) {
		return spareForeignWorkspace
	}
	if workflowID == "" {
		return spareNone
	}
	if info, sErr := os.Stat(filepath.Join(sub, workflowsDirName, workflowID)); sErr == nil && info.IsDir() {
		return spareLiveRun
	}
	return spareNone
}

// sweepCLI reaps orphaned files under sessions/cli/: v3 sess_<id>.* sidecars whose
// id is unreferenced, and dead v2-engine files, both gated on the guard window.
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

// reapOrphanCLIFile removes one sessions/cli/ file when it is an unreferenced v3
// sidecar or a dead v2-engine file, older than the guard. Reports whether a v3
// SESSION was reaped; dead v2 files are not counted.
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
// A sidecar carries no workspacePaths, so ownership is answered by the session DIR
// it belongs to; a sidecar with no dir anywhere is stranded — the shape a crash
// between the two removals leaves — and stays reclaimable. It asks spareReason
// rather than belongsToWorkspace because EVERY reason the dir sweep spares a
// session is a reason to keep its history: a workspace check alone spared the
// directory and deleted the sidecar beside it.
func (r *Reaper) sidecarReapable(sessionID string) bool {
	dirs, _ := filepath.Glob(filepath.Join(r.sessionsDir, "*", sessionID))
	stranded := true
	for _, d := range dirs {
		if info, err := os.Stat(d); err != nil || !info.IsDir() {
			continue
		}
		stranded = false
		if r.spareReason(filepath.Dir(d), d) == spareNone {
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

// validSessionID reports whether id is a well-formed KAS session id safe to use in
// a filesystem glob: the sess_ prefix plus id-safe chars (hex, dashes,
// underscores — no glob metacharacters, path separators or dots).
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
