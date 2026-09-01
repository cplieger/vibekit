package agent

// Hooks state: list the workspace's .kiro/hooks/*.json hooks and flip their
// enabled flag, over the v3 (KAS) _kiro/hooks/* requests.
//
// "Run now" is DELETED, not relocated: it was `_kiro/hooks/triggerHook`,
// whose runCommand path made KAS call back `_kiro/hooks/executeHook` and
// made vibekit run `sh -c` on a command a file specifies — this app's most
// security-sensitive path. The deletion is clean because Run-now was its
// ONLY caller; hook autofire does not use it.
//
// Hooks are workspace-GLOBAL, so these ops route through the long-lived
// UTILITY bridge, which opts into KAS's v2 hook engine via
// _meta.kiro.hooks={enabled,v2} in its initialize handshake. Without that
// gate _kiro/hooks/list throws "not available when v2Hooks is disabled".
//
// Autofire is unaffected: chat bridges enable the same hook engine, and in
// v2 mode KAS loads the hook files and runs runCommand hooks internally in
// its own process runner — verified live (chat autofire produces zero
// executeHook callbacks) and mechanically (no chat-bridge dispatcher has a
// case for that method). vibekit's create_hook command still writes hook
// files directly; this surface manages them.

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
	"github.com/cplieger/webhttp/v2"
)

// hookCallTimeout bounds a list / setEnabled round-trip. The only slow path
// is the first call, lazily spinning up the utility bridge.
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

// hookInfo is one hook in the GET /api/hooks response. ID is a URL-safe
// opaque handle (base64url of the KAS id) the client echoes back to the
// enabled endpoint.
//
// Scope is "workspace" or "global" (kiro-cli 2.13+: $HOME/.kiro/hooks/*.json,
// loaded in every workspace — derived from _meta.filePath, since the wire
// carries no scope field). FilePath is workspace-relative for workspace
// hooks; for global hooks it is a ~-prefixed display path only — the
// filebrowse deny-list blocks that tree, so the client renders no open
// affordance for global rows.
type hookInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Trigger    string `json:"trigger"`
	ActionType string `json:"action_type"` // runCommand | askAgent
	Scope      string `json:"scope"`       // workspace | global
	Command    string `json:"command,omitempty"`
	Prompt     string `json:"prompt,omitempty"`
	Matcher    string `json:"matcher,omitempty"`
	// MatcherWarning is DERIVED: what is wrong with this hook's
	// trigger-and-matcher pairing, or empty when nothing is —
	// `missing_tool_matcher` for a PreToolUse/PostToolUse hook with no
	// matcher, `ineffective` for a matcher on a trigger with nothing to
	// match on. Computed server-side (vibekit.ClassifyHookMatcher) so the
	// trigger-to-subject table lives once.
	//
	// `ineffective` overlaps create_hook's own refusal of that pairing
	// deliberately: the refusal covers what vibekit writes, this field
	// covers what it READS — a hand-written or copied-in hook file.
	MatcherWarning string `json:"matcher_warning,omitempty"`
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
// client-facing path. Workspace hooks → workspace-relative; anything
// outside the workspace is a global hook (kiro-cli 2.13:
// $HOME/.kiro/hooks) → a ~-prefixed display path when it resolves under
// HOME, absolute otherwise. Display only either way.
//
// Uses pathinside.RelEscapes rather than a leading-".." prefix check: the
// separator-precise rule correctly classifies a hook under a directory
// whose name merely BEGINS with two dots ("..drafts/build.kiro.hook") as
// workspace rather than global.
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

// hooksListRaw ensures the utility bridge and issues _kiro/hooks/list for
// the workspace. workspacePaths is passed explicitly so the entry loads
// even if the session hasn't yet.
func (st *Settings) hooksListRaw(ctx context.Context) ([]kasHook, error) {
	u := st.utility()
	cctx, cancel := context.WithTimeout(ctx, hookCallTimeout)
	defer cancel()
	raw, err := u.session.hooksRaw(cctx, methodKiroHooksList, map[string]any{
		keyWorkspacePaths: []string{st.lifecycle.workDir},
		// includeDisabled is load-bearing: without it registry.list()
		// filters disabled hooks out of the response, which made the
		// dashboard toggle a ONE-WAY DOOR — writing enabled:false made the
		// row disappear with no way to re-enable it from the UI.
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
		MatcherWarning: string(vibekit.ClassifyHookMatcher(k.Meta.Trigger, k.Meta.Matcher)),
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

// broadcastHooksChanged fans out hooks_changed (workspace-global) so every
// device refetches GET /api/hooks. Fired after a create/toggle and by the
// utility bridge on a _kiro/hooks/didChange notification — the didChange
// half is what keeps a hand-edited hook FILE reaching the UI, since the
// docs scan's own memoization would otherwise serve a stale trigger.
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

// registerHooksRoutes wires the hooks-state endpoints. No POST
// /api/hooks/{id}/trigger — adding one back would restore the executeHook
// shell path along with it.
func (st *Settings) registerHooksRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/hooks", st.handleHooksList)
	mux.HandleFunc("POST /api/hooks/{id}/enabled", st.handleHookSetEnabled)
}
