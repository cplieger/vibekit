package agent

// Pre-session catalog: GET /api/config-template serves the mode + model
// catalog from kiro-cli 2.14's _kiro/config/template — the session-less
// config-options template. Replaces the former `kiro-cli chat --list-models`
// shell-out behind /api/models: one call seeds both the pre-session model
// picker and the role picker's mode list (the client still merges workspace
// agents from /api/workspace/kiro-config, since the template carries none).
//
// Routed through the long-lived UTILITY bridge: the method is advertised
// unconditionally and needs no session context, but the model registry it
// reads is populated by the governance refresh that runs on session
// creation, which the utility bridge's own session/new covers. Once a chat
// session exists its config_option_update stays the authoritative catalog;
// this endpoint only feeds pre-session UI.

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

// configTemplateTimeout bounds the template round-trip: first call may
// lazily spin up the utility bridge, so this matches hookCallTimeout
// rather than a bare read timeout.
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
			// DefaultEffortLevel is the model's own default tier; the tier
			// list itself is the `effortLevel` option's own options[] — see
			// vibekit.SessionModel.
			DefaultEffortLevel string  `json:"defaultEffortLevel"`
			RateMultiplier     float64 `json:"rateMultiplier"`
			HasEffort          bool    `json:"hasEffort"`
		} `json:"kiro"`
	} `json:"_meta"`
}

// configTemplateResponse is the GET /api/config-template reply.
type configTemplateResponse struct {
	DefaultModel string `json:"default_model,omitempty"`
	// EffortActive is the `effortLevel` option's currentValue: the tier a
	// fresh session would run at. Pre-session, this is the only evidence of
	// a live level.
	EffortActive string                       `json:"effort_active,omitempty"`
	Modes        []vibekit.SessionMode        `json:"modes"`
	Models       []vibekit.SessionModel       `json:"models"`
	EffortLevels []vibekit.SessionEffortLevel `json:"effort_levels"`
}

// handleConfigTemplate: GET /api/config-template → the workspace's mode +
// model catalog, served ONCE.
//
// This is now the only place the client gets that vocabulary. Both lists used to
// ride every ChatHeader as well — 98.6% of a 1.25 MiB /api/chats response,
// identical in all 29 headers, fetched twice per boot — and deleting them from
// there is what makes this endpoint load-bearing rather than a pre-session seed.
//
// A LIVE session's report wins over the template's, per list, because it is
// strictly better: KAS has already resolved which workspace agent shadows which
// bundled mode, and the template is built session-less with no workspace paths
// so it carries no workspace entries at all. The template is what answers before
// any bridge has run, and what fills a list no session has reported yet.
//
// Degrades to empty lists on a failure to read the template, matching the old
// /api/models contract: the client keeps its static fallbacks.
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
	// A failed or undecodable read leaves tpl zero-valued, which
	// templateToResponse renders as empty lists — so the live catalog still
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

// templateToResponse flattens the KAS template into the client-facing
// catalog: modes with their source tag (bundled | global — the template
// carries no workspace entries), and the model catalog with the same
// [Deprecated]/[Legacy] filtering the per-session paths apply.
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
// domain tier list. Same shape as the translate-side flattener; the two feeds
// stay separate because their wire structs are (one is KAS's session frame, one
// is the template result).
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

// flattenTemplateModels converts the model select's choices (flat or
// grouped) into the domain catalog, dropping hidden-tagged entries.
func flattenTemplateModels(choices []kasConfigChoice) []vibekit.SessionModel {
	out := make([]vibekit.SessionModel, 0, len(choices))
	for i := range choices {
		c := &choices[i]
		if len(c.Options) > 0 { // grouped: recurse into the group's choices
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
