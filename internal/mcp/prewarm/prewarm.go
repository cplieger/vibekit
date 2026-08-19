// Package prewarm handles npx package pre-warming for MCP servers.
//
// Vibekit's pitch is "instantly deployable with everything preinstalled",
// but MCP servers running via `npx -y <pkg>` pay a one-time install cost
// on first use (often 5-15s for a mid-sized server). That latency shows
// up in the user's first chat after a container start, which is exactly
// the moment first impressions are made.
//
// The pre-warmer resolves any enabled stdio server whose command is
// `npx` and eagerly runs `npm install -g <identifier>` in the background
// at container start and whenever the user toggles/adds such a server.
package prewarm

import (
	"context"
	"log/slog"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/cplieger/vibekit/internal/buffer"
)

// ServerInfo is the narrow view of an MCP server that prewarm needs.
type ServerInfo struct {
	Transport string
	Command   string
	Args      []string
	Prewarm   bool
	Enabled   bool
}

// ServerLister provides the list of enabled servers for prewarm evaluation.
type ServerLister interface {
	EnabledServers(ctx context.Context) []ServerInfo
}

// npxCommand is the command name for npx-based MCP servers.
const npxCommand = "npx"

// maxConcurrentInstalls caps how many `npm install -g` can run in parallel.
const maxConcurrentInstalls = 3

// tailLogBytes caps how many bytes of `npm install` output we keep in memory.
const tailLogBytes = buffer.DefaultOutputCap

// supportedPackageTransports defines which transport types are valid for prewarm.
var supportedPackageTransports = map[string]bool{"stdio": true, "": true}

// State describes the phase of a prewarm install for UI surfacing.
type State string

// Installing and the following constants define the valid State values for a prewarm install lifecycle.
const (
	Installing State = "installing"
	Done       State = "done"
	Failed     State = "failed"
)

// Runner owns the lifecycle of npx pre-installs.
type Runner struct {
	Lister  ServerLister
	running map[string]struct{}
	sem     *semaphore.Weighted
	// lifetime is the RUNNER's own cancellable context, which Stop cancels, and
	// it is a second fact from the per-pass ctx Run takes: a pass ends when its
	// caller's ctx does, while an install already in flight must also die when
	// the runner is stopped. queue composes BOTH (see there) rather than
	// picking either, so this cannot become a parameter without losing the Stop
	// signal.
	//
	// Named for what it is rather than `ctx`, so it cannot be read as the
	// ambient context at the sites that consume it.
	lifetime context.Context
	cancel   context.CancelFunc
	OnStatus func(pkg string, state State)
	mu       sync.Mutex
	Disabled atomic.Bool
}

// NewRunner returns a runner. Call Run to kick an initial pass at
// container boot; subsequent passes fire from the store's onChange.
//
// ctx is the runner's lifetime and is required — it goes straight to
// context.WithCancel, which refuses a nil one at this single construction site
// rather than defaulting into installs no Stop could reach.
func NewRunner(ctx context.Context, lister ServerLister) *Runner {
	ctx, cancel := context.WithCancel(ctx)
	return &Runner{
		Lister:   lister,
		running:  make(map[string]struct{}),
		sem:      semaphore.NewWeighted(maxConcurrentInstalls),
		lifetime: ctx,
		cancel:   cancel,
	}
}

// Stop cancels all in-flight and future installs.
func (p *Runner) Stop() {
	p.cancel()
}

// Run enumerates enabled prewarm-flagged npx servers and kicks off a
// background install for each.
//
// ctx bounds THIS pass. The runner's own lifetime is separate and both are
// honoured: see the field and installOne.
func (p *Runner) Run(ctx context.Context) {
	if p.lifetime.Err() != nil {
		return
	}
	if p.Disabled.Load() {
		return
	}
	if _, err := exec.LookPath("npm"); err != nil {
		// npm is opt-in (runtimes.node). It may be installed later in
		// the same process lifetime via the tools UI, so DON'T latch
		// Disabled here — just skip this run. The next Run re-probes
		// and prewarm comes alive once node is enabled.
		slog.Debug("mcp: prewarm skipped this run: npm not on PATH yet", "error", err)
		return
	}

	candidates := p.Lister.EnabledServers(ctx)
	queued := 0
	for i := range candidates {
		pkg := ExtractNpxPackage(candidates[i])
		if pkg == "" {
			continue
		}
		if !p.reserve(pkg) {
			continue
		}
		queued++
		slog.Debug("mcp: prewarm queued", "package", pkg, "position", queued)
		go p.queue(ctx, pkg)
	}
	slog.Info("mcp: prewarm pass", "candidates", len(candidates), "queued", queued)
}

// queue waits for one of the maxConcurrentInstalls slots and installs pkg,
// handing the in-flight reservation back when the wait is abandoned instead.
//
// The wait honours the pass ctx AND the runner's lifetime, and the merge is
// made ONCE here for both the wait and the install — installOne relies on it,
// so its ctx must already carry both signals.
//
// A cancelled context never acquires a slot: semaphore.Weighted.Acquire tests
// ctx.Done() before its fast path. A select over a slot channel could not
// promise that — with a slot free, select picks at random among ready cases, so
// an already-dead pass could still win the send and start an install.
func (p *Runner) queue(ctx context.Context, pkg string) {
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(p.lifetime, cancel)
	defer stop()

	if err := p.sem.Acquire(workCtx, 1); err != nil {
		p.release(pkg)
		return
	}
	defer p.sem.Release(1)
	p.installOne(workCtx, pkg)
}

func (p *Runner) reserve(pkg string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.running[pkg]; ok {
		return false
	}
	p.running[pkg] = struct{}{}
	return true
}

// release drops pkg from the in-flight set, so the next pass may retry it.
func (p *Runner) release(pkg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.running, pkg)
}

// RingBuffer keeps the last Cap bytes of a stream.
type RingBuffer struct {
	buf []byte
	Cap int // exported for test construction
}

func (r *RingBuffer) Write(p []byte) (int, error) {
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.Cap {
		r.buf = r.buf[len(r.buf)-r.Cap:]
	}
	return len(p), nil
}

// Bytes returns the buffered content, up to Cap bytes (the most recent tail).
func (r *RingBuffer) Bytes() []byte { return r.buf }

// installOne runs the install. ctx must already carry both the pass and the
// runner's lifetime — queue merges them, and the 5-minute budget hangs off that
// merge, so a ctx carrying only one of the two silently loses the other signal.
func (p *Runner) installOne(ctx context.Context, pkg string) {
	defer p.release(pkg)

	installCtx, installCancel := context.WithTimeout(ctx, 5*time.Minute)
	defer installCancel()

	start := time.Now()
	slog.Info("mcp: prewarm install", "package", pkg)
	if p.OnStatus != nil {
		p.OnStatus(pkg, Installing)
	}
	cmd := exec.CommandContext(installCtx, "npm", "install", "-g", pkg)
	ring := &RingBuffer{Cap: tailLogBytes}
	cmd.Stdout = ring
	cmd.Stderr = ring
	err := cmd.Run()
	out := ring.Bytes()
	if err != nil {
		slog.Warn("mcp: prewarm failed",
			"package", pkg,
			"error", err,
			"duration_ms", time.Since(start).Milliseconds(),
			"output", TailOutput(out, 1024))
		if p.OnStatus != nil {
			p.OnStatus(pkg, Failed)
		}
		return
	}
	slog.Info("mcp: prewarm done",
		"package", pkg, "duration_ms", time.Since(start).Milliseconds())
	if p.OnStatus != nil {
		p.OnStatus(pkg, Done)
	}
}

// NpmPkgSpecRe accepts conservative npm package specs.
var NpmPkgSpecRe = regexp.MustCompile(
	`^(?:@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*` +
		`(?:@[A-Za-z0-9^~><=.+_-][A-Za-z0-9^~><=.+_-]*)?$`)

// ExtractNpxPackage returns the npm identifier a stdio server will run
// via `npx -y <pkg>`, or "" if the server's command isn't an npx run.
func ExtractNpxPackage(s ServerInfo) string {
	if !s.Prewarm || !s.Enabled {
		return ""
	}
	if !supportedPackageTransports[s.Transport] {
		return ""
	}
	if strings.TrimSpace(s.Command) != npxCommand {
		return ""
	}
	for _, arg := range s.Args {
		a := strings.TrimSpace(arg)
		if a == "" || a == "-y" || a == "--yes" {
			continue
		}
		if strings.HasPrefix(a, "-") {
			return ""
		}
		if !NpmPkgSpecRe.MatchString(a) {
			return ""
		}
		return a
	}
	return ""
}

// TailOutput returns the last n bytes of output for a log line.
func TailOutput(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	tail := b[len(b)-n:]
	for len(tail) > 0 && tail[0]&0xC0 == 0x80 {
		tail = tail[1:]
	}
	return "…" + string(tail)
}
