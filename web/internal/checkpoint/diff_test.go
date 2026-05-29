package checkpoint

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func BenchmarkCountLineDelta_RepresentativePayloads(b *testing.B) {
	makeFile := func(lines int) []byte {
		parts := make([]string, lines)
		for i := range lines {
			parts[i] = fmt.Sprintf("line %d: content here for benchmarking purposes", i)
		}
		return []byte(strings.Join(parts, "\n") + "\n")
	}

	makeModified := func(original []byte, changed int) []byte {
		lines := strings.Split(strings.TrimSuffix(string(original), "\n"), "\n")
		start := len(lines) / 3
		for i := 0; i < changed && start+i < len(lines); i++ {
			lines[start+i] = fmt.Sprintf("MODIFIED line %d: different content", start+i)
		}
		return []byte(strings.Join(lines, "\n") + "\n")
	}

	cases := []struct {
		name    string
		lines   int
		changed int
	}{
		{"small_50lines_5changed", 50, 5},
		{"medium_500lines_50changed", 500, 50},
		{"large_2000lines_200changed", 2000, 200},
	}

	for _, tc := range cases {
		from := makeFile(tc.lines)
		to := makeModified(from, tc.changed)
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				countLineDelta(context.Background(), from, to)
			}
		})
	}
}
