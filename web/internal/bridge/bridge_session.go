package bridge

// Session management: types, creation, loading, and result application.
// Extracted from bridge.go for single-responsibility clarity.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"vibekit/internal/api"
)

type sessionMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type sessionModel struct {
	ModelID        string  `json:"modelId"`
	Name           string  `json:"name"`
	Description    string  `json:"description"`
	RateMultiplier float64 `json:"rateMultiplier"`
}

type sessionModes struct {
	CurrentModeID  string        `json:"currentModeId"`
	AvailableModes []sessionMode `json:"availableModes"`
}

type sessionModels struct {
	CurrentModelID  string         `json:"currentModelId"`
	AvailableModels []sessionModel `json:"availableModels"`
}

type sessionCreated struct {
	Modes     *sessionModes  `json:"modes"`
	Models    *sessionModels `json:"models"`
	SessionID string         `json:"sessionId"`
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

func (b *Bridge) newSession(ctx context.Context, mcpServers []map[string]any) error {
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
	defer b.mu.Unlock()
	b.sessionID = api.SessionID(result.SessionID)
	b.applySessionResultLocked(result, "")
	return nil
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
			modes = append(modes, api.SessionMode{ID: m.ID, Name: m.Name, Description: m.Description})
		}
		b.modes.Store(&modes)
	}
	if r.Models != nil {
		b.modelID = api.ModelID(r.Models.CurrentModelID)
		mdls := make([]api.SessionModel, 0, len(r.Models.AvailableModels))
		for _, m := range r.Models.AvailableModels {
			if api.TagExcluded(m.Description, api.HiddenTags) {
				continue
			}
			mdls = append(mdls, api.SessionModel{
				ID: m.ModelID, Name: m.Name, Description: m.Description,
				RateMultiplier: m.RateMultiplier,
			})
		}
		b.models.Store(&mdls)
	}
	if b.modelID == "" {
		b.modelID = api.ModelID(fallbackModel)
	}
}
