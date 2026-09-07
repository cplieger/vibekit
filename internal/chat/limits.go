package chat

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/cplieger/atomicfile/v3"
)

// cgroupMemMaxV2 and cgroupMemMaxV1 are the cgroup files the chat-file cap is
// derived from, v2 first. Package vars so a test can point them at a fixture.
var (
	cgroupMemMaxV2 = "/sys/fs/cgroup/memory.max"
	cgroupMemMaxV1 = "/sys/fs/cgroup/memory/memory.limit_in_bytes"
)

// memLimitDivisor: one chat at the cap costs several times its own bytes to
// serve. Measured on go1.27.0 against a 33,551,470-byte chat, opening it took
// HeapSys to 2.73x the file and writeChat's MarshalIndent to 7.85x, so L/32
// peaks at about a quarter of the limit and leaves room for the bridge and the
// SSE fan-out. L = 1 GiB reproduces the 32 MiB constant this store shipped with.
//
// minChatFileCap: the floor a small container gets. 8 MiB is above the median
// chat here (1.65 MB over 53 files) and peaks near 63 MB on a write.
const (
	memLimitDivisor  = 32
	minChatFileCap   = 8 << 20
	implausibleLimit = 1 << 62
)

// maxHeaderScanBytes bounds ONE streaming header scan, independently of the chat
// cap and so in force when that cap is unlimited; without it readHeadersParallel
// would stream a hostile file of any size, eight at a time.
//
// The scan holds no message bytes, so its cost is sequential I/O: 512 MiB bounds
// one sidebar refresh to 4 GiB of reads. It is also what a 16 GiB container
// derives, the top of the plausible range, so no capped deployment can write a
// chat its own header scan would refuse. A file over it is refused loudly.
const maxHeaderScanBytes = 512 << 20

// chatFileCap is the per-chat-file byte cap: the read bound, the write bound,
// and 0 for UNLIMITED.
//
// UNLIMITED is the live path on this deployment, not a fallback: the container
// declares memory.max = "max".
type chatFileCap int64

// unlimited reports whether no cap applies.
func (c chatFileCap) unlimited() bool { return c <= 0 }

// readBound is the maxBytes to hand atomicfile.ReadBoundedFile for a file that
// measured size bytes at open.
//
// ReadBoundedFile has NO unlimited mode — maxBytes <= 0 refuses everything, and
// the write side's "n <= 0 means no cap" has no read-side twin — so an unlimited
// cap bounds by the file's own measured size. That keeps the grow-during-read
// refusal live where dropping the call would give up the TOCTOU guard too.
func (c chatFileCap) readBound(size int64) int64 {
	if c.unlimited() {
		return size
	}
	return int64(c)
}

// resolveChatFileCap derives the cap from the container's own memory limit and
// logs the outcome with the signal it came from.
//
// HOST RAM IS NOT READ. It is shared with every other container and is not this
// process's to claim, so the only honest signal is the limit the operator set
// on this cgroup. No limit means no cap: vibekit does not invent a bound the
// operator declined to set, and a refused write loses a turn (writeChat).
func resolveChatFileCap() chatFileCap {
	limit, signal := readMemLimit()
	if limit <= 0 {
		slog.Info("chat store: no container memory limit; chat files are uncapped",
			"signal", signal)
		return 0
	}
	capBytes := max(limit/memLimitDivisor, minChatFileCap)
	slog.Info("chat store: derived chat file cap from the container memory limit",
		"signal", signal, "limit_bytes", limit, "divisor", memLimitDivisor,
		"floor_bytes", minChatFileCap, "cap_bytes", capBytes)
	return chatFileCap(capBytes)
}

// readMemLimit returns the cgroup memory limit in bytes, or 0 when the
// container is unlimited, plus the signal that decided it (for the boot log).
func readMemLimit() (limitBytes int64, signal string) {
	if v, err := os.ReadFile(cgroupMemMaxV2); err == nil {
		return parseMemLimit(strings.TrimSpace(string(v))), "cgroup v2 " + cgroupMemMaxV2
	}
	v, err := os.ReadFile(cgroupMemMaxV1)
	if err != nil {
		return 0, "no cgroup memory file readable"
	}
	return parseMemLimit(strings.TrimSpace(string(v))), "cgroup v1 " + cgroupMemMaxV1
}

// parseMemLimit turns a cgroup memory-limit value into bytes, or 0 for the
// spellings of "no limit": v2's literal "max", v1's near-top-of-int64 sentinel
// (9223372036854771712 here, whose L/32 would be a 288 PiB cap in name only),
// a non-positive value, and anything unparseable — an unreadable signal is not
// authority to bound anything.
func parseMemLimit(raw string) int64 {
	if raw == "" || raw == "max" {
		return 0
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 || n >= implausibleLimit {
		return 0
	}
	return n
}

// WithChatFileCap overrides the derived per-chat-file cap. n <= 0 means
// unlimited, matching the derivation's own encoding. Tests inject a small cap
// so the refusal paths are reachable without allocating hundreds of MiB.
func WithChatFileCap(n int64) StoreOption {
	return func(s *Store) { s.fileCap = chatFileCap(n) }
}

// errFileTooLarge reports a read refused on size. The streaming header paths
// check the size themselves rather than through atomicfile.ReadBoundedFile, so
// they wrap ITS sentinel: one errors.Is(err, atomicfile.ErrFileTooLarge) then
// answers for every size refusal in this package, read and write alike.
func errFileTooLarge(label string, size, capBytes int64) error {
	return fmt.Errorf("%s: %w: %d bytes (max %d)", label, atomicfile.ErrFileTooLarge, size, capBytes)
}
