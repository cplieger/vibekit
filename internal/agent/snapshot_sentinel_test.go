package agent

// The cross-language pin on ONE value: how a client says "no chat needs its
// in-flight transcript". A query parameter is not a wire payload, so wiregen never
// sees it and no generated type keeps the two halves honest.
//
// The failure it prevents is silent in both directions. A client spelling it `off`
// declares a chat named `off`, so every busy chat gets a snapshot again — the 265 KB
// connect, with no error anywhere. A server renaming it leaves the client's word
// parsed as an id, same outcome.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// clientSentinelSrc is the client leaf that owns the value. A path rather than an
// embed: `//go:embed` cannot reach outside its own package directory, and copying the
// file in would be the second copy this test exists to prevent.
const clientSentinelSrc = "../../static-src/snapshot-declaration.ts"

// clientSentinelRe reads the VALUE rather than the whole line, so prettier moving the
// declaration cannot fail this while a changed word cannot pass it.
var clientSentinelRe = regexp.MustCompile(`SNAPSHOT_NONE\s*=\s*"([^"]*)"`)

func TestSnapshotSentinel_MatchesTheClient(t *testing.T) {
	src, err := os.ReadFile(clientSentinelSrc)
	if err != nil {
		t.Fatalf("read %s: %v", clientSentinelSrc, err)
	}
	m := clientSentinelRe.FindStringSubmatch(string(src))
	if m == nil {
		t.Fatalf("%s declares no SNAPSHOT_NONE the pattern %s can read: the client's half of the "+
			"sentinel is gone or has been renamed", clientSentinelSrc, clientSentinelRe)
	}
	if m[1] != snapshotNone {
		t.Errorf("%s declares SNAPSHOT_NONE = %q, want the server's %q: one of the two now reads "+
			"the other's word as a chat id", clientSentinelSrc, m[1], snapshotNone)
	}
}

// TestSnapshotSentinel_IsSpelledOnceOnTheServer keeps the Go half a single owner too.
// The literal is easy to re-type at a comparison site, and a second copy is what makes
// a rename land in one place and not the other.
func TestSnapshotSentinel_IsSpelledOnceOnTheServer(t *testing.T) {
	src, err := os.ReadFile("sse.go")
	if err != nil {
		t.Fatalf("read sse.go: %v", err)
	}
	quoted := `"` + snapshotNone + `"`
	if got := strings.Count(string(src), quoted); got != 1 {
		t.Errorf("sse.go spells %s %d times, want 1 (its declaration): every other site reads "+
			"snapshotNone", quoted, got)
	}
}
