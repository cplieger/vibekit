package agent

// Pre-session catalog: GET /api/config-template serves the mode + model catalog
// from kiro-cli's session-less _kiro/config/template, over the UTILITY bridge,
// whose own session/new populates the model registry the method reads.

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

// configTemplateTimeout bounds the template round-trip: the first call may lazily
// spin up the utility bridge, so this matches hookCallTimeout rather than a bare
// read timeout. The CLIENT's bound (fetchModelsFromREST in static-src/app.ts) is
// deliberately LONGER, or this budget can never be spent — the library's 30s
// default aborted every cold start. Move the two together.
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
			// DefaultEffortLevel is the model's own default tier; the tier list is
			// the `effortLevel` option's own options[] — see vibekit.SessionModel.
			DefaultEffortLevel string  `json:"defaultEffortLevel"`
			RateMultiplier     float64 `json:"rateMultiplier"`
			HasEffort          bool    `json:"hasEffort"`
		} `json:"kiro"`
	} `json:"_meta"`
}

// handleConfigTemplate: GET /api/config-template → the pre-session mode +
// model catalog, and the verdict saying which outcome produced it. Every path
// answers 200 with non-null lists (the client keeps its static fallbacks and
// the authoritative per-session catalog arrives with the first bridge); what
// separates them is vibekit.ConfigTemplateResponse.Catalog.
func (rt *Runtime) handleConfigTemplate(w http.ResponseWriter, r *http.Request) {
	u := rt.utility.get()
	cctx, cancel := context.WithTimeout(r.Context(), configTemplateTimeout)
	defer cancel()
	raw, err := u.session.configTemplateRaw(cctx)
	if err != nil {
		slog.Warn("config template failed", "error", err)
		webhttp.WriteJSON(w, unavailableTemplate(vibekit.CatalogReasonRPC))
		return
	}
	var tpl kasConfigTemplate
	if uErr := json.Unmarshal(raw, &tpl); uErr != nil {
		slog.Warn("config template decode failed", "error", uErr)
		webhttp.WriteJSON(w, unavailableTemplate(vibekit.CatalogReasonDecode))
		return
	}
	webhttp.WriteJSON(w, templateToResponse(&tpl))
}

// unavailableTemplate is the body for a read that produced no catalog. ONE builder
// for both failure branches, which used to leave EffortLevels nil and so emitted
// `null` where the success path emits `[]` — one response type with two shapes.
func unavailableTemplate(reason vibekit.CatalogReason) vibekit.ConfigTemplateResponse {
	return vibekit.ConfigTemplateResponse{
		Catalog:       vibekit.CatalogUnavailable,
		CatalogReason: reason,
		Modes:         []vibekit.SessionMode{},
		Models:        []vibekit.SessionModel{},
		EffortLevels:  []vibekit.SessionEffortLevel{},
	}
}

// templateToResponse flattens the KAS template into the client-facing
// catalog: modes with their source tag (bundled | global — the template
// carries no workspace entries), and the model catalog with the same
// [Deprecated]/[Legacy] filtering the per-session paths apply.
func templateToResponse(tpl *kasConfigTemplate) vibekit.ConfigTemplateResponse {
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
	out := vibekit.ConfigTemplateResponse{
		// The verdict is the option's PRESENCE, never len(out.Models): KAS omits
		// the option when its cache holds nothing, and a present option whose
		// entries the [Deprecated] filter all drops is still a catalog KAS answered.
		Catalog:      vibekit.CatalogEmpty,
		Modes:        modes,
		Models:       []vibekit.SessionModel{},
		EffortLevels: []vibekit.SessionEffortLevel{},
	}
	for i := range tpl.ConfigOptions {
		opt := &tpl.ConfigOptions[i]
		switch opt.ID {
		case vibekit.ConfigOptionModel:
			out.Catalog = vibekit.CatalogReady
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
