package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"vibekit/internal/api"
	"vibekit/internal/version"
)

const (
	jsonKeyOutput = api.JSONKeyOutput
	jsonKeyModels = "models"
)

// CLIRunner abstracts subprocess execution for kiro-cli commands,
// enabling unit testing of handler logic without a real binary.
type CLIRunner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// execCLIRunner is the production CLIRunner that shells out to cliPath.
type execCLIRunner struct {
	cliPath string
}

func (r *execCLIRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, r.cliPath, args...).CombinedOutput() //nolint:gosec // G204: binary path from config
}

// cliTimeouts holds the timeout budget for each kiro-cli subprocess
// invocation. Named fields make the budget inspectable and tunable.
type cliTimeouts struct {
	Models       time.Duration
	Version      time.Duration
	Diagnostics  time.Duration
	Settings     time.Duration
	ToolsInstall time.Duration
}

// defaultCLITimeouts returns the production timeout budget.
func defaultCLITimeouts() cliTimeouts {
	return cliTimeouts{
		Models:       5 * time.Second,
		Version:      2 * time.Second,
		Diagnostics:  20 * time.Second,
		Settings:     3 * time.Second,
		ToolsInstall: 10 * time.Minute,
	}
}

// settingKind distinguishes boolean-only from numeric-only kiro-cli settings.
type settingKind int

const (
	settingBool settingKind = iota
	settingInt
	_settingKindCount // must remain last — compile-time exhaustiveness guard
)

// Compile-time assertion: if a new settingKind is added without updating
// safeKiroSettingValueFor, this line will fail to compile.
var _ = [1]struct{}{}[_settingKindCount-2]

// settingMeta carries validation metadata for an allowed kiro-cli setting.
type settingMeta struct {
	Kind settingKind
}

var allowedKiroSettings = map[string]settingMeta{
	"chat.enableCheckpoint":                  {Kind: settingBool},
	"chat.enableTodoList":                    {Kind: settingBool},
	"chat.enableKnowledge":                   {Kind: settingBool},
	"chat.enableSubagent":                    {Kind: settingBool},
	"chat.enablePromptHints":                 {Kind: settingBool},
	"chat.enableContextUsageIndicator":       {Kind: settingBool},
	"chat.disableAutoCompaction":             {Kind: settingBool},
	"hooks.showStatus":                       {Kind: settingBool},
	"telemetry.enabled":                      {Kind: settingBool},
	"toolSearch.enabled":                     {Kind: settingBool},
	"compaction.excludeContextWindowPercent": {Kind: settingInt},
	"compaction.excludeMessages":             {Kind: settingInt},
	"cleanup.periodDays":                     {Kind: settingInt},
}

func safeKiroSetting(k string) string {
	if _, ok := allowedKiroSettings[k]; ok {
		return k
	}
	return ""
}

func safeKiroSettingValueFor(v string, kind settingKind) string {
	switch kind {
	case settingBool:
		if v == "true" || v == "false" {
			return v
		}
		return ""
	case settingInt:
		for _, c := range v {
			if c < '0' || c > '9' {
				return ""
			}
		}
		if v != "" && len(v) <= 4 {
			return v
		}
		return ""
	}
	return ""
}

func parseKiroSettingOutput(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '('); i > 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

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
	out, err := s.cliRunner.Run(ctx, "diagnostic", "--force", "--format", "json-pretty")
	if err != nil {
		slog.Warn("diagnostics: kiro-cli exec failed", "error", err)
		api.WriteJSON(w, map[string]string{api.JSONKeyError: "diagnostic command failed"})
		return
	}
	api.WriteJSON(w, map[string]string{"report": string(out)})
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
			api.WriteJSON(w, map[string]string{api.JSONKeyError: strings.TrimSpace(string(out))})
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
