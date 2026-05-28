// Gitignore-style agent read filter.
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
//     "secrets" AND "secrets/api.key" AND "secrets/sub/deep.json"
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
	"slices"
	"strings"
	"sync"
	"time"

	cfgsettings "vibekit/internal/settings"

	"golang.org/x/sync/singleflight"
)

// maxIgnoreFileSize caps each listed ignore file — delegates to
// settings.MaxBytes (1 MiB) so the cap is maintained in one place.
const maxIgnoreFileSize = cfgsettings.MaxBytes

// settingsFilename delegates to settings.Filename — single source of truth.
const settingsFilename = cfgsettings.Filename

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
// Empty / missing settings produce a no-op matcher (Matches always
// returns false).
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
		if r.dirOnly {
			if isDir && matchSegments(r.segments, pathSegs, r.anchored) {
				ignored = !r.negate
				continue
			}
			for d := len(pathSegs) - 1; d >= 1; d-- {
				if matchSegments(r.segments, pathSegs[:d], r.anchored) {
					ignored = !r.negate
					break
				}
			}
			continue
		}
		if !matchSegments(r.segments, pathSegs, r.anchored) {
			continue
		}
		ignored = !r.negate
	}
	return ignored
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
	newRules := make([]rule, 0, len(cachedLastMTimes)*4)
	newMTimes := make(map[string]time.Time, len(files))
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
		newMTimes[f] = info.ModTime()
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for line := range strings.SplitSeq(string(data), "\n") {
			if r, ok := parseIgnoreLine(line); ok {
				newRules = append(newRules, r)
			}
		}
	}

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

// filesOrMTimesChangedStatic reports whether the ignore-file list or any
// tracked file's mtime has advanced since the last load. Operates on
// passed-in state rather than reading from the struct, so it can run
// without holding m.mu.
func filesOrMTimesChangedStatic(cachedFiles []string, cachedMTimes map[string]time.Time, files []string) bool {
	if !slices.Equal(cachedFiles, files) {
		return true
	}
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			if _, had := cachedMTimes[f]; had {
				return true
			}
			continue
		}
		prev, had := cachedMTimes[f]
		if !had || !prev.Equal(info.ModTime()) {
			return true
		}
	}
	return false
}

// readSettingFiles pulls the agent_ignore_files list out of
// config.json and resolves each entry against the workspace root.
// Uses the settings package's parsedMap cache to avoid redundant
// json.Unmarshal calls when multiple callers read config.json.
func (m *Matcher) readSettingFiles(ctx context.Context) []string {
	var list []string
	if !cfgsettings.FieldInto(ctx, m.configDir, cfgsettings.KeyAgentIgnoreFiles, "agent_ignore_files", &list) {
		return nil
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

// parseIgnoreLine turns one line of a .gitignore into a rule. Empty
// / comment lines return ok=false.
func parseIgnoreLine(line string) (rule, bool) {
	line = strings.TrimRight(line, " \t\r\n")
	if line == "" || strings.HasPrefix(line, "#") {
		return rule{}, false
	}
	if strings.Count(line, "**") > 4 {
		slog.Warn("permissions: ignore rule has too many '**', skipping",
			"pattern", line)
		return rule{}, false
	}
	r := rule{}
	if strings.HasPrefix(line, "!") {
		r.negate = true
		line = line[1:]
	}
	if strings.HasPrefix(line, "/") {
		r.anchored = true
		line = line[1:]
	}
	if strings.HasSuffix(line, "/") {
		r.dirOnly = true
		line = strings.TrimSuffix(line, "/")
	}
	if !r.anchored && strings.Contains(line, "/") {
		r.anchored = true
	}
	r.pattern = line
	r.segments = strings.Split(line, "/")
	return r, true
}

// matchSegments evaluates a single rule's pattern segments against pre-split path segments.
func matchSegments(ruleSegs, pathSegs []string, anchored bool) bool {
	if len(ruleSegs) == 0 {
		return false
	}
	if anchored {
		return matchAnchored(ruleSegs, pathSegs)
	}
	if matchAnchored(ruleSegs, pathSegs) {
		return true
	}
	for i := range pathSegs {
		if matchAnchored(ruleSegs, pathSegs[i+1:]) {
			return true
		}
	}
	return false
}

// matchAnchored walks pattern segments and path segments in lock-step.
func matchAnchored(pSegs, xSegs []string) bool {
	if segMatch(pSegs, xSegs) {
		return true
	}
	if len(xSegs) > len(pSegs) && segMatch(pSegs, xSegs[:len(pSegs)]) {
		return true
	}
	return false
}

// segMatch is the recursive segment matcher.
func segMatch(p, x []string) bool {
	for i := range p {
		if p[i] == "**" {
			rest := p[i+1:]
			if len(rest) == 0 {
				return true
			}
			for j := 0; j <= len(x); j++ {
				if segMatch(rest, x[j:]) {
					return true
				}
			}
			return false
		}
		if i >= len(x) {
			return false
		}
		ok, err := filepath.Match(p[i], x[i])
		if err != nil || !ok {
			return false
		}
	}
	return len(p) == len(x)
}
