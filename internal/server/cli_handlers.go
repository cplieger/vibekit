package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/logsafe"
	"github.com/cplieger/vibekit/internal/sanitize"
	"github.com/cplieger/vibekit/internal/version"
	"github.com/cplieger/webhttp/v2"
)

// The former handleModels (`kiro-cli chat --list-models` shell-out behind
// GET /api/models) was replaced by the runtime's GET /api/config-template,
// which serves the same catalog — plus the mode list — from kiro-cli
// 2.14's session-less _kiro/config/template over the utility bridge.

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	if !httpreply.RequireMethod(w, r, http.MethodGet) {
		return
	}
	payload := map[string]string{"vibekit": version.Build}
	ctx, cancel := context.WithTimeout(r.Context(), s.cliTimeouts.Version)
	defer cancel()
	if out, err := s.cliRunner.Run(ctx, "--version"); err == nil {
		payload["kiro_cli"] = strings.TrimSpace(string(out))
	}
	webhttp.WriteJSON(w, payload)
}

func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cliTimeouts.Diagnostics)
	defer cancel()
	// Capture STDOUT only (stderr is logged, never merged in) and cap the
	// output so a runaway diagnostic dump can't bloat the HTTP response.
	out, truncated, err := s.cliRunner.RunStdoutCapped(ctx, diagnosticsMaxBytes, "diagnostic", "--force", "--format", "json-pretty")
	if err != nil {
		slog.Warn("diagnostics: kiro-cli exec failed", "error", err)
		webhttp.WriteJSON(w, httpreply.ErrorJSON("diagnostic command failed"))
		return
	}
	// Sanitize (ANSI + hidden Unicode) before the report reaches the browser.
	report := sanitize.Output(string(out))
	if truncated {
		report += "\n\n[truncated]"
	}
	webhttp.WriteJSON(w, map[string]string{"report": report})
}

func (s *Server) handleKiroSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		key := safeKiroSetting(r.URL.Query().Get("key"))
		if key == "" {
			httpreply.BadRequest(w, "unknown setting key")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), s.cliTimeouts.Settings)
		defer cancel()
		out, err := s.cliRunner.Run(ctx, "settings", key)
		if err != nil {
			out = nil
		}
		webhttp.WriteJSON(w, map[string]string{"key": key, "value": parseKiroSettingOutput(string(out))})
	case http.MethodPut:
		webhttp.LimitBody(w, r, webhttp.MaxJSONBody)
		var body struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpreply.BadRequest(w, "bad request")
			return
		}
		key := safeKiroSetting(body.Key)
		if key == "" {
			httpreply.BadRequest(w, "unknown setting key")
			return
		}
		meta := allowedKiroSettings[body.Key]
		value := safeKiroSettingValueFor(body.Value, meta.Kind)
		if value == "" {
			httpreply.BadRequest(w, "invalid setting value")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), s.cliTimeouts.Settings)
		defer cancel()
		out, err := s.cliRunner.Run(ctx, "settings", key, value)
		if err != nil {
			// 502, not 200-with-an-error-body. The client's action framework
			// classifies by STATUS, so answering a refused write with 200 made
			// every failure read as success: the toggle stayed flipped, no toast
			// fired, and the setting was not written. kiro-cli is the upstream
			// here and it declined, which is what Bad Gateway means.
			slog.Warn("kiro-cli settings write refused", "key", logsafe.Field(key), "error", logsafe.Field(err.Error()))
			webhttp.WriteJSONStatus(w, http.StatusBadGateway,
				httpreply.ErrorJSON(strings.TrimSpace(string(out))))
			return
		}
		webhttp.Ok(w)
	default:
		httpreply.MethodNotAllowed(w, http.MethodGet, http.MethodPut)
	}
}
