package bridge

// Session management: types, creation, loading, and result application.
// Extracted from bridge.go for single-responsibility clarity.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/cplieger/vibekit/internal/api"
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

// normalizeMCPServers converts in to a non-nil []any for the wire.
func normalizeMCPServers(in []map[string]any) []any {
	if len(in) == 0 {
		return []any{}
	}
	out := make([]any, len(in))
	for i, entry := range in {
		out[i] = entry
	}
	return out
}

// validIdent delegates to api.ValidIdent.
func validIdent(s string) bool {
	return api.ValidIdent(s)
}

func (b *Bridge) newSession(ctx context.Context, mcpServers []map[string]any, mode string) error {
	resp, err := b.Call(ctx, methodSessionNew, map[string]any{
		"cwd": b.workDir, "mcpServers": normalizeMCPServers(mcpServers),
	})
	if err != nil {
		return fmt.Errorf("session/new: %w", err)
	}
	var result sessionCreated
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return fmt.Errorf("parse session/new: %w", err)
	}
	if !api.ValidSessionID(result.SessionID) {
		return fmt.Errorf("session/new returned invalid session id: %q", result.SessionID)
	}
	b.mu.Lock()
	b.sessionID = api.SessionID(result.SessionID)
	b.applySessionResultLocked(result, "")
	sid := string(b.sessionID)
	current := b.currentMode
	b.mu.Unlock()

	// v3 (KAS): session/new starts in the engine default mode (vibe).
	// When the chat asked for a specific role (a bundled mode or a
	// workspace agent-as-mode) switch to it now — session/set_mode is
	// legal on a just-created, idle session (verified on the wire).
	b.applyInitialMode(ctx, sid, current, mode)
	return nil
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
		api.KeySessionID: sessionID,
		"modeId":         wantMode,
	}); err != nil {
		slog.Warn("apply initial session mode", "mode", wantMode, "session_id", sessionID, "error", err)
		return
	}
	b.mu.Lock()
	b.currentMode = wantMode
	b.mu.Unlock()
}

func (b *Bridge) loadSession(ctx context.Context, acpSessionID, fallbackModel string, mcpServers []map[string]any) error {
	resp, err := b.Call(ctx, methodSessionLoad, map[string]any{
		api.KeySessionID: acpSessionID, "cwd": b.workDir, "mcpServers": normalizeMCPServers(mcpServers),
	})
	if err != nil {
		return fmt.Errorf("session/load: %w", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sessionID = api.SessionID(acpSessionID)
	if resp.Result != nil {
		var result sessionCreated
		parseErr := json.Unmarshal(resp.Result, &result)
		if parseErr == nil {
			b.applySessionResultLocked(result, fallbackModel)
			return nil
		}
		slog.Warn("session/load: unparseable result, using fallback",
			"error", parseErr, "result_len", len(resp.Result))
	}
	if b.modelID == "" {
		b.modelID = api.ModelID(fallbackModel)
	}
	return nil
}

// applySessionResultLocked copies the ACP session response into the
// bridge's state. MUST be called with b.mu held.
func (b *Bridge) applySessionResultLocked(r sessionCreated, fallbackModel string) {
	if r.Modes != nil {
		b.currentMode = r.Modes.CurrentModeID
		modes := make([]api.SessionMode, 0, len(r.Modes.AvailableModes))
		for _, m := range r.Modes.AvailableModes {
			modes = append(modes, api.SessionMode{
				ID: m.ID, Name: m.Name, Description: m.Description, Source: m.Meta.Kiro.Source,
			})
		}
		b.modes.Store(&modes)
	}
	b.sessionTitle = r.Meta.Title
	b.applyModelConfigOptionLocked(r.ConfigOptions)
	if b.modelID == "" {
		b.modelID = api.ModelID(fallbackModel)
	}
}

// applyModelConfigOptionLocked sources the current model + catalog from
// the v3 configOptions "model" select. currentValue is the active model
// id; each option carries its rate multiplier under _meta.kiro.
// [Deprecated]/[Legacy] entries are filtered (as the v2 models path did).
// MUST be called with b.mu held.
func (b *Bridge) applyModelConfigOptionLocked(opts []sessionConfigOption) {
	for i := range opts {
		opt := &opts[i]
		if opt.ID != api.ConfigOptionModel {
			continue
		}
		var current string
		_ = json.Unmarshal(opt.CurrentValue, &current) // string; ignore non-string
		if current != "" {
			b.modelID = api.ModelID(current)
		}
		mdls := make([]api.SessionModel, 0, len(opt.Options))
		for _, c := range opt.Options {
			if c.Value == "" || api.TagExcluded(c.Description, api.HiddenTags) {
				continue
			}
			mdls = append(mdls, api.SessionModel{
				ID: c.Value, Name: c.Name, Description: c.Description,
				RateMultiplier: c.Meta.Kiro.RateMultiplier,
			})
		}
		b.models.Store(&mdls)
		return
	}
}
