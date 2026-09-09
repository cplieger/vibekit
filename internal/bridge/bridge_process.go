package bridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/cplieger/vibekit/internal/ids"
	"github.com/cplieger/vibekit/internal/procgroup"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// Start launches the kiro-cli subprocess and either creates a new ACP
// session (acpSessionID == "") or loads an existing one. Exactly one call
// per bridge instance.
//
// ctx bounds the startup handshake only; opts.Lifetime bounds the subprocess
// and is required. The handshake is additionally bounded HERE, by
// handshakeBudget or replayBudget, so a caller that passes a timer-free context
// (every caller does) still gets a bounded handshake.
func (b *Bridge) Start(ctx context.Context, opts *vibekit.StartOpts) error {
	// The subprocess outlives this call. Start's ctx bounds the handshake
	// below; opts.Lifetime bounds the process. See vibekit.StartOpts.Lifetime for
	// what taking the process lifetime from a turn context measured like, and
	// for why the field is required rather than defaulted here.
	if opts.Lifetime == nil {
		return errors.New("bridge: StartOpts.Lifetime is required: it bounds the kiro-cli subprocess, and every default for it is a subprocess nothing can cancel")
	}
	b.lifecycleCtx = opts.Lifetime
	// The engine is NOT stored on the bridge: startProcess below takes it from
	// opts and nothing else ever reads it. The field that used to hold it had no
	// reader in any build, which `unused` cannot see because it was assigned.
	// Immutable after Start; read lock-free by SetModel / initialize.
	b.enableHooks = opts.EnableHooks
	b.secretStorage = opts.SecretStorage
	b.presets = opts.Presets
	b.toolSearch = opts.ToolSearch
	b.knowledge = opts.Knowledge
	b.memory = opts.Memory
	b.extraArgs = opts.ExtraArgs
	if opts.SessionID != "" && !ids.ValidSessionID(opts.SessionID) {
		return fmt.Errorf("invalid acp session id: %q", opts.SessionID)
	}
	if opts.Model != "" && !validIdent(opts.Model) {
		return fmt.Errorf("invalid model identifier: %q", opts.Model)
	}
	if err := b.startProcess(opts.AgentEngine); err != nil {
		return err
	}
	// A turn may legitimately run for hours; a HANDSHAKE may not. Bridge.Call
	// carries no client-side deadline by design (see its doc comment), and with a
	// live subprocess its only other exits are the response arriving and the
	// bridge dying — so an initialize or a session/new that never answers blocks
	// here until the process is killed or the whole runtime shuts down. The
	// caller sees nothing at all in the meantime: spawnBridge has already
	// registered this bridge as starting, so the chat answers 409 busy to every
	// later Send and the singleflight key folds those callers onto this same
	// wedged spawn. Start's contract above has always claimed its ctx bounded the
	// handshake; the timer is what makes the claim true.
	//
	// Bounded per PHASE, because the two phases bound different work. A resume
	// streams the entire prior transcript as notifications BEFORE the load
	// response resolves, so its ceiling has to admit a replay whose length is a
	// property of the chat's history; a fresh session pays none of that.
	budget, phase := handshakeBudget, "session start"
	if opts.SessionID != "" {
		budget, phase = replayBudget, "session resume"
	}
	// Timed because the handshake is the slowest routine operation in the app and
	// nothing measured it: "bridge started" carried the identity of the bridge and
	// not its cost, and the `prompt` line times only the prompt Call. So a
	// regression or an improvement in this window — the KAS runtime unpack, the SSO
	// refresh, the initialize round trip, the session create or replay — was
	// invisible in Loki, and a claim about it could only ever be an argument.
	//
	// Wall clock rather than a phase breakdown: the phases already have their own
	// budgets and their own failure messages, and one number per spawn is what a
	// logfmt query can aggregate. It brackets the two phases only, deliberately
	// excluding the argv build and the process spawn above, so the value is
	// comparable across a cold and a warm container.
	handshakeStart := time.Now()
	hctx, cancelHandshake := context.WithTimeout(ctx, budget)
	defer cancelHandshake()
	if err := b.initialize(hctx); err != nil {
		b.Stop()
		return handshakeTimeout(err, phase, budget)
	}
	var err error
	if opts.SessionID != "" {
		err = b.loadSession(hctx, opts)
	} else {
		err = b.newSession(hctx, opts)
		// Fail CLOSED when the budget expired inside newSession. Its appliers are
		// best-effort by design — each logs and returns rather than failing
		// session creation, because refusing to open a chat over a model
		// preference is worse than opening it on the default — and an expired
		// budget silently converts that into a session in the wrong mode, on the
		// wrong model, and, the one that matters, in autopilot for a chat the user
		// marked supervised, since applySupervised runs last and its whole job is
		// to make writes ask first.
		//
		// So an expiry is the one case where the best-effort contract does not
		// hold. Checked rather than reordering applySupervised to the front: that
		// would protect one applier and would rest on an untested assumption about
		// whether a later set_mode disturbs a config option KAS already stored.
		// The cost of checking is that a handshake which finishes in the last
		// microseconds of its budget is failed anyway; the recovery is the next
		// Send, which is the same recovery every other start failure has.
		if err == nil {
			err = hctx.Err()
		}
	}
	if err != nil {
		b.Stop()
		return handshakeTimeout(err, phase, budget)
	}
	// Lifecycle breadcrumb so operators correlating Loki logs can
	// see which bridge a subsequent Stop / error belongs to without
	// reading the chat store. Cardinality stays sane: session id is
	// bounded by chats, model by catalog.
	slog.Info("bridge started",
		"session_id", b.SessionID(),
		"model", b.ModelID(),
		"work_dir", b.workDir,
		"acp_session_id", opts.SessionID,
		// phase names which budget this elapsed was measured against, so a
		// resume's replay time is never averaged with a fresh session's.
		"phase", phase,
		"elapsed_ms", time.Since(handshakeStart).Milliseconds(),
	)
	return nil
}

// bridgeGroupGrace bounds Stop's confirmation that the killed group emptied.
const bridgeGroupGrace = 2 * time.Second

// waitStatus renders cmd.Wait's error for a log line. A nil error is a clean exit(0),
// which for a relay that should outlive the session is itself the finding.
func waitStatus(err error) string {
	if err == nil {
		return "exit status 0"
	}
	return err.Error()
}

// reapProcess signals the tree, reaps it, and reports what it found. Stop's caller has
// already closed stdin, which is what gives kiro-cli its chance to exit gracefully.
//
// The GROUP, not the head: kiro-cli passes its stdio down, so the stdin close reaches
// the whole chain, but on 2.18.0 that stopped reclaiming the tree and a head-only kill
// left `kiro-cli-chat` plus its `node` child alive at ~250 MB, reparented to init, on
// every model switch, tab close and idle cull. See procgroup.Kill.
func (b *Bridge) reapProcess() {
	// Before the signal empties it; procgroup.GroupOf owns why.
	pgid, owns := procgroup.GroupOf(b.cmd.Process)
	killErr := procgroup.Kill(b.cmd.Process, syscall.SIGKILL)
	// Already gone means kiro-cli ended on its OWN, so the wait status below is the only
	// record of why. procgroup.AlreadyGone owns which errors mean that.
	endedItself := procgroup.AlreadyGone(killErr)
	if killErr != nil && !endedItself {
		slog.Error("kill kiro-cli", "error", killErr)
	}
	// Wait releases the OS process entry, so repeated chat switch and cull-idle cycles
	// do not leak zombies.
	waitErr := b.cmd.Wait()
	// The child's wait status is the whole diagnosis when the stream died for no stated
	// reason, and discarding it is why such a death reads as silent. Debug when we killed
	// it, because then the status is our own signal.
	if endedItself {
		slog.Warn("kiro-cli ended on its own", "wait", waitStatus(waitErr))
	} else {
		slog.Debug("kiro-cli reaped", "wait", waitStatus(waitErr))
	}
	// Reaping the head is not proof the tree went, and a surviving acp-server holds
	// KAS's workflow lease against every later resume.
	if owns && !procgroup.WaitGone(pgid, bridgeGroupGrace) {
		slog.Error("kiro-cli process group outlived its bridge; it still holds any workflow lease it owned",
			"pgid", pgid,
			"grace_ms", bridgeGroupGrace.Milliseconds())
	}
}

// Stop kills the subprocess and closes NotifCh. Safe to call multiple
// times; subsequent calls are no-ops. Multiple call sites (agent.Shutdown,
// tab close, model switch, session/load recovery) can race to stop the
// same bridge; the sync.Once gate prevents a double-close panic on b.done.
// Reaps the process via cmd.Wait so the OS releases its process entry
// immediately (no <defunct> accumulation across chat lifecycle churn).
func (b *Bridge) Stop() {
	b.stopOnce.Do(func() {
		close(b.done)
		if b.stdin != nil {
			b.stdin.Close()
		}
		if b.cmd != nil && b.cmd.Process != nil {
			b.reapProcess()
		}
	})
}

// handshakeBudget bounds a session START: initialize, session/new, and the
// config-option calls that follow it. NOT a setting, on the same reasoning
// run_bounds.go states for the run ceiling — a backstop the user can raise stops
// being a backstop, and no caller supplies one. It is a var rather than a const
// only so a test can shorten it; nothing in production writes either budget.
//
// Sized for the first chat after a version change, when the KAS runtime tree
// is unpacked during this handshake. The relay's credential refresh and the
// four config appliers also sit inside this window.
//
// Deliberately NOT sized against the MCP-server initialization KiroCrew's own 90s
// floor was chosen for: vibekit sends `mcpServers: []` and KAS reads the user's
// servers from its own config file, so that work is out of band here and is
// already bounded by the prompt path's own 30s readiness wait.
//
// It must also stay well under the client's command timeout, so the SERVER is
// what answers first and the user gets vibekit's own actionable message rather
// than a bare browser abort.
var handshakeBudget = 120 * time.Second

// replayBudget bounds a session RESUME instead of a start, and is larger for a
// reason rather than for comfort: KAS begins replaying as soon as it accepts
// session/load and streams the whole prior transcript as notifications BEFORE
// the load response resolves. That length is a property of the chat's history,
// which nothing here bounds, whereas every component of handshakeBudget's window
// is bounded. One number for both phases would have to be this one, and spending
// five minutes before reporting the far commoner fresh-start failure is worse.
//
// If a real transcript ever exceeds this, the fix is an activity-reset deadline
// in the frame reader (which already sees every frame) rather than a bigger flat
// number. It is NOT a timer on the replay projection's settle barrier, which is a
// correctness argument and needs none — see internal/agent/load_projection.go.
var replayBudget = 300 * time.Second

// writeDeadline bounds ONE write to kiro-cli's stdin. NOT a setting, on the same
// reasoning the two budgets above carry — a backstop the user can raise stops
// being a backstop, and no caller supplies one. A var only so a test can shorten
// it; nothing in production writes it.
//
// It bounds a peer that is DRAINING AT ALL, not a peer's processing: writeMu
// serialises every outbound frame, so one wedged write blocks every later Respond,
// Call and Notify on that bridge, session/cancel included, permanently and with
// nothing logged.
//
// 30s, chosen so a healthy peer cannot reach it while a wedge is still reported
// inside one prompt round trip. The pipe buffer is 65,536 bytes, so an 8 MiB reply
// is 128 drains at process speed — comfortably under a second — and single-digit
// seconds is already an order of magnitude of headroom over that. Too short reaps
// a slow-but-alive peer mid-turn, which costs one turn and leaves the session
// resumable; too long is the old unbounded stall, later.
var writeDeadline = 30 * time.Second

// handshakeTimeout rewrites an expired handshake budget into something a person
// can act on, and returns every other failure exactly as it was.
//
// Left raw, the error is `session/new: context deadline exceeded`: it names an
// internal concept, reads as a backend fault, and says nothing about the one
// thing the user needs to know, which is that pressing Send again is the whole
// recovery (the failed start reaps the subprocess and clears the registration, so
// the next Send spawns a fresh bridge). The wrap keeps
// errors.Is(err, context.DeadlineExceeded) true, so a caller that classifies
// still can, and it keeps the raw cause as the tail for diagnosis.
func handshakeTimeout(err error, phase string, budget time.Duration) error {
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%s did not finish within %s. kiro-cli stopped answering during the handshake. Send again to retry, and check /api/health if it keeps happening: %w",
		phase, budget, err)
}

// localeEnvVar is the locale variable every child of this bridge inherits.
// TWIN of internal/agent's termLocaleEnvVar: an agent terminal composes its own
// child environment and internal/agent does not import this package, so the value
// is stated twice on purpose. Change one, change the other.
const localeEnvVar = "LANG"

// localeEnv pins the text encoding of everything kiro-cli spawns.
//
// The runtime image ships no `locales` package, so an unset LANG leaves glibc's
// default C locale and each child then picks its own output encoding — git
// octal-escapes every non-ASCII path in status, diff and log output under it.
// C.UTF-8 is a glibc built-in and needs no generated locale files. The image sets
// the same value, so this is the half that cannot be overridden by whatever the
// server process happened to inherit.
func localeEnv() []string {
	return []string{localeEnvVar + "=C.UTF-8"}
}

// buildACPArgs assembles the kiro-cli ACP invocation. The relay owns
// authentication, so KAS never sends the access-token request to vibekit.
func buildACPArgs(engine string) []string {
	if engine == "" {
		engine = vibekit.AgentEngineV3
	}
	return []string{"acp", "--agent-engine", engine, "--auth-method", "cli"}
}

func (b *Bridge) startProcess(engine string) error {
	if b.cliPath == "" {
		// The install manager has no active version: either the first-boot
		// install is still running or it failed with nothing on the volume to
		// fall back to. Say so, because this message is what the client shows
		// instead of a bare spawn failure -- /api/health carries the same
		// verdict with the phase in its reason.
		return errors.New("kiro-cli is not available yet: the pinned version is still installing or its install failed (see /api/health)")
	}
	// Operator flags land AFTER the derived ones, so a launch flag is an
	// initial value rather than an override in either direction: kiro-cli takes
	// the last spelling of a repeated flag, and vibekit's own switch_model /
	// set_effort commands still win afterwards via session/set_config_option.
	// Already filtered (see acp_args.go) — never trust this slice to be safe
	// because it came through StartOpts.
	args := append(buildACPArgs(engine), b.extraArgs...)
	// Normal teardown is owned by Stop(). lifecycleCtx is the runtime's shutdown
	// context: a belt-and-braces kill so a bridge cannot outlive the process
	// if Stop() races or panics. Start refuses a nil StartOpts.Lifetime, so
	// this is non-nil by construction and needs no fallback — the
	// context.Background() one that used to live here was a second
	// uncancellable substitution behind Start's own. It is never a request or
	// turn context — see vibekit.StartOpts.Lifetime.
	b.cmd = exec.CommandContext(b.lifecycleCtx, b.cliPath, args...) //nolint:gosec // G204: binary path from the install manager, never user input
	// Own process group, so teardown can reclaim the whole tree. Closing stdin
	// first is still what gives kiro-cli a chance to exit on its own, but it is
	// no longer sufficient: see procgroup.Kill for the 2.18.0 measurement.
	b.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Env is set UNCONDITIONALLY, which is what makes the credential screen
	// (bridge_env.go) cover every spawn: a nil Env means os/exec inherits the
	// parent environment implicitly, so a screen applied only when an overlay
	// happens to be configured would be inert on the path that has none.
	// b.extraEnv still lands LAST, so the active version directory wins PATH
	// resolution over anything the inherited environment puts ahead of it.
	env, dropped := screenBridgeEnv(os.Environ(), b.extraEnv, b.envAllow)
	// AFTER the screen, so it wins over anything inherited or overlaid: this is
	// the half of the memory switch that cannot ride the wire, because the
	// settings bridge reaches the gate's veto but not its eligibility term.
	env = append(env, memoryEnv(b.memory)...)
	// AFTER the screen and after the memory lever, for the same reason: os/exec
	// keeps the LAST value for a repeated key, so appending a locale before them
	// would be a silent no-op against an inherited one.
	env = append(env, localeEnv()...)
	b.cmd.Env = env
	if len(dropped) > 0 {
		// NAMES only: their values are the credentials this dropped. One line
		// per spawn rather than a startup summary, because a chat is what
		// carries the environment down to the agent and this is that event.
		slog.Warn("dropped credential-shaped variables from the kiro-cli environment",
			"variables", strings.Join(dropped, ","),
			"hint", "keep credentials out of the server environment, or allow the name via "+EnvAllowVar)
	}
	// Belt-and-braces graceful shutdown when lifecycleCtx is canceled.
	// Default CommandContext behavior is immediate SIGKILL; Cancel + WaitDelay
	// (Go 1.20+) escalate to SIGKILL only after a 5s SIGTERM grace period,
	// giving kiro-cli a chance to flush its own state. Stop() (called for
	// normal teardown) goes straight to SIGKILL so chat-switch and tab-close
	// teardown remain instantaneous; this path only fires if Stop races or
	// panics during agent shutdown.
	//
	// Closing stdin FIRST is what makes the grace period mean anything: vibekit
	// spawns `kiro-cli acp` on pipes and the head passes its stdio down, so the
	// tree (kiro-cli -> kiro-cli-chat -> node, ~300 MB) shares one session and
	// closing our write end delivers EOF to the entire chain, letting kiro-cli
	// exit on its own terms.
	//
	// The signal then goes to the GROUP rather than the head. Closing stdin
	// used to be enough by itself — measured on kiro-cli 2.16.0,
	// signal-without-close leaked 2/2 trials at ~250 MB each while
	// close-then-signal leaked 0/2 — and on 2.18.0 it is not: one ordinary
	// teardown left `kiro-cli-chat` alive at 33 MB with its `node` child at
	// 218 MB, reparented to init. Signal errors are returned; a Close error is
	// not, because a second Close is the expected case when Stop() already ran.
	b.cmd.Cancel = func() error {
		if b.stdin != nil {
			_ = b.stdin.Close()
		}
		return procgroup.Kill(b.cmd.Process, syscall.SIGTERM)
	}
	b.cmd.WaitDelay = 5 * time.Second
	stdin, err := b.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdoutPipe, err := b.cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := b.cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdoutPipe.Close()
		return fmt.Errorf("stderr pipe: %w", err)
	}
	b.stdin = stdin
	// A frameReader rather than a bufio.Scanner: an oversize frame has to be
	// survivable, and ErrTooLong is terminal for the Scanner that raised it.
	// See bridge_frame.go.
	b.stdout = newFrameReader(bufio.NewReaderSize(stdoutPipe, stdoutBufSize))
	if startErr := b.cmd.Start(); startErr != nil {
		_ = stdin.Close()
		_ = stdoutPipe.Close()
		_ = stderrPipe.Close()
		return fmt.Errorf("start kiro-cli acp: %w", startErr)
	}
	go b.forwardStderr(stderrPipe)
	go b.readLoop()
	return nil
}

// forwardStderr drains the subprocess's stderr pipe line-by-line and
// emits each line via slog at the classified level with an explicit
// source field. Keeps kiro-cli crash traces + rate-limit notices in
// Loki with proper level tags so alert rules keyed on level="error"
// and dashboards filtering by source work. Also prevents kiro-cli
// from forging slog-shaped JSON into vibekit's stderr by ensuring
// every line travels through a structured logger.
//
// Classification strategy (in priority order):
//  1. Try to parse the line as JSON and extract a "level" field
//     (kiro-cli emits slog-JSON on stderr in some modes).
//  2. For unstructured lines, match keywords at word boundaries
//     (requires trailing colon or bracket to avoid false positives
//     like "0 errors found").
//
// Lines longer than stderrLineCap are marked as truncated and drained through
// their newline so one oversized diagnostic cannot stop later lines from being
// forwarded. The goroutine exits naturally when cmd.Wait closes the pipe.
func (b *Bridge) forwardStderr(r io.Reader) {
	br := bufio.NewReaderSize(r, stderrLineCap+1)
	for {
		line, ok, err := readStderrLine(br)
		if err != nil || !ok {
			return
		}
		lvl := classifyStderrLevel(line)
		slog.Log(b.lifecycleCtx, lvl, "kiro-cli stderr",
			"source", "kiro_cli_stderr",
			"line", line)
	}
}

const stderrTruncationMarker = "... [truncated]"

func readStderrLine(r *bufio.Reader) (line string, ok bool, err error) {
	buf := make([]byte, 0, 4096)
	truncated := false
	for {
		fragment, readErr := r.ReadSlice('\n')
		if readErr != bufio.ErrBufferFull {
			fragment = bytes.TrimSuffix(fragment, []byte{'\n'})
			fragment = bytes.TrimSuffix(fragment, []byte{'\r'})
		}
		remaining := stderrLineCap - len(buf)
		if len(fragment) > remaining {
			fragment = fragment[:remaining]
			truncated = true
		}
		buf = append(buf, fragment...)
		if readErr == bufio.ErrBufferFull {
			continue
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return "", false, readErr
		}
		if errors.Is(readErr, io.EOF) && len(buf) == 0 && len(fragment) == 0 {
			return "", false, nil
		}
		if truncated {
			buf = buf[:stderrLineCap-len(stderrTruncationMarker)]
			buf = append(buf, stderrTruncationMarker...)
		}
		return string(buf), true, nil
	}
}

// jsonLevelMap maps structured JSON "level" field values to slog levels.
var jsonLevelMap = map[string]slog.Level{
	"ERROR":   slog.LevelError,
	"WARN":    slog.LevelWarn,
	"WARNING": slog.LevelWarn,
	"DEBUG":   slog.LevelDebug,
}

// stderrKeywordRule maps an unstructured keyword to a slog level.
type stderrKeywordRule struct {
	Keyword string
	Level   slog.Level
}

// stderrKeywordRules defines the keyword-to-level classification for
// unstructured stderr lines. Order matters: first match wins.
var stderrKeywordRules = []stderrKeywordRule{
	{"panic", slog.LevelError},
	{"fatal", slog.LevelError},
	{"error", slog.LevelError},
	{"warn", slog.LevelWarn},
}

// classifyStderrLevel determines the slog level for a kiro-cli stderr line.
func classifyStderrLevel(line string) slog.Level {
	// Strategy 1: try structured JSON with a "level" field.
	if line != "" && line[0] == '{' {
		var structured struct {
			Level string `json:"level"`
		}
		if json.Unmarshal([]byte(line), &structured) == nil && structured.Level != "" {
			if lvl, ok := jsonLevelMap[strings.ToUpper(structured.Level)]; ok {
				return lvl
			}
			return slog.LevelInfo
		}
	}

	// Strategy 2: word-boundary matching for unstructured lines.
	low := strings.ToLower(line)
	for _, rule := range stderrKeywordRules {
		if matchesKeyword(low, rule.Keyword) {
			return rule.Level
		}
	}
	return slog.LevelInfo
}

// matchesKeyword checks if keyword appears at a word boundary followed
// by a colon, bracket, or at the start of the line followed by a
// separator. This avoids false positives on substrings like "errors".
func matchesKeyword(low, keyword string) bool {
	idx := 0
	for {
		pos := strings.Index(low[idx:], keyword)
		if pos < 0 {
			return false
		}
		pos += idx
		// Check word boundary before: must be at start or preceded by non-alpha.
		if pos > 0 && low[pos-1] >= 'a' && low[pos-1] <= 'z' {
			idx = pos + len(keyword)
			continue
		}
		// Check word boundary after: must be followed by ':', '[', ']', ' ', or end.
		end := pos + len(keyword)
		if end >= len(low) {
			return true
		}
		ch := low[end]
		if ch == ':' || ch == '[' || ch == ']' || ch == ' ' || ch == '=' {
			return true
		}
		idx = end
	}
}
