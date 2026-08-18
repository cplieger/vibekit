package translate

// v3 (KAS) session/update sub-kind handlers.
//
// v3 moves several things that v2 delivered as dedicated _kiro.dev/*
// notifications into session/update sub-kinds:
//
//	session_info_update  context-usage stats  <- v2 _kiro.dev/metadata
//	                     + compaction status  <- v2 _kiro.dev/compaction/status
//	usage_update         DEAD on 2.16.1 — one bundle hit, no emit site
//	                     (see persistUsage's note; session_info_update carries it)
//	config_option_update live model/mode/effort catalog
//
// These handlers reshape the v3 payloads onto domain outputs (chat usage +
// compaction events + model catalog) so the context ring, compaction UI and
// model picker work without the client knowing which engine is live.
//
// available_commands_update is the one sub-kind arriving here that is NOT
// handled: the slash-command catalog had no consumer and is not decoded, so it
// falls through handleSessionUpdate silently. See static-src/handlers/system.ts
// for why there is no palette.
//
// Shapes verified against the KAS 2.12 acp-server bundle; see
// kiro-cli-research.md "v3 _kiro/* wire surface".

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"

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
			// Workflow marks the frame as a workflow STEP's, which is what
			// lets a step's metering through the parent-only gate below
			// while keeping it out of the chat's turn counters.
			Workflow     *ACPWorkflowMeta `json:"workflow"`
			ContextUsage struct {
				UsagePercentage *float64 `json:"usagePercentage"`
			} `json:"contextUsage"`
			// Focus is the kind=="focus_update" block: the agent's
			// self-declared title/description/status (see focus.go).
			Focus *focusUpdate `json:"focus"`
			// The steering sub-kinds' fields, FLAT beside Kind rather than in a
			// sub-block of their own. That is not a modelling choice here: KAS's
			// buildSessionInfoUpdate spreads the update object straight into
			// _meta.kiro, and its legacyFields() returns {} for all three
			// steering kinds — so unlike focus or contextUsage there is no
			// nested object to key off, and these three must dispatch on the
			// kind STRING. See handleSteeringUpdate.
			MessageIDs           []string `json:"messageIds"`
			MessageID            string   `json:"messageId"`
			Content              string   `json:"content"`
			NotificationSeverity string   `json:"notificationSeverity"`
			Kind                 string   `json:"kind"`
			// PromptTurnSummaries is KAS's per-turn metering record,
			// emitted as a session_info_update just before the
			// session/prompt response returns (verified on the live
			// 2.12.1 wire): [{unit:"credit", unitPlural:"credits",
			// usage:0.0619}], beside elapsedTime (ms).
			PromptTurnSummaries []promptTurnSummary `json:"promptTurnSummaries"`
			ElapsedTime         float64             `json:"elapsedTime"`
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
//
// ONE exception, and it is why the gate moved below the unmarshal: a workflow
// STEP's turn_completion is the only record of what that step SPENT, and the
// blanket gate discarded it — so a run of twenty steps reported no cost at all
// on the chat that launched and paid for it. A step frame is now allowed
// through for its metering only; see persistTurnSummary's owner argument for
// why the turn counters are not the step's to move.
func (t *Translator) HandleSessionInfoUpdate(ctx context.Context, chatID api.ChatID, raw json.RawMessage, subSessionID string) {
	var u sessionInfoUpdate
	if json.Unmarshal(raw, &u) != nil {
		return
	}
	// Steering is dispatched BEFORE the parent-only gate, and deliberately.
	// A steer belongs to the CHAT — the user typed it into this conversation —
	// but it is consumed by whichever execution happens to be running, which may
	// be a subagent's. Gating on attribution would drop the injected signal for
	// exactly the case where the agent delegated, leaving a chip that says
	// "waiting" forever over a message the model already read.
	if t.handleSteeringUpdate(ctx, chatID, &u) {
		return
	}
	step := u.Meta.Kiro.Workflow != nil && u.Meta.Kiro.Workflow.WorkflowID != ""
	if subSessionID != "" || (step && len(u.Meta.Kiro.PromptTurnSummaries) == 0) {
		return
	}
	// Agent focus updates (title / description / status) ride here as
	// kind=="focus_update" frames; see focus.go for the adoption rules.
	if f := u.Meta.Kiro.Focus; f != nil {
		t.handleFocusUpdate(ctx, chatID, f)
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
		t.persistTurnSummary(ctx, chatID, u.Meta.Kiro.PromptTurnSummaries, u.Meta.Kiro.ElapsedTime, step)
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
		// No recognised sub-block and no usage percentage. Most sub-kinds
		// legitimately land here, but an UNKNOWN one is worth a line — see
		// logUnconsumedInfoKind.
		t.logUnconsumedInfoKind(chatID, u.Meta.Kiro.Kind)
		return
	}
	t.persistUsage(ctx, chatID, *pct, 0, -1) // no size/credits on this channel
}

// knownSessionInfoKinds is every `_meta.kiro.kind` value KAS is known to
// multiplex through session_info_update, enumerated from all 30
// buildSessionInfoUpdate call sites plus the two that reach the wire via
// SessionInfoEmitter.send (`steering_injected`, `repositories_update`) and
// so are invisible to a call-site census.
//
// This set exists to tell "a sub-kind vibekit deliberately ignores" apart
// from "a sub-kind KAS added since this was written". Membership implies
// nothing about whether vibekit consumes the kind — most of these are
// ignored on purpose.
var knownSessionInfoKinds = map[string]struct{}{
	"turn_start": {}, "turn_end": {}, "turn_completion": {},
	"context_usage": {}, "summarization_separator": {}, "summary_message": {},
	"summarization_started": {}, "summarization_failed": {}, "summarization_completed": {},
	"user_message_id_assigned": {}, "focus_update": {}, "display_error": {},
	"pending_interaction": {}, "interaction_resolved": {}, "recap": {},
	"steering_inclusion": {}, "queued": {}, "hook_update": {}, "repositories_update": {},
}

// logUnconsumedInfoKind reports a session_info_update that reached the end of
// the dispatch cascade without being consumed.
//
// session_info_update is a CARRIER, not a message type: it multiplexes 22+
// sub-kinds under `_meta.kiro.kind`, and the cascade above dispatches on
// which sub-BLOCK is present (focus / summarization / promptTurnSummaries /
// contextUsage) rather than on the kind string. That is deliberate — those
// four are proven against the live wire, and keying them off kind values
// instead would be a guess — but it means everything else falls through
// here silently.
//
// A kind absent from knownSessionInfoKinds is logged at Warn because it is
// most likely a KAS addition vibekit has not looked at yet, and the whole
// failure mode of a multiplexed carrier is that new payloads vanish without
// a trace. A known-but-ignored kind logs at Debug: expected, not news.
func (t *Translator) logUnconsumedInfoKind(chatID api.ChatID, kind string) {
	if kind == "" {
		return
	}
	if _, known := knownSessionInfoKinds[kind]; known {
		slog.Debug("session_info_update: known kind carries nothing vibekit consumes",
			"chat_id", chatID, "kind", kind)
		return
	}
	slog.Warn("session_info_update: UNKNOWN kind, dropped — KAS may have added a sub-kind",
		"chat_id", chatID, "kind", kind)
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
// persistTurnSummary folds one turn's metering into the chat's usage.
//
// `step` says the metering came from a workflow STEP rather than from a turn of
// the conversation, and it splits the three fields by what each one means:
// CREDITS are real account spend the user is owed a readout of, so they
// accumulate either way; TurnCount and LastTurnMs describe the CONVERSATION, so
// a step must not touch them — twenty steps would otherwise report a four-message
// chat as twenty-four turns and overwrite "last turn" with an unrelated duration.
func (t *Translator) persistTurnSummary(ctx context.Context, chatID api.ChatID, summaries []promptTurnSummary, elapsedMs float64, step bool) {
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
		if !step {
			c.Usage.TurnCount++
			if elapsedMs > 0 {
				c.Usage.LastTurnMs = elapsedMs
			}
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
//
// The context-percentage gate is a MATERIAL delta, not any delta, and that
// distinction is the whole cost of this function. A chat file is rewritten
// wholesale on every Mutate (load, Unmarshal, Marshal, atomic save with an
// fsync), and KAS emits contextUsagePercentAtModelResponse once per model
// response plus a second breakdown-bearing frame, so an exact-inequality gate
// turned a 20-tool-call turn into roughly 40 full-transcript rewrites,
// serialized on the per-chat mutex, to move a float by fractions of a point.
// Measured on this fleet's real KAS transcripts: p50 737 KB, p90 5.6 MB, max
// 21.1 MB across 12,473 records. The fsync is cheap (~9 ms for 20 MB on ZFS);
// the JSON round trip is not.
//
// The threshold is 1.0 percentage point because that is the resolution the
// context ring actually renders, plus the two tier boundaries KAS itself keys
// on (80 = warning, 95 = critical) so a crossing is never rounded away. A
// change the UI cannot show is not worth a transcript rewrite.
func (t *Translator) persistUsage(ctx context.Context, chatID api.ChatID, pct float64, size int, credits float64) {
	if err := t.deps.ChatStore().Mutate(ctx, chatID, func(c *api.Chat, exists bool) bool {
		if !exists {
			return false
		}
		changed := false
		if !c.Usage.HasRealData || materialPctDelta(c.Usage.ContextPct, pct) {
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
		// Credits keep an exact-inequality gate deliberately, and it costs
		// nothing: the only caller that passes a non-negative credits value is
		// HandleUsageUpdate, and `usage_update` has no emit site in KAS 2.16.0 or
		// 2.16.1 (one occurrence, in a Zod schema). The live channel is
		// session_info_update's context-usage sub-kind, which passes -1 here. So
		// this arm is unreachable today; when KAS revives the notification, a
		// money value is also the one field where rounding a change away would be
		// wrong.
		return changed
	}); err != nil {
		slog.Error("persist v3 usage", "chat_id", chatID, "error", err)
	}
}

// contextPctEpsilon is the smallest context-percentage change worth a full
// transcript rewrite: one point, the resolution the context ring renders.
const contextPctEpsilon = 1.0

// contextPctTiers are the thresholds a percentage crossing must always persist
// through, because each one changes what the CLIENT does rather than merely how
// it rounds: 70 and 90 are where the context ring changes colour, and 95 is
// DEFAULT_CUTOFF_PCT, where the client stops accepting input entirely.
//
// vibekit's own thresholds, not KAS's. An earlier revision used KAS's 80/95 tier
// boundaries, which are the ones its TUI colours on and which this client never
// renders — so the epsilon could round away a crossing of the one threshold that
// disables the composer.
var contextPctTiers = [...]float64{70, 90, 95}

// materialPctDelta reports whether moving the stored context percentage from
// prev to next is worth persisting: a move of at least contextPctEpsilon, or any
// move that crosses a tier boundary.
func materialPctDelta(prev, next float64) bool {
	if math.Abs(next-prev) >= contextPctEpsilon {
		return true
	}
	for _, tier := range contextPctTiers {
		if (prev < tier) != (next < tier) {
			return true
		}
	}
	return false
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
	cat := readConfigCatalog(p.ConfigOptions)
	if len(cat.models) == 0 && !cat.sawEffort {
		return
	}
	if err := t.deps.ChatStore().Mutate(ctx, chatID, func(c *api.Chat, exists bool) bool {
		if !exists {
			return false
		}
		return cat.applyTo(c)
	}); err != nil {
		slog.Error("persist v3 config catalog", "chat_id", chatID, "error", err)
	}
}

// configCatalog is what one config_option_update says about the two options
// vibekit consumes: the model select and the effortLevel select.
//
// sawEffort is tracked apart from the list because an EMPTY effort list is a real
// answer — kiro-cli reports it for a model with no tiers, and its own TUI reads
// that as "effort is not available on the current model" — so it must be applied,
// while a frame carrying no effort option at all must leave the chat's tiers
// alone.
type configCatalog struct {
	currentModel  string
	currentEffort string
	models        []api.SessionModel
	efforts       []api.SessionEffortLevel
	sawEffort     bool
}

// readConfigCatalog extracts both options from one frame.
func readConfigCatalog(opts []configOption) configCatalog {
	var cat configCatalog
	for i := range opts {
		opt := &opts[i]
		switch {
		case opt.ID == api.ConfigOptionModel || opt.Category == api.ConfigOptionModel:
			_ = json.Unmarshal(opt.CurrentValue, &cat.currentModel) // string; ignore non-string
			cat.models = flattenModelChoices(opt.Options)
		case opt.ID == api.ConfigOptionEffort:
			cat.sawEffort = true
			_ = json.Unmarshal(opt.CurrentValue, &cat.currentEffort) // string; ignore non-string
			cat.efforts = flattenEffortChoices(opt.Options)
		}
	}
	return cat
}

// applyTo writes the catalog onto the chat, reporting whether anything changed
// (the store only persists and broadcasts on a change, so a repeated frame must
// answer false).
func (cat *configCatalog) applyTo(c *api.Chat) bool {
	changed := false
	if len(cat.models) > 0 && !sameModelIDs(c.AvailableModels, cat.models) {
		c.AvailableModels = cat.models
		changed = true
	}
	if cat.currentModel != "" && c.Model != cat.currentModel {
		c.Model = cat.currentModel
		changed = true
	}
	if !cat.sawEffort {
		return changed
	}
	if !sameEffortLevels(c.EffortLevels, cat.efforts) {
		c.EffortLevels = cat.efforts
		changed = true
	}
	if c.EffortActive != cat.currentEffort {
		c.EffortActive = cat.currentEffort
		changed = true
	}
	return changed
}

// flattenEffortChoices converts the effortLevel option's choices into the
// domain tier list. Flat by construction (KAS groups only the model select),
// but recursing costs one branch and cannot be wrong.
func flattenEffortChoices(choices []configChoice) []api.SessionEffortLevel {
	out := make([]api.SessionEffortLevel, 0, len(choices))
	for i := range choices {
		c := &choices[i]
		if len(c.Options) > 0 {
			out = append(out, flattenEffortChoices(c.Options)...)
			continue
		}
		if c.Value == "" {
			continue
		}
		out = append(out, api.SessionEffortLevel{ID: c.Value, Name: c.Name})
	}
	return out
}

// sameEffortLevels reports whether two tier lists carry the same ids in the
// same order — the change-detector that keeps a repeated catalog from
// rewriting the chat file (and broadcasting) on every frame.
func sameEffortLevels(a, b []api.SessionEffortLevel) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].Name != b[i].Name {
			return false
		}
	}
	return true
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
		effort := choiceEffort(c.Meta)
		out = append(out, api.SessionModel{
			ID: c.Value, Name: c.Name, Description: c.Description,
			HasEffort:          effort.HasEffort,
			DefaultEffortLevel: effort.Default,
		})
	}
	return out
}

// choiceEffortMeta is the reasoning-effort half of a model choice's
// `_meta.kiro`. Two fields, and only one of them is live against kiro-cli
// 2.18.0: `defaultEffortLevel` is stamped and `hasEffort` is not (measured
// against the shipped chat sidecar). The TIER LIST is deliberately absent here —
// it belongs to the `effortLevel` config option, not to a model choice.
type choiceEffortMeta struct {
	Default   string
	HasEffort bool
}

// choiceEffort reads _meta.kiro's effort fields off a config-option model
// choice (KAS stamps them per model in config_option_update; the levels and the
// default arrived in 2.16). Absent meta yields the zero value, which the client
// reads as "not plumbed" and answers with its own canonical level list rather
// than an empty control.
func choiceEffort(meta json.RawMessage) choiceEffortMeta {
	if len(meta) == 0 {
		return choiceEffortMeta{}
	}
	var m struct {
		Kiro struct {
			DefaultEffortLevel string `json:"defaultEffortLevel"`
			HasEffort          bool   `json:"hasEffort"`
		} `json:"kiro"`
	}
	_ = json.Unmarshal(meta, &m)
	return choiceEffortMeta{
		HasEffort: m.Kiro.HasEffort,
		Default:   m.Kiro.DefaultEffortLevel,
	}
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
