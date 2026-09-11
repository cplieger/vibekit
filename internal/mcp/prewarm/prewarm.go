// Package prewarm handles npx package pre-warming for MCP servers.
//
// Vibekit's pitch is "instantly deployable with everything preinstalled",
// but MCP servers running via `npx -y <pkg>` pay a one-time install cost
// on first use (often 5-15s for a mid-sized server). That latency shows
// up in the user's first chat after a container start, which is exactly
// the moment first impressions are made.
//
// The pre-warmer resolves any enabled stdio server whose command is
// `npx` and eagerly fills the npm CACHE for its identifier in the
// background at container start and whenever the user toggles/adds such
// a server.
//
// The npm cache is the whole mechanism, and that is measured rather than
// assumed. `npx -y <pkg>` NEVER runs an already-installed copy: --yes makes
// npm exec install into `<cache>/_npx/<hash>` unconditionally. Proved on npm
// 11.16 by replacing a global bin with a marker script — `npx -y cowsay` printed
// the real cowsay's output and built a populated `_npx` tree beside it. So the
// `npm install -g` this used to run contributed nothing to the spawn except the
// cache it filled on the way past, and the version-addressed global tree it left
// behind was read by nobody.
//
// What that changes is not just tidiness: a global install runs every lifecycle
// script in the dependency tree, as the container user, at boot, for any package
// name in mcp.json — unattended, with no permission request and no transcript,
// because the SERVER spawns it. Filling the cache needs none of that capability,
// so installOne runs --ignore-scripts into a throwaway tree it deletes. The
// package's own scripts still run when the server actually spawns, which is where
// they ran before prewarm existed and where a user is present.
//
// Measured on @modelcontextprotocol/server-everything (103 packages), spawn cost
// of a bare `npx -y <pkg>`: 2236ms cold, 1304ms with only the cache warm, 1415ms
// with the old global install. The cache-only warm is the whole win.
package prewarm

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cplieger/vibekit/internal/buffer"
	"golang.org/x/sync/semaphore"
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
	// The probe's ANSWER is threaded to installOne as argv[0] instead of being
	// discarded and the bare name re-resolved per package: one pass resolves once,
	// and the file that was probed is the file that runs.
	//
	// It does NOT confine. npm is installed by the toolbelt engine into
	// /config/tools/bin, which IS PATH[0], so an absolute pin names the same
	// agent-writable file a PATH lookup finds. Confining it is custody on the
	// toolbelt bin tree — toolbelt's question, not this file's.
	npmBin, err := exec.LookPath("npm")
	if err != nil {
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
		go p.queue(ctx, npmBin, pkg)
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
func (p *Runner) queue(ctx context.Context, npmBin, pkg string) {
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(p.lifetime, cancel)
	defer stop()

	if err := p.sem.Acquire(workCtx, 1); err != nil {
		p.release(pkg)
		return
	}
	defer p.sem.Release(1)
	p.installOne(workCtx, npmBin, pkg)
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

// installOne warms the npm cache for pkg. ctx must already carry both the pass
// and the runner's lifetime — queue merges them, and the 5-minute budget hangs off
// that merge, so a ctx carrying only one of the two silently loses the other
// signal.
//
// The install goes into a THROWAWAY tree with --ignore-scripts, for the reason the
// package comment measures: the cache it fills is the only thing the spawn reads,
// and running the tree's lifecycle scripts at boot is a capability this needs none
// of. A per-install tree rather than a shared one because npm runs
// maxConcurrentInstalls at a time and two of them writing one node_modules would
// race; the cache underneath IS shared, and cacache is built for that.
func (p *Runner) installOne(ctx context.Context, npmBin, pkg string) {
	defer p.release(pkg)

	installCtx, installCancel := context.WithTimeout(ctx, 5*time.Minute)
	defer installCancel()

	start := time.Now()
	slog.Info("mcp: prewarm install", "package", pkg)
	if p.OnStatus != nil {
		p.OnStatus(pkg, Installing)
	}
	fail := func(err error, out []byte) {
		slog.Warn("mcp: prewarm failed",
			"package", pkg,
			"error", err,
			"duration_ms", time.Since(start).Milliseconds(),
			"output", TailOutput(out, 1024))
		if p.OnStatus != nil {
			p.OnStatus(pkg, Failed)
		}
	}

	tree, err := stageTree()
	if err != nil {
		fail(err, nil)
		return
	}
	defer removeTree(tree)

	cmd := exec.CommandContext(installCtx, npmBin,
		"install", "--ignore-scripts", "--no-audit", "--no-fund", pkg)
	// The staging tree is the working directory AND carries a package.json, so npm
	// resolves nothing from above it: --prefix alone leaves npm free to find an
	// ancestor manifest and install against somebody else's tree.
	cmd.Dir = tree
	ring := &RingBuffer{Cap: tailLogBytes}
	cmd.Stdout = ring
	cmd.Stderr = ring
	if err := cmd.Run(); err != nil {
		fail(err, ring.Bytes())
		return
	}
	slog.Info("mcp: prewarm done",
		"package", pkg, "duration_ms", time.Since(start).Milliseconds())
	if p.OnStatus != nil {
		p.OnStatus(pkg, Done)
	}
}

// stagingManifest is the throwaway tree's own package.json. Private and
// unversioned, so nothing about it can be mistaken for a publishable package.
const stagingManifest = `{"name":"vibekit-prewarm","version":"0.0.0","private":true}` + "\n"

// stageTree makes one install's throwaway tree and returns its path; the caller
// removes it. A tree that cannot be given its manifest is removed HERE rather than
// handed back, so no caller's failure path has to clean up a partial one.
func stageTree() (string, error) {
	tree, err := os.MkdirTemp("", "vibekit-prewarm-")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(tree, "package.json"), []byte(stagingManifest), 0o600); err != nil {
		removeTree(tree)
		return "", err
	}
	return tree, nil
}

// removeTree drops a staging tree, reporting a failure rather than swallowing it:
// a tree left behind is a slow leak of the container's temp space, and nothing
// else ever revisits the path.
func removeTree(tree string) {
	if err := os.RemoveAll(tree); err != nil {
		slog.Warn("mcp: prewarm could not remove its staging tree",
			"path", tree, "error", err)
	}
}

// NpmPkgSpecRe accepts conservative npm package specs.
var NpmPkgSpecRe = regexp.MustCompile(
	`^(?:@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*` +
		`(?:@[A-Za-z0-9^~><=.+_-][A-Za-z0-9^~><=.+_-]*)?$`,
)

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
