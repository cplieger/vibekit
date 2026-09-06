package chat

import (
	"os"
	"path/filepath"
	"testing"
)

// pointCgroupAt redirects both cgroup probes at a fixture directory, writing v2
// content when v2 is non-empty and v1 content otherwise. An empty string for a
// file means "this file does not exist", which is one of the shapes the
// derivation has to read as unlimited.
func pointCgroupAt(t *testing.T, v2, v1 string) {
	t.Helper()
	dir := t.TempDir()
	set := func(name, content string) string {
		p := filepath.Join(dir, name)
		if content == "" {
			return p // deliberately absent
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("Setup: write %s: %v", name, err)
		}
		return p
	}
	origV2, origV1 := cgroupMemMaxV2, cgroupMemMaxV1
	cgroupMemMaxV2 = set("memory.max", v2)
	cgroupMemMaxV1 = set("memory.limit_in_bytes", v1)
	t.Cleanup(func() { cgroupMemMaxV2, cgroupMemMaxV1 = origV2, origV1 })
}

// TestResolveChatFileCap pins the whole derivation: which cgroup shape produces a
// cap, which produces UNLIMITED, and that the divisor and the floor are the two
// things deciding the number.
//
// The fixture must be able to OMIT a file, because absence is one of the answers:
// a test that only wrote values could not tell "no limit set" from "no cgroup".
// Serial, not parallel: it swaps the package's cgroup paths.
func TestResolveChatFileCap(t *testing.T) {
	const giB = 1 << 30
	cases := []struct {
		name string
		v2   string
		v1   string
		want chatFileCap
	}{
		// The LIVE path on this deployment: cgroup v2 with no limit set.
		{"v2 literal max is unlimited", "max", "", 0},
		{"v2 max with trailing newline", "max\n", "", 0},
		{"no cgroup file at all is unlimited", "", "", 0},
		// v1 spells no-limit as a sentinel near the top of int64, not as a word
		// and not as zero, so dividing it down would invent a 288 PiB "cap".
		{"v1 int64 sentinel is unlimited", "", "9223372036854771712", 0},
		{"v1 minus one is unlimited", "", "-1", 0},
		{"unparseable is unlimited", "", "not-a-number", 0},
		// A real limit: 1 GiB reproduces the 32 MiB constant this store shipped
		// with, which is the divisor's justification.
		{"v2 one gibibyte derives 32 MiB", "1073741824", "", giB / memLimitDivisor},
		{"v1 one gibibyte derives the same", "", "1073741824", giB / memLimitDivisor},
		{"v2 four gibibytes derive 128 MiB", "4294967296", "", 4 * giB / memLimitDivisor},
		// Below 256 MiB the divisor would go under the floor.
		{"a small container gets the floor", "134217728", "", minChatFileCap},
		{"a tiny container still gets the floor", "16777216", "", minChatFileCap},
		// v2 present and readable wins, so a stale v1 file cannot override it.
		{"v2 wins over v1", "1073741824", "16777216", giB / memLimitDivisor},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pointCgroupAt(t, tc.v2, tc.v1)
			if got := resolveChatFileCap(); got != tc.want {
				t.Errorf("resolveChatFileCap() = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestChatFileCapReadBound pins what an unlimited cap hands
// atomicfile.ReadBoundedFile, which has no unlimited mode of its own: passing 0
// would refuse every non-empty file, and passing the file's measured size keeps
// the grow-during-read refusal live.
func TestChatFileCapReadBound(t *testing.T) {
	cases := []struct {
		name string
		cap  chatFileCap
		size int64
		want int64
	}{
		{"unlimited bounds by the measured size", 0, 4096, 4096},
		{"a negative cap is unlimited too", -1, 4096, 4096},
		{"unlimited on an empty file", 0, 0, 0},
		{"a real cap is the bound", 2048, 4096, 2048},
		{"a real cap applies below it too", 2048, 10, 2048},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cap.readBound(tc.size); got != tc.want {
				t.Errorf("chatFileCap(%d).readBound(%d) = %d, want %d", tc.cap, tc.size, got, tc.want)
			}
		})
	}
}
