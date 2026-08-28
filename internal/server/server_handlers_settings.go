package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	"github.com/cplieger/webhttp/v2"
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

// handleSettingsGet answers the EFFECTIVE settings: the value in force for every
// preference the client renders, defaults resolved underneath the stored file.
//
// It used to echo the file's bytes verbatim when it could read them and emit
// settings.Default() when it could not, which made the response shape depend on
// the file's state. A stored config.json only ever accumulates keys somebody
// explicitly set, so every untouched key was absent from the response and the
// client had to decide what absence meant — which is how it came to carry copies
// of these defaults, and how the agent-ignore list came to render empty while the
// read filter was applying two patterns.
//
// It FAILS OPEN. An unreadable file logs one Warn and serves the defaults, rather
// than refusing: this is a dev-box container whose operator reshapes /config by
// hand, so a surface that shows defaults and says the file is bad gives them more
// to work with than one that shows nothing. The data is protected by the other
// half of the asymmetry — the write path REFUSES the same file (see
// mergeSettingsPatch), so a user who edits something after this warning gets a
// visible save failure instead of an overwrite that destroys what it could not
// read. Read open, write closed.
//
// Unknown stored keys do not reach the client, which the byte passthrough used to
// allow. Nothing in static-src consumes one, and PATCH merges against the FILE
// rather than against this response, so they survive a round trip untouched.
func handleSettingsGet(w http.ResponseWriter, path string) {
	stored, err := readStoredSettings(path)
	if err != nil {
		slog.Warn("settings: serving defaults, stored config unreadable", "path", path, "error", err)
		webhttp.WriteJSON(w, settings.EffectiveDefaults())
		return
	}
	effective, rejected := settings.EffectiveFrom(stored)
	if len(rejected) > 0 {
		// The key, never the value: a settings file can hold a token somebody pasted
		// into the wrong field, and this line goes to Loki.
		slog.Warn("settings: stored values did not fit their type, defaults applied",
			"path", path, "keys", rejected)
	}
	webhttp.WriteJSON(w, effective)
}

// maxSettingsBytes caps the existing settings file read+merged on PATCH so a
// corrupt or runaway config can't pin memory.
const maxSettingsBytes = 1 << 20 // 1 MiB

// msgSettingsUnreadable is what a settings write answers when it could not read
// what is already stored. It states both halves the user needs: the file could
// not be read, and nothing was written over it — so the preferences they can no
// longer see are still on disk, and the remedy is config.json itself.
const msgSettingsUnreadable = "config.json could not be read; your settings were not overwritten"

// readStoredSettings reads and parses the on-disk settings document. BOTH verbs
// call it: the GET resolves the effective view over the result, and a write merges
// its request over it. An ABSENT file is the one outcome that yields an empty map
// and a nil error: a fresh volume has no config.json, and neither reading nor
// writing one is a failure.
//
// Every other outcome is an error — a stat or read fault, a non-regular file at
// the name, an oversize file, invalid JSON, a top-level null.
//
// The two verbs share these MECHANICS and not the failure POLICY, which is the
// distinction to preserve when editing either caller. A write must REFUSE, because
// its next act is an atomic whole-file write of the merge result and an empty map
// there is indistinguishable from "nothing was stored", so it would replace
// config.json with just the keys in the request and durably destroy the rest. A
// read FAILS OPEN and serves defaults, because showing an operator the values in
// force plus a warning beats showing them nothing, and because the write's
// refusal is what actually protects the file.
//
// OpenRegular, not os.Open: handleSettingsWrite holds s.settingsMu across this
// call, and os.Open on a FIFO blocks in open(2) with no context deadline to
// rescue it, so one FIFO planted at config.json would wedge every settings write
// for the life of the process. It also refuses a symlink at the final component,
// which the read side in internal/settings already refuses, so the two halves of
// this file agree about what may stand in for it. The GET inherits both, which is
// the other half of why it shares this reader: it used to call os.ReadFile with no
// size cap and no type check at all.
func readStoredSettings(path string) (map[string]json.RawMessage, error) {
	// Absolute because OpenRegular requires it. A relative configDir resolved
	// against the process cwd under os.Open and filepath.Abs preserves exactly
	// that, so no deployment's meaning changes.
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	f, info, err := atomicfile.OpenRegular(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]json.RawMessage{}, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if info.Size() > maxSettingsBytes {
		return nil, fmt.Errorf("settings: %s is %d bytes, over the %d-byte cap", abs, info.Size(), maxSettingsBytes)
	}
	data := make([]byte, info.Size())
	if _, err := io.ReadFull(f, data); err != nil {
		return nil, err
	}
	existing := make(map[string]json.RawMessage)
	if err := json.Unmarshal(data, &existing); err != nil {
		return nil, err
	}
	// A stored top-level `null` is the one JSON value that parses into the wrong Go
	// state without an error: json.Unmarshal sets the map to NIL and returns nil,
	// overriding the make above. maps.Copy onto a nil map then panics, so a
	// four-byte config.json used to make every PATCH fail with an opaque 500 (via
	// webhttp.Recoverer). `[]` and `"str"` both error correctly on their own; null
	// is the gap. Refusing it here is also the right answer for the GET, which
	// cannot show a document that says nothing.
	if existing == nil {
		return nil, fmt.Errorf("settings: %s contains a top-level null, not an object", abs)
	}
	return existing, nil
}

// mergeSettingsPatch resolves the request body against the file already on disk.
//
// PATCH merges the incoming keys over the existing file. PUT replaces the file,
// but must not silently wipe server-managed keys written by other flows
// (agent_ignore_files from the Permissions UI, model_effort from the model
// switcher): carry over any managed key the PUT body omits so a full-object PUT
// stays non-destructive of them. Any other method writes the body verbatim.
//
// The caller holds settingsMu across this and the write that follows: this is the
// read half of a read-modify-write.
func mergeSettingsPatch(method, path string, patch map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	switch method {
	case http.MethodPatch:
		existing, err := readStoredSettings(path)
		if err != nil {
			return nil, err
		}
		maps.Copy(existing, patch)
		return existing, nil
	case http.MethodPut:
		existing, err := readStoredSettings(path)
		if err != nil {
			return nil, err
		}
		for _, k := range settings.ServerManagedKeys() {
			if _, inBody := patch[k]; inBody {
				continue
			}
			if v, ok := existing[k]; ok {
				patch[k] = v
			}
		}
	}
	return patch, nil
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
	patch, mErr := mergeSettingsPatch(r.Method, path, patch)
	if mErr != nil {
		httpreply.ServerError(w, msgSettingsUnreadable, mErr)
		return
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
	persisted := lazySettings{path: filepath.Join(s.configDir, settings.Filename)}
	for _, k := range kinds {
		prefs[k.Kind] = k.DefaultOn
		if k.SettingsKey == "" {
			continue // an unconfigurable floor: no key, so nothing to read
		}
		v, ok := patch[k.SettingsKey]
		if !ok {
			if v, ok = persisted.lookup(k.SettingsKey); !ok {
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

// lazySettings reads a settings document at most once, and only when a key is
// actually asked for: the merged document every current syncPushPreferences caller
// passes needs no disk read at all.
//
// A read failure answers absent for every key, leaving the caller's default
// standing — the only answer available once the stored value is unreachable, and
// it is reached only after the write this runs behind already succeeded, so a
// file that cannot be read here is a state no settings write produced.
//
// Single-goroutine: the caller holds settingsMu.
type lazySettings struct {
	doc  map[string]json.RawMessage
	path string
	read bool
}

func (l *lazySettings) lookup(key string) (json.RawMessage, bool) {
	if !l.read {
		l.read = true
		doc, err := readStoredSettings(l.path)
		if err != nil {
			slog.Warn("settings: notification preferences fell back to defaults; config.json could not be read",
				"error", err)
		}
		l.doc = doc
	}
	v, ok := l.doc[key]
	return v, ok
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
