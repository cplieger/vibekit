package auth

import "testing"

func FuzzLineRingPushSample(f *testing.F) {
	f.Add("line1\nline2\nline3\nline4\nline5", 3, 64)
	f.Add("short", 1, 10)
	f.Add("", 5, 100)
	f.Add("a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk", 2, 8)

	f.Fuzz(func(t *testing.T, lines string, halfCap, perLineCap int) {
		if halfCap < 1 || halfCap > 100 {
			return
		}
		if perLineCap < 1 || perLineCap > 1024 {
			return
		}
		r := newLineRing(halfCap, perLineCap)
		var pushed int
		for _, line := range splitLines(lines) {
			r.Push(line)
			pushed++
		}
		sample := r.Sample()
		// Sample length must not exceed 2 * halfCap.
		if len(sample) > 2*halfCap {
			t.Errorf("sample length %d exceeds 2*halfCap=%d", len(sample), 2*halfCap)
		}
		// Every line in sample must respect perLineCap.
		for _, s := range sample {
			if len(s) > perLineCap {
				t.Errorf("sample line length %d exceeds perLineCap=%d", len(s), perLineCap)
			}
		}
		// If pushed <= 2*halfCap, sample should contain all pushed lines (truncated).
		if pushed <= 2*halfCap && len(sample) != pushed {
			t.Errorf("pushed %d <= 2*halfCap=%d but sample has %d entries",
				pushed, 2*halfCap, len(sample))
		}
	})
}

// splitLines splits on newline without importing strings to keep the
// test self-contained.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	var lines []string
	start := 0
	for i := range len(s) {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
