package checkpoint

import "strings"

// lcsCellCap bounds the product N*M before the LCS algorithm falls
// back to treating everything as changed. Prevents pathological
// inputs from allocating unbounded memory.
const lcsCellCap = 16 << 20

// countLineDelta returns (added, removed) line counts by running a
// classic line-LCS diff between two byte slices. Equal lines are
// considered matched only when they appear in compatible order —
// a pure bag/multiset count (which we used to do) wrongly labels
// reordered lines as unchanged, so the UI's "+N/-M" summary lied
// on simple refactors like "hoist imports to top". Uses a two-row
// rolling approach that bounds memory to O(min(N,M)) instead of
// O(N*M). The full table is not needed since we only need the LCS
// length, not the actual subsequence. Time complexity remains
// O(N*M) but memory drops from ~128 MiB worst-case to ~128 KiB
// for a 16K-line file.
func countLineDelta(from, to []byte) (added, removed int) {
	fromLines := bytesToLines(from)
	toLines := bytesToLines(to)
	n := len(fromLines)
	m := len(toLines)
	if n == 0 {
		return m, 0
	}
	if m == 0 {
		return 0, n
	}
	// Product-cap gate: if the product exceeds lcsCellCap, fall
	// back to treating everything as changed. This preserves the
	// safety bound against pathological inputs while the rolling
	// approach makes the common case memory-efficient.
	if n > lcsCellCap || m > lcsCellCap || int64(n)*int64(m) > lcsCellCap {
		return m, n
	}
	// Two-row rolling LCS: only keep the current row and the
	// previous row. Built from the bottom-right corner so the
	// final answer lives in prev[0] after the loop completes.
	// Memory: 2*(m+1) ints instead of (n+1)*(m+1).
	prev := make([]int, m+1)
	curr := make([]int, m+1)
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if fromLines[i] == toLines[j] {
				curr[j] = prev[j+1] + 1
			} else {
				curr[j] = max(prev[j], curr[j+1])
			}
		}
		prev, curr = curr, prev
		// Zero out curr for the next iteration.
		clear(curr)
	}
	common := prev[0]
	return m - common, n - common
}

func bytesToLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	s := strings.TrimSuffix(string(b), "\n")
	return strings.Split(s, "\n")
}
