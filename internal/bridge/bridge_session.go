package bridge

// Session management: types, creation, loading, and result application.
// Extracted from bridge.go for single-responsibility clarity.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/cplieger/vibekit/internal/ids"
	"github.com/cplieger/vibekit/internal/kascap"
	"github.com/cplieger/vibekit/internal/modeltext"
	"github.com/cplieger/vibekit/internal/vibekit"
)

type sessionMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Meta carries kiro-cli's v3 per-mode metadata. Only source is used:
	// "bundled" (workflow modes + Kiro-shipped agents like semantic_reviewer)
	// vs "workspace" (custom agents from .kiro/agents/). v2 omits _meta.
	Meta struct {
		Kiro struct {
			Source string `json:"source"`
		} `json:"kiro"`
	} `json:"_meta"`
}

type sessionModes struct {
	CurrentModeID  string        `json:"currentModeId"`
	AvailableModes []sessionMode `json:"availableModes"`
}

// sessionConfigOption is one entry in the v3 configOptions array returned
// by session/new and session/load. The model catalog lives here (id ==
// "model"): currentValue is the active model id and options[] is the
// selectable catalog. v3 never returns a top-level `models` block.
type sessionConfigOption struct {
	ID           string                `json:"id"`
	CurrentValue json.RawMessage       `json:"currentValue"`
	Options      []sessionConfigChoice `json:"options"`
}

// sessionConfigChoice is one selectable value in a config-option select.
// For the model option each choice's rate multiplier rides _meta.kiro
// (moved off ModelInfo on v3).
type sessionConfigChoice struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Meta        struct {
		Kiro struct {
			RateMultiplier float64 `json:"rateMultiplier"`
		} `json:"kiro"`
	} `json:"_meta"`
}

// sessionCreated is the session/new and session/load result.
//
// _meta is KAS's session-metadata object spread verbatim onto the result —
// FLAT, not under `_meta.kiro` (probed 2026-08-02 against both verbs). Only
// `title` is decoded: session/new always carries the literal "New Session"
// placeholder, while session/load hands back the real stored title, which is
// the case worth adopting. session/load's result carries no `sessionId`, which
// is why loadSession sets it from its own argument.
type sessionCreated struct {
	Modes     *sessionModes `json:"modes"`
	SessionID string        `json:"sessionId"`
	Meta      struct {
		Title string `json:"title"`
	} `json:"_meta"`
	ConfigOptions []sessionConfigOption `json:"configOptions"`
}

// session/new and session/load carry `mcpServers: []` — always EMPTY, never
// vibekit's server set. KAS reads the user's MCP servers from its own
// hot-reloading config file, which vibekit renders (internal/mcp/kasfile.go).
//
// The key itself is REQUIRED: kiro-cli 2.16's session/new schema declares
// mcpServers as a non-optional array, so omitting it fails every session
// with "Invalid params: expected array, received undefined". The empty
// array is SAFE alongside the file: KAS's mergeServers unions the four
// sources per NAME (client entries win only for names they carry), so zero
// client entries leaves the file-based set untouched.
//
// Do not put real entries back as a convenience. A client entry OUTRANKS
// the file for its name: the file would still hot-reload, the agent would
// keep using the inline copy, and every edit in the UI would look like it
// did nothing. The parameter is also lossier — KAS's acpServerToWire drops
// oauth, oauthScopes, autoApprove, cwd and timeout from a client-supplied entry.

// validIdent delegates to ids.ValidIdent.
func validIdent(s string) bool {
	return ids.ValidIdent(s)
}

// withSessionMeta adds the session door's _meta.kiro block to a session/new or
// session/load parameter map, and returns the map.
//
// One function used by both verbs, deliberately. KAS resolves a session key from
// the call's own _meta and falls back to the value persisted when the session was
// created, so a key sent only on session/new works until the first resume and
// then silently stops for every session created before the key existed. Sending
// it from one place makes the two calls agree by construction rather than by two
// people remembering.
//
// Only when the map is NON-EMPTY: an empty _meta.kiro would add bytes to a call
// that carries none, and the projection is empty whenever the table declares no
// session-door row (or an operator disabled the only one).
func (b *Bridge) withSessionMeta(params map[string]any) map[string]any {
	if meta := kascap.SessionMeta(b.spawn()); len(meta) > 0 {
		params["_meta"] = map[string]any{metaKeyKiro: meta}
	}
	return params
}

// withSessionChoices adds this chat's composer choices to the session door's
// _meta.kiro block: the model to start ON and the reasoning-effort level to start
// AT. session/new only, because a resumed session already carries both in KAS's
// own metadata.
//
// This is the one-trip door, and it replaced two follow-up
// session/set_config_option calls. Probed on kiro-cli 2.19.1 / KAS 0.48.0:
// session/new with `_meta.kiro.{modelId,effortLevel}` returns a session already on
// claude-opus-5 at max, the config_option_update agrees, and KAS persists BOTH
// into its own session.json, so a later session/load restores them. The launch
// flags are still not an option — `--model` and `--effort` are refused alongside
// `--agent-engine=v3` with `error: the following arguments are not supported`,
// re-probed on the same build.
//
// Setting the model here rather than afterwards is what fixes a silent loss, not
// just two round trips. The `auto` model has NO effort tiers (`hasEffort:false`),
// so KAS builds no effortLevel option for it and `setSessionConfigOption` drops
// any level sent while a session sits there — measured: the call returns success,
// the option stays absent, and session.json persists `effortLevel: null`. A
// session/new starts on `auto`, so a chat whose model vibekit could not send (never
// picked, or withheld by the entitlement gate) had its level thrown away, and
// KAS's own first-prompt model pin then set that model's DEFAULT tier. Here the
// model is chosen before the session exists, so the window never opens.
//
// Keyed into the SAME _meta.kiro map the capability table builds rather than a
// second _meta block, because KAS reads one object; a second would replace the
// first and take the whole door's declarations with it.
func (b *Bridge) withSessionChoices(params map[string]any, opts *vibekit.StartOpts) map[string]any {
	choices := make(map[string]any, 2)
	if opts.Model != "" && opts.Model != vibekit.ModelAuto {
		choices[metaKeyModelID] = opts.Model
	}
	if opts.Effort != "" && vibekit.EffortLevel(opts.Effort).Valid() {
		choices[metaKeyEffortLevel] = opts.Effort
	}
	if len(choices) == 0 {
		return params
	}
	meta, ok := params["_meta"].(map[string]any)
	if !ok {
		meta = make(map[string]any, 1)
		params["_meta"] = meta
	}
	kiro, ok := meta[metaKeyKiro].(map[string]any)
	if !ok {
		kiro = make(map[string]any, len(choices))
		meta[metaKeyKiro] = kiro
	}
	for k, v := range choices {
		kiro[k] = v
	}
	return params
}

func (b *Bridge) newSession(ctx context.Context, opts *vibekit.StartOpts) error {
	resp, err := b.Call(ctx, methodSessionNew, b.withSessionChoices(b.withSessionMeta(map[string]any{
		"cwd": b.workDir, "mcpServers": []any{},
	}), opts))
	if err != nil {
		return fmt.Errorf("session/new: %w", err)
	}
	var result sessionCreated
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return fmt.Errorf("parse session/new: %w", err)
	}
	if !ids.ValidSessionID(result.SessionID) {
		return fmt.Errorf("session/new returned invalid session id: %q", result.SessionID)
	}
	b.mu.Lock()
	b.sessionID = vibekit.SessionID(result.SessionID)
	b.applySessionResultLocked(result, "")
	sid := string(b.sessionID)
	current := b.currentMode
	b.mu.Unlock()

	// v3 (KAS): session/new starts in the engine default mode (vibe).
	// When the chat asked for a specific role (a bundled mode or a
	// workspace agent-as-mode) switch to it now — session/set_mode is
	// legal on a just-created, idle session (verified on the wire).
	b.applyInitialMode(ctx, sid, current, opts.Mode)
	// The model and the level rode _meta.kiro above, so both of these are
	// REPAIRS: each compares what the result reported against what was asked for
	// and calls only on a mismatch. In the happy path that is zero round trips,
	// and a build that ignores the meta keys still converges instead of running a
	// chat on the wrong model at the wrong tier.
	b.applyInitialModel(ctx, opts.Model)
	b.applyInitialEffort(ctx, sid, opts.Effort)
	b.applySupervised(ctx, sid, opts.Supervised)
	return nil
}

// applyInitialModel selects the chat's model on a freshly-created session.
//
// It cannot be a launch flag. kiro-cli REFUSES `--model` alongside
// `--agent-engine=v3` and exits before answering initialize, so passing one
// does not merely fail to take effect, it kills the bridge (see
// bridge_process.go buildACPArgs for the measured error). session/new starts
// on KAS's own default model, so this is the only door.
//
// Best-effort like applyInitialMode: a failure leaves the session on that
// default and logs, rather than failing session creation — the alternative is
// refusing to open a chat over a model preference.
//
// Only session/new needs it. KAS persists modelId in its own session metadata
// alongside agentMode and effortLevel, so a session/load already carries the
// choice and re-asserting it would be a round trip that changes nothing.
func (b *Bridge) applyInitialModel(ctx context.Context, model string) {
	if model == "" || model == vibekit.ModelAuto {
		return
	}
	b.mu.Lock()
	current := string(b.modelID)
	b.mu.Unlock()
	if model == current {
		return
	}
	if err := b.SetModel(ctx, model); err != nil {
		slog.Warn("apply initial session model", "model", model, "error", err)
	}
}

// applyInitialEffort applies the chat's reasoning-effort level to a
// freshly-created or resumed session, for the same reason applyInitialModel
// exists: `--effort` is refused with `--agent-engine=v3` too, and it appeared in
// the same rejection line (re-probed on 2.19.1: `error: the following arguments
// are not supported with --agent-engine=v3: --model, --effort`).
//
// A repair, not the mechanism, and EnsureEffort is what makes it one. session/new
// carries the level in _meta.kiro and its result reports the level back, so a
// match costs nothing. A resumed session always asserts: KAS's session/load result
// carries no effortLevel option at all, so the bridge knows no level and the chat
// record is what vibekit has.
//
// This wrapper exists for the log line. Every other caller of EnsureEffort is
// reporting a failure to somebody; on the session-creation path there is nobody to
// report to, and refusing to open a chat over an effort preference would be worse
// than opening it at the service's own level.
func (b *Bridge) applyInitialEffort(ctx context.Context, sessionID, effort string) {
	if err := b.EnsureEffort(ctx, effort); err != nil {
		slog.Warn("apply initial reasoning effort",
			"effort", effort, "session_id", sessionID, "error", err)
	}
}

// applySupervised turns KAS's turn-approval gate on for this session by setting
// `autopilot` to false. No-op when the chat is not supervised, because true is
// already the session default.
//
// Set ONCE, at creation. The value persists into KAS's own session metadata, so
// it survives session/load and re-asserting it would be a round trip that
// changes nothing. Best-effort like applyInitialMode: a failure leaves the
// session in autopilot and logs, rather than failing session creation — but it
// logs at ERROR rather than WARN, because the consequence is that writes the user
// asked to review get applied without review.
func (b *Bridge) applySupervised(ctx context.Context, sessionID string, supervised bool) {
	if !supervised {
		return
	}
	if _, err := b.Call(ctx, vibekit.MethodSetConfigOption, map[string]any{
		vibekit.KeySessionID: sessionID,
		keyConfigID:          vibekit.ConfigOptionAutopilot,
		keyConfigValue:       false,
	}); err != nil {
		slog.Error("supervised mode not applied; this session will NOT ask before writing",
			"session_id", sessionID, "error", err)
	}
}

// applyInitialMode switches a freshly-created session to wantMode when it
// differs from the session's default. Best-effort: a failed switch logs a
// warning and leaves the session in its default mode rather than failing
// session creation. No-op when wantMode is empty (v2, or a chat that never
// picked a non-default mode).
func (b *Bridge) applyInitialMode(ctx context.Context, sessionID, currentMode, wantMode string) {
	if wantMode == "" || wantMode == currentMode {
		return
	}
	if _, err := b.Call(ctx, methodSetMode, map[string]any{
		vibekit.KeySessionID: sessionID,
		"modeId":             wantMode,
	}); err != nil {
		slog.Warn("apply initial session mode", "mode", wantMode, "session_id", sessionID, "error", err)
		return
	}
	b.mu.Lock()
	b.currentMode = wantMode
	b.mu.Unlock()
}

func (b *Bridge) loadSession(ctx context.Context, opts *vibekit.StartOpts) error {
	resp, err := b.Call(ctx, methodSessionLoad, b.withSessionMeta(map[string]any{
		vibekit.KeySessionID: opts.SessionID, "cwd": b.workDir, "mcpServers": []any{},
	}))
	if err != nil {
		return fmt.Errorf("session/load: %w", err)
	}
	b.adoptLoadedSession(opts.SessionID, opts.Model, resp)
	b.mu.Lock()
	sid := string(b.sessionID)
	b.mu.Unlock()

	// Re-assert the chat's reasoning-effort level on a resumed session, and ONLY
	// that. Every other session option is KAS's to own on a load — tryLoadSession
	// copies the mode, the mode list, the model list and the title back onto the
	// chat record, so the two agree afterwards. Effort has no such reconciliation:
	// Chat.Effort is the user's CHOICE and nothing overwrites it (Chat.EffortActive
	// is the separate mirror of what the session reports running at), so a resumed
	// session that lost the level would answer at something else while the record
	// and the pill both kept saying max, forever.
	//
	// The model is deliberately NOT re-asserted here. It has the reconciliation
	// effort lacks, and opts.Model on this path is a display fallback for a result
	// that names no model rather than a value to assert.
	b.applyInitialEffort(ctx, sid, opts.Effort)
	return nil
}

// adoptLoadedSession copies a session/load result onto the bridge, falling back
// to the requested model when the result is absent or unparseable. Split out of
// loadSession so the lock is released before the post-load config-option calls,
// which take it themselves.
func (b *Bridge) adoptLoadedSession(acpSessionID, fallbackModel string, resp *vibekit.RPCResponse) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sessionID = vibekit.SessionID(acpSessionID)
	if resp.Result != nil {
		var result sessionCreated
		parseErr := json.Unmarshal(resp.Result, &result)
		if parseErr == nil {
			b.applySessionResultLocked(result, fallbackModel)
			return
		}
		slog.Warn("session/load: unparseable result, using fallback",
			"error", parseErr, "result_len", len(resp.Result))
	}
	if b.modelID == "" {
		b.modelID = vibekit.ModelID(fallbackModel)
	}
}

// applySessionResultLocked copies the ACP session response into the
// bridge's state. MUST be called with b.mu held.
func (b *Bridge) applySessionResultLocked(r sessionCreated, fallbackModel string) {
	if r.Modes != nil {
		b.currentMode = r.Modes.CurrentModeID
		// ABSENT and PRESENT-BUT-EMPTY are different states and must not collapse.
		// The gate above is on the modes BLOCK: a frame with no block at all says
		// nothing about modes and leaves the previous list standing. A block
		// carrying an EMPTY list used to replace that list with a zero-length one,
		// which empties the mode picker for the rest of the session on a frame that
		// reported no catalog rather than an empty catalog.
		//
		// This matters more the moment anything VALIDATES a mode id against the
		// list: validating against an accidentally-empty list refuses every mode,
		// which is the fail-closed-on-no-information shape upstream had to fix
		// (KiroCrew #1542). Keeping the previous list is the "absent means attempt"
		// half; the empty case is logged so it is visible rather than silent.
		if len(r.Modes.AvailableModes) == 0 {
			slog.Warn("session reported an empty mode list; keeping the previous catalog",
				"current_mode", r.Modes.CurrentModeID)
		} else {
			modes := make([]vibekit.SessionMode, 0, len(r.Modes.AvailableModes))
			for _, m := range r.Modes.AvailableModes {
				modes = append(modes, vibekit.SessionMode{
					ID: m.ID, Name: m.Name, Description: m.Description, Source: m.Meta.Kiro.Source,
				})
			}
			b.modes.Store(&modes)
		}
	}
	b.sessionTitle = r.Meta.Title
	b.applyModelConfigOptionLocked(r.ConfigOptions)
	if b.modelID == "" {
		b.modelID = vibekit.ModelID(fallbackModel)
	}
}

// applyModelConfigOptionLocked sources the current model + catalog from
// the v3 configOptions "model" select. currentValue is the active model
// id; each option carries its rate multiplier under _meta.kiro.
// MUST be called with b.mu held.
//
// TWO lists come out of the one loop, and keeping them in one loop is the point:
//
//   - models is for DISPLAY, so [Deprecated]/[Legacy] entries are filtered out
//     (as the v2 models path did). The picker should not offer an end-of-life
//     model.
//   - servedModels is every advertised id, unfiltered, and it is the only sound
//     input to an ENTITLEMENT check. Validating a configured id against the
//     display list would refuse a deprecated model the account can still use,
//     turning a working session into a client-side refusal — which is worse than
//     the defect the check exists to prevent.
//
// applyEffortConfigOptionLocked records the reasoning-effort level the session
// reports running at, from the `effortLevel` option's currentValue. MUST be
// called with b.mu held.
//
// This exists so applyInitialEffort can be a repair rather than an unconditional
// call: session/new now carries the level in _meta.kiro and the result reports it
// back, so the common case is a match and no second round trip.
//
// An ABSENT option means the level is genuinely unknown, not that it is empty, so
// the previous value stands. KAS omits the option entirely for a model with no
// tiers (`auto`), and its session/load result omits it too — probed on 2.19.1,
// where the load result carries neither the model nor the effort option and the
// real state arrives on the config_option_update notification afterwards. Both
// cases must leave a resumed bridge asserting the chat's level rather than
// concluding it already matches.
func (b *Bridge) applyEffortConfigOptionLocked(opts []sessionConfigOption) {
	for i := range opts {
		opt := &opts[i]
		if opt.ID != vibekit.ConfigOptionEffort {
			continue
		}
		var current string
		_ = json.Unmarshal(opt.CurrentValue, &current) // string; ignore non-string
		if current != "" {
			b.effortLevel = current
		}
		return
	}
}

func (b *Bridge) applyModelConfigOptionLocked(opts []sessionConfigOption) {
	b.applyEffortConfigOptionLocked(opts)
	for i := range opts {
		opt := &opts[i]
		if opt.ID != vibekit.ConfigOptionModel {
			continue
		}
		var current string
		_ = json.Unmarshal(opt.CurrentValue, &current) // string; ignore non-string
		if current != "" {
			b.modelID = vibekit.ModelID(current)
		}
		mdls := make([]vibekit.SessionModel, 0, len(opt.Options))
		served := make([]string, 0, len(opt.Options))
		for _, c := range opt.Options {
			if c.Value == "" {
				continue
			}
			served = append(served, c.Value)
			if modeltext.Hidden(c.Description) {
				continue
			}
			mdls = append(mdls, vibekit.SessionModel{
				ID: c.Value, Name: c.Name, Description: c.Description,
				RateMultiplier: c.Meta.Kiro.RateMultiplier,
			})
		}
		b.models.Store(&mdls)
		b.servedModels.Store(&served)
		return
	}
}
