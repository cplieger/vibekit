// Pure pattern matching logic for gitignore-style rules.
//
// These functions have no I/O dependencies and are unit-testable
// without filesystem setup. Separated from ignore.go which handles
// the I/O lifecycle (refresh, cache, read).

package ignore

import (
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// maxSegMatchSteps bounds the iteration count in segMatchBounded to
// prevent pathological backtracking on crafted ignore rules with deep
// paths and multiple ** segments.
const maxSegMatchSteps = 10_000

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
	if line == "" {
		return rule{}, false
	}
	if strings.HasPrefix(line, "/") {
		r.anchored = true
		line = line[1:]
	}
	if strings.HasSuffix(line, "/") {
		r.dirOnly = true
		line = strings.TrimSuffix(line, "/")
	}
	if line == "" {
		return rule{}, false
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
	if segMatchBounded(pSegs, xSegs) {
		return true
	}
	if len(xSegs) > len(pSegs) && segMatchBounded(pSegs, xSegs[:len(pSegs)]) {
		return true
	}
	return false
}

// segWalkResult is the reason walkLiterals stopped advancing.
type segWalkResult int

const (
	segMismatch   segWalkResult = iota // literal mismatch or path exhausted
	segDoubleStar                      // stopped at a "**" segment
	segComplete                        // consumed the whole pattern
)

// segFrame is one DFS position: pattern index pi against path index xi.
type segFrame struct {
	pi, xi int
}

// segMatcher carries segMatchBounded's bounded-backtracking state: the
// explicit DFS stack of frames and the step counter that caps pathological
// work on crafted "**" patterns.
type segMatcher struct {
	stack []segFrame
	steps int
}

// tick spends one step of the budget and reports whether it is exhausted.
func (m *segMatcher) tick() bool {
	m.steps++
	return m.steps > maxSegMatchSteps
}

// pushSplits enqueues one successor frame per possible "**" match length,
// each advancing past the "**" at p[pi] to p[pi+1]. Frames are pushed in
// reverse so the smallest xi (shortest "**" match) is tried first. Returns
// true if the budget is exhausted mid-push (fail-open).
func (m *segMatcher) pushSplits(x []string, pi, xi int) bool {
	for j := len(x) - xi; j >= 0; j-- {
		if m.tick() {
			return true
		}
		m.stack = append(m.stack, segFrame{pi + 1, xi + j})
	}
	return false
}

// walkLiterals advances from (pi,xi) through literal/glob pattern segments
// until it reaches a "**", a mismatch, or the end of the pattern. It spends
// no budget; only the outer pop and the "**" expansion do.
func walkLiterals(p, x []string, pi, xi int) (res segWalkResult, stopPi, stopXi int) {
	for pi < len(p) {
		if p[pi] == "**" {
			return segDoubleStar, pi, xi
		}
		if xi >= len(x) {
			return segMismatch, pi, xi
		}
		ok, err := filepath.Match(p[pi], x[xi])
		if err != nil || !ok {
			return segMismatch, pi, xi
		}
		pi++
		xi++
	}
	return segComplete, pi, xi
}

// segMatchBounded is the iterative segment matcher with a bounded step
// counter. Returns false (fail-open) when the step budget exhausts,
// matching the matcher's overall fail-open philosophy.
func segMatchBounded(p, x []string) bool {
	m := segMatcher{stack: make([]segFrame, 0, 16)}
	m.stack = append(m.stack, segFrame{0, 0})
	for len(m.stack) > 0 {
		if m.tick() {
			return false // fail-open on budget exhaustion
		}
		f := m.stack[len(m.stack)-1]
		m.stack = m.stack[:len(m.stack)-1]

		res, pi, xi := walkLiterals(p, x, f.pi, f.xi)
		switch res {
		case segComplete:
			if xi == len(x) {
				return true
			}
		case segDoubleStar:
			if pi+1 >= len(p) {
				return true // trailing "**" matches the rest
			}
			if m.pushSplits(x, pi, xi) {
				return false
			}
		}
	}
	return false
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
