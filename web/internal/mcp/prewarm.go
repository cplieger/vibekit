// npx prewarm.
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
// kiro-cli's subprocess spawn still runs `npx -y`; that call becomes a
// no-op because the package is already cached in the global store.
//
// Scope:
//
//   - Opt-in per server via Server.Prewarm; off by default because
//     not every published-to-npm server is trustworthy enough for
//     container-scoped pre-install.
//   - Restricted to enabled stdio servers with command == "npx".
//     Docker (oci) packages are not pre-warmed — `docker pull` needs
//     Docker access the vibekit container deliberately doesn't have.
//   - Idempotent: re-running `npm install -g` is a no-op when the
//     package is already at the target version.

package mcp

import (
	"context"
	"log/slog"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// npxCommand is the command name for npx-based MCP servers.
const npxCommand = "npx"

// maxConcurrentInstalls caps how many `npm install -g` can run in
// parallel. At container boot with N enabled npx servers, N concurrent
// installs will thunder the npm registry, contend on the global
// package-lock, and spike RSS at exactly the moment the user is
// trying to load the UI. Three is a sweet spot for the common 1-5
// server case and lets power users with 20+ servers finish within a
// few timeouts worst case (npm install is network-bound so 3 in
// parallel approximates max throughput anyway).
const maxConcurrentInstalls = 3

// tailLogBytes caps how many bytes of `npm install` output we keep in
// memory. A well-behaved package emits <10 KiB; a rogue postinstall
// can spew MBs (e.g. `yes 'pwned' | head -c 500M` style pranks). The
// ring buffer trades memory for tail fidelity — we keep only the last
// tailLogBytes of output and slice the last 1 KiB for logging.
const tailLogBytes = 64 * 1024

// PrewarmState describes the phase of a prewarm install for UI surfacing.
type PrewarmState string

const (
	PrewarmInstalling PrewarmState = "installing"
	PrewarmDone       PrewarmState = "done"
	PrewarmFailed     PrewarmState = "failed"
)

// PrewarmRunner owns the lifecycle of npx pre-installs.
type PrewarmRunner struct {
	store    *Store
	running  map[string]struct{}
	sem      chan struct{}
	ctx      context.Context
	cancel   context.CancelFunc
	OnStatus func(pkg string, state PrewarmState)
	mu       sync.Mutex
	disabled atomic.Bool // set once when npm is not on PATH; stops retrying.
}

// NewPrewarmRunner returns a runner. Call Run to kick an initial pass at
// container boot; subsequent passes fire from the store's onChange.
// The runner's lifecycle is tied to the parent context: if ctx is
// cancelled (e.g. hub shutdown), the runner stops automatically without
// requiring the caller to call Stop(). Stop() remains as an explicit
// early-cancel mechanism.
func NewPrewarmRunner(ctx context.Context, store *Store) *PrewarmRunner {
	ctx, cancel := context.WithCancel(ctx)
	return &PrewarmRunner{
		store:   store,
		running: make(map[string]struct{}),
		sem:     make(chan struct{}, maxConcurrentInstalls),
		ctx:     ctx,
		cancel:  cancel,
	}
}

// Stop cancels all in-flight and future installs. Safe to call more
// than once. After Stop returns, no new installs will start and
// running installs will be cancelled on their next context check.
func (p *PrewarmRunner) Stop() {
	p.cancel()
}

// Run enumerates enabled prewarm-flagged npx servers and kicks off a
// background install for each. Safe to call repeatedly; duplicates are
// de-duplicated against the in-flight set. The provided ctx allows
// callers to cancel a specific Run pass without stopping the entire
// runner; goroutines select on both ctx and the runner's lifecycle
// context.
func (p *PrewarmRunner) Run(ctx context.Context) {
	// Early exit if the runner has been stopped.
	if p.ctx.Err() != nil {
		return
	}
	// Graceful-degradation gate: if npm isn't on PATH we disable the
	// runner after the first probe and leave it disabled for the
	// container's lifetime. Re-enable requires a container restart.
	// npm is provided by the vibekit image so this path should
	// normally be unreachable in production. The disabled flag is
	// write-once (false→true) so an atomic bool suffices — no mutex
	// needed. Concurrent Run() calls both probing LookPath is benign:
	// LookPath is idempotent and the second Store(true) is a no-op.
	if p.disabled.Load() {
		return
	}
	if _, err := exec.LookPath("npm"); err != nil {
		p.disabled.Store(true)
		slog.Warn("mcp: prewarm disabled: npm not on PATH", "error", err)
		return
	}

	candidates := p.store.EnabledRaw(ctx)
	queued := 0
	for _, sv := range candidates {
		pkg := extractNpxPackage(sv)
		if pkg == "" {
			continue
		}
		if !p.reserve(pkg) {
			continue
		}
		queued++
		// Visible-before-parking breadcrumb so ops can tell parked
		// installs from running ones when diagnosing "why is my MCP
		// not ready yet?". Installs 4..N would otherwise be invisible
		// in Loki until their semaphore slot frees, because the first
		// Info inside installOne runs after acquisition. Debug keeps
		// baseline log volume low.
		slog.Debug("mcp: prewarm queued",
			"package", pkg, "position", queued)
		// Acquire the semaphore before launching so we never run more
		// than maxConcurrentInstalls installs in parallel. Acquisition
		// happens inside the goroutine so Run returns immediately;
		// the goroutine parks on the channel until a slot frees.
		go func(pkg string) {
			// Respect cancellation while waiting for a semaphore slot
			// so Stop() or the caller's context unblocks parked goroutines.
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
	slog.Info("mcp: prewarm pass",
		"candidates", len(candidates), "queued", queued)
}

// reserve claims the in-flight slot for pkg. Returns true on claim,
// false if the install is already running.
func (p *PrewarmRunner) reserve(pkg string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.running[pkg]; ok {
		return false
	}
	p.running[pkg] = struct{}{}
	return true
}

// ringBuffer keeps the last cap bytes of a stream. Writes append;
// when the buffer grows past cap, the head is trimmed. Used to bound
// memory when an npm install's postinstall hook streams MBs of output.
type ringBuffer struct {
	buf []byte
	cap int
}

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.cap {
		r.buf = r.buf[len(r.buf)-r.cap:]
	}
	return len(p), nil
}

func (r *ringBuffer) Bytes() []byte { return r.buf }

// installOne runs `npm install -g <pkg>` with a 5-minute ceiling so a
// hanging network call can't leak a goroutine forever. Output is
// piped through a capped ring buffer so a rogue postinstall can't OOM
// the vibekit container by spewing MBs of progress bytes. The provided
// ctx is composed with the runner's lifecycle context so a shutdown
// signal propagates into running installs immediately.
func (p *PrewarmRunner) installOne(ctx context.Context, pkg string) {
	defer func() {
		p.mu.Lock()
		delete(p.running, pkg)
		p.mu.Unlock()
	}()

	// Compose caller ctx with runner lifecycle ctx: cancel on whichever
	// fires first, then layer the 5-minute timeout on top.
	mergedCtx, mergedCancel := context.WithCancel(ctx)
	stop := context.AfterFunc(p.ctx, mergedCancel)

	installCtx, installCancel := context.WithTimeout(mergedCtx, 5*time.Minute)
	defer installCancel()
	defer mergedCancel()
	defer stop()

	start := time.Now()
	slog.Info("mcp: prewarm install", "package", pkg)
	if p.OnStatus != nil {
		p.OnStatus(pkg, PrewarmInstalling)
	}
	cmd := exec.CommandContext(installCtx, "npm", "install", "-g", pkg)
	ring := &ringBuffer{cap: tailLogBytes}
	cmd.Stdout = ring
	cmd.Stderr = ring
	err := cmd.Run()
	out := ring.Bytes()
	if err != nil {
		slog.Warn("mcp: prewarm failed",
			"package", pkg,
			"error", err,
			"duration_ms", time.Since(start).Milliseconds(),
			"output", tailOutput(out, 1024))
		if p.OnStatus != nil {
			p.OnStatus(pkg, PrewarmFailed)
		}
		return
	}
	slog.Info("mcp: prewarm done",
		"package", pkg, "duration_ms", time.Since(start).Milliseconds())
	if p.OnStatus != nil {
		p.OnStatus(pkg, PrewarmDone)
	}
}

// npmPkgSpecRe accepts conservative npm package specs: `pkg`,
// `@scope/pkg`, `pkg@1.2.3`, `pkg@latest`, `@scope/pkg@^1`,
// `pkg@~1.0`, `pkg@>=1`. It rejects URL / git / file / alias specs —
// npm's grammar allows forms like `pkg@https://evil/tar.tgz`,
// `pkg@git+https://...`, `pkg@npm:other` and `pkg@file:/path` that
// redirect resolution without a leading dash. Any of those would
// execute attacker-served preinstall/postinstall scripts inside the
// vibekit container. The leading-dash check catches flag injection;
// this regex catches the non-flag package-spec tricks. The @-suffix
// character class excludes `/` (URL/git paths), `:` (scheme or
// `npm:` alias), and whitespace; every conventional semver operator
// (^, ~, >, <, =, space-free) is allowed. Conservative: may reject
// otherwise-valid edge cases, in which case prewarm is skipped and
// the server still works at bridge-spawn time.
var npmPkgSpecRe = regexp.MustCompile(
	`^(?:@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*` +
		`(?:@[A-Za-z0-9^~><=.+_-][A-Za-z0-9^~><=.+_-]*)?$`)

// extractNpxPackage returns the npm identifier a stdio server will run
// via `npx -y <pkg>`, or "" if the server's command isn't an npx run.
// Recognises both `npx -y @scope/name` and `npx @scope/name` forms;
// the `-y` (auto-answer-yes) flag is optional in our model but standard
// in the wild.
//
// Any leading-dash arg after -y/--yes is rejected (returns ""). That
// guards against an adversarial Args payload like
// `["--registry=http://evil", "legit-pkg"]` being fed as the install
// target to `npm install -g`, which would redirect resolution to a
// hostile registry and execute the attacker's preinstall/postinstall
// scripts inside the vibekit container. npm package names cannot start
// with `-`, so rejecting them wholesale is both correct and tighter.
//
// Non-flag specs that don't match npmPkgSpecRe are also rejected:
// `legit@npm:malicious`, `pkg@https://evil/tar.tgz`, `pkg@git+https://...`,
// `pkg@file:/...` all escape the leading-dash guard by carrying the
// redirect in the @-suffix.
func extractNpxPackage(s *Server) string {
	if !s.Prewarm || !s.Enabled {
		return ""
	}
	// Use the shared supportedPackageTransports map (registry_normalise.go)
	// so the invariant "prewarm only targets packages the registry would
	// surface" is enforced by shared data rather than parallel checks.
	if !supportedPackageTransports[string(s.Transport)] {
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
		// First non-flag arg after optional -y/--yes is the package spec.
		// Any other leading dash means this is a flag for the MCP server
		// itself (e.g. `npx -y pkg --port 1234`), not something we can
		// safely feed to `npm install -g` — so we return empty and skip
		// prewarm. The server still works at bridge-spawn time because
		// kiro-cli runs `npx -y`; prewarm just loses the cache benefit.
		if strings.HasPrefix(a, "-") {
			return ""
		}
		// Defense in depth beyond the leading-dash check: reject any
		// spec that carries a URL / git / file / alias suffix.
		if !npmPkgSpecRe.MatchString(a) {
			return ""
		}
		return a
	}
	return ""
}

// tailOutput returns the last n bytes of output for a log line, with
// truncation indicated. Advances past any UTF-8 continuation bytes at
// the cut point so the returned string is always a valid UTF-8 prefix
// — npm occasionally emits box-drawing / emoji in progress output, and
// slicing mid-rune would hand the log pipeline an invalid sequence.
func tailOutput(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	tail := b[len(b)-n:]
	for len(tail) > 0 && tail[0]&0xC0 == 0x80 {
		tail = tail[1:]
	}
	return "…" + string(tail)
}
