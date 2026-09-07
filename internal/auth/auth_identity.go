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
// refresh behind the answer. Sign-in and sign-out publish directly, so the timer
// only has to catch a change vibekit did not make: expiring credentials, or
// `kiro-cli logout` run in a terminal.
const identityTTL = time.Minute

// Reasons the `unavailable` arm carries: a closed set of server-authored strings,
// because the client renders one in a banner. unavailableIdentity enforces it.
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
// whose answer could not be read. The reason goes through identityText here, not
// at the callers, so a future reason carrying upstream text cannot skip it.
func unavailableIdentity(reason string) WhoamiResponse {
	return WhoamiResponse{State: WhoamiUnavailable, Reason: identityText(reason)}
}

// identityCache is the identity /api/whoami answers from.
//
// Stale-while-revalidate, and it NEVER blocks a reader: the read behind it is a
// kiro-cli fork with a multi-second tail, while the endpoint fires on every page
// load and SSE reconnect. The held value survives invalidation on purpose, so a
// revalidating poll still answers with the last known identity rather than an
// `unavailable` it would have to render.
type identityCache struct {
	// read performs one identity read; a field so a test can drive the staleness
	// and coalescing rules without a subprocess.
	read func(context.Context) WhoamiResponse
	at   time.Time
	resp WhoamiResponse
	// budget is the wall clock one read gets. Must stay after the strings, or
	// govet fieldalignment fires.
	budget time.Duration
	// gen counts published identities. rebuild captures it before forking and
	// drops its answer if it moved, so a read begun before a logout cannot
	// republish the pre-logout identity.
	gen  uint64
	mu   sync.Mutex
	busy bool
}

// newIdentityCache returns a cache seeded with the `unavailable` arm: before the
// first read the server does not know, which is not "nobody is signed in".
// budget is the wall clock one read gets, captured here because a reader can be a
// timer with no request behind it.
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

// publish records an identity vibekit itself decided and marks the entry fresh.
// The generation bump makes it WIN against a read already in flight: without it,
// a fork started just before a logout republishes `signed_in` over this
// `signed_out`.
func (c *identityCache) publish(resp *WhoamiResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resp = *resp
	c.at = time.Now()
	c.gen++
}

// invalidate marks the entry stale without discarding it, so the next read
// revalidates while still answering with the last known identity. No generation
// bump: it asks for a fresh read rather than asserting an answer, so an in-flight
// read is what it wants to land. This is what lets the login window converge
// instead of serving `signed_out` for a full TTL after the flow succeeds.
func (c *identityCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = time.Time{}
}

// refresh reads the identity in the calling goroutine and publishes it,
// coalescing into a refresh already in flight rather than forking again.
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
// The context is deliberately detached from any request: a refresh kicked by a
// page load must outlive it, and the budget plus boundChild's group kill is what
// guarantees the goroutine exits. The generation check discards a read that
// describes the world before a publish that landed mid-fork; busy is cleared
// either way, or the cache would never refresh again.
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

// Run keeps the cached identity warm until ctx is done: one read now, so the
// first page load after a restart sees a real identity rather than the seed, then
// one per identityTTL. Synchronous, so the caller owns the goroutine.
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

// readIdentity runs `kiro-cli whoami --format json` and maps the outcome onto one
// of the three arms. Never returns an error: every failure IS an arm.
func (h *Handler) readIdentity(ctx context.Context) WhoamiResponse {
	// h.cliPath resolves the install manager's active version, never user input.
	cmd := exec.CommandContext(ctx, h.cliPath(), "whoami", "--format", "json") //nolint:gosec // G204: binary path from config
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
		// manager finishes, and the timer asks once a minute.
		slog.Warn("whoami: kiro-cli binary not found", "cli_path", h.cliPath())
		return unavailableIdentity(reasonCLIMissing)
	default:
		// Full details server-side; the client gets the arm, never raw CLI output.
		attrs := make([]any, 0, 6)
		attrs = append(attrs, "error", err, "stdout_bytes", outBytes)
		attrs = append(attrs, stderrAttr(stderr)...)
		slog.Warn("whoami: kiro-cli invocation failed", attrs...)
		return unavailableIdentity(reasonCLIFailed)
	}
}
