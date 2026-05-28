package server

import (
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"vibekit/internal/api"
	"vibekit/internal/fileutil"
	"vibekit/internal/logctl"
	"vibekit/internal/permissions"
	"vibekit/internal/settings"
)

func (s *Server) handleSteering(w http.ResponseWriter, r *http.Request) {
	path := s.steering.CustomPath()
	switch r.Method {
	case http.MethodGet:
		data, err := os.ReadFile(path)
		if err != nil {
			api.WriteJSON(w, map[string]string{"content": ""})
			return
		}
		api.WriteJSON(w, map[string]string{"content": string(data)})
	case http.MethodPut:
		api.LimitBody(w, r, api.MaxJSONBody)
		var body struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			api.BadRequest(w, "bad request")
			return
		}
		// Empty content (whitespace only) means "no custom instructions":
		// remove the file instead of writing an empty one. Otherwise
		// kiro-cli would include the empty file on every agent load.
		if strings.TrimSpace(body.Content) == "" {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				api.InternalError(w, err)
				return
			}
			api.Ok(w)
			return
		}
		if r.Context().Err() != nil {
			return
		}
		// Atomic write via temp+fsync+rename+dir-fsync (fileutil.SaveBytes
		// also creates the parent dir). Replaces a bare os.WriteFile
		// that could leave a truncated file on a crash mid-write.
		if err := fileutil.SaveBytes(path, []byte(body.Content), 0o644); err != nil {
			api.InternalError(w, err)
			return
		}
		api.Ok(w)
	default:
		api.MethodNotAllowed(w)
	}
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	path := filepath.Join(s.configDir, settings.Filename)
	switch r.Method {
	case http.MethodGet:
		data, err := os.ReadFile(path)
		if err != nil {
			api.WriteJSON(w, settings.DefaultSettings())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	case http.MethodPut, http.MethodPatch:
		api.LimitBody(w, r, api.MaxJSONBody)
		var patch map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			api.BadRequest(w, "invalid json")
			return
		}
		// Warn (don't reject) on unknown top-level keys so a typo
		// or stale frontend write surfaces in operator logs without
		// breaking forward-compatible additions. The patch still
		// gets persisted verbatim — readers tolerate unknown keys
		// via the Field[T] pattern.
		patchKeys := make([]string, 0, len(patch))
		for k := range patch {
			patchKeys = append(patchKeys, k)
		}
		_ = settings.WarnUnknownKeys(patchKeys, r.Method+" "+r.URL.Path)
		s.settingsMu.Lock()
		defer s.settingsMu.Unlock()
		// For PATCH, merge with existing; for PUT, replace entirely.
		if r.Method == http.MethodPatch {
			existing := make(map[string]json.RawMessage)
			if f, err := os.Open(path); err == nil {
				defer f.Close()
				const maxSettingsBytes = 1 << 20 // 1 MiB
				if info, sErr := f.Stat(); sErr == nil && info.Size() <= maxSettingsBytes {
					data := make([]byte, info.Size())
					if _, rErr := io.ReadFull(f, data); rErr == nil {
						_ = json.Unmarshal(data, &existing)
					}
				}
			}
			maps.Copy(existing, patch)
			patch = existing
		}
		pretty, err := json.MarshalIndent(patch, "", "  ")
		if err != nil {
			api.BadRequest(w, "invalid json")
			return
		}
		if r.Context().Err() != nil {
			return
		}
		// Atomic write via temp+fsync+rename+dir-fsync. Replaces a bare
		// os.WriteFile so a crash mid-write cannot truncate config.json
		// to zero bytes (which would silently revert every preference to
		// its consumer-side default on the next read).
		if wErr := fileutil.SaveBytes(path, append(pretty, '\n'), 0o644); wErr != nil {
			api.InternalError(w, wErr)
			return
		}
		api.Ok(w)
		s.hub.Broadcast(r.Context(), api.NewEvent(api.EventSettingsUpdated, "", api.SettingsUpdatedPayload{}))
		s.syncPushPreferences(patch)
		s.syncDebugLogs(patch)
	default:
		api.MethodNotAllowed(w)
	}
}

// handleCommandRules handles GET/POST/DELETE for the unified
// per-command rule list.
func (s *Server) handleCommandRules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		api.WriteJSON(w, map[string]any{"entries": s.rules.List()})
	case http.MethodPost:
		api.LimitBody(w, r, api.MaxJSONBody)
		var body struct {
			Pattern  string               `json:"pattern"`
			Mode     permissions.RuleMode `json:"mode"`
			Priority int                  `json:"priority"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			api.BadRequest(w, "invalid JSON")
			return
		}
		if body.Pattern == "" {
			api.BadRequest(w, "pattern is required")
			return
		}
		if body.Mode == "" {
			body.Mode = permissions.RuleAllow
		}
		if err := s.rules.Add(body.Pattern, body.Mode, body.Priority); err != nil {
			if errors.Is(err, permissions.ErrInvalidMode) {
				api.BadRequest(w, "mode must be allow or deny")
				return
			}
			api.InternalError(w, err)
			return
		}
		api.Ok(w)
	case http.MethodDelete:
		pattern := r.URL.Query().Get("pattern")
		if pattern == "" {
			api.BadRequest(w, "pattern query param is required")
			return
		}
		if err := s.rules.Remove(pattern); err != nil {
			api.InternalError(w, err)
			return
		}
		api.Ok(w)
	default:
		api.MethodNotAllowed(w)
	}
}

// syncPushPreferences reads notification preference toggles from the settings
// patch and forwards them to the push service.
func (s *Server) syncPushPreferences(patch map[string]json.RawMessage) {
	prefs := map[api.PushKind]bool{
		api.PushKindAgentFinished: true,
		api.PushKindPermission:    true,
	}
	if v, ok := patch["notify_agent_finished"]; ok {
		var af bool
		if json.Unmarshal(v, &af) == nil {
			prefs[api.PushKindAgentFinished] = af
		}
	}
	if v, ok := patch["notify_permission"]; ok {
		var pn bool
		if json.Unmarshal(v, &pn) == nil {
			prefs[api.PushKindPermission] = pn
		}
	}
	s.push.SetPreferences(prefs)
}

// syncDebugLogs flips the process-wide slog level when the user
// toggles the Debug logs setting.
func (s *Server) syncDebugLogs(patch map[string]json.RawMessage) {
	v, ok := patch["debug_logs"]
	if !ok {
		return
	}
	var on bool
	if err := json.Unmarshal(v, &on); err != nil {
		return
	}
	logctl.SetDebug(on)
}
