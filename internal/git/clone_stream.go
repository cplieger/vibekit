// The streaming transfer runner: a clone's liveness is measured from git's
// own progress stream rather than a wall clock. A fixed budget is wrong in
// both directions for a network transfer — it kills a large repo that is
// downloading fine (a 511 MB clone measured 8 minutes) and it waits out the
// whole budget on a transfer that died in its first second. git reports
// progress continuously on stderr with --progress, so the honest liveness
// signal is that stream: keep going while data arrives, kill on stall.

package git

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"time"
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
	// The transfer runs in its own process group and the kill targets the
	// GROUP: git spawns helpers (git-remote-https carries the actual
	// transfer), and a head-only kill leaves the helper holding the stderr
	// pipe open — so the read loop below would block out the very stall
	// the watchdog just detected.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	stdout := &cappedBuffer{cap: 64 * 1024}
	cmd.Stdout = stdout
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}

	// The watchdog kills the subprocess (via the context) when no stderr
	// token has arrived for cloneStallTimeout. Reset through a coalescing
	// channel rather than timer.Reset from the read loop, so the reset and
	// the expiry cannot race.
	activity := make(chan struct{}, 1)
	watchdogDone := make(chan struct{})
	defer close(watchdogDone)
	go func() {
		timer := time.NewTimer(cloneStallTimeout)
		defer timer.Stop()
		for {
			select {
			case <-watchdogDone:
				return
			case <-activity:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(cloneStallTimeout)
			case <-timer.C:
				cancel(errCloneStalled)
				return
			}
		}
	}()

	// git's progress updates are \r-separated within a phase and
	// \n-separated between phases; both are token boundaries here.
	var tail []string
	sc := bufio.NewScanner(stderr)
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024)
	sc.Split(scanProgressTokens)
	for sc.Scan() {
		select {
		case activity <- struct{}{}:
		default:
		}
		token := strings.TrimSpace(sc.Text())
		if token == "" {
			continue
		}
		tail = append(tail, token)
		if len(tail) > 32 {
			tail = tail[1:]
		}
		if onProgress != nil {
			onProgress(token)
		}
	}
	waitErr := cmd.Wait()
	if waitErr != nil {
		cause := context.Cause(tctx)
		switch {
		case errors.Is(cause, errCloneStalled):
			return "", fmt.Errorf("the transfer stalled: no progress from git for %s", cloneStallTimeout)
		case errors.Is(cause, errCloneCeiling):
			return "", errCloneCeiling
		}
	}
	out := strings.TrimSpace(stdout.String() + "\n" + strings.Join(tail, "\n"))
	return out, waitErr
}

// scanProgressTokens is a bufio.SplitFunc yielding tokens separated by \r
// OR \n — git rewrites a phase's progress line in place with \r and ends
// it with \n, and both mark a complete token.
func scanProgressTokens(data []byte, atEOF bool) (int, []byte, error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// cappedBuffer keeps the first cap bytes written and reports the rest as
// written, so a flooding subprocess cannot grow the buffer unboundedly.
type cappedBuffer struct {
	buf bytes.Buffer
	cap int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.cap - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string { return c.buf.String() }
