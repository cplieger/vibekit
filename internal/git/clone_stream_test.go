package git

import (
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
)

// TestCappedBuffer_CutsOnALineBoundarySoARedactionPatternCannotStraddleIt is the
// test for the leak class, and the composition is the subject rather than either
// half: runTransfer composes this buffer's contents into its output and a caller
// then runs redactCredentials over the result, so a cut that lands INSIDE a credential
// URL leaves redactCredentials looking at a URL with no userinfo left to match.
//
// The payload puts the 64-byte boundary in the middle of the userinfo, which is
// the shape a byte cut mishandles and a line cut cannot produce.
func TestCappedBuffer_CutsOnALineBoundarySoARedactionPatternCannotStraddleIt(t *testing.T) {
	const secret = "s3cr3t-token-value"
	url := "https://user:" + secret + "@example.com/org/repo.git"
	// One complete line, then a second line whose credential straddles the cap.
	first := "Cloning into 'repo'...\n"
	c := &cappedBuffer{cap: len(first) + len("fatal: could not read from https://user:s3cr3")}
	if _, err := c.Write([]byte(first + "fatal: could not read from " + url + "\n")); err != nil {
		t.Fatal(err)
	}

	got := redactCredentials(c.String())
	// The assertion is on the USERINFO surviving, not on the whole secret: a byte
	// cut inside the credential leaves a PREFIX of it ("https://user:s3cr3"),
	// which is still a leak and which a whole-secret check cannot see. What the
	// redactor guarantees is that no unredacted userinfo reaches a sink, so that
	// is what to assert.
	if strings.Contains(got, "user:") {
		t.Errorf("the retained output leaks userinfo after redaction: %q", got)
	}
	if strings.Contains(got, secret) {
		t.Errorf("the retained output leaks the whole credential after redaction: %q", got)
	}
	if !strings.Contains(got, first) {
		t.Errorf("the retained output dropped the complete line that fit: %q", got)
	}
	if !strings.Contains(got, cappedBufferTruncated) {
		t.Errorf("a truncated buffer must say so, so a reader can tell it from complete output: %q", got)
	}
}

func TestCappedBuffer_KeepsCompleteInputUnmarked(t *testing.T) {
	c := &cappedBuffer{cap: 64}
	in := "one\ntwo\n"
	if _, err := c.Write([]byte(in)); err != nil {
		t.Fatal(err)
	}
	if got := c.String(); got != in {
		t.Errorf("cappedBuffer.String() = %q, want the input verbatim %q", got, in)
	}
}

// A chunk with no newline in the room available contributes nothing: retaining
// its head is exactly the arbitrary-offset cut the type exists to avoid.
func TestCappedBuffer_DropsAPartialLineWholeAndReportsEveryByteWritten(t *testing.T) {
	c := &cappedBuffer{cap: 8}
	payload := []byte("no newline here at all")
	n, err := c.Write(payload)
	if err != nil {
		t.Fatal(err)
	}
	// Every byte must be reported as written, or exec kills the subprocess with
	// an I/O error instead of letting it finish.
	if n != len(payload) {
		t.Errorf("Write reported %d of %d bytes; a short write kills the subprocess", n, len(payload))
	}
	if got := c.String(); got != cappedBufferTruncated {
		t.Errorf("cappedBuffer.String() = %q, want the marker alone", got)
	}
}

// Writes arrive in chunks that need not align with lines, so the cap and the
// boundary rule have to hold across several calls.
func TestCappedBuffer_HoldsTheBoundaryAcrossWrites(t *testing.T) {
	c := &cappedBuffer{cap: 12}
	for _, chunk := range []string{"aa\n", "bb\n", "cccccccccc\n", "dd\n"} {
		if _, err := c.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	got := c.String()
	if !strings.HasPrefix(got, "aa\nbb\n") {
		t.Errorf("cappedBuffer.String() = %q, want the lines that fit first", got)
	}
	if strings.Contains(got, "cccc") {
		t.Errorf("cappedBuffer.String() = %q, want no partial line retained", got)
	}
	if !strings.HasSuffix(got, cappedBufferTruncated) {
		t.Errorf("cappedBuffer.String() = %q, want the truncation marker", got)
	}
}

// TestProgressReader_TokenizesOnEitherTerminator pins the reason this is not a
// bufio.Reader.ReadSlice loop: git rewrites a phase's line in place with '\r' and
// ends it with '\n', so waiting for '\n' would swallow a whole phase of in-place
// rewrites and starve the stall watchdog of the very activity it measures.
func TestProgressReader_TokenizesOnEitherTerminator(t *testing.T) {
	pr := &progressReader{r: strings.NewReader("Counting: 10%\rCounting: 100%\rdone.\nfatal: early EOF\n")}
	var got []string
	for {
		tok, truncated, err := pr.readToken()
		if truncated {
			t.Errorf("token %q reported truncated, want a complete read", tok)
		}
		if tok != "" {
			got = append(got, tok)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatalf("readToken: %v", err)
			}
			break
		}
	}
	want := []string{"Counting: 10%", "Counting: 100%", "done.", "fatal: early EOF"}
	if !slices.Equal(got, want) {
		t.Errorf("tokens = %q, want %q", got, want)
	}
}

// TestProgressReader_SurvivesAnOversizeToken is the defect the Scanner could not
// survive: bufio.ErrTooLong is terminal, so one token over the cap ended the read
// permanently, the watchdog starved, and the transfer was killed and reported as
// "stalled" — a false diagnosis for a remote that printed one long line.
//
// The oversize token is reported as truncated, and the stream RESYNCHRONISES: the
// token after it must arrive intact. Both cases below are oversize; they differ in
// WHERE the terminator lands relative to the read that crosses the cap, which is
// what selects between the reader's two paths — and each path had its own defect.
func TestProgressReader_SurvivesAnOversizeToken(t *testing.T) {
	tests := map[string]struct{ length int }{
		// Terminator inside the SAME read as the byte that crosses the cap, so the
		// cap must be applied to the token the terminator BOUNDS. Checking it only
		// against unterminated bytes let a token of any length through here.
		"terminated within the crossing read": {length: progressTokenCap + 4096},
		// Terminator only in a LATER read, so the reader enters its draining state:
		// the prefix is abandoned, the bytes are counted, and the state must be
		// CLEARED at the terminator or every later token reads as truncated too.
		"terminated in a later read": {length: 2 * progressTokenCap},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			pr := &progressReader{r: strings.NewReader("first\r" + strings.Repeat("x", test.length) + "\rlast\n")}

			tok, truncated, err := pr.readToken()
			if tok != "first" || truncated || err != nil {
				t.Fatalf("first token = (%q, %v, %v), want (\"first\", false, nil)", tok, truncated, err)
			}
			// The oversize one: no bytes claimed (a cut at an arbitrary offset can
			// split a rune AND a credential pattern), but the loss is reported.
			tok, truncated, err = pr.readToken()
			if tok != "" || !truncated || err != nil {
				t.Fatalf("oversize token = (%q, %v, %v), want (\"\", true, nil)", tok, truncated, err)
			}
			tok, truncated, err = pr.readToken()
			if tok != "last" || truncated {
				t.Fatalf("token after the drain = (%q, %v, %v), want (\"last\", false, …) — the stream did not resynchronise", tok, truncated, err)
			}
		})
	}
}

// TestProgressReader_ExhaustsTheDrainBudgetOnAnUnterminatedBlob is the other end:
// a blob that never terminates leaves no boundary to resynchronise on, so it is
// declared garbage rather than read forever.
func TestProgressReader_ExhaustsTheDrainBudgetOnAnUnterminatedBlob(t *testing.T) {
	pr := &progressReader{r: strings.NewReader(strings.Repeat("y", progressDrainCap+progressTokenCap+1))}
	var err error
	for range 1000 {
		if _, _, err = pr.readToken(); err != nil {
			break
		}
	}
	if !errors.Is(err, errProgressDrainExhausted) {
		t.Errorf("readToken over an unterminated blob = %v, want errProgressDrainExhausted", err)
	}
}

// TestProgressReader_ReportsAReadError is what the Scanner discarded: sc.Err()
// was never checked, so a stderr read that failed mid-transfer was indistinguishable
// from a clean end of stream, and the caller reported git's exit status instead.
func TestProgressReader_ReportsAReadError(t *testing.T) {
	boom := errors.New("pipe went away")
	pr := &progressReader{r: io.MultiReader(strings.NewReader("Counting: 1%\r"), errReader{boom})}

	if tok, _, err := pr.readToken(); tok != "Counting: 1%" || err != nil {
		t.Fatalf("first token = (%q, %v), want the token and no error", tok, err)
	}
	if _, _, err := pr.readToken(); !errors.Is(err, boom) {
		t.Errorf("readToken after a failed read = %v, want the read error", err)
	}
}

// TestForwardProgress_ReportsTheReadErrorAndKeepsTheTail composes the two: the
// tokens seen before the failure are still git's own message, so they are returned
// alongside the error rather than discarded with it.
func TestForwardProgress_ReportsTheReadErrorAndKeepsTheTail(t *testing.T) {
	boom := errors.New("pipe went away")
	activity := make(chan struct{}, 8)
	tail, err := forwardProgress(
		io.MultiReader(strings.NewReader("remote: Counting\rfatal: bad object\n"), errReader{boom}),
		activity, nil,
	)
	if !errors.Is(err, boom) {
		t.Errorf("forwardProgress err = %v, want the read error", err)
	}
	if !slices.Contains(tail, "fatal: bad object") {
		t.Errorf("tail = %q, want git's own message kept despite the error", tail)
	}
	if len(activity) == 0 {
		t.Error("no activity was reported, so the stall watchdog would have killed a live transfer")
	}
}

// errReader fails every read, for the read-error paths above.
type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }
