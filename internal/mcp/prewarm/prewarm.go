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

	"vibekit/internal/buffer"
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

const (
	Installing State = "installing"
	Done       State = "done"
	Failed     State = "failed"
)

// Runner owns the lifecycle of npx pre-installs.
type Runner struct {
	Lister   ServerLister
	running  map[string]struct{}
	sem      chan struct{}
	ctx      context.Context
	cancel   context.CancelFunc
	OnStatus func(pkg string, state State)
	mu       sync.Mutex
	Disabled atomic.Bool
}

// NewRunner returns a runner. Call Run to kick an initial pass at
// container boot; subsequent passes fire from the store's onChange.
func NewRunner(ctx context.Context, lister ServerLister) *Runner {
	ctx, cancel := context.WithCancel(ctx)
	return &Runner{
		Lister:  lister,
		running: make(map[string]struct{}),
		sem:     make(chan struct{}, maxConcurrentInstalls),
		ctx:     ctx,
		cancel:  cancel,
	}
}

// Lock locks the runner's mutex (for test access).
func (p *Runner) Lock() { p.mu.Lock() }

// Unlock unlocks the runner's mutex (for test access).
func (p *Runner) Unlock() { p.mu.Unlock() }

// Running returns the in-flight set (for test access).
func (p *Runner) Running() map[string]struct{} { return p.running }

// Stop cancels all in-flight and future installs.
func (p *Runner) Stop() {
	p.cancel()
}

// Run enumerates enabled prewarm-flagged npx servers and kicks off a
// background install for each.
func (p *Runner) Run(ctx context.Context) {
	if p.ctx.Err() != nil {
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
		go func(pkg string) {
			select {
			case p.sem <- struct{}{}:
			case <-p.ctx.Done():
				p.mu.Lock()
				delete(p.running, pkg)
				p.mu.Unlock()
				return
			case <-ctx.Done():
				p.mu.Lock()
				delete(p.running, pkg)
				p.mu.Unlock()
				return
			}
			defer func() { <-p.sem }()
			p.installOne(ctx, pkg)
		}(pkg)
	}
	slog.Info("mcp: prewarm pass", "candidates", len(candidates), "queued", queued)
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

func (r *RingBuffer) Bytes() []byte { return r.buf }

func (p *Runner) installOne(ctx context.Context, pkg string) {
	defer func() {
		p.mu.Lock()
		delete(p.running, pkg)
		p.mu.Unlock()
	}()

	mergedCtx, mergedCancel := context.WithCancel(ctx)
	stop := context.AfterFunc(p.ctx, mergedCancel)

	installCtx, installCancel := context.WithTimeout(mergedCtx, 5*time.Minute)
	defer installCancel()
	defer mergedCancel()
	defer stop()

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
