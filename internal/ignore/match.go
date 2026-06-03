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

// segMatchBounded is the iterative segment matcher with a bounded step
// counter. Returns false (fail-open) when the step budget exhausts,
// matching the matcher's overall fail-open philosophy.
func segMatchBounded(p, x []string) bool {
	type frame struct {
		pi, xi int
	}
	stack := make([]frame, 0, 16)
	stack = append(stack, frame{0, 0})
	steps := 0
	for len(stack) > 0 {
		steps++
		if steps > maxSegMatchSteps {
			return false // fail-open on budget exhaustion
		}
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		pi, xi := f.pi, f.xi
		matched := true
		for pi < len(p) {
			if p[pi] == "**" {
				rest := p[pi+1:]
				if len(rest) == 0 {
					return true
				}
				// Push all possible split points onto the stack
				// (reverse order so smallest xi is tried first).
				for j := len(x) - xi; j >= 0; j-- {
					steps++
					if steps > maxSegMatchSteps {
						return false
					}
					stack = append(stack, frame{pi + 1, xi + j})
				}
				matched = false
				break
			}
			if xi >= len(x) {
				matched = false
				break
			}
			ok, err := filepath.Match(p[pi], x[xi])
			if err != nil || !ok {
				matched = false
				break
			}
			pi++
			xi++
		}
		if matched && pi == len(p) && xi == len(x) {
			return true
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
