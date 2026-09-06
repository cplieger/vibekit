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
		s.readKiroSettings(w, r)
	case http.MethodPut:
		s.writeKiroSetting(w, r)
	default:
		httpreply.MethodNotAllowed(w, http.MethodGet, http.MethodPut)
	}
}

// readKiroSettings answers GET /api/kiro-settings?keys=a,b,c — or every
// allowlisted key when the parameter is absent — as {"settings": {key: value}}.
//
// One `settings list` spawn answers the whole read, falling back to the per-key
// invocation for a key it does not carry — a build older than 2.20.2 must still
// fill the panel. ONE DEADLINE covers every spawn, which is what bounds that
// fallback: the per-key reads are sequential and an absent ?keys= names the whole
// allowlist. A key the deadline beats answers "", rendered as the default.
func (s *Server) readKiroSettings(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if unknownKiroSettingsQuery(q) {
		httpreply.BadRequest(w, "unknown query parameter")
		return
	}
	keys := requestedKiroSettings(q.Get(kiroSettingsKeysParam))
	if len(keys) == 0 {
		httpreply.BadRequest(w, "unknown setting key")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cliTimeouts.Settings)
	defer cancel()
	listed := s.readKiroSettingsList(ctx)
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		value, ok := listed[key]
		if !ok {
			value = s.readOneKiroSetting(ctx, key)
		}
		out[key] = value
	}
	webhttp.WriteJSON(w, map[string]any{"settings": out})
}

// readKiroSettingsList reads the whole settings document in one spawn, or nil
// when kiro-cli cannot answer it.
//
// STDOUT only (RunStdoutCapped, not Run): the document is parsed as JSON, and
// CombinedOutput would fold any stderr line kiro-cli writes into the bytes
// json.Unmarshal reads.
//
// The deadline is the caller's — readKiroSettings owns the whole read's budget.
func (s *Server) readKiroSettingsList(ctx context.Context) map[string]string {
	out, truncated, err := s.cliRunner.RunStdoutCapped(ctx, settingsListMaxBytes, settingsListArgs...)
	if err != nil {
		slog.Debug("kiro-cli settings list failed; reading the requested keys one at a time",
			"error", logsafe.Field(err.Error()))
		return nil
	}
	if truncated {
		// A truncated document is not JSON, so parsing it would fail anyway;
		// say which of the two things went wrong.
		slog.Warn("kiro-cli settings list exceeded its cap", "cap", settingsListMaxBytes)
		return nil
	}
	listed, err := parseKiroSettingsList(out)
	if err != nil {
		slog.Debug("kiro-cli settings list is not a JSON object; reading the requested keys one at a time",
			"error", logsafe.Field(err.Error()))
		return nil
	}
	return listed
}

// readOneKiroSetting reads a single setting with the per-key invocation,
// answering "" when kiro-cli declines. An empty value is what the client reads
// as "unset", which renders the control's default rather than failing the panel.
//
// The deadline is the caller's — readKiroSettings owns the whole read's budget.
func (s *Server) readOneKiroSetting(ctx context.Context, key string) string {
	out, err := s.cliRunner.Run(ctx, "settings", key)
	if err != nil {
		return ""
	}
	return parseKiroSettingOutput(string(out))
}

// writeKiroSetting serves PUT /api/kiro-settings: one allowlisted key, one
// validated value.
func (s *Server) writeKiroSetting(w http.ResponseWriter, r *http.Request) {
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
}
