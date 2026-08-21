package agent

// Hooks state: list the workspace's .kiro/hooks/*.json hooks and flip their
// enabled flag, over the v3 (KAS) _kiro/hooks/* requests. Mirrors the
// REST-backed-by-bridge.Call shape of knowledge.go / spec.go.
//
// TWO verbs, and there used to be three. "Run now" is DELETED, not relocated:
// it was `_kiro/hooks/triggerHook`, whose runCommand path made KAS call back
// `_kiro/hooks/executeHook` and made vibekit run `sh -c` on a command a file
// specifies. That was this app's most security-sensitive path, and the deletion
// is clean because Run-now was its ONLY caller — hook autofire does not use it
// (see below), so hooks keep working exactly as they did.
//
// Bridge-targeting model (hooks are workspace-GLOBAL, like knowledge + specs):
// the store is the on-disk .kiro/hooks/*.json set, shared by every kiro-cli
// process on the same workspace. So these ops route through the long-lived
// UTILITY bridge — always available even with no chat open — which opts into
// KAS's v2 hook engine via _meta.kiro.hooks={enabled,v2} in its initialize
// handshake (ensureUtility sets StartOpts.EnableHooks; see bridge.go). The
// gate is real: without it, _kiro/hooks/list throws "not available when
// v2Hooks is disabled" (verified live).
//
// AUTOFIRE IS UNAFFECTED, and that is what made the deletion safe. Chat bridges
// enable the hook engine (bridge_coord.go sets StartOpts.EnableHooks:true) so the
// workspace's hooks fire during agent turns, and in v2 mode KAS loads the hook
// files and runs runCommand hooks INTERNALLY, in its own process runner in the
// workspace. Verified two ways: on the live v3 wire, chat autofire produced zero
// executeHook callbacks; and mechanically, no chat-bridge dispatcher ever had a
// case for that method, so a callback there would have gone unanswered and wedged
// every turn. vibekit's create_hook command (command/hooks.go) still writes hook
// files directly; this surface manages them.
//
// Wire shapes verified live against kiro-cli acp v3 2.12.0:
//   - list   {workspacePaths?} → {hooks:[{id:"<absPath>#hook-N", name,
//              action:{type:"runCommand",command}|{type:"askAgent",prompt},
//              _meta:{trigger,source,matcher,filePath,enabled,disabledReason?}}]}
//   - setEnabled {hookId, enabled} → {success, code?, error?}; PERSISTS the
//              enabled flag into the .kiro/hooks/*.json file.
//   - didChange (A→C notification) fires after a hook file changes → an
//              hooks_changed SSE (utility_bridge.go forwards it).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/cplieger/pathinside/v2"
	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/webhttp"
)

// hookCallTimeout bounds a list / setEnabled round-trip. The only slow path is
// the first call, which lazily spins up the utility bridge (session/new + auth
// handshake); the read itself is fast. bridge.Call has no timeout of its own.
const hookCallTimeout = 45 * time.Second

// actionRunCommand / actionAskAgent are the two KAS hook action types.
const (
	actionRunCommand = "runCommand"
	actionAskAgent   = "askAgent"
)

// --- KAS wire types (_kiro/hooks/list result) ---

type kasHookAction struct {
	Type    string `json:"type"` // runCommand | askAgent
	Command string `json:"command"`
	Prompt  string `json:"prompt"`
}

type kasHookMeta struct {
	Trigger        string `json:"trigger"`
	Source         string `json:"source"`
	Matcher        string `json:"matcher"`
	FilePath       string `json:"filePath"`
	DisabledReason string `json:"disabledReason"`
	Enabled        bool   `json:"enabled"`
}

type kasHook struct {
	ID     string        `json:"id"`
	Name   string        `json:"name"`
	Action kasHookAction `json:"action"`
	Meta   kasHookMeta   `json:"_meta"`
}

type kasHooksListResult struct {
	Hooks []kasHook `json:"hooks"`
}

// kasHookResult is setEnabled's {success, code?, error?} reply.
type kasHookResult struct {
	Code    string `json:"code"`
	Error   string `json:"error"`
	Success bool   `json:"success"`
}

// --- client-facing types (GET /api/hooks) ---

// hookInfo is one hook in the GET /api/hooks response. ID is a URL-safe opaque
// handle (base64url of the KAS "<absPath>#hook-N" id) the client echoes back to
// the enabled endpoint; the raw KAS id (with its '/' and '#') is not path-safe.
//
// Its READER is the configuration browser's Hooks tab (static-src/docs.ts), which
// joins these rows onto the ones GET /api/workspace/kiro-docs reports for the same
// files. What the tab needs from here is the state that scan cannot know — Enabled,
// DisabledReason — plus Scope and FilePath to decide which affordances a row may
// offer, and ID for the setEnabled call. Command / Prompt / Matcher are
// display-only, and carry a GLOBAL hook's whole row: those files live outside the
// workspace, so the docs scan emits no row for them at all and this is their only
// source. See "the three gates" in docs.ts.
//
// Scope is "workspace" (the hook file lives under the workspace) or "global"
// (kiro-cli 2.13+: $HOME/.kiro/hooks/*.json, loaded in every workspace — the
// wire carries no scope field, so it is derived from _meta.filePath). FilePath
// is workspace-relative for workspace hooks (the client's editor link target);
// for global hooks it is a ~-prefixed DISPLAY path only — the global dir lives
// under the container HOME, which the file-editor surface deny-lists
// (internal/filebrowse sensitive paths), so the client renders it without an
// open affordance.
type hookInfo struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Trigger        string `json:"trigger"`
	ActionType     string `json:"action_type"` // runCommand | askAgent
	Scope          string `json:"scope"`       // workspace | global
	Command        string `json:"command,omitempty"`
	Prompt         string `json:"prompt,omitempty"`
	Matcher        string `json:"matcher,omitempty"`
	FilePath       string `json:"file_path,omitempty"`
	DisabledReason string `json:"disabled_reason,omitempty"`
	Enabled        bool   `json:"enabled"`
}

// Hook scopes for hookInfo.Scope.
const (
	hookScopeWorkspace = "workspace"
	hookScopeGlobal    = "global"
)

type hooksListResponse struct {
	Hooks []hookInfo `json:"hooks"`
}

// encodeHookID / decodeHookID map the KAS hook id (an absolute path with a
// "#hook-N" suffix — not URL-path-safe) to/from a base64url opaque handle used
// in the /api/hooks/{id}/... routes.
func encodeHookID(kasID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(kasID))
}

func decodeHookID(encoded string) (string, error) {
	b, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// hookScopeAndPath classifies a KAS absolute hook filePath and renders its
// client-facing path. Workspace hooks → workspace-relative (the editor link
// target). Anything outside the workspace is a global hook (kiro-cli 2.13:
// $HOME/.kiro/hooks) → a ~-prefixed display path when it resolves under HOME,
// the absolute path otherwise (display only either way; the client renders no
// open affordance for global rows).
//
// Both escape tests use pathinside.RelEscapes rather than a leading-".."
// string prefix: the rel is needed for the display path anyway, and the
// separator-precise rule keeps a hook that genuinely lives under a directory
// whose name merely BEGINS with two dots ("..drafts/build.kiro.hook")
// classified as the workspace hook it is instead of a global one.
func (st *Settings) hookScopeAndPath(abs string) (scope, path string) {
	if abs == "" {
		return hookScopeWorkspace, ""
	}
	if rel, err := filepath.Rel(st.lifecycle.workDir, abs); err == nil && !pathinside.RelEscapes(rel) {
		return hookScopeWorkspace, filepath.ToSlash(rel)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, abs); err == nil && !pathinside.RelEscapes(rel) {
			return hookScopeGlobal, "~/" + filepath.ToSlash(rel)
		}
	}
	return hookScopeGlobal, abs
}

// hooksListRaw ensures the utility bridge and issues _kiro/hooks/list for the
// workspace, returning the parsed KAS hooks. workspacePaths is passed
// explicitly so the entry is loaded even if the session hasn't yet.
func (st *Settings) hooksListRaw(ctx context.Context) ([]kasHook, error) {
	u := st.utility()
	cctx, cancel := context.WithTimeout(ctx, hookCallTimeout)
	defer cancel()
	raw, err := u.session.hooksRaw(cctx, methodKiroHooksList, map[string]any{
		keyWorkspacePaths: []string{st.lifecycle.workDir},
		// includeDisabled is load-bearing, not a nicety. Without it
		// registry.list() filters disabled hooks out of the response, which made
		// the dashboard toggle a ONE-WAY DOOR: writing enabled:false broadcast
		// hooks_changed, the client refetched, and the row it had just toggled
		// was gone from the payload. There was no re-enable path in the UI at
		// all -- the user had to find the file by hand, and the row that
		// vanished was the only pointer to it. It also made a whole tranche of
		// hooks.ts unreachable, since no disabled hook could ever render.
		"includeDisabled": true,
	})
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return []kasHook{}, nil
	}
	var res kasHooksListResult
	if uErr := json.Unmarshal(raw, &res); uErr != nil {
		return nil, uErr
	}
	return res.Hooks, nil
}

// toHookInfo flattens a KAS hook into the client-facing shape.
func (st *Settings) toHookInfo(k *kasHook) hookInfo {
	scope, path := st.hookScopeAndPath(k.Meta.FilePath)
	info := hookInfo{
		ID:             encodeHookID(k.ID),
		Name:           k.Name,
		Trigger:        k.Meta.Trigger,
		ActionType:     k.Action.Type,
		Scope:          scope,
		Matcher:        k.Meta.Matcher,
		FilePath:       path,
		DisabledReason: k.Meta.DisabledReason,
		Enabled:        k.Meta.Enabled,
	}
	switch k.Action.Type {
	case actionRunCommand:
		info.Command = k.Action.Command
	case actionAskAgent:
		info.Prompt = k.Action.Prompt
	}
	return info
}

// --- HTTP handlers (registered by registerHooksRoutes) ---

// handleHooksList: GET /api/hooks → the workspace's hooks (name, trigger,
// action summary, enabled). Read-only.
func (st *Settings) handleHooksList(w http.ResponseWriter, r *http.Request) {
	hooks, err := st.hooksListRaw(r.Context())
	if err != nil {
		writeHookErr(w, err)
		return
	}
	out := make([]hookInfo, 0, len(hooks))
	for i := range hooks {
		out = append(out, st.toHookInfo(&hooks[i]))
	}
	// Workspace hooks before global ones (stable within each group) so the
	// dashboard's canonical order matches the scope grouping on every device.
	slices.SortStableFunc(out, func(a, b hookInfo) int {
		return hookScopeRank(a.Scope) - hookScopeRank(b.Scope)
	})
	webhttp.WriteJSON(w, hooksListResponse{Hooks: out})
}

// hookScopeRank orders hook scopes for the dashboard: workspace first.
func hookScopeRank(scope string) int {
	if scope == hookScopeGlobal {
		return 1
	}
	return 0
}

type hookEnabledReq struct {
	Enabled bool `json:"enabled"`
}

// handleHookSetEnabled: POST /api/hooks/{id}/enabled {enabled} → flip a hook's
// enabled flag (persisted to its .kiro/hooks/*.json file by KAS). Broadcasts
// hooks_changed so every device refetches.
func (st *Settings) handleHookSetEnabled(w http.ResponseWriter, r *http.Request) {
	hookID, ok := hookIDFromPath(w, r)
	if !ok {
		return
	}
	var body hookEnabledReq
	if !httpreply.DecodeJSON(w, r, &body) {
		return
	}
	u := st.utility()
	cctx, cancel := context.WithTimeout(r.Context(), hookCallTimeout)
	defer cancel()
	raw, err := u.session.hooksRaw(cctx, methodKiroHooksSetEnabled, map[string]any{
		"hookId":  hookID,
		"enabled": body.Enabled,
	})
	if err != nil {
		writeHookErr(w, err)
		return
	}
	res := parseHookResult(raw)
	if !res.Success {
		writeHookResultErr(w, res)
		return
	}
	st.broadcastHooksChanged()
	webhttp.Ok(w)
}

// broadcastHooksChanged fans out an hooks_changed SSE (workspace-global, empty
// chatID) so every device refetches GET /api/hooks. Fired after a create /
// toggle and by the utility bridge on a _kiro/hooks/didChange notification.
//
// The didChange half is what keeps a hand-edited hook FILE reaching the UI: KAS
// watches the .kiro/hooks tree itself, so an edit made in the editor (or by the
// agent) arrives here with no polling. Its reader is the configuration browser's
// Hooks tab, which must subscribe to this event and not only to
// `settings_updated` — the docs scan is memoized behind directory mtime AND entry
// names, so an in-place body edit changes neither and that endpoint alone would
// serve a stale trigger forever.
func (st *Settings) broadcastHooksChanged() {
	st.broadcast(context.Background(), vibekit.NewEvent(vibekit.EventHooksChanged, "", vibekit.HooksChangedPayload{}))
}

// hookIDFromPath decodes the base64url {id} path segment into a KAS hook id,
// writing a 400 and returning ok=false on a malformed id.
func hookIDFromPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	hookID, err := decodeHookID(r.PathValue("id"))
	if err != nil || hookID == "" {
		httpreply.BadRequest(w, "invalid hook id")
		return "", false
	}
	return hookID, true
}

// parseHookResult decodes a {success, code?, error?} reply; a nil/empty body is
// treated as failure so a silent success is never assumed.
func parseHookResult(raw json.RawMessage) kasHookResult {
	if len(raw) == 0 {
		return kasHookResult{}
	}
	var res kasHookResult
	if json.Unmarshal(raw, &res) != nil {
		return kasHookResult{}
	}
	return res
}

// writeHookResultErr maps a {success:false, code} reply to an HTTP status.
func writeHookResultErr(w http.ResponseWriter, res kasHookResult) {
	if res.Code == "hook_not_found" {
		httpreply.NotFound(w, "hook not found")
		return
	}
	msg := strings.TrimSpace(res.Error)
	if msg == "" {
		msg = "hook operation failed"
	}
	httpreply.BadRequest(w, msg)
}

// writeHookErr maps a bridge / kiro-cli failure to 502 with a generic message
// (details logged, not leaked). Like knowledge.go there is no errNoLiveBridge
// case: the utility bridge is auto-started, so a failure here is a backend
// fault, not a "open a chat first" condition.
func writeHookErr(w http.ResponseWriter, err error) {
	slog.Warn("hooks op failed", "error", err)
	webhttp.WriteJSONStatus(w, http.StatusBadGateway, httpreply.ErrorJSON("hooks request failed"))
}

// registerHooksRoutes wires the hooks-state endpoints. TWO routes: there is no
// POST /api/hooks/{id}/trigger, and adding one back would restore the executeHook
// shell path along with it.
func (st *Settings) registerHooksRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/hooks", st.handleHooksList)
	mux.HandleFunc("POST /api/hooks/{id}/enabled", st.handleHookSetEnabled)
}
