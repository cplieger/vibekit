// The streaming transfer runner: a clone's liveness is measured from git's
// own progress stream rather than a wall clock. A fixed budget is wrong in
// both directions for a network transfer — it kills a large repo that is
// downloading fine (a 511 MB clone measured 8 minutes) and it waits out the
// whole budget on a transfer that died in its first second. git reports
// progress continuously on stderr with --progress, so the honest liveness
// signal is that stream: keep going while data arrives, kill on stall.

package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/cplieger/vibekit/internal/logsafe"
)

// cloneCeiling bounds the WHOLE clone operation, stall detection included:
// a hostile or broken remote could drip progress forever, and the request
// deserves an end. Generous on purpose — the stall watchdog is what does
// the real work, so this only has to be longer than any legitimate clone.
const cloneCeiling = 60 * time.Minute

// errCloneCeiling is cloneCeiling's context cause, so a kill at the
// ceiling names itself instead of reading as a generic deadline.
var errCloneCeiling = errors.New("the transfer exceeded the 60-minute ceiling")

// cloneStallTimeout is how long a transfer may go without git reporting
// ANY progress before it is killed. git emits progress many times a second
// while data moves and during the remote's counting/compressing phases, so
// a quiet stretch this long means the transfer is dead, not slow. A var so
// tests can drive the stall path in milliseconds; never reassigned in
// production.
var cloneStallTimeout = 90 * time.Second

// errCloneStalled is the stall watchdog's context cause.
var errCloneStalled = errors.New("transfer stalled")

// runTransfer runs one git command that moves data over the network,
// reading its stderr as it arrives. Every read feeds the stall watchdog
// and, when onProgress is non-nil, reports the progress token to it.
//
// On an ordinary git failure the returned output carries git's own message
// (the "fatal:" line), exactly like gitCmd. On a stall or ceiling kill the
// output is deliberately EMPTY and the error names the reason: the tail of
// a killed transfer is a progress line ("Receiving objects: 42%"), and
// composing that into an error envelope reads as nonsense.
func runTransfer(ctx context.Context, dir string, onProgress func(string), args ...string) (string, error) {
	if sub, ok := allowedSubcommand(args); !ok {
		return "", fmt.Errorf("git: subcommand not allowed: %s", sub)
	}
	tctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	cmd := gitExec(tctx, dir, args...)
	killWholeGroup(cmd)
	stdout := &cappedBuffer{cap: 64 * 1024}
	cmd.Stdout = stdout
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}

	activity := make(chan struct{}, 1)
	watchdogDone := make(chan struct{})
	defer close(watchdogDone)
	// The stall window is read ONCE, here on the caller's goroutine, and
	// handed to the watchdog as a value: the var exists for tests to
	// shorten, and a goroutine reading it directly races the restore.
	go stallWatchdog(cloneStallTimeout, activity, watchdogDone, cancel)

	tail, readErr := forwardProgress(stderr, activity, onProgress)
	waitErr := cmd.Wait()
	if waitErr != nil {
		if killErr := transferKillReason(tctx); killErr != nil {
			return "", killErr
		}
	}
	// A read that ended early outranks git's own exit status as the DIAGNOSIS: the
	// tail is short for a reason the tail itself cannot state, and reporting the
	// exit code alone sends the reader looking at the remote. It does not outrank a
	// kill reason above, which is more specific still.
	if readErr != nil && waitErr != nil {
		return "", fmt.Errorf("reading git's progress output: %w", readErr)
	}
	out := strings.TrimSpace(stdout.String() + "\n" + strings.Join(tail, "\n"))
	if readErr != nil {
		slog.Warn("git transfer succeeded but its progress output could not be read whole",
			"error", logsafe.Field(readErr.Error()))
	}
	return out, waitErr
}

// killWholeGroup puts the transfer in its own process group and makes the
// context kill target the GROUP: git spawns helpers (git-remote-https
// carries the actual transfer), and a head-only kill leaves the helper
// holding the stderr pipe open — so the progress read loop would block out
// the very stall the watchdog just detected.
func killWholeGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// stallWatchdog cancels the transfer when no activity arrives for stall.
// Reset rides a coalescing channel rather than a timer.Reset from the read
// loop, so the reset and the expiry cannot race.
func stallWatchdog(stall time.Duration, activity, done <-chan struct{}, cancel context.CancelCauseFunc) {
	timer := time.NewTimer(stall)
	defer timer.Stop()
	for {
		select {
		case <-done:
			return
		case <-activity:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(stall)
		case <-timer.C:
			cancel(errCloneStalled)
			return
		}
	}
}

// forwardProgress drains the transfer's stderr, feeding the watchdog on every
// token and forwarding each to onProgress. Returns the last tokens seen, which on
// an ordinary failure carry git's own message, and a read error.
//
// A bufio.Scanner is deliberately NOT used, and the swap is the same one
// internal/bridge/bridge_frame.go made for the same reason: bufio.ErrTooLong is
// terminal for the Scanner that raised it, so one stderr token over the cap ended
// this loop permanently — the watchdog then saw no activity, killed the group at
// the stall timeout, and told the user "the transfer stalled: no progress from git
// for 1m30s" for a remote that had merely printed one long line. It also left the
// only cut in this package that could land at an ARBITRARY byte offset rather than
// on a terminator, which is the cut a credential pattern can straddle
// (redactCredentials states why every cut here must be a line boundary).
//
// So an oversize token is drained to its terminator and reported as truncated,
// the stream resynchronises on a real boundary, and only a blob that never
// terminates at all exhausts the budget.
func forwardProgress(stderr io.Reader, activity chan<- struct{}, onProgress func(string)) ([]string, error) {
	tail := &progressTail{onToken: onProgress, activity: activity}
	pr := &progressReader{r: stderr}
	for {
		token, truncated, err := pr.readToken()
		tail.add(token, truncated)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return tail.tokens, nil
			}
			return tail.tokens, err
		}
	}
}

// progressTail keeps the last progressTailLen tokens and does the two things every
// token owes: feed the stall watchdog, and reach onToken. Its own type because
// forwardProgress is otherwise one loop wrapping five concerns.
type progressTail struct {
	onToken  func(string)
	activity chan<- struct{}
	tokens   []string
}

// progressTailLen bounds the tokens kept for the transfer's error envelope. git's
// own failure message is the last few lines, so this only has to outlive the
// progress noise printed after it.
const progressTailLen = 32

// add records one token, ignoring the empty one a truncated or blank read yields.
// Activity is reported on a NON-blocking send: the watchdog coalesces, so a missed
// poke is a poke it was already going to get.
func (t *progressTail) add(token string, truncated bool) {
	if token == "" {
		return
	}
	select {
	case t.activity <- struct{}{}:
	default:
	}
	if truncated {
		token += progressTruncated
	}
	t.tokens = append(t.tokens, token)
	if len(t.tokens) > progressTailLen {
		t.tokens = t.tokens[1:]
	}
	if t.onToken != nil {
		t.onToken(token)
	}
}

// progressTokenCap bounds ONE progress token. git's own progress lines are tens
// of bytes; this is generous enough that a legitimately chatty remote always fits.
const progressTokenCap = 64 * 1024

// progressDrainCap bounds the bytes discarded while draining one oversize token,
// mirroring bridge_frame.go's ratio and its reasoning: the budget is per TOKEN and
// in BYTES rather than a count of oversize tokens, because each drain provably
// ends on a terminator, so a remote emitting many long-but-terminated lines keeps
// getting a fresh budget. Only a single blob that never terminates exhausts it.
const progressDrainCap = 16 * progressTokenCap

// progressTruncated marks a token the cap cut, so a reader can tell it from a
// complete one.
const progressTruncated = "[truncated]"

// errProgressDrainExhausted says a single stderr token consumed the whole drain
// budget without a terminator, so there is no boundary left to resynchronise on.
var errProgressDrainExhausted = errors.New("git progress output did not terminate within the drain budget")

// progressReader tokenizes an io.Reader on '\r' OR '\n' — git rewrites a phase's
// progress line in place with '\r' and ends it with '\n', and both mark a
// complete token, which is why this cannot be a bufio.Reader.ReadSlice loop
// (ReadSlice takes ONE delimiter, and waiting for '\n' would swallow a whole
// phase's worth of in-place rewrites and starve the stall watchdog).
//
// Not safe for concurrent use: one goroutine owns it for the transfer's life.
type progressReader struct {
	r io.Reader
	// pending holds bytes read but not yet tokenized. Bounded by the cap plus one
	// read window, because crossing the cap switches to draining and empties it.
	pending []byte
	dropped int
	window  [16 << 10]byte
	// draining is set once the token in progress crossed the cap: the bytes are
	// counted and discarded until a terminator arrives.
	draining bool
	eof      bool
}

// readToken returns the next token with its terminator stripped, whether the cap
// cut it, and a terminal error (io.EOF at end of stream, a read error, or
// errProgressDrainExhausted).
//
// A token whose cap was crossed returns ("", true, nil) — the accumulated prefix
// is dropped rather than reported, for bridge_frame.go's reason: it is a cut at an
// arbitrary offset, so it can split a multi-byte rune AND a credential pattern.
// The caller marks the loss without claiming the bytes.
func (pr *progressReader) readToken() (token string, truncated bool, err error) {
	for {
		// The terminator decides first, and the cap is applied to the token it
		// bounds: checking the cap only against UNTERMINATED bytes lets a token
		// whose terminator lands in the same read as its last chunk slip past,
		// however long it is.
		if i := bytes.IndexAny(pr.pending, "\r\n"); i >= 0 {
			tok := pr.pending[:i]
			pr.pending = pr.pending[i+1:]
			return pr.finish(tok, nil)
		}
		if pr.eof {
			// A trailing unterminated token at end of stream is still git's own
			// message on an ordinary failure, so it is reported rather than dropped.
			tok := pr.pending
			pr.pending = nil
			return pr.finish(tok, io.EOF)
		}
		if dropErr := pr.dropOversizePrefix(); dropErr != nil {
			return "", true, dropErr
		}
		if readErr := pr.fill(); readErr != nil {
			return "", false, readErr
		}
	}
}

// finish grades one complete token against the cap and clears any drain state, so
// the terminator arm and the end-of-stream arm cannot disagree about what counts as
// oversize. term is the terminal error to report alongside it, nil mid-stream.
func (pr *progressReader) finish(tok []byte, term error) (token string, truncated bool, err error) {
	over := pr.draining || len(tok) > progressTokenCap
	pr.draining, pr.dropped = false, 0
	if over {
		return "", true, term
	}
	return strings.TrimSpace(string(tok)), false, term
}

// dropOversizePrefix abandons the token in progress once it crosses the cap with no
// terminator in hand, counting the bytes against the drain budget. The prefix is
// dropped rather than reported for bridge_frame.go's reason — it is a cut at an
// arbitrary offset, so it can split a multi-byte rune AND a credential pattern.
func (pr *progressReader) dropOversizePrefix() error {
	if len(pr.pending) <= progressTokenCap {
		return nil
	}
	pr.draining = true
	pr.dropped += len(pr.pending)
	pr.pending = pr.pending[:0]
	if pr.dropped > progressDrainCap {
		pr.draining, pr.dropped = false, 0
		return errProgressDrainExhausted
	}
	return nil
}

// fill reads one window into pending. An end of stream is RECORDED rather than
// returned: the bytes already in hand are a token the caller still owes its reader,
// and readToken's own eof arm is what reports it once they are handed over.
func (pr *progressReader) fill() error {
	n, err := pr.r.Read(pr.window[:])
	pr.pending = append(pr.pending, pr.window[:n]...)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, io.EOF):
		pr.eof = true
		return nil
	default:
		return err
	}
}

// transferKillReason names WHY the runner killed the transfer, nil when
// the failure was git's own.
func transferKillReason(tctx context.Context) error {
	cause := context.Cause(tctx)
	switch {
	case errors.Is(cause, errCloneStalled):
		return fmt.Errorf("the transfer stalled: no progress from git for %s", cloneStallTimeout)
	case errors.Is(cause, errCloneCeiling):
		return errCloneCeiling
	}
	return nil
}

// cappedBuffer keeps the first cap bytes written and reports the rest as
// written, so a flooding subprocess cannot grow the buffer unboundedly.
//
// The retained text ends on a LINE BOUNDARY, and that is a correctness property
// rather than tidiness. Its contents are composed into the transfer's output and
// then run through the credential redactor, whose three patterns each match
// within one line — `scheme://user:pwd@host` needs the '@' to the RIGHT of the
// '://' it anchors on. A cut at an arbitrary byte offset can land between them,
// and then the redactor sees a URL with no userinfo and passes the credential
// through. Truncating where a pattern cannot straddle turns git's own
// one-line-per-URL habit into an invariant the redactor can rely on, instead of
// a coincidence. It is also less mechanism than an end-anchored second pattern,
// which would have to redact the host of every legitimately unterminated URL.
//
// A cut is MARKED, so a reader can tell truncated output from complete output —
// the shape internal/bridge/bridge_process.go already uses for a bounded stderr
// line. Bytes past the cap are reported as written: this is a subprocess's
// stdout sink, and a short write would make exec kill the process with an I/O
// error rather than let it finish.
type cappedBuffer struct {
	buf bytes.Buffer
	cap int
	// dropped records that at least one byte did not fit, so String can say so.
	dropped bool
}

// cappedBufferTruncated is appended to a cappedBuffer whose input did not fit.
const cappedBufferTruncated = "[output truncated]"

func (c *cappedBuffer) Write(p []byte) (int, error) {
	room := c.cap - c.buf.Len()
	if room <= 0 {
		c.dropped = len(p) > 0 || c.dropped
		return len(p), nil
	}
	if len(p) <= room {
		c.buf.Write(p)
		return len(p), nil
	}
	// Keep only up to the last newline that fits. A chunk with no newline in the
	// room available contributes NOTHING rather than a partial line: retaining
	// the head of it is exactly the arbitrary-offset cut this type exists to
	// avoid, and a caller reading a marked-truncated buffer loses no information
	// it could have trusted.
	fits := p[:room]
	if i := bytes.LastIndexByte(fits, '\n'); i >= 0 {
		c.buf.Write(fits[:i+1])
	}
	c.dropped = true
	return len(p), nil
}

func (c *cappedBuffer) String() string {
	if !c.dropped {
		return c.buf.String()
	}
	return c.buf.String() + cappedBufferTruncated
}
