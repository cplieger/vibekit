// Package ignore is a gitignore-style agent read filter.
//
// Users can point vibekit at one or more ignore files (.gitignore,
// .kiroignore, etc.). Any read path that matches the combined
// patterns is refused by the fs/read_text_file handler. Writes are
// NOT affected — matches git's semantics where ignored files are
// writable, just not tracked. The intent is to stop the agent from
// slurping decrypted secrets (`.env.dec`), build artifacts, and
// other files the user deliberately excluded from version control
// into its context window.
//
// Supported gitignore features (subset):
//
//   - Literal paths ("node_modules").
//   - Globs: `*`, `?`, and `**` (matches across directory
//     boundaries).
//   - Leading `/` anchors to the ignore file's directory — "/node"
//     matches only a top-level "node" entry, not "src/node".
//   - Trailing `/` restricts the rule to directories.
//   - Directory-match implies descendants: "/secrets" matches
//     "secrets" AND "secrets/vibekit.key" AND "secrets/sub/deep.json"
//     (standard gitignore semantics).
//   - Leading `!` negates a previously-matched rule so users can
//     carve out exceptions ("!/.env.example").
//   - `#` comments; blank lines ignored.
//
// Supported via filepath.Match: character classes (`[abc]`, `[a-z]`,
// `[^abc]`). Not supported (deliberately): backslash escapes and the
// full git `!` re-inclusion semantics
// that depend on whether the parent directory was itself ignored.
// If we need those we'll rebuild against go-gitignore later.
//
// Matcher is case-sensitive and operates on slash-separated paths
// (matching what resolveInsideWorkDir produces).
//
// Fail mode: a transient config.json read/parse failure (oversized,
// corrupt JSON, wrong-type list) clears the in-memory ruleset,
// effectively disabling the matcher (fail-open) until the next
// successful read. The alternative — fail-closed block-all — would
// lock the agent out of the workspace on any disk blip. Errors are
// logged at slog.Warn so transient failures are visible in Loki. A
// persistently broken config.json surfaces as a one-time warn plus
// all-permissive matches until fixed.
package ignore

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/settings"
	"golang.org/x/sync/singleflight"
)

// maxIgnoreFileSize caps each listed ignore file — delegates to
// settings.MaxBytes (1 MiB) so the cap is maintained in one place.
const maxIgnoreFileSize = settings.MaxBytes

// settingsFilename delegates to settings.Filename — single source of truth.
const settingsFilename = settings.Filename

// Matcher evaluates read paths against a set of ignore files
// listed in the server settings. Patterns are re-parsed on demand
// when the on-disk mtime of any listed ignore file advances, so
// edits to `.gitignore` take effect on the next agent read without
// a restart.
type Matcher struct {
	lastLoad      time.Time
	settingsMTime time.Time
	sfGroup       singleflight.Group
	lastMTimes    map[string]time.Time
	configDir     string
	workDir       string
	files         []string
	rules         []rule
	settingsSize  int64
	mu            sync.Mutex
}

// rule is a single parsed ignore entry. `negate` flips the semantics
// so a later `!foo` resurrects an earlier `foo` match. `dirOnly`
// means the pattern only matches paths whose last segment is a
// directory; evaluated via the caller-provided `isDir` hint.
// `segments` is the pre-split pattern (split on "/") to avoid
// per-Matches allocations.
type rule struct {
	pattern  string
	segments []string
	anchored bool
	dirOnly  bool
	negate   bool
}

// NewMatcher builds a matcher backed by the agent_ignore_files
// setting in `<configDir>/config.json`. Relative entries in the
// setting are resolved against workDir so users can type
// `.gitignore` and have it pick up the workspace's top-level file.
// When the key is unset (fresh install / no config.json), the matcher
// falls back to settings.DefaultAgentIgnoreFiles() so a workspace
// `.gitignore` / `.kiroignore` is honoured out of the box; it is still
// a no-op when none of the resolved ignore files exist. An explicit
// empty list in config.json opts out entirely.
func NewMatcher(configDir, workDir string) *Matcher {
	return &Matcher{
		configDir: configDir,
		workDir:   workDir,
	}
}

// Matches reports whether the given path should be blocked from
// agent reads. `rel` is the workspace-relative path (forward
// slashes). `isDir` is a hint for directory-only patterns; false
// is the safe default (matches files).
func (m *Matcher) Matches(ctx context.Context, rel string, isDir bool) bool {
	m.refresh(ctx)

	m.mu.Lock()
	rules := m.rules
	m.mu.Unlock()

	if len(rules) == 0 {
		return false
	}

	rel = strings.TrimPrefix(rel, "./")
	rel = strings.TrimPrefix(rel, "/")

	// Pre-split once to avoid O(rules×ancestors) repeated splits.
	pathSegs := strings.Split(rel, "/")

	// Walk rules in order; later matches override earlier ones.
	ignored := false
	for _, r := range rules {
		var matched bool
		if r.dirOnly {
			matched = dirRuleMatches(r, pathSegs, isDir)
		} else {
			matched = matchSegments(r.segments, pathSegs, r.anchored)
		}
		if matched {
			ignored = !r.negate
		}
	}
	return ignored
}

// dirRuleMatches reports whether a directory-only rule applies to a path.
// It matches when the path itself is a directory matching the pattern, or
// when any ancestor directory matches — so ignoring a directory covers
// every file beneath it (standard gitignore semantics).
func dirRuleMatches(r rule, pathSegs []string, isDir bool) bool {
	if isDir && matchSegments(r.segments, pathSegs, r.anchored) {
		return true
	}
	for d := len(pathSegs) - 1; d >= 1; d-- {
		if matchSegments(r.segments, pathSegs[:d], r.anchored) {
			return true
		}
	}
	return false
}

// refresh re-reads the file list from config.json and re-parses
// ignore files whose mtimes have advanced. Uses singleflight to
// deduplicate concurrent refresh I/O — the first caller in a burst
// does the work; others share its result. Accepts ctx so callers'
// cancellation propagates to the singleflight-guarded I/O path.
func (m *Matcher) refresh(ctx context.Context) {
	//nolint:errcheck // doRefresh has no error return; singleflight result is discarded.
	m.sfGroup.Do("refresh", func() (any, error) {
		m.doRefresh(ctx)
		return nil, nil
	})
}

// doRefresh performs the actual refresh logic. Stats config.json
// first — if its mtime hasn't advanced, the file list is reused from
// cache (fast path: single stat). Only when config.json changes or
// ignore-file mtimes advance does it re-read and re-parse.
//
// I/O (stat, read, parse) runs WITHOUT holding m.mu — the singleflight
// already serializes concurrent refreshes. The mutex is taken only for
// the final pointer swap of rules/files/mtimes, minimizing the window
// where Matches() callers block.
func (m *Matcher) doRefresh(ctx context.Context) {
	// Fast path: stat config.json. If mtime hasn't advanced, reuse
	// the cached file list and only check ignore-file mtimes.
	settingsPath := filepath.Join(m.configDir, settingsFilename)
	settingsInfo, settingsErr := os.Stat(settingsPath)

	// Read cached state under lock (brief).
	m.mu.Lock()
	cachedFiles := m.files
	cachedSettingsMTime := m.settingsMTime
	cachedSettingsSize := m.settingsSize
	cachedLastLoad := m.lastLoad
	cachedLastMTimes := m.lastMTimes
	m.mu.Unlock()

	var files []string
	settingsChanged := true
	if settingsErr == nil && !cachedSettingsMTime.IsZero() &&
		settingsInfo.ModTime().Equal(cachedSettingsMTime) &&
		settingsInfo.Size() == cachedSettingsSize {
		// config.json unchanged — reuse cached file list.
		files = cachedFiles
		settingsChanged = false
	}

	var newSettingsMTime time.Time
	var newSettingsSize int64
	if settingsChanged {
		files = m.readSettingFiles(ctx)
		if settingsErr == nil {
			newSettingsMTime = settingsInfo.ModTime()
			newSettingsSize = settingsInfo.Size()
		}
	} else {
		newSettingsMTime = cachedSettingsMTime
		newSettingsSize = cachedSettingsSize
	}

	// Check if ignore-file mtimes changed (I/O phase, no lock).
	if !cachedLastLoad.IsZero() && !filesOrMTimesChangedStatic(cachedFiles, cachedLastMTimes, files) {
		// Nothing changed — update settings cache if needed and return.
		if settingsChanged {
			m.mu.Lock()
			m.settingsMTime = newSettingsMTime
			m.settingsSize = newSettingsSize
			m.files = files
			m.mu.Unlock()
		}
		return
	}

	// I/O phase: read and parse all ignore files (no lock held).
	newRules, newMTimes := loadRules(files, len(cachedLastMTimes))

	// Pointer-swap phase: brief lock to install new state.
	m.mu.Lock()
	m.rules = newRules
	m.lastMTimes = newMTimes
	m.files = files
	m.lastLoad = time.Now()
	m.settingsMTime = newSettingsMTime
	m.settingsSize = newSettingsSize
	m.mu.Unlock()
}

// loadRules stats, size-checks, reads, and parses every listed ignore file,
// returning the combined rule set and the per-file mtimes used for change
// detection on the next refresh. Unreadable or oversized files are skipped
// (fail-open). sizeHint pre-sizes the rule slice from the previous load's
// tracked-file count.
func loadRules(files []string, sizeHint int) (rules []rule, mtimes map[string]time.Time) {
	rules = make([]rule, 0, sizeHint*4)
	mtimes = make(map[string]time.Time, len(files))
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			continue
		}
		if info.Size() > maxIgnoreFileSize {
			slog.Warn("permissions: ignore file exceeds cap, skipping",
				"path", f, "size", info.Size(), "cap", maxIgnoreFileSize)
			continue
		}
		mtimes[f] = info.ModTime()
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for line := range strings.SplitSeq(string(data), "\n") {
			if r, ok := parseIgnoreLine(line); ok {
				rules = append(rules, r)
			}
		}
	}
	return rules, mtimes
}

// readSettingFiles pulls the agent_ignore_files list out of
// config.json and resolves each entry against the workspace root.
// Uses the settings package's parsedMap cache to avoid redundant
// json.Unmarshal calls when multiple callers read config.json.
func (m *Matcher) readSettingFiles(ctx context.Context) []string {
	var list []string
	if !settings.FieldInto(ctx, m.configDir, settings.KeyAgentIgnoreFiles, "agent_ignore_files", &list) {
		// Key unset: fresh install (no config.json), a config.json that
		// predates the key, or a transient read/parse failure. Fall back to
		// the seeded default so the agent read filter is ON out of the box
		// (the settled decision; matches the ignore-file names the IDE
		// recognizes). An explicit "agent_ignore_files":[] is a real opt-out —
		// FieldInto returns true with an empty list, so this branch does not
		// run and no filtering happens.
		list = settings.DefaultAgentIgnoreFiles()
	}
	out := make([]string, 0, len(list))
	for _, entry := range list {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if !filepath.IsAbs(entry) {
			entry = filepath.Join(m.workDir, entry)
		}
		out = append(out, entry)
	}
	return out
}
