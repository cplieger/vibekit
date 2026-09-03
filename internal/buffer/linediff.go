package buffer

import (
	"slices"
	"strings"
)

// maxDiffCells bounds the COUNTS pass by the m×n cells it would fill. Past it the
// answer degrades to delete-the-whole-middle / add-the-whole-middle: bounded,
// valid and coarse. Sibling constant: diff.ts's TIME_BUDGET_CELLS, which bounds
// the client's lineDiff — the two must agree, or the turn footer and the delegate
// footer report different numbers for one file.
const maxDiffCells = 25_000_000

// maxHunkCells bounds the TRACEBACK, which is a separate number because the two
// paths bound different resources: the counts keep two rolling rows, a traceback
// fills a dense m×n table at 8 bytes a cell, so maxDiffCells would license ~200 MB
// for one fragment. 4M cells is ~32 MB and matches diff.ts's SPACE_THRESHOLD, the
// point at which the client stops building a dense table too.
const maxHunkCells = 4_000_000

// lineHunk is one contiguous run of NEW-text lines a diff touched, 1-based and
// inclusive on both ends.
type lineHunk struct {
	StartLine int
	EndLine   int
}

// splitDiffLines splits s into lines for counting: split on "\n", strip one
// trailing "\r" per line so a CRLF-to-LF rewrite is not read as a whole-file
// change, and drop the single empty element a final newline produces — so
// "a\nb\n" and "a\nb" are both two lines, and "\n" is one empty line. Its
// TypeScript twin is splitDeltaLines in static-src/diff.ts.
func splitDiffLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for i, l := range lines {
		if strings.HasSuffix(l, "\r") {
			lines[i] = l[:len(l)-1]
		}
	}
	return lines
}

// lcsLen returns the length of the longest common subsequence of a and b.
//
// Two rolling rows, so space is O(min(len(a), len(b))) — the counts below need
// the LENGTH only, never the edit script, which is what keeps this linear in
// space where a rendered diff needs a table or Hirschberg. Any LCS-optimal
// implementation yields the same length even when it picks a different script,
// which is what makes the Go and TypeScript halves agree by construction.
func lcsLen(a, b []string) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	// LCS length is symmetric, so index the rows by the shorter side.
	if len(b) > len(a) {
		a, b = b, a
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for _, x := range a {
		for j, y := range b {
			if x == y {
				curr[j+1] = prev[j] + 1
			} else {
				curr[j+1] = max(curr[j], prev[j+1])
			}
		}
		prev, curr = curr, prev
		clear(curr)
	}
	return prev[len(b)]
}

// lineDelta reports how many lines a diff added and removed.
//
// A real diff, not a newline count per side: KAS sends whole-file text for its
// edit tools, so counting newlines reported the whole file as deleted and
// re-added for a one-line change (~100x on the live volume). added =
// len(newLines) - lcs and removed = len(oldLines) - lcs, so the LCS LENGTH is all
// this needs; the prefix/suffix trim is what makes the whole-file case cheap.
func lineDelta(oldText, newText string) (added, removed int) {
	if oldText == newText {
		return 0, 0
	}
	a := splitDiffLines(oldText)
	b := splitDiffLines(newText)

	p := 0
	maxTrim := min(len(a), len(b))
	for p < maxTrim && a[p] == b[p] {
		p++
	}
	s := 0
	for s < maxTrim-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	midOld := a[p : len(a)-s]
	midNew := b[p : len(b)-s]

	// One side empty is a pure insertion or a pure deletion, and the budget
	// fallback is the same coarse answer: nothing is common, so no LCS pass
	// can improve on the trimmed lengths.
	if len(midOld) == 0 || len(midNew) == 0 || len(midOld)*len(midNew) > maxDiffCells {
		return len(midNew), len(midOld)
	}
	k := lcsLen(midOld, midNew)
	return len(midNew) - k, len(midOld) - k
}

// lineHunks reports the NEW-text line ranges a diff touched, in file order.
//
// The editor gutter's input: whole-file NewText used to mark every line of the
// file as agent-modified, so a one-line edit painted accent dots from line 1 to
// the end. Ranges are 1-based and inclusive, and a run that only DELETED lines
// is reported as the single new-text line the deletion landed at — the removed
// lines are not in the new text, so that junction is the only honest place to
// mark. Returns nothing when the two texts are equal.
func lineHunks(oldText, newText string) []lineHunk {
	if oldText == newText {
		return nil
	}
	a := splitDiffLines(oldText)
	b := splitDiffLines(newText)

	p := 0
	maxTrim := min(len(a), len(b))
	for p < maxTrim && a[p] == b[p] {
		p++
	}
	s := 0
	for s < maxTrim-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	midOld := a[p : len(a)-s]
	midNew := b[p : len(b)-s]
	if len(midOld) == 0 && len(midNew) == 0 {
		return nil
	}

	// Coarse fallback: one hunk spanning the whole differing middle. Also the
	// exact answer when one side of the middle is empty, since then the middle
	// is a single insertion or a single deletion.
	if len(midOld) == 0 || len(midNew) == 0 || len(midOld)*len(midNew) > maxHunkCells {
		return []lineHunk{newRunHunk(p, p+len(midNew), len(b))}
	}
	return traceHunks(midOld, midNew, p, len(b))
}

// traceHunks walks a minimal edit script over the trimmed middles and groups
// consecutive non-context steps into hunks. Dense LCS table plus a traceback,
// under lineHunks' cell budget: a hunk needs the script, not just its length.
// offset is how many context lines were trimmed from the front, and newLen the
// new text's total line count (the clamp for a deletion at end of file).
func traceHunks(midOld, midNew []string, offset, newLen int) []lineHunk {
	t := denseLCS(midOld, midNew)
	var hunks []lineHunk
	var run runAcc
	run.reset()
	i, j := 0, 0
	for i < len(midOld) || j < len(midNew) {
		takeOld, takeNew := diffStep(midOld, midNew, t, i, j)
		switch {
		case takeOld && takeNew:
			// A context line closes whatever run was open.
			if lo, hi, ok := run.flush(); ok {
				hunks = append(hunks, newRunHunk(offset+lo, offset+hi, newLen))
			}
			i++
			j++
		case takeOld:
			// A deletion consumes an old line and no new one, so the run stays
			// anchored at the current new-text position.
			run.anchor(j)
			i++
		default:
			run.extend(j)
			j++
		}
	}
	if lo, hi, ok := run.flush(); ok {
		hunks = append(hunks, newRunHunk(offset+lo, offset+hi, newLen))
	}
	return hunks
}

// denseLCS fills the LCS length table for a and b, bottom-up. O(len(a)*len(b))
// space is what a traceback needs, which is why maxHunkCells bounds it and not
// maxDiffCells.
func denseLCS(a, b []string) [][]int {
	t := make([][]int, len(a)+1)
	for i := range t {
		t[i] = make([]int, len(b)+1)
	}
	for i, x := range slices.Backward(a) {
		for j, y := range slices.Backward(b) {
			if x == y {
				t[i][j] = t[i+1][j+1] + 1
			} else {
				t[i][j] = max(t[i+1][j], t[i][j+1])
			}
		}
	}
	return t
}

// diffStep decides one step of the traceback from position (i, j): whether it
// consumes a line of the old side, of the new side, or of both (a context match).
func diffStep(a, b []string, t [][]int, i, j int) (takeOld, takeNew bool) {
	if i < len(a) && j < len(b) && a[i] == b[j] {
		return true, true
	}
	if j >= len(b) || (i < len(a) && t[i+1][j] >= t[i][j+1]) {
		return true, false
	}
	return false, true
}

// runAcc accumulates one open run of touched NEW-text lines as half-open indices.
// A struct rather than two locals plus a closure: the closure captured the hunk
// slice as well, which put the whole grouping rule inside one function.
type runAcc struct {
	lo int
	hi int
}

func (r *runAcc) reset() { r.lo, r.hi = -1, -1 }

// anchor opens a run at j without consuming a new line — a deletion.
func (r *runAcc) anchor(j int) {
	if r.lo < 0 {
		r.lo, r.hi = j, j
	}
}

// extend opens or grows a run over the new line at j — an addition.
func (r *runAcc) extend(j int) {
	if r.lo < 0 {
		r.lo = j
	}
	r.hi = j + 1
}

// flush reports the open run and closes it, or false when none is open.
func (r *runAcc) flush() (lo, hi int, ok bool) {
	if r.lo < 0 {
		return 0, 0, false
	}
	lo, hi = r.lo, r.hi
	r.reset()
	return lo, hi, true
}

// newRunHunk turns a half-open range of NEW-text line indices into a 1-based
// inclusive LineRange span, clamped into the new text. An empty range is a
// deletion, marked at the line that now sits where the removed lines were.
func newRunHunk(lo, hi, newLen int) lineHunk {
	if newLen <= 0 {
		// The new text has no lines at all (the file was emptied), so line 1 is the
		// only thing a gutter could mark.
		return lineHunk{StartLine: 1, EndLine: 1}
	}
	start := min(lo+1, newLen)
	end := min(max(hi, start), newLen)
	return lineHunk{StartLine: start, EndLine: end}
}
