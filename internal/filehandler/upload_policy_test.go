package filehandler

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// clientPolicyPath is the TypeScript module holding the composer's copy of the
// upload policy. Relative to this package's directory, which is the cwd of a
// Go test.
const clientPolicyPath = "../../static-src/upload-policy.ts"

var (
	// MAX_UPLOAD_BYTES = 50 * 1024 * 1024;
	clientBytesRe = regexp.MustCompile(
		`MAX_UPLOAD_BYTES\s*=\s*(\d+)\s*\*\s*(\d+)\s*\*\s*(\d+)\s*;`)
	// UPLOADS_DIR = "/workspace/uploads";
	clientDirRe = regexp.MustCompile(`UPLOADS_DIR\s*=\s*"([^"]*)"\s*;`)
	// MULTIPART_RESERVE_BYTES = 1024 * 1024;
	clientReserveRe = regexp.MustCompile(
		`MULTIPART_RESERVE_BYTES\s*=\s*(\d+)\s*\*\s*(\d+)\s*;`)
	// MAX_UPLOAD_TOTAL_BYTES = MAX_UPLOAD_BYTES - MULTIPART_RESERVE_BYTES;
	clientTotalRe = regexp.MustCompile(
		`MAX_UPLOAD_TOTAL_BYTES\s*=\s*MAX_UPLOAD_BYTES\s*-\s*MULTIPART_RESERVE_BYTES\s*;`)
)

// productOf multiplies a regexp's numeric submatches, so `50 * 1024 * 1024`
// and `1024 * 1024` both read as one value.
func productOf(t *testing.T, name string, factors [][]byte) int {
	t.Helper()
	got := 1
	for _, factor := range factors {
		n, err := strconv.Atoi(string(factor))
		if err != nil {
			t.Fatalf("%s factor %q: %v", name, factor, err)
		}
		got *= n
	}
	return got
}

// The client duplicates two upload constants it cannot fetch: the per-file
// byte ceiling and the composer's target directory. Both are compile-time
// facts with no runtime input, so an endpoint to report them would be a
// request per boot to learn numbers that cannot change without a rebuild.
// This test is what makes the duplication safe: Go stays the single
// definition and a change on either side fails CI here rather than surfacing
// as a 413 the client should have predicted, or an upload landing somewhere
// the attachment path does not name.
func TestUploadPolicyMatchesClient(t *testing.T) {
	src, err := os.ReadFile(clientPolicyPath)
	if err != nil {
		t.Fatalf("read %s: %v", clientPolicyPath, err)
	}

	t.Run("byte ceiling", func(t *testing.T) {
		m := clientBytesRe.FindSubmatch(src)
		if m == nil {
			t.Fatalf("no MAX_UPLOAD_BYTES literal in %s (pattern %s)",
				clientPolicyPath, clientBytesRe)
		}
		if got := productOf(t, "MAX_UPLOAD_BYTES", m[1:]); got != maxUploadSize {
			t.Errorf("MAX_UPLOAD_BYTES in %s = %d, want maxUploadSize = %d",
				clientPolicyPath, got, maxUploadSize)
		}
	})

	// The pre-flight's budget is a DERIVED number, and this is the half of the
	// contract that lives on the Go side: maxUploadSize bounds the whole
	// MULTIPART BODY (http.MaxBytesReader in handleUpload), and multipart
	// framing is part of that body, so a client that accepted a file of exactly
	// maxUploadSize was promising a request the server must refuse. What has to
	// hold is that the client enforces something strictly SMALLER, by a margin
	// that covers the framing.
	t.Run("pre-flight budget is below the whole-request ceiling", func(t *testing.T) {
		if !clientTotalRe.Match(src) {
			t.Fatalf("no MAX_UPLOAD_TOTAL_BYTES derivation in %s (pattern %s): the client must "+
				"enforce a total below maxUploadSize, not maxUploadSize itself",
				clientPolicyPath, clientTotalRe)
		}
		m := clientReserveRe.FindSubmatch(src)
		if m == nil {
			t.Fatalf("no MULTIPART_RESERVE_BYTES literal in %s (pattern %s)",
				clientPolicyPath, clientReserveRe)
		}
		reserve := productOf(t, "MULTIPART_RESERVE_BYTES", m[1:])
		// A part costs a boundary line plus two headers, and the longest legal
		// filename is 255 bytes; 64 KiB covers that for every file the client
		// will send in one request, with room to spare.
		const minReserve = 64 * 1024
		if reserve < minReserve {
			t.Errorf("MULTIPART_RESERVE_BYTES = %d, want at least %d to cover multipart framing",
				reserve, minReserve)
		}
		if reserve >= maxUploadSize {
			t.Errorf("MULTIPART_RESERVE_BYTES = %d leaves nothing of maxUploadSize = %d",
				reserve, maxUploadSize)
		}
	})

	t.Run("uploads directory", func(t *testing.T) {
		m := clientDirRe.FindSubmatch(src)
		if m == nil {
			t.Fatalf("no UPLOADS_DIR literal in %s (pattern %s)",
				clientPolicyPath, clientDirRe)
		}
		// Both spellings clean to the same path, which is the property that
		// matters: the client's target and the server's default must name one
		// directory. The client's must be absolute for a second reason of its
		// own (it doubles as the attachment path prefix), so compare the
		// cleaned forms rather than the raw strings.
		got := filepath.Clean("/" + string(m[1]))
		want := filepath.Clean("/" + defaultUploadDir)
		if got != want {
			t.Errorf("UPLOADS_DIR in %s = %q (cleans to %q), want defaultUploadDir %q (cleans to %q)",
				clientPolicyPath, m[1], got, defaultUploadDir, want)
		}
	})
}
