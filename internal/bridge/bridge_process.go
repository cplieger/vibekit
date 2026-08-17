package bridge

import (
	"bufio"
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

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/procgroup"
)

// Start launches the kiro-cli subprocess and either creates a new ACP
// session (acpSessionID == "") or loads an existing one. Exactly one call
// per bridge instance.
//
// ctx bounds the startup handshake only; opts.Lifetime bounds the subprocess.
func (b *Bridge) Start(ctx context.Context, opts *api.StartOpts) error {
	// The subprocess outlives this call. Start's ctx bounds the handshake
	// below; opts.Lifetime bounds the process. See api.StartOpts.Lifetime for
	// what taking the process lifetime from a turn context measured like.
	b.lifecycleCtx = opts.Lifetime
	if b.lifecycleCtx == nil {
		b.lifecycleCtx = context.WithoutCancel(ctx)
	}
	// Immutable after Start; read lock-free by SetModel / initialize.
	b.agentEngine = opts.AgentEngine
	b.enableHooks = opts.EnableHooks
	b.secretStorage = opts.SecretStorage
	b.extraArgs = opts.ExtraArgs
	if opts.SessionID != "" && !api.ValidSessionID(opts.SessionID) {
		return fmt.Errorf("invalid acp session id: %q", opts.SessionID)
	}
	if opts.Model != "" && !validIdent(opts.Model) {
		return fmt.Errorf("invalid model identifier: %q", opts.Model)
	}
	if err := b.startProcess(opts.AgentEngine); err != nil {
		return err
	}
	if err := b.initialize(ctx); err != nil {
		b.Stop()
		return err
	}
	var err error
	if opts.SessionID != "" {
		err = b.loadSession(ctx, opts.SessionID, opts.Model)
	} else {
		err = b.newSession(ctx, opts)
	}
	if err != nil {
		b.Stop()
		return err
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
	)
	return nil
}

// Stop kills the subprocess and closes NotifCh. Safe to call multiple
// times; subsequent calls are no-ops. Multiple call sites (hub.Shutdown,
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
			// The GROUP, not the head. kiro-cli passes its stdio down, so the
			// stdin close above reaches the whole chain and is what lets it exit
			// gracefully — but on kiro-cli 2.18.0 that no longer reclaims the
			// tree, and a head-only kill left `kiro-cli-chat` plus its `node`
			// child alive at ~250 MB, reparented to init, on every model switch,
			// tab close and idle cull. See procgroup.Kill.
			//
			// Demote the expected case (process already exited after stdin
			// close) to Debug so every graceful teardown doesn't emit an ERROR.
			if err := procgroup.Kill(b.cmd.Process, syscall.SIGKILL); err != nil && !errors.Is(err, os.ErrProcessDone) {
				slog.Error("kill kiro-cli", "error", err)
			}
			// Wait releases the OS process entry so repeated chat
			// switch / cull-idle cycles do not leak zombies. Kill
			// guarantees a non-zero exit status, so we intentionally
			// discard the returned error.
			_ = b.cmd.Wait()
		}
	})
}

// buildACPArgs assembles the kiro-cli `acp` invocation arguments. Kept
// pure (no process side effects) so the arg shaping is unit-testable.
//
// The arg set is the engine and nothing else. vibekit passes no
// permission/trust flags: tool-call authorization on v3 (KAS) is owned by
// kiro-cli's native Cedar policy engine, which ignores the legacy
// --trust-all-tools / --trust-tools flags (confirmed inert on the v3 acp
// wire — the permission prompt fires regardless).
//
// It carries NO --model and NO --effort either, because kiro-cli REFUSES both
// alongside --agent-engine=v3 and exits before it answers initialize:
//
//	error: the following arguments are not supported with --agent-engine=v3: --model, --effort
//
// Measured against 2.17.0 and 2.18.0; `-v` is the only other flag v3 accepts.
// So a launch flag was never how a v3 session got its model, and emitting one
// killed the process: see bridge_session.go applyInitialModel /
// applyInitialEffort for the config-option path that replaces it.
func buildACPArgs(engine string) []string {
	// The agent engine determines which ACP methods the agent registers.
	// Default to v3 (KAS); vibekit is v3-only. v3 requires the host to
	// answer the _kiro/auth/getAccessToken + _kiro/terminal/shell_type
	// callbacks (see internal/hub/bridge_v3_auth.go).
	if engine == "" {
		engine = api.AgentEngineV3
	}
	return []string{"acp", "--agent-engine", engine}
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
	// Normal teardown is owned by Stop(). lifecycleCtx is the hub's shutdown
	// context: a belt-and-braces kill so a bridge cannot outlive the process
	// if Stop() races or panics. Start guarantees it is non-nil, and it is
	// never a request or turn context — see api.StartOpts.Lifetime.
	ctx := b.lifecycleCtx
	if ctx == nil {
		ctx = context.Background()
	}
	b.cmd = exec.CommandContext(ctx, b.cliPath, args...) //nolint:gosec // G204: binary path from the install manager, never user input
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
	// panics during hub shutdown.
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
// Line cap is stderrLineCap; longer lines are truncated by the
// scanner (bufio.Scanner silently drops the tail on cap hit), which
// is acceptable for log lines. The goroutine exits naturally when
// cmd.Wait closes the pipe on process exit.
func (b *Bridge) forwardStderr(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), stderrLineCap)
	for sc.Scan() {
		line := sc.Text()
		lvl := classifyStderrLevel(line)
		slog.Log(b.lifecycleCtx, lvl, "kiro-cli stderr",
			"source", "kiro_cli_stderr",
			"line", line)
	}
	// Scanner error (pipe closed on process exit) is the expected
	// terminal condition; no need to log.
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
