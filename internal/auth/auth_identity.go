package auth

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/procout"
)

// identityTTL is how long a cached identity is served before a read kicks a
// refresh behind the answer.
//
// The number is set by what the endpoint is FOR: it fills a sidebar row and
// gates the sign-in prompt, both of which change only when the person signs in
// or out — and both of those publish directly, so the timer exists to catch a
// change vibekit did not make (credentials expiring, `kiro-cli logout` run in a
// terminal). A minute is far tighter than that needs and far looser than the
// per-page-load fork it replaces.
const identityTTL = time.Minute

// Reasons the `unavailable` arm carries. A closed set of server-authored
// strings: the client renders one in a retry banner, so a reason built out of
// upstream bytes would be an injection surface. unavailableIdentity is where
// that is enforced.
const (
	reasonNotRead    = "identity not read yet"
	reasonTimedOut   = "kiro-cli timed out"
	reasonCLIMissing = "kiro-cli is not installed"
	reasonCLIFailed  = "kiro-cli failed"
	reasonUnreadable = "kiro-cli output was not recognisable"
)

// signedOutIdentity is the answer for a working kiro-cli that reports nobody
// signed in.
func signedOutIdentity() WhoamiResponse {
	return WhoamiResponse{State: WhoamiSignedOut}
}

// unavailableIdentity is the answer for a kiro-cli that could not be asked, or
// whose answer could not be read.
//
// The reason goes through identityText for the same reason every identity
// string does: a single-line, bounded label is the only shape the sidebar and
// the log can both take safely. Every caller passes a constant today, which is
// exactly why the sanitize belongs HERE — a future reason carrying upstream
// text cannot skip it.
func unavailableIdentity(reason string) WhoamiResponse {
	return WhoamiResponse{State: WhoamiUnavailable, Reason: identityText(reason)}
}

// identityCache is the identity /api/whoami answers from.
//
// Stale-while-revalidate, and it NEVER blocks a reader: the endpoint fires on
// every page load and every SSE reconnect, and the read behind it is a
// kiro-cli fork measured at p50 457 ms with a 4,420-5,002 ms tail and three
// hard 5 s timeouts in 88 calls. So a page load reads memory, and the fork
// happens on a background timer, on login, and on logout — the three moments
// the answer can actually change.
//
// The held value survives invalidation on purpose. A login window invalidates
// the entry so every poll revalidates, and each of those polls still gets the
// last known answer rather than an `unavailable` it would have to render.
type identityCache struct {
	// read performs one identity read. A field rather than a direct call so a
	// test can drive the cache's staleness and coalescing rules without a
	// subprocess, and so the production reader stays one function.
	read func(context.Context) WhoamiResponse
	at   time.Time
	resp WhoamiResponse
	// budget is the wall clock one read gets. Placed after the strings so the GC
	// scan region stops at them (govet fieldalignment).
	budget time.Duration
	// gen counts published identities. A rebuild captures it before forking and
	// discards its answer if it changed, which is what stops a read that started
	// before a logout from republishing the pre-logout identity over it.
	gen  uint64
	mu   sync.Mutex
	busy bool
}

// newIdentityCache returns a cache seeded with the `unavailable` arm, which is
// the honest answer before the first read lands: the server does not yet know,
// which is a different claim from "nobody is signed in".
//
// budget is the wall clock one read gets. It is captured here rather than read
// per call because the cache's readers include a timer with no request behind
// it, so there is no other place the value could come from.
func newIdentityCache(read func(context.Context) WhoamiResponse, budget time.Duration) *identityCache {
	return &identityCache{read: read, budget: budget, resp: unavailableIdentity(reasonNotRead)}
}

// snapshot returns the current identity immediately, kicking a background
// refresh when the entry is stale or has never been read.
func (c *identityCache) snapshot() WhoamiResponse {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.busy && (c.at.IsZero() || time.Since(c.at) >= identityTTL) {
		c.busy = true
		go c.rebuild()
	}
	return c.resp
}

// publish records an identity vibekit itself decided, and marks the entry
// fresh. Used by the logout path, which knows the outcome without asking.
//
// The generation bump is what makes it WIN against a read already in flight.
// The TTL is 60 s and a stale read kicks a refresh, so a page load one second
// before a logout is enough to have a fork running; without the bump that
// fork's pre-logout `signed_in` lands on top of this `signed_out` and the
// sidebar keeps the old identity until the next tick.
func (c *identityCache) publish(resp *WhoamiResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resp = *resp
	c.at = time.Now()
	c.gen++
}

// invalidate marks the entry stale without discarding it, so the next read
// revalidates while still answering with the last known identity.
//
// No generation bump: invalidate asks for a fresh read rather than asserting an
// answer, so an in-flight read is exactly what it wants to land.
//
// This is what makes the login window converge. The browser polls /api/whoami
// every 3 s while the device flow is open, and a fresh 60-second entry would
// answer `signed_out` for the whole of it — so the login that just succeeded
// would look like it never did.
func (c *identityCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = time.Time{}
}

// refresh reads the identity now, in the calling goroutine, and publishes it.
// Coalesces into a refresh already in flight rather than forking a second
// kiro-cli.
func (c *identityCache) refresh() {
	c.mu.Lock()
	if c.busy {
		c.mu.Unlock()
		return
	}
	c.busy = true
	c.mu.Unlock()
	c.rebuild()
}

// rebuild performs one read and publishes it, clearing busy. Entered only with
// busy already claimed.
//
// The context is built here rather than threaded in, and it is deliberately
// detached from any request: a refresh kicked by a page load must outlive that
// page load or the fork is killed before it answers, which is how the caller
// would end up waiting for it after all. WhoamiTimeout bounds it, and
// boundChild kills the process group at the deadline, so the goroutine's exit
// is guaranteed by the budget rather than by a cancel nobody holds.
//
// The generation check is the fence against a publish that landed while the
// fork was running: this read describes the world BEFORE that publish, so it is
// discarded rather than written. busy is still cleared, or the cache would
// never refresh again.
func (c *identityCache) rebuild() {
	c.mu.Lock()
	gen := c.gen
	budget := c.budget
	read := c.read
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	resp := read(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.busy = false
	if c.gen != gen {
		return
	}
	c.resp = resp
	c.at = time.Now()
}

// Run keeps the cached identity warm until ctx is done: one read now, then one
// per identityTTL.
//
// Synchronous by design, so the caller decides the concurrency — the
// composition root runs it as `go authHandler.Run(appCtx)`. The prime is what
// makes the FIRST page load after a restart answer with a real identity
// instead of the `unavailable` seed.
func (h *Handler) Run(ctx context.Context) {
	h.identity.refresh()
	t := time.NewTicker(identityTTL)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.identity.refresh()
		}
	}
}

// readIdentity runs `kiro-cli whoami --format json` and maps the outcome onto
// one of the three arms. Never returns an error: every failure IS an arm, and a
// caller that had to distinguish them would be re-deriving the union.
//
// The three failure kinds are logged separately so each can be alerted on
// independently, and every captured stderr goes through sanitize.Output on the
// way to the log.
func (h *Handler) readIdentity(ctx context.Context) WhoamiResponse {
	// h.cliPath resolves the install manager's active version, never user
	// input — no G204 risk.
	cmd := exec.CommandContext(ctx, h.cliPath(), "whoami", "--format", "json") //nolint:gosec // G204: binary path from config
	// Honour the read budget rather than the child's lifetime — see boundChild.
	boundChild(cmd)
	stderr := procout.NewBuffer(stderrCap)
	stdout := procout.NewBuffer(whoamiMaxOutput)
	cmd.Stderr = stderr
	cmd.Stdout = stdout
	err := cmd.Run()
	out := stdout.Bytes()
	if err != nil {
		return h.identityReadFailure(ctx, err, stderr, len(out))
	}
	info, err := whoamiInfo(out)
	if err != nil {
		slog.Warn("whoami: cli output not parseable as json",
			"error", err, "stdout_bytes", len(out))
		return unavailableIdentity(reasonUnreadable)
	}
	return info
}

// identityReadFailure classifies a failed identity read, logs it, and returns
// the `unavailable` arm carrying the matching reason.
func (h *Handler) identityReadFailure(
	ctx context.Context, err error, stderr *procout.Buffer, outBytes int,
) WhoamiResponse {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		attrs := make([]any, 0, 4)
		attrs = append(attrs, "timeout", h.cfg.WhoamiTimeout)
		attrs = append(attrs, stderrAttr(stderr)...)
		slog.Warn("whoami: kiro-cli timed out", attrs...)
		return unavailableIdentity(reasonTimedOut)
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, fs.ErrNotExist):
		// Warn, not Error: a fresh volume has no kiro-cli until the install
		// manager finishes, and the background timer asks once a minute.
		slog.Warn("whoami: kiro-cli binary not found", "cli_path", h.cliPath())
		return unavailableIdentity(reasonCLIMissing)
	default:
		// Full details server-side; the client gets the arm, never raw CLI
		// output.
		attrs := make([]any, 0, 6)
		attrs = append(attrs, "error", err, "stdout_bytes", outBytes)
		attrs = append(attrs, stderrAttr(stderr)...)
		slog.Warn("whoami: kiro-cli invocation failed", attrs...)
		return unavailableIdentity(reasonCLIFailed)
	}
}
