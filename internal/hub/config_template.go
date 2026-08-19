package hub

// Pre-session catalog: GET /api/config-template serves the mode + model
// catalog from kiro-cli 2.14's _kiro/config/template — the session-less
// config-options template (what a fresh session/new WOULD return), built
// from KAS's bundled modes + bundled agents + the user's ~/.kiro/agents
// and the service-refreshed model registry. Replaces the former
// `kiro-cli chat --list-models` shell-out behind /api/models: one call
// now seeds BOTH the pre-session model picker and the role picker's mode
// list (which the client still merges with workspace agents from
// /api/workspace/kiro-config — the template deliberately carries no
// workspace agents, it is built with empty workspacePaths upstream).
//
// Routed through the long-lived UTILITY bridge like hooks/knowledge: the
// method is advertised unconditionally and needs no session context, but
// the model registry it reads is populated by the governance refresh that
// runs on session creation — the utility bridge's own session/new covers
// that, so the catalog is the same service-derived list a live session
// sees. Once a chat session exists its config_option_update stays the
// authoritative per-chat catalog; this endpoint only feeds pre-session UI.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/cplieger/webhttp"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/modeltext"
)

// configTemplateTimeout bounds the template round-trip. First call may
// lazily spin up the utility bridge (session/new + auth handshake), so
// this matches hookCallTimeout rather than a bare read timeout.
const configTemplateTimeout = 45 * time.Second

// kasConfigTemplate is the _kiro/config/template result shape. Modes
// reuses the wire layout of session/new's modes block; configOptions
// carries the model catalog under the entry with id "model".
type kasConfigTemplate struct {
	Modes struct {
		CurrentModeID  string        `json:"currentModeId"`
		AvailableModes []kasModeInfo `json:"availableModes"`
	} `json:"modes"`
	ConfigOptions []kasConfigOption `json:"configOptions"`
}

type kasModeInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Meta        struct {
		Kiro struct {
			Source string `json:"source"`
		} `json:"kiro"`
	} `json:"_meta"`
}

type kasConfigOption struct {
	ID           string            `json:"id"`
	CurrentValue json.RawMessage   `json:"currentValue"`
	Options      []kasConfigChoice `json:"options"`
}

type kasConfigChoice struct {
	Value       string            `json:"value"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Options     []kasConfigChoice `json:"options"` // grouped selects nest
	Meta        struct {
		Kiro struct {
			// The model's own default tier. The TIER LIST is not here — it is the
			// `effortLevel` option's own options[]. See api.SessionModel.
			DefaultEffortLevel string  `json:"defaultEffortLevel"`
			RateMultiplier     float64 `json:"rateMultiplier"`
			HasEffort          bool    `json:"hasEffort"`
		} `json:"kiro"`
	} `json:"_meta"`
}

// configTemplateResponse is the GET /api/config-template reply.
type configTemplateResponse struct {
	DefaultModel string `json:"default_model,omitempty"`
	// EffortActive is the `effortLevel` option's currentValue in the template:
	// the tier a fresh session would run at. Pre-session, this is the only
	// evidence of a live level, and without it the effort control rendered with
	// nothing selected on every chat before its first pick.
	EffortActive string                   `json:"effort_active,omitempty"`
	Modes        []api.SessionMode        `json:"modes"`
	Models       []api.SessionModel       `json:"models"`
	EffortLevels []api.SessionEffortLevel `json:"effort_levels"`
}

// handleConfigTemplate: GET /api/config-template → the pre-session mode +
// model catalog. Degrades to empty lists on any failure (same contract the
// old /api/models had): the client keeps its static fallbacks and the
// authoritative per-session catalog arrives with the first bridge.
func (h *Hub) handleConfigTemplate(w http.ResponseWriter, r *http.Request) {
	u := h.ensureUtility()
	cctx, cancel := context.WithTimeout(r.Context(), configTemplateTimeout)
	defer cancel()
	raw, err := u.session.configTemplateRaw(cctx)
	if err != nil {
		slog.Warn("config template failed", "error", err)
		webhttp.WriteJSON(w, configTemplateResponse{Modes: []api.SessionMode{}, Models: []api.SessionModel{}})
		return
	}
	var tpl kasConfigTemplate
	if uErr := json.Unmarshal(raw, &tpl); uErr != nil {
		slog.Warn("config template decode failed", "error", uErr)
		webhttp.WriteJSON(w, configTemplateResponse{Modes: []api.SessionMode{}, Models: []api.SessionModel{}})
		return
	}
	webhttp.WriteJSON(w, templateToResponse(&tpl))
}

// templateToResponse flattens the KAS template into the client-facing
// catalog: modes with their source tag (bundled | global — the template
// carries no workspace entries), and the model catalog with the same
// [Deprecated]/[Legacy] filtering the per-session paths apply.
func templateToResponse(tpl *kasConfigTemplate) configTemplateResponse {
	modes := make([]api.SessionMode, 0, len(tpl.Modes.AvailableModes))
	for i := range tpl.Modes.AvailableModes {
		m := &tpl.Modes.AvailableModes[i]
		if m.ID == "" {
			continue
		}
		modes = append(modes, api.SessionMode{
			ID:          m.ID,
			Name:        m.Name,
			Description: m.Description,
			Source:      m.Meta.Kiro.Source,
		})
	}
	out := configTemplateResponse{
		Modes:        modes,
		Models:       []api.SessionModel{},
		EffortLevels: []api.SessionEffortLevel{},
	}
	for i := range tpl.ConfigOptions {
		opt := &tpl.ConfigOptions[i]
		switch opt.ID {
		case api.ConfigOptionModel:
			_ = json.Unmarshal(opt.CurrentValue, &out.DefaultModel) // string; ignore non-string
			out.Models = flattenTemplateModels(opt.Options)
		case api.ConfigOptionEffort:
			_ = json.Unmarshal(opt.CurrentValue, &out.EffortActive) // string; ignore non-string
			out.EffortLevels = flattenTemplateEfforts(opt.Options)
		}
	}
	return out
}

// flattenTemplateEfforts converts the effortLevel option's choices into the
// domain tier list. Same shape as the translate-side flattener; the two feeds
// stay separate because their wire structs are (one is KAS's session frame, one
// is the template result).
func flattenTemplateEfforts(choices []kasConfigChoice) []api.SessionEffortLevel {
	out := make([]api.SessionEffortLevel, 0, len(choices))
	for i := range choices {
		c := &choices[i]
		if len(c.Options) > 0 {
			out = append(out, flattenTemplateEfforts(c.Options)...)
			continue
		}
		if c.Value == "" {
			continue
		}
		out = append(out, api.SessionEffortLevel{ID: c.Value, Name: c.Name})
	}
	return out
}

// flattenTemplateModels converts the model select's choices (flat or
// grouped) into the domain catalog, dropping hidden-tagged entries.
func flattenTemplateModels(choices []kasConfigChoice) []api.SessionModel {
	out := make([]api.SessionModel, 0, len(choices))
	for i := range choices {
		c := &choices[i]
		if len(c.Options) > 0 { // grouped: recurse into the group's choices
			out = append(out, flattenTemplateModels(c.Options)...)
			continue
		}
		if c.Value == "" || modeltext.Hidden(c.Description) {
			continue
		}
		out = append(out, api.SessionModel{
			ID:                 c.Value,
			Name:               c.Name,
			Description:        c.Description,
			RateMultiplier:     c.Meta.Kiro.RateMultiplier,
			HasEffort:          c.Meta.Kiro.HasEffort,
			DefaultEffortLevel: c.Meta.Kiro.DefaultEffortLevel,
		})
	}
	return out
}
