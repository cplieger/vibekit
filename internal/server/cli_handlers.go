package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/version"
)

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.cliTimeouts.Models)
	defer cancel()

	type modelsResult struct {
		filtered []map[string]any
	}

	v, err, _ := s.modelsSF.Do(jsonKeyModels, func() (any, error) {
		out, runErr := s.cliRunner.Run(ctx, "chat", "--list-models", "--format", "json")
		if runErr != nil {
			return nil, runErr
		}
		var raw struct {
			Models []struct {
				ModelID             string  `json:"model_id"`
				Name                string  `json:"model_name"`
				Description         string  `json:"description"`
				RateMultiplier      float64 `json:"rate_multiplier"`
				ContextWindowTokens int     `json:"context_window_tokens"`
			} `json:"models"`
		}
		if jErr := json.Unmarshal(out, &raw); jErr != nil {
			return nil, jErr
		}
		filtered := make([]map[string]any, 0, len(raw.Models))
		for _, m := range raw.Models {
			if api.TagExcluded(m.Description, api.HiddenTags) {
				continue
			}
			filtered = append(filtered, map[string]any{
				"model_id":              m.ModelID,
				"model_name":            m.Name,
				"description":           m.Description,
				"rate_multiplier":       m.RateMultiplier,
				"context_window_tokens": m.ContextWindowTokens,
			})
		}
		return &modelsResult{filtered: filtered}, nil
	})

	if err != nil {
		slog.Warn("list-models failed", "error", err)
		api.WriteJSON(w, map[string]any{jsonKeyModels: []any{}})
		return
	}
	result, ok := v.(*modelsResult)
	if !ok {
		slog.Error("list-models: singleflight returned unexpected type",
			"type", fmt.Sprintf("%T", v))
		api.WriteJSON(w, map[string]any{jsonKeyModels: []any{}})
		return
	}
	api.WriteJSON(w, map[string]any{jsonKeyModels: result.filtered})
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		api.MethodNotAllowed(w)
		return
	}
	payload := map[string]string{"vibekit": version.Build}
	ctx, cancel := context.WithTimeout(r.Context(), s.cliTimeouts.Version)
	defer cancel()
	if out, err := s.cliRunner.Run(ctx, "--version"); err == nil {
		payload["kiro_cli"] = strings.TrimSpace(string(out))
	}
	api.WriteJSON(w, payload)
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
		api.WriteJSON(w, api.ErrorJSON("diagnostic command failed"))
		return
	}
	// Sanitize (ANSI + hidden Unicode) then best-effort redact obvious
	// secret patterns before the report reaches the browser.
	report := redactSecrets(api.SanitizeOutput(string(out)))
	if truncated {
		report += "\n\n[truncated]"
	}
	api.WriteJSON(w, map[string]string{"report": report})
}

func (s *Server) handleKiroSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		key := safeKiroSetting(r.URL.Query().Get("key"))
		if key == "" {
			api.BadRequest(w, "unknown setting key")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), s.cliTimeouts.Settings)
		defer cancel()
		out, err := s.cliRunner.Run(ctx, "settings", key)
		if err != nil {
			out = nil
		}
		api.WriteJSON(w, map[string]string{"key": key, "value": parseKiroSettingOutput(string(out))})
	case http.MethodPut:
		api.LimitBody(w, r, api.MaxJSONBody)
		var body struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			api.BadRequest(w, "bad request")
			return
		}
		key := safeKiroSetting(body.Key)
		if key == "" {
			api.BadRequest(w, "unknown setting key")
			return
		}
		meta := allowedKiroSettings[body.Key]
		value := safeKiroSettingValueFor(body.Value, meta.Kind)
		if value == "" {
			api.BadRequest(w, "invalid setting value")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), s.cliTimeouts.Settings)
		defer cancel()
		out, err := s.cliRunner.Run(ctx, "settings", key, value)
		if err != nil {
			api.WriteJSON(w, api.ErrorJSON(strings.TrimSpace(string(out))))
			return
		}
		api.Ok(w)
	default:
		api.MethodNotAllowed(w)
	}
}

func (s *Server) handleToolsInstall(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	if !s.installing.CompareAndSwap(false, true) {
		api.Conflict(w, "install already in progress")
		return
	}
	defer s.installing.Store(false)
	ctx, cancel := context.WithTimeout(r.Context(), s.cliTimeouts.ToolsInstall)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "/opt/vibekit/setup-tools.sh")
	out, err := cmd.CombinedOutput()
	result := map[string]string{jsonKeyOutput: string(out)}
	if err != nil {
		result["error"] = err.Error()
	}
	api.WriteJSON(w, result)
}
