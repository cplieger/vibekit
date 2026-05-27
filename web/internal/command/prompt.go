package command

// Prompt command handler: validates, persists user message, acquires
// bridge, sends to kiro-cli, handles empty-turn recovery.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"vibekit/internal/api"
	"vibekit/internal/permissions"
)

// ACP method constant for prompt.
const methodPrompt = "session/prompt"

// validatePromptPayload parses and validates the prompt command payload.
func validatePromptPayload(cmd *api.ClientCommand) (api.PromptCommand, int, error) {
	var p api.PromptCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return p, http.StatusBadRequest, errInvalidPayload
	}
	if p.Text == "" {
		return p, http.StatusBadRequest, errEmptyPrompt
	}
	if len(p.Text) > maxPromptBytes {
		return p, http.StatusRequestEntityTooLarge, errPromptTooLong
	}
	if p.MessageID == "" {
		return p, http.StatusBadRequest, errMissingMessageID
	}
	if !ValidMessageID(p.MessageID) {
		return p, http.StatusBadRequest, errInvalidPayload
	}
	if !ValidIdent(p.Agent) || !ValidIdent(p.Model) {
		return p, http.StatusBadRequest, errInvalidPayload
	}
	return p, 0, nil
}

// retryWithBackoff retries fn up to maxAttempts times with a fixed delay.
func retryWithBackoff[T any](ctx context.Context, maxAttempts int, delay time.Duration, shouldRetry func(error) bool, fn func() (T, error)) (T, error) {
	result, err := fn()
	if err == nil || !shouldRetry(err) {
		return result, err
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		timer.Reset(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			return result, err
		}
		result, err = fn()
		if err == nil || !shouldRetry(err) {
			break
		}
	}
	return result, err
}

// callPromptWithRetry sends the prompt to kiro-cli with retry on transient errors.
func callPromptWithRetry(ctx context.Context, sb Bridge, params map[string]any, chatID api.ChatID) (*api.RPCResponse, error) {
	return retryWithBackoff(ctx, 2, 2*time.Second, func(err error) bool {
		if IsRetryablePromptError(err) {
			slog.Warn("prompt retry", "chat_id", chatID, keyError, err)
			return true
		}
		return false
	}, func() (*api.RPCResponse, error) {
		return sb.Call(ctx, methodPrompt, params)
	})
}

// recoverEmptyTurn handles empty turn recovery: recreate session and retry.
func recoverEmptyTurn(deps Dependencies, ctx context.Context, chatID api.ChatID, resp *api.RPCResponse, p *api.PromptCommand, params map[string]any) *api.RPCResponse { //nolint:revive // context-as-argument: dispatcher handler signature
	if !deps.IsEmptyTurn(resp, chatID) {
		return resp
	}
	slog.Warn("empty turn detected, recreating session", "chat_id", chatID)
	deps.CloseBridge(chatID)
	if err := deps.ChatStore().Mutate(ctx, chatID, func(c *api.Chat, ex bool) bool {
		if !ex {
			return false
		}
		c.ACPSessionID = ""
		return true
	}); err != nil {
		slog.Error("empty turn: clear session ID", "chat_id", chatID, keyError, err)
	}
	evt := api.Message{
		ID: NewMessageID(), Role: api.RoleEvent, Ts: time.Now().UnixMilli(),
		EventKind: api.EventInterrupted, Content: "Session refreshed, retrying...",
	}
	if err := deps.ChatStore().AppendMessage(ctx, chatID, &evt); err != nil {
		slog.Error("empty turn: append event", "chat_id", chatID, keyError, err)
	}
	sb2, err2 := deps.GetOrCreateBridge(ctx, chatID, p.Agent, p.Model)
	if err2 != nil {
		slog.Error("empty turn: respawn failed",
			"chat_id", chatID, keyError, err2)
		deps.Broadcast(ctx, api.NewEvent(api.EventError, chatID, api.ErrorPayload{
			Code:    api.ErrCodeRecoveryFailed,
			Message: "Session refresh failed: " + err2.Error(),
		}))
		return resp
	}
	sb2.SetLastActive()
	sb2.SetPrompting()
	defer sb2.ReleaseAfterPrompt()
	deps.AdvanceCheckpointTurn(ctx, chatID)
	params["sessionId"] = sb2.SessionID()
	retryResp, retryErr := callPromptWithRetry(ctx, sb2, params, chatID)
	if retryErr != nil {
		slog.Error("retry prompt failed", "chat_id", chatID, keyError, retryErr)
		deps.Broadcast(ctx, api.NewEvent(api.EventError, chatID, api.ErrorPayload{
			Code:    api.ErrCodeRecoveryFailed,
			Message: "Retry prompt failed: " + retryErr.Error(),
		}))
		return resp
	}
	return retryResp
}

// appendUserMessage adds the prompt's user message to the chat.
func appendUserMessage(deps Dependencies, ctx context.Context, chatID api.ChatID, p *api.PromptCommand) error { //nolint:revive // context-as-argument: dispatcher handler signature
	supervisedDefault := permissions.SupervisedDefault(ctx, deps.ConfigDir())
	err := deps.ChatStore().Mutate(ctx, chatID, func(c *api.Chat, exists bool) bool {
		if !exists {
			c.Name = api.DefaultChatName
			c.Agent = p.Agent
			c.Model = p.Model
			c.SupervisedMode = supervisedDefault
		}
		userMsg := api.Message{
			ID:      p.MessageID,
			Role:    api.RoleUser,
			Ts:      time.Now().UnixMilli(),
			Content: p.Text,
		}
		c.Messages = append(c.Messages, userMsg)
		if c.Name == api.DefaultChatName && len(c.Messages) == 1 {
			name := TruncateRunes(p.Text, 80)
			if name != p.Text {
				name += ellipsis
			}
			c.Name = name
			deps.InflightGo(func() { AsyncRenameChat(deps, chatID, p.Text) })
		}
		deps.Broadcast(ctx, api.NewEvent(api.EventMessageAppended, chatID, &userMsg))
		return true
	})
	return err
}

// CmdPrompt handles the prompt command.
func CmdPrompt(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	deps := d.Deps()
	if cmd.ChatID == "" {
		d.RespondErr(w, http.StatusBadRequest, errMissingChatID)
		return
	}
	p, code, vErr := validatePromptPayload(cmd)
	if vErr != nil {
		d.RespondErr(w, code, vErr)
		return
	}

	// Shell command interception.
	if strings.HasPrefix(p.Text, "!") {
		HandleShellInterception(d, deps, ctx, w, cmd, &p)
		return
	}

	// 1. Ensure the chat exists, append the user message, auto-rename.
	if err := appendUserMessage(deps, ctx, cmd.ChatID, &p); err != nil {
		d.RespondErr(w, http.StatusInternalServerError, err)
		return
	}

	deps.AdvanceCheckpointTurn(ctx, cmd.ChatID)

	// 2. Ensure the bridge exists and serialize per-chat prompts.
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(deps.ShutdownCtx(), cancel)
	defer stop()
	defer cancel()
	sb, err := deps.GetOrCreateBridge(ctx, cmd.ChatID, p.Agent, p.Model)
	if err != nil {
		deps.Broadcast(ctx, api.NewEvent(api.EventError, cmd.ChatID, api.ErrorPayload{Code: api.ErrCodeBridgeStartFailed, Message: err.Error()}))
		d.RespondErr(w, http.StatusInternalServerError, err)
		return
	}
	if !sb.TryAcquireForPrompt() {
		d.RespondErr(w, http.StatusConflict, errBusy)
		return
	}
	defer sb.ReleaseAfterPrompt()
	sb.SetLastActive()

	deps.InflightAdd(1)
	defer deps.InflightDone()

	// 3. Prime with history if the bridge needs it.
	if !sb.IsPrimed() {
		sb.SetPrimed()
		deps.PrimeIfNeeded(ctx, cmd.ChatID, sb)
	}

	// 4. Send the prompt to kiro-cli.
	if !deps.MCPWaitForReady(ctx, 30*time.Second) {
		slog.Warn("MCP readiness timeout, proceeding anyway", "chat_id", cmd.ChatID)
	}
	var creditsBeforeTurn float64
	if chat, ok := deps.ChatStore().Get(ctx, cmd.ChatID); ok {
		creditsBeforeTurn = chat.Usage.Credits
	}
	slog.Info("prompt", "chat_id", cmd.ChatID, "len", len(p.Text))
	start := time.Now()
	promptParams := BuildPromptParams(deps, sb, &p)
	resp, err := callPromptWithRetry(ctx, sb, promptParams, cmd.ChatID)
	elapsed := time.Since(start)
	if err != nil {
		slog.Error("prompt failed", "chat_id", cmd.ChatID, keyError, err, "elapsed", elapsed)
		deps.Broadcast(ctx, api.NewEvent(api.EventError, cmd.ChatID, api.ErrorPayload{Code: api.ErrCodePromptFailed, Message: err.Error()}))
		d.RespondErr(w, http.StatusInternalServerError, err)
		return
	}
	slog.Info("prompt complete", "chat_id", cmd.ChatID, "elapsed", elapsed)

	// Empty turn recovery.
	resp = recoverEmptyTurn(deps, ctx, cmd.ChatID, resp, &p, promptParams)

	// Compute credit delta for the turn summary.
	var creditsDelta float64
	if chat, ok := deps.ChatStore().Get(ctx, cmd.ChatID); ok {
		creditsDelta = chat.Usage.Credits - creditsBeforeTurn
	}
	deps.EmitTurnEndedWithStats(ctx, cmd.ChatID, resp, creditsDelta, float64(elapsed.Milliseconds()))
	d.Respond(w, cmd.RequestID, map[string]bool{"ok": true})
}

// BuildPromptParams constructs the full session/prompt parameter map.
func BuildPromptParams(deps Dependencies, sb Bridge, p *api.PromptCommand) map[string]any {
	params := SessionParams(sb, map[string]any{
		"prompt": BuildPromptBlocks(p.Text, p.Attachments, deps.ResolveInsideWorkDir),
	})
	kiroMeta := map[string]any{}
	if p.ActiveFile != "" {
		kiroMeta["activeFile"] = p.ActiveFile
	}
	if len(p.OpenFiles) > 0 {
		kiroMeta["openFiles"] = p.OpenFiles
	}
	if len(kiroMeta) > 0 {
		params["_meta"] = map[string]any{"kiro": kiroMeta}
	}
	return params
}

// IsRetryablePromptError returns true for errors that indicate kiro-cli
// is temporarily busy or hit a transient internal error.
func IsRetryablePromptError(err error) bool {
	if err == nil {
		return false
	}
	var te *api.TransportError
	if errors.As(err, &te) {
		return te.Retryable
	}
	if errors.Is(err, api.ErrNotIdle) {
		return true
	}
	var re *api.RPCError
	if errors.As(err, &re) {
		switch re.Code {
		case api.RPCCodeNotIdle:
			return true
		case api.RPCCodeInternal:
			return true
		}
		return false
	}
	return false
}

// AsyncRenameChat generates a better chat title via the utility bridge.
func AsyncRenameChat(deps Dependencies, chatID api.ChatID, firstPrompt string) {
	prompt := "Give this chat a 2-3 word title (max 30 characters) based on the topic of the message below. Return ONLY the title.\n\n" + firstPrompt
	ctx, cancel := context.WithTimeout(deps.ShutdownCtx(), 30*time.Second)
	defer cancel()
	title, err := deps.UtilityPrompt(ctx, prompt)
	if err != nil || strings.TrimSpace(title) == "" {
		return
	}
	title = strings.TrimSpace(title)
	if len(title) >= 2 && ((title[0] == '"' && title[len(title)-1] == '"') ||
		(title[0] == '\'' && title[len(title)-1] == '\'')) {
		title = title[1 : len(title)-1]
	}
	if len(title) > 30 {
		title = title[:27] + "..."
	}
	if title == "" {
		return
	}
	if err := deps.ChatStore().Mutate(ctx, chatID, func(c *api.Chat, exists bool) bool {
		if !exists {
			return false
		}
		c.Name = title
		return true
	}); err != nil {
		slog.Error("auto-rename chat", "chat_id", chatID, keyError, err)
	}
	slog.Info("chat auto-renamed", "chat_id", chatID, "title", title)
}
