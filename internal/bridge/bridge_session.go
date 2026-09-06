package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"

	"github.com/cplieger/vibekit/internal/ids"
	"github.com/cplieger/vibekit/internal/kascap"
	"github.com/cplieger/vibekit/internal/modeltext"
	"github.com/cplieger/vibekit/internal/vibekit"
)

type sessionMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Meta carries kiro-cli's v3 per-mode metadata. Only source is used: "bundled"
	// (workflow modes, Kiro-shipped agents) vs "workspace" (.kiro/agents/).
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

// sessionConfigOption is one entry in the v3 configOptions array. The model catalog
// lives here (id == "model"); v3 never returns a top-level `models` block.
type sessionConfigOption struct {
	ID           string                `json:"id"`
	CurrentValue json.RawMessage       `json:"currentValue"`
	Options      []sessionConfigChoice `json:"options"`
}

// sessionConfigChoice is one selectable value in a config-option select. For the
// model option the rate multiplier rides _meta.kiro (moved off ModelInfo on v3).
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
// _meta is KAS's session-metadata object spread FLAT onto the result, not under
// `_meta.kiro` (probed 2026-08-02). session/load's result carries no `sessionId`,
// which is why loadSession sets it from its own argument.
type sessionCreated struct {
	Modes     *sessionModes `json:"modes"`
	SessionID string        `json:"sessionId"`
	Meta      struct {
		// WorkflowsEnabled is KAS's RESOLVED answer for settings.workflows. A POINTER
		// because absent and false are different states, and the failure is otherwise
		// silent: the agent loses its workflowChatTools array with no error, no -32601.
		WorkflowsEnabled *bool  `json:"workflowsEnabled"`
		Title            string `json:"title"`
	} `json:"_meta"`
	ConfigOptions []sessionConfigOption `json:"configOptions"`
}

// session/new and session/load carry `mcpServers: []` — always EMPTY; KAS reads the
// user's servers from its own hot-reloading file (internal/mcp/kasfile.go). The KEY is
// required (2.16 declares it non-optional, so omitting it fails every session). Do not
// put real entries back: a client entry OUTRANKS the file, so UI edits would do nothing.

// validIdent delegates to ids.ValidIdent.
func validIdent(s string) bool {
	return ids.ValidIdent(s)
}

// withSessionMeta adds the session door's _meta.kiro block to a session/new or
// session/load parameter map, and returns the map. One function for both verbs: KAS
// falls back to the value persisted at creation, so a key sent only on session/new
// silently stops working at the first resume. Skipped when the projection is empty.
func (b *Bridge) withSessionMeta(params map[string]any) map[string]any {
	if meta := kascap.SessionMeta(b.spawn()); len(meta) > 0 {
		params["_meta"] = map[string]any{metaKeyKiro: meta}
	}
	return params
}

// withSessionChoices adds this chat's composer choices to the session door's
// _meta.kiro block: the model to start ON and the effort level to start AT.
// session/new only — a resumed session already carries both in KAS's own metadata.
//
// Choosing the model HERE rather than afterwards is what fixes a silent loss: `auto`
// has no effort tiers, so KAS drops any level sent while a session sits on it. Keyed
// into the SAME _meta.kiro map, because a second _meta block would replace the first.
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
	maps.Copy(kiro, choices)
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

	// v3 (KAS): session/new starts in the engine default mode (vibe). session/set_mode
	// is legal on a just-created idle session, so switch to the chat's role now.
	b.applyInitialMode(ctx, sid, current, opts.Mode)
	// The model and level rode _meta.kiro above, so both of these are REPAIRS: each
	// calls only on a mismatch, and a build ignoring the meta keys still converges.
	b.applyInitialModel(ctx, opts.Model)
	b.applyInitialEffort(ctx, sid, opts.Effort)
	b.applySupervised(ctx, sid, opts.Supervised)
	return nil
}

// applyInitialModel selects the chat's model on a freshly-created session. It cannot
// be a launch flag: kiro-cli REFUSES `--model` alongside `--agent-engine=v3` and
// exits before answering initialize, killing the bridge. Best-effort — a failure
// leaves the session on KAS's default rather than refusing to open the chat.
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

// applyInitialEffort applies the chat's reasoning-effort level, for the reason
// applyInitialModel exists: `--effort` is refused with `--agent-engine=v3` too. A
// repair on session/new (the level rode _meta.kiro); an unconditional assert on a
// resume, whose result carries no effortLevel option at all. Hence the log line.
func (b *Bridge) applyInitialEffort(ctx context.Context, sessionID, effort string) {
	if err := b.EnsureEffort(ctx, effort); err != nil {
		slog.Warn("apply initial reasoning effort",
			"effort", effort, "session_id", sessionID, "error", err)
	}
}

// applySupervised turns KAS's turn-approval gate on by setting `autopilot` to "off".
// No-op when the chat is not supervised: the option defaults to on. The VALUE is a
// string — a boolean is refused with -32602 and leaves the session in autopilot.
// Set ONCE at creation; it persists into KAS's session metadata. Best-effort, but at
// ERROR: the consequence is that writes the user asked to review are applied unreviewed.
func (b *Bridge) applySupervised(ctx context.Context, sessionID string, supervised bool) {
	if !supervised {
		return
	}
	if _, err := b.Call(ctx, vibekit.MethodSetConfigOption, map[string]any{
		vibekit.KeySessionID: sessionID,
		keyConfigID:          vibekit.ConfigOptionAutopilot,
		keyConfigValue:       vibekit.ConfigValueAutopilotOff,
	}); err != nil {
		slog.Error("supervised mode not applied; this session will NOT ask before writing",
			"session_id", sessionID, "error", err)
	}
}

// applyInitialMode switches a freshly-created session to wantMode when it differs
// from the session's default. Best-effort: a failed switch logs and leaves the
// default rather than failing session creation. No-op when wantMode is empty.
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
	// CallAt rather than Call: KAS answers a load by REPLAYING the session as
	// notifications that precede the result, so the caller needs the result's position.
	resp, seq, err := b.CallAt(ctx, methodSessionLoad, b.withSessionMeta(map[string]any{
		vibekit.KeySessionID: opts.SessionID, "cwd": b.workDir, "mcpServers": []any{},
	}))
	if err != nil {
		return fmt.Errorf("session/load: %w", err)
	}
	b.adoptLoadedSession(opts.SessionID, opts.Model, resp)
	b.mu.Lock()
	b.loadSeq = seq
	sid := string(b.sessionID)
	b.mu.Unlock()

	// Re-assert the chat's effort level on a resume, and ONLY that. Every other option
	// is reconciled by tryLoadSession copying it back onto the chat record; Chat.Effort
	// is the user's CHOICE and nothing overwrites it, so a lost level would never heal.
	b.applyInitialEffort(ctx, sid, opts.Effort)
	return nil
}

// adoptLoadedSession copies a session/load result onto the bridge, falling back to
// the requested model when the result is absent or unparseable. Split out of
// loadSession so the lock is released before the post-load config-option calls.
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
		// ABSENT and PRESENT-BUT-EMPTY are different states. The gate above is on the
		// modes BLOCK: a frame with no block leaves the previous list standing, where a
		// block carrying an EMPTY list used to replace it — emptying the mode picker for
		// the rest of the session, and failing closed once anything validates an id.
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
	b.reportWorkflowsDisagreement(r.Meta.WorkflowsEnabled)
	b.applyModelConfigOptionLocked(r.ConfigOptions)
	if b.modelID == "" {
		b.modelID = vibekit.ModelID(fallbackModel)
	}
}

// reportWorkflowsDisagreement logs when the session resolved settings.workflows to
// something other than what this spawn declared. A log line and nothing more: KAS
// freezes the setting at session creation, so no repair is available. The declared
// side is read off the BUILT door so it cannot drift from the table — the operator
// override withholds the key, both sides then agree on false, and nothing logs.
//
// MUST be called with b.mu held (spawn() reads fields immutable after Start).
func (b *Bridge) reportWorkflowsDisagreement(resolved *bool) {
	if resolved == nil {
		return
	}
	declared := declaredSessionWorkflows(b.spawn())
	if *resolved == declared {
		return
	}
	slog.Warn("session resolved the workflows setting against what vibekit declared; "+
		"the agent's workflow tools are not what this spawn asked for",
		"declared", declared, "resolved", *resolved)
}

// declaredSessionWorkflows reports whether a spawn's session door carries
// settings.workflows enabled, read out of the door kascap actually builds.
func declaredSessionWorkflows(s kascap.Spawn) bool {
	settings, ok := kascap.SessionMeta(s)["settings"].(map[string]any)
	if !ok {
		return false
	}
	wf, ok := settings["workflows"].(map[string]any)
	if !ok {
		return false
	}
	on, _ := wf["enabled"].(bool)
	return on
}

// applyEffortConfigOptionLocked records the reasoning-effort level the session
// reports running at, from the `effortLevel` option's currentValue. MUST be called
// with b.mu held. An ABSENT option means the level is unknown, not empty, so the
// previous value stands — KAS omits it for a tierless model and on every load result.
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

// applyModelConfigOptionLocked sources the current model and catalog from the v3
// configOptions "model" select. MUST be called with b.mu held. TWO lists come out of
// the one loop: models is for DISPLAY, so end-of-life entries are filtered out, while
// servedModels is every advertised id and the only sound input to an ENTITLEMENT
// check — validating against the display list would refuse a model the account has.
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
		// Same asymmetry the modes branch spells out: a `model` option carrying NO
		// choices reports the catalog as unknown, not empty. `currentValue` above is
		// applied either way — which model is active stands on its own.
		if len(opt.Options) == 0 {
			slog.Warn("session reported an empty model catalog; keeping the previous one",
				"current_model", b.modelID)
			return
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
