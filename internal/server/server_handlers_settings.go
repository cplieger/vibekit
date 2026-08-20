package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/cplieger/atomicfile/v3"
	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/logctl"
	"github.com/cplieger/vibekit/internal/push"
	"github.com/cplieger/vibekit/internal/settings"
	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/webhttp"
)

func (s *Server) handleSteering(w http.ResponseWriter, r *http.Request) {
	path := s.steering.CustomPath()
	switch r.Method {
	case http.MethodGet:
		handleSteeringGet(w, path)
	case http.MethodPut:
		handleSteeringPut(w, r, path)
	default:
		httpreply.MethodNotAllowed(w, http.MethodGet, http.MethodPut)
	}
}

func handleSteeringGet(w http.ResponseWriter, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		webhttp.WriteJSON(w, map[string]string{"content": ""})
		return
	}
	webhttp.WriteJSON(w, map[string]string{"content": string(data)})
}

func handleSteeringPut(w http.ResponseWriter, r *http.Request, path string) {
	webhttp.LimitBody(w, r, webhttp.MaxJSONBody)
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpreply.BadRequest(w, "bad request")
		return
	}
	// Empty content (whitespace only) means "no custom instructions":
	// remove the file instead of writing an empty one. Otherwise
	// kiro-cli would include the empty file on every agent load.
	if strings.TrimSpace(body.Content) == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			httpreply.InternalError(w, err)
			return
		}
		webhttp.Ok(w)
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
		httpreply.InternalError(w, err)
		return
	}
	if !res.Durable {
		slog.Warn("steering: saved but parent-dir fsync unconfirmed; not guaranteed durable across an immediate crash",
			"path", path)
	}
	webhttp.Ok(w)
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	path := filepath.Join(s.configDir, settings.Filename)
	switch r.Method {
	case http.MethodGet:
		handleSettingsGet(w, path)
	case http.MethodPut, http.MethodPatch:
		s.handleSettingsWrite(w, r, path)
	default:
		httpreply.MethodNotAllowed(w, http.MethodGet, http.MethodPut, http.MethodPatch)
	}
}

func handleSettingsGet(w http.ResponseWriter, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		webhttp.WriteJSON(w, settings.Default())
		return
	}
	w.Header().Set("Content-Type", httpreply.MIMETypeJSON)
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
	webhttp.LimitBody(w, r, webhttp.MaxJSONBody)
	var patch map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		httpreply.BadRequest(w, "invalid json")
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
		httpreply.BadRequest(w, "invalid json")
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
		httpreply.InternalError(w, wErr)
		return
	}
	if !res.Durable {
		slog.Warn("settings: saved but parent-dir fsync unconfirmed; not guaranteed durable across an immediate crash",
			"path", path)
	}
	webhttp.Ok(w)
	s.agent.Broadcast(r.Context(), vibekit.NewEvent(vibekit.EventSettingsUpdated, "", vibekit.SettingsUpdatedPayload{}))
	s.syncPushPreferences(patch)
	syncDebugLogs(patch)
}

// syncPushPreferences reads notification preference toggles from the settings
// patch and forwards them to the push service.
//
// It DERIVES the kind set from push.Kinds() rather than naming the kinds here.
// This used to be a hand-written map with both kinds spelled out and a single
// `if` reading one key, which made it a THIRD copy of the kind set beside
// vibekit.pushKinds and push.kindRegistry — so a newly added kind's toggle persisted
// to config.json and then never reached the running service until the next SSE
// reconnect happened to call ReloadPreferences.
//
// A key the patch does not carry is resolved against the PERSISTED settings
// before it falls back to the registry default, which is the same order
// push.loadPreferences reads (disk, else default) with the patch as the freshest
// layer on top. Seeding the default for an absent key and overriding only on a
// hit is correct only while the argument happens to be the whole merged document:
// on a genuinely sparse map it re-enables every kind the caller did not mention,
// which for a preference the user turned OFF is the one direction that must never
// happen silently. The function owns that resolution rather than trusting its
// caller's shape.
//
// PushKindPermission has no branch that can lower it: its registry entry declares
// no settings key, so neither lookup below can find a value to read for it and
// this write path cannot silence a turn-blocking ask however the body was
// assembled. See the "no notify_permission key" note in
// internal/settings/defaults.go.
func (s *Server) syncPushPreferences(patch map[string]json.RawMessage) {
	kinds := push.Kinds()
	prefs := make(map[vibekit.PushKind]bool, len(kinds))
	// Read at most once, and only when a key is actually missing: the merged
	// document every current caller passes needs no disk read at all.
	var persisted map[string]json.RawMessage
	for _, k := range kinds {
		prefs[k.Kind] = k.DefaultOn
		if k.SettingsKey == "" {
			continue // an unconfigurable floor: no key, so nothing to read
		}
		v, ok := patch[k.SettingsKey]
		if !ok {
			if persisted == nil {
				// readExistingSettings never returns nil, so the nil check above
				// distinguishes "not read yet" from "read and empty".
				persisted = readExistingSettings(filepath.Join(s.configDir, settings.Filename))
			}
			if v, ok = persisted[k.SettingsKey]; !ok {
				continue
			}
		}
		var on bool
		if json.Unmarshal(v, &on) == nil {
			prefs[k.Kind] = on
		}
	}
	s.push.SetPreferences(prefs)
}

// syncDebugLogs flips the process-wide slog level when the user
// toggles the Debug logs setting.
func syncDebugLogs(patch map[string]json.RawMessage) {
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
