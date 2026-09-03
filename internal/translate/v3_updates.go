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
	"errors"
	"log/slog"
	"math"
	"reflect"

	"github.com/cplieger/runesafe/v2"
	"github.com/cplieger/vibekit/internal/chat"
	"github.com/cplieger/vibekit/internal/modeltext"
	"github.com/cplieger/vibekit/internal/vibekit"
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
		Kiro sessionInfoKiroBlock `json:"kiro"`
	} `json:"_meta"`
}

// sessionInfoKiroBlock is the `kiro` object inside a session_info_update's
// `_meta`. A NAMED type rather than an anonymous struct so it can carry the wire
// census; every field access site is unchanged.
//
// This is the richest carrier on the wire — 22+ sub-kinds multiplexed under one
// update type, dispatched on which sub-BLOCK is present rather than on the kind
// string — so it is also where a KAS addition is least likely to be noticed:
// logUnconsumedInfoKind reports the KIND, and says nothing about the payload that
// arrived with it.
type sessionInfoKiroBlock struct {
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
	// TurnStart and TurnEnd are the wire's own turn bracket, which KAS emits for
	// EVERY turn including one vibekit never prompted. Both are dispatched on
	// BLOCK presence like focus and summarization, rather than on the kind string:
	// KAS's legacyFields gives turn_end a nested object and turn_start a flat
	// `true`, so a pointer is what makes presence readable.
	TurnStart *bool         `json:"turnStart"`
	TurnEnd   *turnEndBlock `json:"turnEnd"`
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
}

// sessionInfoKiroShadow strips the UnmarshalJSON method so the real decode can
// run without recursing into it.
type sessionInfoKiroShadow sessionInfoKiroBlock

// UnmarshalJSON decodes the block and reports any member KAS sent that this type
// does not read. Same reasoning as ACPKiroBlock.UnmarshalJSON: the method receives
// exactly this object's bytes, so the probe is bounded to it, and the census can
// contribute no error.
func (b *sessionInfoKiroBlock) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, (*sessionInfoKiroShadow)(b)); err != nil {
		return err
	}
	censusMeta("session_info_update._meta.kiro", data, reflect.TypeFor[sessionInfoKiroShadow]())
	return nil
}

// turnEndBlock is the kind=="turn_end" sub-block. stopDetails is pinned upstream
// and has never been observed, so it is not decoded — see the design's F2.
type turnEndBlock struct {
	StopReason string `json:"stopReason"`
}

// promptTurnSummary is one metering line of a turn-end summary.
type promptTurnSummary struct {
	Unit  string  `json:"unit"`
	Usage float64 `json:"usage"`
}

// meteringUnitCredit is the one unit persistTurnSummary counts as spend. Named
// rather than inline because the census reports every OTHER unit, so the two must
// agree by construction: a rename here that missed the census would silently stop
// counting and report nothing.
const meteringUnitCredit = "credit"

// HandleSessionInfoUpdate folds v3 context-usage into the chat's usage
// so the context ring works on the KAS engine, and routes v3 compaction
// status. Parent-only: subagent updates are dropped so they don't
// overwrite the parent chat.
//
// One exception: a workflow step's turn_completion is the only record
// of what that step spent, so it is let through for its metering only —
// see persistTurnSummary's owner argument for why the turn counters are
// not the step's to move.
func (t *Translator) HandleSessionInfoUpdate(ctx context.Context, chatID vibekit.ChatID, raw json.RawMessage, attr FrameAttribution) {
	var u sessionInfoUpdate
	if json.Unmarshal(raw, &u) != nil {
		return
	}
	// Steering is dispatched before the parent-only gate, deliberately:
	// a steer belongs to the chat, but it is consumed by whichever
	// execution happens to be running, which may be a subagent's. Gating
	// on attribution would drop the injected signal for exactly the case
	// where the agent delegated.
	if t.handleSteeringUpdate(ctx, chatID, &u) {
		return
	}
	// A step's frame is identified by its session, never by this
	// payload: `_meta.kiro.workflow` never reaches a session_info_update,
	// so every step's metering used to be counted as one of this chat's
	// own turns.
	step := attr.Step
	if attr.SubSessionID != "" || (step && len(u.Meta.Kiro.PromptTurnSummaries) == 0) {
		return
	}
	// The wire's own turn bracket, dispatched after the attribution
	// gate deliberately: pre-gate, every workflow step's turn_start
	// would close the launching chat's live turn.
	if u.Meta.Kiro.TurnStart != nil {
		t.turns.WireTurnStart(ctx, chatID)
		return
	}
	if e := u.Meta.Kiro.TurnEnd; e != nil {
		t.turns.WireTurnEnd(ctx, chatID, vibekit.StopReason(e.StopReason))
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
	// Context usage. This sub-kind is the channel that actually arrives: the
	// standalone usage_update frame HandleUsageUpdate decodes is never emitted by
	// any KAS build vibekit has run against, so the handler is a fallback rather
	// than the primary and this mirror is what keeps the ring fresh. Both are
	// honoured because either would be correct if it came.
	pct := u.Meta.Kiro.ContextUsage.UsagePercentage
	if pct == nil {
		pct = u.Meta.Kiro.UsagePercentage
	}
	if pct == nil {
		// No recognised sub-block and no usage percentage. Most sub-kinds
		// legitimately land here, but an UNKNOWN one is worth a line — see
		// logUnconsumedInfoKind.
		logUnconsumedInfoKind(chatID, u.Meta.Kiro.Kind)
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
	// turn_start and turn_end are ABSENT because both are CONSUMED now, so a
	// bracket kind reaching this table means its sub-block did not decode.
	"turn_completion": {},
	"context_usage":   {}, "summarization_separator": {}, "summary_message": {},
	"summarization_started": {}, "summarization_failed": {}, "summarization_completed": {},
	"user_message_id_assigned": {}, "focus_update": {}, "display_error": {},
	"pending_interaction": {}, "interaction_resolved": {}, "recap": {},
	"steering_inclusion": {}, "queued": {}, "hook_update": {}, "repositories_update": {},
}

// logUnconsumedInfoKind reports a session_info_update that reached the
// end of the dispatch cascade without being consumed.
//
// session_info_update is a carrier, not a message type: it multiplexes
// 22+ sub-kinds under `_meta.kiro.kind`, and the cascade above dispatches
// on which sub-block is present rather than on the kind string, so
// everything else falls through here silently.
//
// A kind absent from knownSessionInfoKinds is logged at Warn, since it
// is most likely a KAS addition vibekit has not looked at yet. A
// known-but-ignored kind logs at Debug.
func logUnconsumedInfoKind(chatID vibekit.ChatID, kind string) {
	if kind == "" {
		return
	}
	// Sanitized and bounded like every other upstream string this
	// package logs: the kind is backend-controlled, and a raw newline
	// forges a log line.
	safe := runesafe.SanitizeSingleLineBounded(kind, maxCensusNameBytes)
	if _, known := knownSessionInfoKinds[kind]; known {
		slog.Debug("session_info_update: known kind carries nothing vibekit consumes",
			"chat_id", chatID, "kind", safe)
		return
	}
	slog.Warn("session_info_update: UNKNOWN kind, dropped — KAS may have added a sub-kind",
		"chat_id", chatID, "kind", safe)
}

// handleV3Summarization maps the v3 summarization sub-states onto the
// compaction domain events. "running"/"success" drive the started/completed
// path; a "canceled" reason is benign and produces no failed-compaction
// boundary or error banner; any other non-empty status is a genuine failure.
func (t *Translator) handleV3Summarization(ctx context.Context, chatID vibekit.ChatID, s *v3Summarization) {
	switch s.Status {
	case "running":
		t.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventCompactionStarted, chatID, vibekit.CompactionStartedPayload{}))
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

// persistTurnSummary routes one turn-end metering frame to the two
// operations it carries.
//
// `step` says the metering came from a workflow step rather than from a
// turn of the conversation: credits are real account spend the
// launching chat is owed a readout of, so they accumulate either way,
// while the turn count and duration describe the conversation and a
// step must not touch them.
//
// Credits accumulate; the absolute usage_update.cost channel
// (persistUsage) keeps overwrite precedence if KAS ever ships both.
func (t *Translator) persistTurnSummary(ctx context.Context, chatID vibekit.ChatID, summaries []promptTurnSummary, elapsedMs float64, step bool) {
	var credits float64
	for i := range summaries {
		if summaries[i].Unit == "" || summaries[i].Unit == meteringUnitCredit {
			credits += summaries[i].Usage
			continue
		}
		// A unit this does not sum is either a dimension KAS added or a
		// rename of the one above, invisible to a field-name probe.
		// Reported once per process, since the alternative is a spend
		// line that silently stops counting.
		censusMeteringUnit(summaries[i].Unit)
	}
	t.metering.AccumulateSpend(ctx, chatID, credits)
	if !step {
		t.metering.StageConversationTurnSummary(ctx, chatID, elapsedMs)
	}
}

// usageUpdate is the v3 usage_update payload. size is the context window
// (tokens), used is tokens consumed (the ring wants a 0-100 percentage); cost
// carries the credit spend ({amount, currency}), which v2 sourced from
// _kiro.dev/metadata's metering. cost is nullish upstream, so it is a pointer —
// absent cost leaves the stored credits untouched.
//
// It used to be described as "the primary context-window usage channel" and it is
// not: the frame has no emit site in any KAS build vibekit has run against (the
// header of this file records the measurement), so the live channel is the
// context_usage session_info_update sub-kind and this decoder is the fallback for
// a frame that would be correct if it ever came.
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
func (t *Translator) HandleUsageUpdate(ctx context.Context, chatID vibekit.ChatID, raw json.RawMessage) {
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
// credits < 0 leaves credits unchanged.
//
// The context-percentage gate is a material delta, not any delta: a chat
// file is rewritten wholesale on every Mutate, and KAS emits its
// percentage more than once per model response, so an exact-inequality
// gate turned a 20-tool-call turn into dozens of full-transcript
// rewrites to move a float by fractions of a point.
//
// The threshold is 1.0 percentage point, the resolution the context ring
// actually renders, plus the tier boundaries the ring itself colours on
// so a crossing is never rounded away.
func (t *Translator) persistUsage(ctx context.Context, chatID vibekit.ChatID, pct float64, size int, credits float64) {
	err := t.chats.Mutate(ctx, chatID, func(c *vibekit.Chat, exists bool) bool {
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
		// Credits keep an exact-inequality gate deliberately: the only
		// caller passing a non-negative credits value is
		// HandleUsageUpdate, whose frame has no emit site in any KAS
		// build measured so far, so this arm is unreachable today and a
		// money value is where rounding away a change would be wrong.
		return changed
	})
	if errors.Is(err, chat.ErrTombstoned) {
		return
	}
	if err != nil {
		slog.Error("persist v3 usage", "chat_id", chatID, "error", err)
	}
}

// contextPctEpsilon is the smallest context-percentage change worth a full
// transcript rewrite: one point, the resolution the context ring renders.
const contextPctEpsilon = 1.0

// contextPctTiers are the thresholds a percentage crossing must always
// persist through, since each changes what the client does rather than
// merely how it rounds: 70 and 90 are where the context ring changes
// colour, and 95 is where the client stops accepting input entirely.
// vibekit's own thresholds, not KAS's — using KAS's 80/95 tier
// boundaries let the epsilon round away the crossing that disables the
// composer.
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

// HandleConfigOptionUpdate refreshes the model catalog and the chat's own
// effort state from the v3 config_option_update.
//
// The frame carries two things with different owners, which is why it writes to
// two places. The MODEL LIST is a workspace vocabulary and goes to the one
// catalog holder. The current model and the effort state are this chat's, and
// go on the chat record.
//
// Modes are intentionally NOT refreshed here: the config catalog omits the
// bundled/workspace source tag the picker groups by, so the authoritative mode
// list stays the one captured on session/new.
func (t *Translator) HandleConfigOptionUpdate(ctx context.Context, chatID vibekit.ChatID, raw json.RawMessage) {
	var p configOptionUpdate
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	cat := readConfigCatalog(p.ConfigOptions)
	if len(cat.models) == 0 && !cat.sawEffort {
		return
	}
	t.catalog.SetModels(cat.models)
	err := t.chats.Mutate(ctx, chatID, func(c *vibekit.Chat, exists bool) bool {
		if !exists {
			return false
		}
		return cat.applyTo(c)
	})
	if errors.Is(err, chat.ErrTombstoned) {
		return
	}
	if err != nil {
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
	models        []vibekit.SessionModel
	efforts       []vibekit.SessionEffortLevel
	sawEffort     bool
}

// readConfigCatalog extracts both options from one frame.
func readConfigCatalog(opts []configOption) configCatalog {
	var cat configCatalog
	for i := range opts {
		opt := &opts[i]
		switch {
		case opt.ID == vibekit.ConfigOptionModel || opt.Category == vibekit.ConfigOptionModel:
			_ = json.Unmarshal(opt.CurrentValue, &cat.currentModel) // string; ignore non-string
			cat.models = flattenModelChoices(opt.Options)
		case opt.ID == vibekit.ConfigOptionEffort:
			cat.sawEffort = true
			_ = json.Unmarshal(opt.CurrentValue, &cat.currentEffort) // string; ignore non-string
			cat.efforts = flattenEffortChoices(opt.Options)
		}
	}
	return cat
}

// applyTo writes the chat's own share of the catalog onto the chat, reporting
// whether anything changed (the store only persists and broadcasts on a change,
// so a repeated frame must answer false).
//
// The model LIST is not here: it is the workspace's, and HandleConfigOptionUpdate
// hands it to the catalog holder. What is left is what genuinely differs between
// chats — which model this one is on, and the effort vocabulary of that model.
func (cat *configCatalog) applyTo(c *vibekit.Chat) bool {
	changed := false
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
func flattenEffortChoices(choices []configChoice) []vibekit.SessionEffortLevel {
	out := make([]vibekit.SessionEffortLevel, 0, len(choices))
	for i := range choices {
		c := &choices[i]
		if len(c.Options) > 0 {
			out = append(out, flattenEffortChoices(c.Options)...)
			continue
		}
		if c.Value == "" {
			continue
		}
		out = append(out, vibekit.SessionEffortLevel{ID: c.Value, Name: c.Name})
	}
	return out
}

// sameEffortLevels reports whether two tier lists carry the same ids in the
// same order — the change-detector that keeps a repeated catalog from
// rewriting the chat file (and broadcasting) on every frame.
func sameEffortLevels(a, b []vibekit.SessionEffortLevel) bool {
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
func flattenModelChoices(choices []configChoice) []vibekit.SessionModel {
	var out []vibekit.SessionModel
	for i := range choices {
		c := &choices[i]
		if len(c.Options) > 0 { // grouped: recurse into the group's choices
			out = append(out, flattenModelChoices(c.Options)...)
			continue
		}
		if c.Value == "" || modeltext.Hidden(c.Description) {
			continue
		}
		effort := choiceEffort(c.Meta)
		out = append(out, vibekit.SessionModel{
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
func sameModelIDs(a, b []vibekit.SessionModel) bool {
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
