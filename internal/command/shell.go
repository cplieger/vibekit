package command

// The `!cmd` shell interception.
//
// Output capture is procout's, not this package's: internal/auth and
// internal/server already share procout.Buffer, whose Write also reports
// the bytes it kept, which makes io.Copy return io.ErrShortWrite on a
// truncated capture — Cmd.Wait then hands that back as the command's error
// on a child that exited 0.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/cplieger/vibekit/internal/ids"
	"github.com/cplieger/vibekit/internal/procout"
	"github.com/cplieger/vibekit/internal/sanitize"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// ShellOutputCap bounds the captured stdout+stderr of a `!cmd` shell interception.
const ShellOutputCap = 1 * 1024 * 1024

// ShellTimeout is the default timeout for user-initiated `!cmd` shell
// interceptions. Exposed as a package-level constant so tests and
// future settings overrides can reference the default.
const ShellTimeout = 30 * time.Second

// appendShellUserMessage persists the user's "!cmd" message and, on the
// chat's first message, derives an initial chat name from the command text.
// Returns whether the message was persisted (false when the chat record
// doesn't exist).
func appendShellUserMessage(ctx context.Context, chats ChatStore, bus Broadcaster, chatID vibekit.ChatID, msg *vibekit.Message, text string) (persisted bool, err error) {
	err = chats.Mutate(ctx, chatID, func(c *vibekit.Chat, exists bool) bool {
		if !exists {
			c.Name = vibekit.DefaultChatName
		}
		c.Messages = append(c.Messages, *msg)
		if c.Name == vibekit.DefaultChatName && len(c.Messages) == 1 {
			name := TruncateRunes(text, 80)
			if name != text {
				name += ellipsis
			}
			c.Name = name
		}
		bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventMessageAppended, chatID, msg))
		persisted = true
		return true
	})
	return persisted, err
}

// HandleShellInterception runs a "!" prefixed prompt as a local shell command.
func HandleShellInterception(ctx context.Context, roles *promptRoles, cmd *vibekit.ClientCommand, p *vibekit.PromptCommand) (any, error) {
	shellCmd := strings.TrimPrefix(p.Text, "!")
	shellCmd = strings.TrimSpace(shellCmd)
	if shellCmd == "" {
		return nil, StatusError(http.StatusBadRequest, errEmptyPrompt)
	}

	// Admission: the same per-chat reservation a prompt takes, as a try —
	// never a wait. One mechanism serializes prompts and shells, whatever
	// state the bridge is in.
	if !roles.turnOutcome.TryReserveTurn(cmd.ChatID, vibekit.TurnSourceLocalShell) {
		return nil, StatusError(http.StatusConflict, errBusy)
	}
	defer roles.turnOutcome.ReleaseTurnReservation(cmd.ChatID)

	roles.lifecycle.InflightAdd(1)
	defer roles.lifecycle.InflightDone()

	// Persist the user message.
	userMsg := vibekit.Message{
		ID: p.MessageID, Role: vibekit.RoleUser, Ts: time.Now().UnixMilli(),
		Content: p.Text,
	}
	persisted, err := appendShellUserMessage(ctx, roles.chats, roles.bus, cmd.ChatID, &userMsg, p.Text)
	if err != nil {
		return nil, StatusError(http.StatusInternalServerError, err)
	}
	if !persisted {
		return nil, StatusError(http.StatusConflict, ErrChatNotFound)
	}

	// The shell turn opens a turn like any other: without a record its end
	// would be a broadcast nothing owned. A zero epoch is the source rule
	// refusing while an agent turn is open.
	epoch := roles.turnOutcome.StartTurn(ctx, cmd.ChatID, vibekit.TurnSourceLocalShell)
	if epoch == 0 {
		return nil, StatusError(http.StatusConflict, errBusy)
	}
	defer roles.turnOutcome.ReleaseTurn(cmd.ChatID, epoch)

	slog.Info("shell interception", "chat_id", cmd.ChatID, "cmd_len", len(shellCmd))
	start := time.Now()

	// Bound the command with a 30s timeout derived from the request
	// context, so a client disconnect also cancels the shell process.
	shellCtx, cancel := context.WithTimeout(ctx, ShellTimeout)
	defer cancel()

	shellProc := exec.CommandContext(shellCtx, "sh", "-c", shellCmd)
	shellProc.Dir = roles.workspace.Dir
	// One *procout.Buffer on both streams is the documented way to merge them:
	// os/exec compares the two writers and guarantees at most one goroutine
	// calls Write at a time, so the two copiers do not race.
	capped := procout.NewBuffer(ShellOutputCap)
	shellProc.Stdout = capped
	shellProc.Stderr = capped
	runErr := shellProc.Run()

	raw := capped.String()
	if capped.Truncated() {
		raw += "\n[output truncated at 1 MiB]"
	}
	output := sanitize.Output(raw)
	timedOut := errors.Is(shellCtx.Err(), context.DeadlineExceeded)

	slog.Info("shell interception complete",
		"chat_id", cmd.ChatID,
		"elapsed_ms", time.Since(start).Milliseconds(),
		"exit_error", runErr != nil,
		"timed_out", timedOut,
		"truncated", capped.Truncated())

	content := renderShellResult(output, runErr, timedOut)
	msgID := ids.NewMessageID()
	assistantMsg := vibekit.Message{
		ID: msgID, Role: vibekit.RoleAssistant, Ts: time.Now().UnixMilli(),
		Content: content,
	}
	appendErr := roles.chats.AppendMessage(ctx, cmd.ChatID, &assistantMsg)
	if appendErr != nil {
		slog.Error("shell interception: persist output", "chat_id", cmd.ChatID, keyError, appendErr)
	}
	if _, stillExists := roles.chats.Get(ctx, cmd.ChatID); stillExists {
		roles.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventMessageAppended, cmd.ChatID, &assistantMsg))
		roles.turnOutcome.FinalizeLocalShellTurn(ctx, cmd.ChatID, epoch)
	}
	return responseOK, nil
}

// renderShellResult wraps sanitized command output in a Markdown code
// fence and appends a status line describing how the command ended.
//
// The fence length is dynamic — one backtick longer than the longest
// backtick run anywhere in the body — so output that itself contains a
// ``` run (e.g. `!cat README.md`, `!git show`) can never close the fence
// early and leak the remainder to the client as rendered Markdown.
func renderShellResult(output string, runErr error, timedOut bool) string {
	output = strings.TrimRight(output, "\n")
	body := shellStatusLine(runErr, timedOut)
	if output != "" {
		body = output + "\n" + body
	}
	fence := shellFence(body)
	return fence + "\n" + body + "\n" + fence
}

// shellStatusLine reports the command's outcome as a bracketed status
// line shown beneath the output. A timeout gets a clear message rather
// than the opaque "signal: killed" the OS reports for the SIGKILL; every
// other outcome shows the process exit code so a non-zero exit is visible.
func shellStatusLine(runErr error, timedOut bool) string {
	switch {
	case timedOut:
		return fmt.Sprintf("[command timed out after %s]", ShellTimeout)
	case runErr == nil:
		return "[exit 0]"
	default:
		if exitErr, ok := errors.AsType[*exec.ExitError](runErr); ok {
			return fmt.Sprintf("[exit %d]", exitErr.ExitCode())
		}
		// The command could not be run at all (e.g. sh missing, or the
		// request context was cancelled before the process started).
		return "[error: " + runErr.Error() + "]"
	}
}

// shellFence returns a backtick code fence long enough to wrap body
// without body's own backtick runs closing it: at least three backticks,
// and always one more than the longest consecutive backtick run in body.
func shellFence(body string) string {
	longest, run := 0, 0
	for _, r := range body {
		if r == '`' {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	return strings.Repeat("`", max(longest+1, 3))
}
