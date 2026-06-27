package checkpoint

import (
	"context"
	"strings"
)

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
func countLineDelta(ctx context.Context, from, to []byte) (added, removed int) {
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
	common, cancelled := lcsRollingLength(ctx, fromLines, toLines)
	if cancelled {
		return m, n
	}
	return m - common, n - common
}

// lcsRollingLength returns the length of the longest common
// subsequence of fromLines and toLines using a two-row rolling table
// (memory O(min over the row width) instead of O(N*M)). Built from the
// bottom-right corner so the answer lands in prev[0] after the loop.
// The context is checked every 65536 inner iterations so a cancelled
// context short-circuits a pathological diff; cancelled=true signals
// the caller to fall back to "everything changed".
func lcsRollingLength(ctx context.Context, fromLines, toLines []string) (common int, cancelled bool) {
	n := len(fromLines)
	m := len(toLines)
	prev := make([]int, m+1)
	curr := make([]int, m+1)
	var iter int
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if fromLines[i] == toLines[j] {
				curr[j] = prev[j+1] + 1
			} else {
				curr[j] = max(prev[j], curr[j+1])
			}
			iter++
			if iter&0xFFFF == 0 && ctx.Err() != nil {
				return 0, true
			}
		}
		prev, curr = curr, prev
		// Zero out curr for the next iteration.
		clear(curr)
	}
	return prev[0], false
}

func bytesToLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	s := strings.TrimSuffix(string(b), "\n")
	return strings.Split(s, "\n")
}
