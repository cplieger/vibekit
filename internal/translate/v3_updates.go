package translate

// v3 (KAS) session/update sub-kind handlers.
//
// v3 moves several things that v2 delivered as dedicated _kiro.dev/*
// notifications into session/update sub-kinds:
//
//	session_info_update       context-usage stats  <- v2 _kiro.dev/metadata
//	                          + compaction status  <- v2 _kiro.dev/compaction/status
//	available_commands_update slash-command catalog <- v2 _kiro.dev/commands/available
//	usage_update              context-window usage (primary v3 channel)
//	config_option_update      live model/mode/effort catalog
//
// These handlers reshape the v3 payloads onto the same domain outputs
// (chat usage + commands_updated SSE + compaction events + model catalog)
// so the context ring, slash menu, compaction UI, and model picker work on
// the KAS engine without the client knowing which engine is live.
//
// Shapes verified against the KAS 2.12 acp-server bundle; see
// kiro-cli-research.md "v3 _kiro/* wire surface".

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/cplieger/vibekit/internal/api"
)

// v3Summarization is the compaction status carried under
// session_info_update _meta.kiro.summarization on v3. Status is "running"
// while compacting, "success" on completion (with the summary), or a
// failure-reason string otherwise.
type v3Summarization struct {
	Summary *struct {
		ConversationSummary string `json:"conversationSummary"`
	} `json:"summary"`
	Status string `json:"status"`
}

// sessionInfoUpdate is the v3 session_info_update payload. The _meta.kiro
// block carries context-usage stats (kind == "context_usage"), on
// compaction a summarization sub-block, and at turn end a per-turn
// metering summary (promptTurnSummaries + elapsedTime). Other kinds carry
// none of these and are ignored.
type sessionInfoUpdate struct {
	Meta struct {
		Kiro struct {
			Summarization   *v3Summarization `json:"summarization"`
			UsagePercentage *float64         `json:"usagePercentage"`
			ContextUsage    struct {
				UsagePercentage *float64 `json:"usagePercentage"`
			} `json:"contextUsage"`
			// PromptTurnSummaries is KAS's per-turn metering record,
			// emitted as a session_info_update just before the
			// session/prompt response returns (verified on the live
			// 2.12.1 wire): [{unit:"credit", unitPlural:"credits",
			// usage:0.0619}], beside elapsedTime (ms).
			PromptTurnSummaries []promptTurnSummary `json:"promptTurnSummaries"`
			ElapsedTime         float64             `json:"elapsedTime"`
			Kind                string              `json:"kind"`
		} `json:"kiro"`
	} `json:"_meta"`
}

// promptTurnSummary is one metering line of a turn-end summary.
type promptTurnSummary struct {
	Unit  string  `json:"unit"`
	Usage float64 `json:"usage"`
}

// HandleSessionInfoUpdate folds v3 context-usage into the chat's usage so
// the context ring works on the KAS engine (v2 sourced this from
// _kiro.dev/metadata), and routes v3 compaction status (v2 sourced this
// from _kiro.dev/compaction/status). Parent-only: subagent updates are
// dropped so they don't overwrite the parent chat.
func (t *Translator) HandleSessionInfoUpdate(ctx context.Context, chatID api.ChatID, raw json.RawMessage, subSessionID string) {
	if subSessionID != "" {
		return
	}
	var u sessionInfoUpdate
	if json.Unmarshal(raw, &u) != nil {
		return
	}
	// Compaction rides here on v3 (v2 used _kiro.dev/compaction/status).
	if s := u.Meta.Kiro.Summarization; s != nil && s.Status != "" {
		t.handleV3Summarization(ctx, chatID, s)
		return
	}
	// Turn-end metering summary: the ONLY v3 channel that reliably carries
	// the turn's credit spend + duration (usage_update.cost never arrived
	// on the live 2.12.1 wire). Without this, Usage.TurnCount/LastTurnMs/
	// Credits had no writer and the context popup showed zeros forever.
	if len(u.Meta.Kiro.PromptTurnSummaries) > 0 {
		t.persistTurnSummary(ctx, chatID, u.Meta.Kiro.PromptTurnSummaries, u.Meta.Kiro.ElapsedTime)
		return
	}
	// Context usage. usage_update is the primary v3 channel (HandleUsageUpdate),
	// but the context_usage session_info_update kind mirrors it, so honour
	// both — whichever arrives keeps the ring fresh.
	pct := u.Meta.Kiro.ContextUsage.UsagePercentage
	if pct == nil {
		pct = u.Meta.Kiro.UsagePercentage
	}
	if pct == nil {
		return // this session_info_update carries no context-usage percentage
	}
	t.persistUsage(ctx, chatID, *pct, 0, -1) // no size/credits on this channel
}

// handleV3Summarization maps the v3 summarization sub-states onto the
// compaction domain events. "running"/"success" drive the started/completed
// path; a "canceled" reason is benign and produces no failed-compaction
// boundary or error banner; any other non-empty status is a genuine failure.
func (t *Translator) handleV3Summarization(ctx context.Context, chatID api.ChatID, s *v3Summarization) {
	switch s.Status {
	case "running":
		t.deps.Broadcast(ctx, api.NewEvent(api.EventCompactionStarted, chatID, api.CompactionStartedPayload{}))
	case "success":
		var summary *string
		if s.Summary != nil {
			summary = &s.Summary.ConversationSummary
		}
		t.handleCompactionCompleted(ctx, chatID, summary)
	case "canceled", "cancelled":
		// Cancellation is benign — KAS reports it as a summarization "reason",
		// but the IDE treats it as a no-op. Do NOT surface a failed-compaction
		// boundary or an error banner; the turn just continues uncompacted.
		slog.Debug("compaction canceled", "chat_id", chatID)
	default:
		// Any other non-empty status is a genuine failure reason.
		t.handleCompactionFailed(ctx, chatID, s.Status)
	}
}

// persistTurnSummary folds a turn-end metering summary into the chat's
// usage: one more turn, the turn's wall time, and the credit spend summed
// over the summary's credit lines. Credits ACCUMULATE here; the absolute
// usage_update.cost channel (persistUsage) keeps overwrite precedence if
// KAS ever ships both — an absolute value arriving after an increment
// simply corrects it.
func (t *Translator) persistTurnSummary(ctx context.Context, chatID api.ChatID, summaries []promptTurnSummary, elapsedMs float64) {
	var credits float64
	for i := range summaries {
		if summaries[i].Unit == "" || summaries[i].Unit == "credit" {
			credits += summaries[i].Usage
		}
	}
	if err := t.deps.ChatStore().Mutate(ctx, chatID, func(c *api.Chat, exists bool) bool {
		if !exists {
			return false
		}
		c.Usage.TurnCount++
		if elapsedMs > 0 {
			c.Usage.LastTurnMs = elapsedMs
		}
		if credits > 0 {
			c.Usage.Credits += credits
			c.Usage.HasRealData = true
		}
		return true
	}); err != nil {
		slog.Error("persist v3 turn summary", "chat_id", chatID, "error", err)
	}
}

// usageUpdate is the v3 usage_update payload — the primary context-window
// usage channel. size is the context window (tokens), used is tokens
// consumed (the ring wants a 0-100 percentage); cost carries the credit
// spend ({amount, currency}), which v2 sourced from _kiro.dev/metadata's
// metering. cost is nullish upstream, so it is a pointer — absent cost
// leaves the stored credits untouched.
type usageUpdate struct {
	Cost *struct {
		Amount float64 `json:"amount"`
	} `json:"cost"`
	Size int64 `json:"size"`
	Used int64 `json:"used"`
}

// HandleUsageUpdate folds v3 usage_update into the chat's usage (context %,
// window size, and credits). Parent attribution is handled by
// ignoreSubSession in the dispatch table.
func (t *Translator) HandleUsageUpdate(ctx context.Context, chatID api.ChatID, raw json.RawMessage) {
	var u usageUpdate
	if json.Unmarshal(raw, &u) != nil || u.Size <= 0 {
		return
	}
	pct := float64(u.Used) / float64(u.Size) * 100
	credits := -1.0 // sentinel: leave credits unchanged when cost is absent
	if u.Cost != nil {
		credits = u.Cost.Amount
	}
	t.persistUsage(ctx, chatID, pct, int(u.Size), credits)
}

// persistUsage writes the context percentage, and optionally the
// context-window size and credits, into the chat's usage, skipping a
// no-op write. size <= 0 leaves the stored context size unchanged;
// credits < 0 leaves credits unchanged (the session_info_update
// context-usage path carries no cost).
func (t *Translator) persistUsage(ctx context.Context, chatID api.ChatID, pct float64, size int, credits float64) {
	if err := t.deps.ChatStore().Mutate(ctx, chatID, func(c *api.Chat, exists bool) bool {
		if !exists {
			return false
		}
		changed := false
		if !c.Usage.HasRealData || c.Usage.ContextPct != pct {
			c.Usage.ContextPct = pct
			c.Usage.HasRealData = true
			changed = true
		}
		if size > 0 && c.Usage.ContextSize != size {
			c.Usage.ContextSize = size
			changed = true
		}
		if credits >= 0 && c.Usage.Credits != credits {
			c.Usage.Credits = credits
			changed = true
		}
		return changed
	}); err != nil {
		slog.Error("persist v3 usage", "chat_id", chatID, "error", err)
	}
}

// availableCommandsUpdate is the v3 available_commands_update payload.
// Each command is a name/description plus opaque input/_meta that flow
// through toAvailableCommands' passthrough Meta map.
type availableCommandsUpdate struct {
	AvailableCommands []map[string]any `json:"availableCommands"`
}

// HandleAvailableCommandsUpdate maps the v3 command catalog onto the same
// commands_updated SSE the v2 _kiro.dev/commands/available handler emits,
// so the client slash-command type-ahead is engine-agnostic. Parent-only.
func (t *Translator) HandleAvailableCommandsUpdate(ctx context.Context, chatID api.ChatID, raw json.RawMessage, subSessionID string) {
	if subSessionID != "" {
		return
	}
	var p availableCommandsUpdate
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	t.deps.Broadcast(ctx, api.NewEvent(api.EventCommandsUpdated, chatID, api.CommandsUpdatedPayload{
		Commands: toAvailableCommands(p.AvailableCommands),
	}))
}

// configOptionUpdate is the v3 config_option_update payload: the live
// model/mode/effort catalog. On v3 session/new returns an empty model
// list (unlike v2), so this is the per-session channel that populates the
// model picker; we extract the "model" option's choices.
type configOptionUpdate struct {
	ConfigOptions []configOption `json:"configOptions"`
}

// configOption is one entry in the config_option_update catalog. id is the
// configId ("model" | "mode" | "effortLevel"); for select options the
// choices are in options[] (possibly grouped).
type configOption struct {
	ID           string          `json:"id"`
	Category     string          `json:"category"`
	Type         string          `json:"type"`
	CurrentValue json.RawMessage `json:"currentValue"`
	Options      []configChoice  `json:"options"`
}

// configChoice is one selectable value (or, when Options is non-empty, a
// group of nested choices) in a select-type config option.
type configChoice struct {
	Name        string          `json:"name"`
	Value       string          `json:"value"`
	Description string          `json:"description"`
	Meta        json.RawMessage `json:"_meta"`
	Options     []configChoice  `json:"options"`
}

// HandleConfigOptionUpdate refreshes the chat's model catalog from the v3
// config_option_update. Modes are intentionally NOT refreshed here: the
// config catalog omits the bundled/workspace source tag the picker groups
// by, so the authoritative mode list stays the one captured on session/new.
func (t *Translator) HandleConfigOptionUpdate(ctx context.Context, chatID api.ChatID, raw json.RawMessage) {
	var p configOptionUpdate
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	var models []api.SessionModel
	currentModel := ""
	for i := range p.ConfigOptions {
		opt := &p.ConfigOptions[i]
		if opt.ID != api.ConfigOptionModel && opt.Category != api.ConfigOptionModel {
			continue
		}
		_ = json.Unmarshal(opt.CurrentValue, &currentModel) // string; ignore non-string
		models = flattenModelChoices(opt.Options)
		break
	}
	if len(models) == 0 {
		return
	}
	if err := t.deps.ChatStore().Mutate(ctx, chatID, func(c *api.Chat, exists bool) bool {
		if !exists {
			return false
		}
		changed := false
		if !sameModelIDs(c.AvailableModels, models) {
			c.AvailableModels = models
			changed = true
		}
		if currentModel != "" && c.Model != currentModel {
			c.Model = currentModel
			changed = true
		}
		return changed
	}); err != nil {
		slog.Error("persist v3 model catalog", "chat_id", chatID, "error", err)
	}
}

// flattenModelChoices converts select choices (flat or grouped) into the
// domain model catalog, dropping [Deprecated]/[Legacy]-tagged entries the
// same way bridge.applySessionResultLocked does for v2.
func flattenModelChoices(choices []configChoice) []api.SessionModel {
	var out []api.SessionModel
	for i := range choices {
		c := &choices[i]
		if len(c.Options) > 0 { // grouped: recurse into the group's choices
			out = append(out, flattenModelChoices(c.Options)...)
			continue
		}
		if c.Value == "" || api.TagExcluded(c.Description, api.HiddenTags) {
			continue
		}
		out = append(out, api.SessionModel{
			ID: c.Value, Name: c.Name, Description: c.Description,
			HasEffort: choiceHasEffort(c.Meta),
		})
	}
	return out
}

// choiceHasEffort reads _meta.kiro.hasEffort from a config-option model
// choice. KAS stamps it per model in config_option_update (true when the
// model has any reasoning-effort levels). Absent meta or a non-effort model
// yields false, which the client renders as "hide the effort row" for that
// model (while a catalog with no has_effort anywhere safely shows it).
func choiceHasEffort(meta json.RawMessage) bool {
	if len(meta) == 0 {
		return false
	}
	var m struct {
		Kiro struct {
			HasEffort bool `json:"hasEffort"`
		} `json:"kiro"`
	}
	_ = json.Unmarshal(meta, &m)
	return m.Kiro.HasEffort
}

// sameModelIDs reports whether two model catalogs carry the same ids in
// the same order — a cheap change-detector so a repeated config catalog
// doesn't churn the chat file.
func sameModelIDs(a, b []api.SessionModel) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			return false
		}
	}
	return true
}
