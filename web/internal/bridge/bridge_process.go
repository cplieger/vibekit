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

	"vibekit/internal/api"
)

// Start launches the kiro-cli subprocess and either creates a new ACP
// session (acpSessionID == "") or loads an existing one. Exactly one call
// per bridge instance. extraArgs are permission-mode flags derived from
// the user's settings at spawn time (nil == prompt for every tool).
// mcpServers is the ACP mcpServers array (enabled user-configured MCP
// servers); pass nil for an empty set.
func (b *Bridge) Start(ctx context.Context, opts *api.StartOpts) error {
	b.lifecycleCtx = ctx
	if opts.SessionID != "" {
		if !validSessionID(opts.SessionID) {
			return fmt.Errorf("invalid acp session id: %q", opts.SessionID)
		}
		RemoveStaleLock(ctx, opts.SessionID)
	}
	if err := b.startProcess(opts.Agent, opts.Model, opts.ExtraArgs); err != nil {
		return err
	}
	if err := b.initialize(ctx); err != nil {
		b.Stop()
		return err
	}
	var err error
	if opts.SessionID != "" {
		err = b.loadSession(ctx, opts.SessionID, opts.Model, opts.MCPServers)
	} else {
		err = b.newSession(ctx, opts.MCPServers)
	}
	if err != nil {
		b.Stop()
		return err
	}
	// Lifecycle breadcrumb so operators correlating Loki logs can
	// see which bridge a subsequent Stop / error belongs to without
	// reading the chat store. Cardinality stays sane: session id is
	// bounded by chats, model/agent by catalog.
	slog.Info("bridge started",
		"session_id", b.SessionID(),
		"agent", opts.Agent,
		"model", b.ModelID(),
		"work_dir", b.workDir,
		"acp_session_id", opts.SessionID,
	)
	return nil
}

// Stop kills the subprocess and closes NotifCh. Safe to call multiple
// times; subsequent calls are no-ops. Multiple call sites (hub.Shutdown,
// cullIdleBridges, session/load recovery) can race to stop the same
// bridge; the sync.Once gate prevents a double-close panic on b.done.
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
			_ = b.cmd.Wait() //nolint:errcheck // expected non-zero exit after Kill
		}
	})
}

func (b *Bridge) startProcess(agent, model string, extraArgs []string) error {
	if !validIdent(agent) {
		return fmt.Errorf("invalid agent identifier: %q", agent)
	}
	if !validIdent(model) {
		return fmt.Errorf("invalid model identifier: %q", model)
	}
	args := []string{"acp"}
	args = append(args, extraArgs...)
	if agent != "" {
		args = append(args, "--agent", agent)
	}
	if model != "" && model != "auto" {
		args = append(args, "--model", model)
	}
	// Lifecycle is owned by Stop(), not by a context. The
	// lifecycleCtx is a belt-and-braces kill signal: if the hub
	// shuts down and Stop() races or panics, the OS-level context
	// cancellation kills the subprocess.
	ctx := b.lifecycleCtx
	if ctx == nil {
		ctx = context.Background()
	}
	b.cmd = exec.CommandContext(ctx, b.cliPath, args...)
	// Belt-and-braces graceful shutdown when lifecycleCtx is canceled.
	// Default CommandContext behavior is immediate SIGKILL; Cancel + WaitDelay
	// (Go 1.20+) escalate to SIGKILL only after a 5s SIGTERM grace period,
	// giving kiro-cli a chance to flush its own state. Stop() (called for
	// normal teardown) still uses Process.Kill directly so chat-switch and
	// cull-idle remain instantaneous; this path only fires if Stop races
	// or panics during hub shutdown.
	b.cmd.Cancel = func() error { return b.cmd.Process.Signal(syscall.SIGTERM) }
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

// classifyStderrLevel determines the slog level for a kiro-cli stderr line.
func classifyStderrLevel(line string) slog.Level {
	// Strategy 1: try structured JSON with a "level" field.
	if line != "" && line[0] == '{' {
		var structured struct {
			Level string `json:"level"`
		}
		if json.Unmarshal([]byte(line), &structured) == nil && structured.Level != "" {
			switch strings.ToUpper(structured.Level) {
			case "ERROR":
				return slog.LevelError
			case "WARN", "WARNING":
				return slog.LevelWarn
			case "DEBUG":
				return slog.LevelDebug
			default:
				return slog.LevelInfo
			}
		}
	}

	// Strategy 2: word-boundary matching for unstructured lines.
	// Require the keyword to appear at the start of the line or
	// followed by a colon/bracket to avoid false positives like
	// "0 errors found" or "ErrorBoundary component loaded".
	low := strings.ToLower(line)
	for _, kw := range []string{"panic", "fatal"} {
		if matchesKeyword(low, kw) {
			return slog.LevelError
		}
	}
	if matchesKeyword(low, "error") {
		return slog.LevelError
	}
	if matchesKeyword(low, "warn") {
		return slog.LevelWarn
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
