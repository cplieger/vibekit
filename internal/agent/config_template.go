package agent

// Routed through the long-lived UTILITY bridge: the method needs no session
// context, but the model registry it reads is populated by the governance
// refresh that runs on session creation, which that bridge's session/new covers.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/cplieger/vibekit/internal/modeltext"
	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/webhttp/v2"
)

// configTemplateTimeout bounds the template round-trip. Matches hookCallTimeout
// rather than a bare read timeout: the first call may spin up the utility bridge.
const configTemplateTimeout = 45 * time.Second

// kasConfigTemplate is the _kiro/config/template result shape. ConfigOptions
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
			// DefaultEffortLevel is the model's own default tier; the tier list
			// itself is the `effortLevel` option's own options[].
			DefaultEffortLevel string  `json:"defaultEffortLevel"`
			RateMultiplier     float64 `json:"rateMultiplier"`
			HasEffort          bool    `json:"hasEffort"`
		} `json:"kiro"`
	} `json:"_meta"`
}

// configTemplateResponse is the GET /api/config-template reply.
type configTemplateResponse struct {
	DefaultModel string `json:"default_model,omitempty"`
	// EffortActive is the `effortLevel` option's currentValue: the tier a fresh
	// session would run at, and pre-session the only evidence of a live level.
	EffortActive string                       `json:"effort_active,omitempty"`
	Modes        []vibekit.SessionMode        `json:"modes"`
	Models       []vibekit.SessionModel       `json:"models"`
	EffortLevels []vibekit.SessionEffortLevel `json:"effort_levels"`
}

// handleConfigTemplate answers GET /api/config-template with the workspace's
// mode + model catalog, and is the only place the client gets that vocabulary.
//
// A LIVE session's report wins over the template's, per list: KAS has already
// resolved which workspace agent shadows which bundled mode, while the template
// is built session-less with no workspace paths and so carries no workspace
// entries at all. Failing to read the template degrades to empty lists, matching
// the old /api/models contract — the client keeps its static fallbacks.
func (rt *Runtime) handleConfigTemplate(w http.ResponseWriter, r *http.Request) {
	u := rt.utility.get()
	cctx, cancel := context.WithTimeout(r.Context(), configTemplateTimeout)
	defer cancel()
	var tpl kasConfigTemplate
	raw, err := u.session.configTemplateRaw(cctx)
	switch {
	case err != nil:
		slog.Warn("config template failed", "error", err)
	default:
		if uErr := json.Unmarshal(raw, &tpl); uErr != nil {
			slog.Warn("config template decode failed", "error", uErr)
		}
	}
	// A failed read leaves tpl zero-valued, so the live catalog below still
	// reaches the client on a template outage.
	out := templateToResponse(&tpl)
	if modes := rt.catalog.Modes(); len(modes) > 0 {
		out.Modes = modes
	}
	if models := rt.catalog.Models(); len(models) > 0 {
		out.Models = models
	}
	webhttp.WriteJSON(w, out)
}

// templateToResponse flattens the KAS template into the client-facing catalog:
// modes with their source tag (bundled | global — the template carries no
// workspace entries), and the model catalog with the same [Deprecated]/[Legacy]
// filtering the per-session paths apply.
func templateToResponse(tpl *kasConfigTemplate) configTemplateResponse {
	modes := make([]vibekit.SessionMode, 0, len(tpl.Modes.AvailableModes))
	for i := range tpl.Modes.AvailableModes {
		m := &tpl.Modes.AvailableModes[i]
		if m.ID == "" {
			continue
		}
		modes = append(modes, vibekit.SessionMode{
			ID:          m.ID,
			Name:        m.Name,
			Description: m.Description,
			Source:      m.Meta.Kiro.Source,
		})
	}
	out := configTemplateResponse{
		Modes:        modes,
		Models:       []vibekit.SessionModel{},
		EffortLevels: []vibekit.SessionEffortLevel{},
	}
	for i := range tpl.ConfigOptions {
		opt := &tpl.ConfigOptions[i]
		switch opt.ID {
		case vibekit.ConfigOptionModel:
			_ = json.Unmarshal(opt.CurrentValue, &out.DefaultModel) // string; ignore non-string
			out.Models = flattenTemplateModels(opt.Options)
		case vibekit.ConfigOptionEffort:
			_ = json.Unmarshal(opt.CurrentValue, &out.EffortActive) // string; ignore non-string
			out.EffortLevels = flattenTemplateEfforts(opt.Options)
		}
	}
	return out
}

// flattenTemplateEfforts converts the effortLevel option's choices into the
// domain tier list. Kept separate from the translate-side flattener because the
// two wire structs differ (a KAS session frame vs this template result).
func flattenTemplateEfforts(choices []kasConfigChoice) []vibekit.SessionEffortLevel {
	out := make([]vibekit.SessionEffortLevel, 0, len(choices))
	for i := range choices {
		c := &choices[i]
		if len(c.Options) > 0 {
			out = append(out, flattenTemplateEfforts(c.Options)...)
			continue
		}
		if c.Value == "" {
			continue
		}
		out = append(out, vibekit.SessionEffortLevel{ID: c.Value, Name: c.Name})
	}
	return out
}

// flattenTemplateModels converts the model select's choices (flat or grouped)
// into the domain catalog, dropping hidden-tagged entries.
func flattenTemplateModels(choices []kasConfigChoice) []vibekit.SessionModel {
	out := make([]vibekit.SessionModel, 0, len(choices))
	for i := range choices {
		c := &choices[i]
		if len(c.Options) > 0 {
			out = append(out, flattenTemplateModels(c.Options)...)
			continue
		}
		if c.Value == "" || modeltext.Hidden(c.Description) {
			continue
		}
		out = append(out, vibekit.SessionModel{
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
