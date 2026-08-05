package command

// Prompt command handler: validates, persists user message, acquires
// bridge, sends to kiro-cli, handles empty-turn recovery.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/ids"
	cfgsettings "github.com/cplieger/vibekit/internal/settings"
)

// validatePromptPayload parses and validates the prompt command payload.
func validatePromptPayload(cmd *api.ClientCommand) (api.PromptCommand, int, error) {
	var p api.PromptCommand
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		return p, http.StatusBadRequest, ErrInvalidPayload
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
		return p, http.StatusBadRequest, ErrInvalidPayload
	}
	if !ValidIdent(p.Model) {
		return p, http.StatusBadRequest, ErrInvalidPayload
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
		return sb.Call(ctx, api.MethodPrompt, params)
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
		// Detach, don't forget: the abandoned session still holds this chat's
		// earlier transcript on disk, and it must stay in the chain or the
		// reaper sweeps it as an orphan.
		c.RecordSession("")
		return true
	}); err != nil {
		slog.Error("empty turn: clear session ID", "chat_id", chatID, keyError, err)
	}
	evt := api.Message{
		ID: ids.NewMessageID(), Role: api.RoleEvent, Ts: time.Now().UnixMilli(),
		EventKind: api.EventInterrupted, Content: "Session refreshed, retrying...",
	}
	if err := deps.ChatStore().AppendMessage(ctx, chatID, &evt); err != nil {
		slog.Error("empty turn: append event", "chat_id", chatID, keyError, err)
	}
	sb2, err2 := deps.GetOrCreateBridge(ctx, chatID, p.Model)
	if err2 != nil {
		slog.Error("empty turn: respawn failed",
			"chat_id", chatID, keyError, err2)
		deps.Broadcast(ctx, api.NewEvent(api.EventError, chatID, api.ErrorPayload{
			Code:    api.ErrCodeRecoveryFailed,
			Message: "Session refresh failed: " + err2.Error(),
		}))
		return resp
	}
	sb2.SetPrompting()
	defer sb2.ReleaseAfterPrompt()
	params[api.KeySessionID] = sb2.SessionID()
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

// supervisedDefaultSetting reads the settings-wide Supervised default applied to
// newly auto-created chats. Fails CLOSED to false: supervised mode is opt-in, and
// a corrupt config.json must not suddenly gate every write on approval.
//
// Read here rather than through an internal/permissions package. That package was
// 29 lines wrapping one settings read with a single caller, and it was named for a
// responsibility vibekit no longer has — tool authorization is Cedar's and the
// write gate is KAS's, so a package called "permissions" holding one default was
// the last thing making it look otherwise.
func supervisedDefaultSetting(ctx context.Context, configDir string) bool {
	var b bool
	if !cfgsettings.FieldInto(ctx, configDir, cfgsettings.KeySupervisedDefault, cfgsettings.KeySupervisedDefault, &b) {
		return false
	}
	return b
}

// appendUserMessage adds the prompt's user message to the chat.
func appendUserMessage(deps Dependencies, ctx context.Context, chatID api.ChatID, p *api.PromptCommand) error { //nolint:revive // context-as-argument: dispatcher handler signature
	supervisedDefault := supervisedDefaultSetting(ctx, deps.ConfigDir())
	err := deps.ChatStore().Mutate(ctx, chatID, func(c *api.Chat, exists bool) bool {
		// Idempotent by message id (the documented invariant): if this id
		// is already in the store — e.g. a 409-queued prompt whose first
		// attempt persisted the user message before the busy check, now
		// re-sent by the client's prompt queue — skip the append AND the
		// broadcast so no duplicate user bubble renders. The prompt itself
		// still proceeds (Mutate returns nil on a false mutator).
		if hasMessageID(c, p.MessageID) {
			return false
		}
		if !exists {
			c.Name = api.DefaultChatName
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
		}
		deps.Broadcast(ctx, api.NewEvent(api.EventMessageAppended, chatID, &userMsg))
		return true
	})
	return err
}

// hasMessageID reports whether the chat already contains a message with
// the given id. Scans backwards — a retried prompt's original append is
// almost always the most recent message.
func hasMessageID(c *api.Chat, id string) bool {
	for i := range slices.Backward(c.Messages) {
		if c.Messages[i].ID == id {
			return true
		}
	}
	return false
}

// turnContext derives the context an in-flight turn runs under. It
// deliberately severs the caller's (prompt POST r.Context()) cancellation
// via context.WithoutCancel: a mid-turn client drop — iOS backgrounding,
// a proxy timeout, a network blip — must NOT cancel the bridge Call. If
// it did, CmdPrompt would emit prompt_failed and return BEFORE
// EmitTurnEndedWithStats, so no turn_ended fires and the assistant buffer
// is never persisted, even though kiro-cli keeps running the turn to
// completion. Request-scoped values are preserved. Cancellation is
// re-attached to the hub shutdown context via AfterFunc so the turn still
// dies on hub shutdown; the returned cancel also tears it down on handler
// return. Explicit user cancellation is unaffected — it goes through
// session/cancel (Notify), not this context.
//
// This mirrors the established pattern in hub/agent_terminal.go, which
// runs agent-spawned subprocesses under context.WithCancel(
// context.WithoutCancel(ctx)) + AfterFunc(shutdownCtx, cancel) for the
// same reason (a per-request ctx must not tear down longer-lived work).
func turnContext(reqCtx, shutdownCtx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(reqCtx))
	stop := context.AfterFunc(shutdownCtx, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

// CmdPrompt handles the prompt command.
func CmdPrompt(d *Dispatcher, ctx context.Context, w http.ResponseWriter, cmd *api.ClientCommand) { //nolint:revive // context-as-argument: dispatcher handler signature
	deps := d.Deps()
	if cmd.ChatID == "" {
		d.RespondErr(w, http.StatusBadRequest, ErrMissingChatID)
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

	// 1. Ensure the chat exists and append the user message, naming the chat
	// from its first prompt.
	if err := appendUserMessage(deps, ctx, cmd.ChatID, &p); err != nil {
		d.RespondErr(w, http.StatusInternalServerError, err)
		return
	}

	// 2. Ensure the bridge exists and serialize per-chat prompts. The turn
	// runs under a context detached from the prompt POST's r.Context()
	// (see turnContext): a mid-turn client disconnect must not cancel the
	// in-flight bridge Call, or the turn fails before it can finalize and
	// persist the assistant buffer.
	ctx, cancel := turnContext(ctx, deps.ShutdownCtx())
	defer cancel()
	sb, err := deps.GetOrCreateBridge(ctx, cmd.ChatID, p.Model)
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
	promptParams := BuildPromptParams(ctx, deps, sb, &p)
	resp, err := callPromptWithRetry(ctx, sb, promptParams, cmd.ChatID)
	elapsed := time.Since(start)
	if err != nil {
		slog.Error("prompt failed", "chat_id", cmd.ChatID, keyError, err, "elapsed", elapsed)
		// Finalize the turn before returning. Without this the assistant buffer
		// survives with Started == true, so the NEXT prompt's ensureTurnStarted
		// no-ops, emits no message_created, and extends this dead turn's blocks
		// under this dead turn's message id: one persisted assistant message
		// holding two turns' replies. The partial is persisted rather than
		// dropped -- see AbandonInFlightTurn for why that direction.
		deps.AbandonInFlightTurn(ctx, cmd.ChatID)
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
	d.RespondOK(w, cmd.RequestID)
}

// BuildPromptParams constructs the full session/prompt parameter map.
func BuildPromptParams(ctx context.Context, deps Dependencies, sb Bridge, p *api.PromptCommand) map[string]any {
	params := SessionParams(sb, map[string]any{
		"prompt": BuildPromptBlocks(ctx, p.Text, p.Attachments, deps.ResolveInsideWorkDir),
	})
	// Forward the client-generated user message id so KAS stores this turn under
	// vibekit's own id. That shared id space is what makes rewind addressable:
	// _kiro/checkpoint/revertMultiple takes a messageId and requires it to name a
	// USER message, and a user turn's id is one KAS only knows because it was
	// sent here (an assistant turn carries KAS's own `<uuid>-say`). Drop this and
	// rewind loses its handle on the transcript — see CmdRewindChat.
	if p.MessageID != "" {
		params["messageId"] = p.MessageID
	}
	return params
}

// IsRetryablePromptError returns true for errors that indicate kiro-cli
// is temporarily busy or hit a transient internal error.
func IsRetryablePromptError(err error) bool {
	if err == nil {
		return false
	}
	if te, ok := errors.AsType[*api.TransportError](err); ok {
		return te.Retryable
	}
	if errors.Is(err, api.ErrNotIdle) {
		return true
	}
	if re, ok := errors.AsType[*api.RPCError](err); ok {
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
