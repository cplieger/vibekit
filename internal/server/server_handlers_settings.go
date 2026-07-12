package server

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/cplieger/atomicfile/v2"
	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/logctl"
	"github.com/cplieger/vibekit/internal/permissions"
	"github.com/cplieger/vibekit/internal/settings"
)

func (s *Server) handleSteering(w http.ResponseWriter, r *http.Request) {
	path := s.steering.CustomPath()
	switch r.Method {
	case http.MethodGet:
		handleSteeringGet(w, path)
	case http.MethodPut:
		handleSteeringPut(w, r, path)
	default:
		api.MethodNotAllowed(w)
	}
}

func handleSteeringGet(w http.ResponseWriter, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		api.WriteJSON(w, map[string]string{"content": ""})
		return
	}
	api.WriteJSON(w, map[string]string{"content": string(data)})
}

func handleSteeringPut(w http.ResponseWriter, r *http.Request, path string) {
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
	// Atomic write via temp+fsync+rename+dir-fsync. WithMkdirMode
	// auto-creates the parent dir (replacing the old SaveBytes
	// behavior); WithMode keeps the 0o644 perm. Replaces a bare
	// os.WriteFile that could leave a truncated file on a crash
	// mid-write. A non-nil error means the content did NOT land —
	// surface it as 500. A nil error with res.Durable==false means
	// the content is on disk but the parent-dir fsync was
	// unconfirmed; log and proceed (the library already logged the
	// fsync failure at Warn).
	res, err := atomicfile.WriteFile(r.Context(), path, []byte(body.Content),
		atomicfile.WithMode(0o644), atomicfile.WithMkdirMode(0o755))
	if err != nil {
		api.InternalError(w, err)
		return
	}
	if !res.Durable {
		slog.Warn("steering: saved but parent-dir fsync unconfirmed; not guaranteed durable across an immediate crash",
			"path", path)
	}
	api.Ok(w)
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	path := filepath.Join(s.configDir, settings.Filename)
	switch r.Method {
	case http.MethodGet:
		handleSettingsGet(w, path)
	case http.MethodPut, http.MethodPatch:
		s.handleSettingsWrite(w, r, path)
	default:
		api.MethodNotAllowed(w)
	}
}

func handleSettingsGet(w http.ResponseWriter, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		api.WriteJSON(w, settings.DefaultSettings())
		return
	}
	w.Header().Set("Content-Type", api.MIMETypeJSON)
	_, _ = w.Write(data)
}

// maxSettingsBytes caps the existing settings file read+merged on PATCH so a
// corrupt or runaway config can't pin memory.
const maxSettingsBytes = 1 << 20 // 1 MiB

// readExistingSettings reads and parses the on-disk settings for a PATCH
// merge. A missing, unreadable, oversize, or invalid file yields an empty map
// — PATCH then writes just the patched keys, matching the prior behavior where
// any read failure was silently ignored.
func readExistingSettings(path string) map[string]json.RawMessage {
	existing := make(map[string]json.RawMessage)
	f, err := os.Open(path)
	if err != nil {
		return existing
	}
	defer f.Close()
	info, sErr := f.Stat()
	if sErr != nil || info.Size() > maxSettingsBytes {
		return existing
	}
	data := make([]byte, info.Size())
	if _, rErr := io.ReadFull(f, data); rErr != nil {
		return existing
	}
	_ = json.Unmarshal(data, &existing)
	return existing
}

func (s *Server) handleSettingsWrite(w http.ResponseWriter, r *http.Request, path string) {
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
	// PATCH merges the incoming keys over the existing file. PUT replaces the
	// file, but must not silently wipe server-managed keys written by other
	// flows (agent_ignore_files from the Permissions UI, model_effort from the
	// model switcher): carry over any managed key the PUT body omits so a
	// full-object PUT stays non-destructive of them.
	switch r.Method {
	case http.MethodPatch:
		existing := readExistingSettings(path)
		maps.Copy(existing, patch)
		patch = existing
	case http.MethodPut:
		existing := readExistingSettings(path)
		for _, k := range settings.ServerManagedKeys() {
			if _, inBody := patch[k]; inBody {
				continue
			}
			if v, ok := existing[k]; ok {
				patch[k] = v
			}
		}
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
	// its consumer-side default on the next read). A non-nil error
	// means the data did not land — surface 500. A nil error with
	// res.Durable==false means the config is on disk but the
	// parent-dir fsync was unconfirmed; fall through so the broadcast
	// and preference syncs still run.
	res, wErr := atomicfile.WriteFile(r.Context(), path, append(pretty, '\n'),
		atomicfile.WithMode(0o644), atomicfile.WithMkdirMode(0o755))
	if wErr != nil {
		api.InternalError(w, wErr)
		return
	}
	if !res.Durable {
		slog.Warn("settings: saved but parent-dir fsync unconfirmed; not guaranteed durable across an immediate crash",
			"path", path)
	}
	api.Ok(w)
	s.hub.Broadcast(r.Context(), api.NewEvent(api.EventSettingsUpdated, "", api.SettingsUpdatedPayload{}))
	s.syncPushPreferences(patch)
	s.syncDebugLogs(patch)
}

// handleCommandRules handles GET/POST/DELETE for the unified
// per-command rule list.
func (s *Server) handleCommandRules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		api.WriteJSON(w, map[string]any{"entries": s.rules.List()})
	case http.MethodPost:
		s.handleCommandRulesPost(w, r)
	case http.MethodDelete:
		s.handleCommandRulesDelete(w, r)
	default:
		api.MethodNotAllowed(w)
	}
}

func (s *Server) handleCommandRulesPost(w http.ResponseWriter, r *http.Request) {
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
}

func (s *Server) handleCommandRulesDelete(w http.ResponseWriter, r *http.Request) {
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
}

// syncPushPreferences reads notification preference toggles from the settings
// patch and forwards them to the push service.
func (s *Server) syncPushPreferences(patch map[string]json.RawMessage) {
	prefs := map[api.PushKind]bool{
		api.PushKindAgentFinished: true,
		api.PushKindPermission:    true,
	}
	if v, ok := patch[settings.KeyNotifyAgentFinished]; ok {
		var af bool
		if json.Unmarshal(v, &af) == nil {
			prefs[api.PushKindAgentFinished] = af
		}
	}
	if v, ok := patch[settings.KeyNotifyPermission]; ok {
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
	v, ok := patch[settings.KeyDebugLogs]
	if !ok {
		return
	}
	var on bool
	if err := json.Unmarshal(v, &on); err != nil {
		return
	}
	logctl.SetDebug(on)
}
