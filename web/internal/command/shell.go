package command

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"vibekit/internal/api"
	"vibekit/internal/ids"
)

// ShellOutputCap bounds the captured stdout+stderr of a `!cmd` shell interception.
const ShellOutputCap = 1 * 1024 * 1024

// ShellTimeout is the default timeout for user-initiated `!cmd` shell
// interceptions. Exposed as a package-level constant so tests and
// future settings overrides can reference the default.
const ShellTimeout = 30 * time.Second

// ShellCappedBuffer writes to an underlying bytes.Buffer, rejecting
// bytes past the cap.
type ShellCappedBuffer struct {
	Buf       bytes.Buffer
	Truncated bool
}

func (b *ShellCappedBuffer) Write(p []byte) (int, error) {
	remaining := ShellOutputCap - b.Buf.Len()
	if remaining <= 0 {
		b.Truncated = true
		return len(p), nil
	}
	if len(p) <= remaining {
		return b.Buf.Write(p)
	}
	b.Truncated = true
	if _, err := b.Buf.Write(p[:remaining]); err != nil {
		return 0, err
	}
	return len(p), nil
}

// HandleShellInterception runs a "!" prefixed prompt as a local shell command.
func HandleShellInterception(d *Dispatcher, deps Dependencies, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand, p *api.PromptCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	shellCmd := strings.TrimPrefix(p.Text, "!")
	shellCmd = strings.TrimSpace(shellCmd)
	if shellCmd == "" {
		d.RespondErr(w, http.StatusBadRequest, errEmptyPrompt)
		return
	}

	deps.InflightAdd(1)
	defer deps.InflightDone()

	// Persist the user message.
	userMsg := api.Message{
		ID: p.MessageID, Role: api.RoleUser, Ts: time.Now().UnixMilli(),
		Content: p.Text,
	}
	var persisted bool
	var triggerRename bool
	if err := deps.ChatStore().Mutate(ctx, cmd.ChatID, func(c *api.Chat, exists bool) bool {
		if !exists {
			c.Name = api.DefaultChatName
		}
		c.Messages = append(c.Messages, userMsg)
		if c.Name == api.DefaultChatName && len(c.Messages) == 1 {
			name := TruncateRunes(p.Text, 80)
			if name != p.Text {
				name += ellipsis
			}
			c.Name = name
			triggerRename = true
		}
		deps.Broadcast(ctx, api.NewEvent(api.EventMessageAppended, cmd.ChatID, &userMsg))
		persisted = true
		return true
	}); err != nil {
		d.RespondErr(w, http.StatusInternalServerError, err)
		return
	}
	if !persisted {
		d.RespondErr(w, http.StatusConflict, ErrChatNotFound)
		return
	}
	if triggerRename {
		if prompter := d.Prompter(); prompter != nil {
			deps.InflightGo(func() { AsyncRenameChat(deps, prompter, cmd.ChatID, p.Text) })
		}
	}

	slog.Info("shell interception", "chat_id", cmd.ChatID, "cmd_len", len(shellCmd))
	start := time.Now()

	// Derive timeout from the request context so both client disconnect
	// and server shutdown cancel the shell process.
	shellCtx, cancel := context.WithTimeout(ctx, ShellTimeout)
	defer cancel()

	shellProc := exec.CommandContext(shellCtx, "sh", "-c", shellCmd) //nolint:gosec // G702: user-initiated shell command
	shellProc.Dir = deps.WorkDir()
	var capped ShellCappedBuffer
	shellProc.Stdout = &capped
	shellProc.Stderr = &capped
	runErr := shellProc.Run()

	raw := capped.Buf.String()
	if runErr != nil {
		raw += "\n" + runErr.Error()
	}
	if capped.Truncated {
		raw += "\n[output truncated at 1 MiB]"
	}
	output := api.SanitizeOutput(raw)

	slog.Info("shell interception complete",
		"chat_id", cmd.ChatID,
		"elapsed_ms", time.Since(start).Milliseconds(),
		"exit_error", runErr != nil,
		"truncated", capped.Truncated)

	content := "```\n" + strings.TrimRight(output, "\n") + "\n```"
	msgID := ids.NewMessageID()
	assistantMsg := api.Message{
		ID: msgID, Role: api.RoleAssistant, Ts: time.Now().UnixMilli(),
		Content: content,
	}
	appendErr := deps.ChatStore().AppendMessage(ctx, cmd.ChatID, &assistantMsg)
	if appendErr != nil {
		slog.Error("shell interception: persist output", "chat_id", cmd.ChatID, keyError, appendErr)
	}
	if _, stillExists := deps.ChatStore().Get(ctx, cmd.ChatID); stillExists {
		deps.Broadcast(ctx, api.NewEvent(api.EventMessageAppended, cmd.ChatID, &assistantMsg))
		deps.Broadcast(ctx, api.NewEvent(api.EventTurnEnded, cmd.ChatID, api.TurnEndedPayload{StopReason: api.StopReasonEndTurn}))
	}
	d.RespondOK(w, cmd.RequestID)
}
