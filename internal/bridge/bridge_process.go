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
)

// Start launches the kiro-cli subprocess and either creates a new ACP
// session (acpSessionID == "") or loads an existing one. Exactly one call
// per bridge instance. mcpServers is the ACP mcpServers array (enabled
// user-configured MCP servers); pass nil for an empty set.
func (b *Bridge) Start(ctx context.Context, opts *api.StartOpts) error {
	b.lifecycleCtx = ctx
	// Immutable after Start; read lock-free by SetModel / initialize.
	b.agentEngine = opts.AgentEngine
	b.enableHooks = opts.EnableHooks
	b.secretStorage = opts.SecretStorage
	b.extraArgs = opts.ExtraArgs
	if opts.SessionID != "" && !api.ValidSessionID(opts.SessionID) {
		return fmt.Errorf("invalid acp session id: %q", opts.SessionID)
	}
	if err := b.startProcess(opts.AgentEngine, opts.Model, opts.Effort); err != nil {
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
		err = b.newSession(ctx, opts.Mode, opts.Supervised)
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
			// Demote the expected case (process already exited after
			// stdin close) to Debug so every graceful teardown doesn't
			// emit an ERROR line.
			if err := b.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
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
// pure (no process side effects) so the arg shaping — including the
// kiro-cli >=2.6 `--effort` flag and its validation — is unit-testable.
//
// The arg set is fixed (engine + model + effort): vibekit passes no
// permission/trust flags. Tool-call authorization on v3 (KAS) is owned
// by kiro-cli's native Cedar policy engine, which ignores the legacy
// --trust-all-tools / --trust-tools flags (confirmed inert on the v3
// acp wire — the permission prompt fires regardless), so vibekit no
// longer emits them.
func buildACPArgs(engine, model, effort string) []string {
	// The agent engine determines which ACP methods the agent registers.
	// Default to v3 (KAS); vibekit is v3-only. v3 requires the host to
	// answer the _kiro/auth/getAccessToken + _kiro/terminal/shell_type
	// callbacks (see internal/hub/bridge_v3_auth.go).
	if engine == "" {
		engine = api.AgentEngineV3
	}
	args := []string{"acp", "--agent-engine", engine}
	if model != "" && model != api.ModelAuto {
		args = append(args, "--model", model)
	}
	// kiro-cli >=2.6 accepts an initial effort at launch. Validate
	// defensively — the value flows into exec args.
	if effort != "" && api.EffortLevel(effort).Valid() {
		args = append(args, "--effort", effort)
	}
	return args
}

func (b *Bridge) startProcess(engine, model, effort string) error {
	if !validIdent(model) {
		return fmt.Errorf("invalid model identifier: %q", model)
	}
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
	args := append(buildACPArgs(engine, model, effort), b.extraArgs...)
	// Lifecycle is owned by Stop(), not by a context. The
	// lifecycleCtx is a belt-and-braces kill signal: if the hub
	// shuts down and Stop() races or panics, the OS-level context
	// cancellation kills the subprocess.
	ctx := b.lifecycleCtx
	if ctx == nil {
		ctx = context.Background()
	}
	b.cmd = exec.CommandContext(ctx, b.cliPath, args...) //nolint:gosec // G204: binary path from the install manager, never user input
	if len(b.extraEnv) > 0 {
		// Appended LAST, so the active version directory wins PATH resolution
		// over anything the inherited environment puts ahead of it. A nil Env
		// (no overlay configured) keeps the plain inherited environment.
		b.cmd.Env = append(os.Environ(), b.extraEnv...)
	}
	// Belt-and-braces graceful shutdown when lifecycleCtx is canceled.
	// Default CommandContext behavior is immediate SIGKILL; Cancel + WaitDelay
	// (Go 1.20+) escalate to SIGKILL only after a 5s SIGTERM grace period,
	// giving kiro-cli a chance to flush its own state. Stop() (called for
	// normal teardown) still uses Process.Kill directly so chat-switch and
	// tab-close teardown remain instantaneous; this path only fires if Stop
	// races or panics during hub shutdown.
	//
	// Closing stdin FIRST is what makes the grace period mean anything, and it
	// is the whole reason Stop() reclaims the tree. vibekit spawns
	// `kiro-cli acp` on pipes and the head passes its stdio down, so the tree
	// (kiro-cli -> kiro-cli-chat -> node, ~300 MB) shares one session with no
	// setsid() and closing our write end delivers EOF to the entire chain. A
	// bare head SIGTERM does not: WaitDelay's SIGKILL escalation targets the
	// head only, so the children keep running with nobody holding their stdin.
	// Measured on kiro-cli 2.16.0: signal-without-close leaked 2/2 trials at
	// ~250 MB each, while close-then-signal leaked 0/2. Signal errors are
	// returned; a Close error is not, because a second Close is the expected
	// case when Stop() already ran.
	b.cmd.Cancel = func() error {
		if b.stdin != nil {
			_ = b.stdin.Close()
		}
		return b.cmd.Process.Signal(syscall.SIGTERM)
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
	b.stdout = bufio.NewScanner(stdoutPipe)
	// Line cap must fit fsWriteCap worst-case; see scannerLineCap.
	b.stdout.Buffer(make([]byte, 0, 64*1024), scannerLineCap)
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
