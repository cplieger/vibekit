package hub

// Hooks-management dashboard: list / enable-disable / run the workspace's
// .kiro/hooks/*.json hooks over the v3 (KAS) _kiro/hooks/* requests. Mirrors
// the REST-backed-by-bridge.Call shape of knowledge.go / spec.go.
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
// Why the DASHBOARD ops route through the utility bridge (not a chat bridge):
// the reads are workspace-global and must work with no chat open, and the
// utility bridge issues no agent tool calls, so its only _kiro/hooks/executeHook
// is a user-initiated "Run now" trigger (utility_bridge.go gates it on
// expectingHookExec). Chat bridges ALSO enable the hook engine now
// (bridge_coord.go sets StartOpts.EnableHooks:true) so the workspace's hooks
// AUTOFIRE during agent turns — but that is a separate concern from this
// dashboard, and it does NOT flow through executeHook: in v2 mode KAS loads the
// hook files and runs runCommand hooks INTERNALLY (its own process runner, in
// the workspace), verified on the live v3 wire (chat autofire produced zero
// executeHook callbacks). So chat bridges never answer executeHook; only the
// utility bridge does, and only for "Run now". vibekit's create_hook command
// (command/hooks.go) still writes hook files directly; this surface manages them.
//
// executeHook (the "Run now" path) is SECURITY-SENSITIVE — it makes vibekit run
// a shell command the hook file specifies. It is genuinely invoked over the acp
// bridge (verified live), so it is handled: the command is user-authored
// (.kiro/hooks/*.json), sourced server-side from the hook the user clicked "Run
// now" on (never from the client body), and run via runHookCommand with the
// same guards as the `!cmd` shell interception (workDir cwd, timeout, output
// cap, sanitization).
//
// Wire shapes verified live against kiro-cli acp v3 2.12.0:
//   - list   {workspacePaths?} → {hooks:[{id:"<absPath>#hook-N", name,
//              action:{type:"runCommand",command,timeout}|{type:"askAgent",prompt},
//              _meta:{trigger,source,matcher,filePath,enabled,disabledReason?}}]}
//   - setEnabled {hookId, enabled} → {success, code?, error?}; PERSISTS the
//              enabled flag into the .kiro/hooks/*.json file.
//   - triggerHook {sessionId,hookId,hookName,hookActionType,command,approved}
//              → {success,...}; for runCommand, KAS calls back executeHook.
//   - didChange (A→C notification) fires after a hook file changes → an
//              hooks_changed SSE (utility_bridge.go forwards it).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/cplieger/pathinside"
	"github.com/cplieger/vibekit/internal/api"
)

// hookCallTimeout bounds a list / setEnabled round-trip. The only slow path is
// the first call, which lazily spins up the utility bridge (session/new + auth
// handshake); the read itself is fast. bridge.Call has no timeout of its own.
const hookCallTimeout = 45 * time.Second

// hookTriggerTimeout bounds a triggerHook round-trip. For a runCommand hook
// KAS awaits the executeHook callback (which runs the command, itself bounded
// by hookCommandTimeout), so this must comfortably exceed the command cap.
const hookTriggerTimeout = 90 * time.Second

// hookCommandTimeout is the default wall-clock cap for a runCommand hook's
// shell command when the hook file declares no explicit timeout. Matches the
// `!cmd` shell-interception default feel; overridable per-hook (bounded by
// hookMaxCommandTimeout).
const hookCommandTimeout = 60 * time.Second

// hookMaxCommandTimeout caps a per-hook timeout override so a pathological
// hook file can't pin a runner indefinitely.
const hookMaxCommandTimeout = 10 * time.Minute

// hookOutputCap bounds captured stdout+stderr of a runCommand hook run before
// truncation. Modest: the output is surfaced inline in the dashboard.
const hookOutputCap = 64 * 1024

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
	Timeout int    `json:"timeout"`
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

// kasHookResult is the {success, code?, error?} reply shared by setEnabled and
// triggerHook.
type kasHookResult struct {
	Code    string `json:"code"`
	Error   string `json:"error"`
	Success bool   `json:"success"`
}

// --- client-facing types (GET /api/hooks) ---

// hookInfo is one hook in the GET /api/hooks response. ID is a URL-safe opaque
// handle (base64url of the KAS "<absPath>#hook-N" id) the client echoes back to
// the enabled / trigger endpoints; the raw KAS id (with its '/' and '#') is not
// path-safe.
//
// Scope is "workspace" (the hook file lives under the workspace) or "global"
// (kiro-cli 2.13+: $HOME/.kiro/hooks/*.json, loaded in every workspace — the
// wire carries no scope field, so it is derived from _meta.filePath). FilePath
// is workspace-relative for workspace hooks (the client's editor link target);
// for global hooks it is a ~-prefixed DISPLAY path only — the global dir lives
// under the container HOME, which the file-editor surface deny-lists
// (internal/filehandler sensitive paths), so the client renders it without an
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
	Timeout        int    `json:"timeout,omitempty"`
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

// hookRunResult is the outcome of running a runCommand hook's shell command
// (captured by the utility bridge's executeHook responder, surfaced by the
// trigger endpoint). Ran is false when no command actually ran (e.g. the
// trigger was cancelled before executeHook fired).
type hookRunResult struct {
	Output   string
	ExitCode int
	Ran      bool
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
func (h *Hub) hookScopeAndPath(abs string) (scope, path string) {
	if abs == "" {
		return hookScopeWorkspace, ""
	}
	if rel, err := filepath.Rel(h.lifecycle.workDir, abs); err == nil && !pathinside.RelEscapes(rel) {
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
func (h *Hub) hooksListRaw(ctx context.Context) ([]kasHook, error) {
	u := h.ensureUtility()
	cctx, cancel := context.WithTimeout(ctx, hookCallTimeout)
	defer cancel()
	raw, err := u.session.hooksRaw(cctx, methodKiroHooksList, map[string]any{
		keyWorkspacePaths: []string{h.lifecycle.workDir},
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
func (h *Hub) toHookInfo(k *kasHook) hookInfo {
	scope, path := h.hookScopeAndPath(k.Meta.FilePath)
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
		info.Timeout = k.Action.Timeout
	case actionAskAgent:
		info.Prompt = k.Action.Prompt
	}
	return info
}

// --- HTTP handlers (registered by registerHooksRoutes) ---

// handleHooksList: GET /api/hooks → the workspace's hooks (name, trigger,
// action summary, enabled). Read-only.
func (h *Hub) handleHooksList(w http.ResponseWriter, r *http.Request) {
	hooks, err := h.hooksListRaw(r.Context())
	if err != nil {
		h.writeHookErr(w, err)
		return
	}
	out := make([]hookInfo, 0, len(hooks))
	for i := range hooks {
		out = append(out, h.toHookInfo(&hooks[i]))
	}
	// Workspace hooks before global ones (stable within each group) so the
	// dashboard's canonical order matches the scope grouping on every device.
	slices.SortStableFunc(out, func(a, b hookInfo) int {
		return hookScopeRank(a.Scope) - hookScopeRank(b.Scope)
	})
	api.WriteJSON(w, hooksListResponse{Hooks: out})
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
func (h *Hub) handleHookSetEnabled(w http.ResponseWriter, r *http.Request) {
	hookID, ok := hookIDFromPath(w, r)
	if !ok {
		return
	}
	var body hookEnabledReq
	if !api.DecodeJSON(w, r, &body) {
		return
	}
	u := h.ensureUtility()
	cctx, cancel := context.WithTimeout(r.Context(), hookCallTimeout)
	defer cancel()
	raw, err := u.session.hooksRaw(cctx, methodKiroHooksSetEnabled, map[string]any{
		"hookId":  hookID,
		"enabled": body.Enabled,
	})
	if err != nil {
		h.writeHookErr(w, err)
		return
	}
	res := parseHookResult(raw)
	if !res.Success {
		h.writeHookResultErr(w, res)
		return
	}
	h.broadcastHooksChanged()
	api.Ok(w)
}

// hookTriggerResponse is the POST /api/hooks/{id}/trigger reply for a
// runCommand hook: the captured command output + exit code.
type hookTriggerResponse struct {
	Output   string `json:"output"`
	Ran      bool   `json:"ran"`
	ExitCode int    `json:"exit_code"`
}

// handleHookTrigger: POST /api/hooks/{id}/trigger → run a runCommand hook now.
// The hook's command is sourced server-side from the current hook list (never
// from the client) and run via KAS's triggerHook → executeHook callback. Only
// runCommand hooks are supported here — an askAgent hook runs as a prompt in a
// chat (the client sends its prompt through the normal prompt flow), so this
// returns 400 for one.
func (h *Hub) handleHookTrigger(w http.ResponseWriter, r *http.Request) {
	hookID, ok := hookIDFromPath(w, r)
	if !ok {
		return
	}
	hook, found, err := h.findHook(r.Context(), hookID)
	if err != nil {
		h.writeHookErr(w, err)
		return
	}
	if !found {
		api.NotFound(w, "hook not found")
		return
	}
	if hook.Action.Type != actionRunCommand {
		api.BadRequest(w, "only runCommand hooks can be run here; agent hooks run in a chat")
		return
	}
	u := h.ensureUtility()
	cctx, cancel := context.WithTimeout(r.Context(), hookTriggerTimeout)
	defer cancel()
	// approved:true — the user's explicit "Run now" click is the consent, so
	// we skip KAS's extra per-command approval round-trip. The command is the
	// server-sourced hook command, not client input.
	// hook.Action.Timeout is the hook file's own declared cap, already decoded
	// into kasHookAction. Forwarding it is what makes a file-declared timeout
	// mean anything on Run-now; without it every trigger was capped at KAS's
	// 60s default regardless of what the file said.
	run, err := u.session.triggerRunCommandHook(cctx, hook.ID, hook.Name, hook.Action.Command, hook.Action.Timeout)
	if err != nil {
		h.writeHookErr(w, err)
		return
	}
	api.WriteJSON(w, hookTriggerResponse{Output: run.Output, ExitCode: run.ExitCode, Ran: run.Ran})
}

// findHook re-lists the workspace hooks and returns the one whose KAS id
// matches (server-authoritative lookup so the trigger command can't be spoofed
// by the client).
func (h *Hub) findHook(ctx context.Context, kasID string) (kasHook, bool, error) {
	hooks, err := h.hooksListRaw(ctx)
	if err != nil {
		return kasHook{}, false, err
	}
	for i := range hooks {
		if hooks[i].ID == kasID {
			return hooks[i], true, nil
		}
	}
	return kasHook{}, false, nil
}

// runHookCommand runs a runCommand hook's shell command in the workspace with
// the same guards as the `!cmd` shell interception (workDir cwd, bounded
// timeout, capped + sanitized output). Wired onto the utility bridge as its
// executeHook responder (ensureUtility); invoked only for a user-initiated
// "Run now" trigger (utility_bridge.go gates it on expectingHookExec).
func (h *Hub) runHookCommand(ctx context.Context, cmdStr string, timeoutSecs int) hookRunResult {
	cmdStr = strings.TrimSpace(cmdStr)
	if cmdStr == "" {
		return hookRunResult{Ran: true}
	}
	timeout := hookCommandTimeout
	if timeoutSecs > 0 {
		timeout = min(time.Duration(timeoutSecs)*time.Second, hookMaxCommandTimeout)
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// The command is user-authored (.kiro/hooks/*.json), sourced server-side
	// from the hook the user clicked "Run now" on — same trust model as the
	// `!cmd` shell interception (command/shell.go), which likewise runs sh -c.
	proc := exec.CommandContext(cctx, "sh", "-c", cmdStr)
	proc.Dir = h.lifecycle.workDir
	var capped hookCappedBuffer
	proc.Stdout = &capped
	proc.Stderr = &capped
	runErr := proc.Run()

	raw := capped.buf.String()
	exitCode := 0
	if runErr != nil {
		exitCode = 1
		if exitErr := (&exec.ExitError{}); errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			raw += "\n" + runErr.Error()
		}
	}
	if capped.truncated {
		raw += "\n[output truncated]"
	}
	return hookRunResult{Output: api.SanitizeOutput(raw), ExitCode: exitCode, Ran: true}
}

// hookCappedBuffer is a bytes-bounded writer for a hook command's combined
// stdout+stderr (cap hookOutputCap).
type hookCappedBuffer struct {
	buf       strings.Builder
	truncated bool
}

func (b *hookCappedBuffer) Write(p []byte) (int, error) {
	remaining := hookOutputCap - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) <= remaining {
		return b.buf.Write(p)
	}
	b.truncated = true
	_, _ = b.buf.Write(p[:remaining])
	return len(p), nil
}

// broadcastHooksChanged fans out an hooks_changed SSE (workspace-global, empty
// chatID) so every device refetches GET /api/hooks. Fired after a create /
// toggle and by the utility bridge on a _kiro/hooks/didChange notification.
func (h *Hub) broadcastHooksChanged() {
	h.Broadcast(context.Background(), api.NewEvent(api.EventHooksChanged, "", api.HooksChangedPayload{}))
}

// hookIDFromPath decodes the base64url {id} path segment into a KAS hook id,
// writing a 400 and returning ok=false on a malformed id.
func hookIDFromPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	hookID, err := decodeHookID(r.PathValue("id"))
	if err != nil || hookID == "" {
		api.BadRequest(w, "invalid hook id")
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
func (h *Hub) writeHookResultErr(w http.ResponseWriter, res kasHookResult) {
	if res.Code == "hook_not_found" {
		api.NotFound(w, "hook not found")
		return
	}
	msg := strings.TrimSpace(res.Error)
	if msg == "" {
		msg = "hook operation failed"
	}
	api.BadRequest(w, msg)
}

// hookTriggerError turns a triggerHook {success:false} reply into an error
// carrying the KAS message (or a generic fallback).
func hookTriggerError(res kasHookResult) error {
	if msg := strings.TrimSpace(res.Error); msg != "" {
		return errors.New(msg)
	}
	return errors.New("hook trigger failed")
}

// writeHookErr maps a bridge / kiro-cli failure to 502 with a generic message
// (details logged, not leaked). Like knowledge.go there is no errNoLiveBridge
// case: the utility bridge is auto-started, so a failure here is a backend
// fault, not a "open a chat first" condition.
func (h *Hub) writeHookErr(w http.ResponseWriter, err error) {
	slog.Warn("hooks op failed", "error", err)
	api.WriteJSONStatus(w, http.StatusBadGateway, api.ErrorJSON("hooks request failed"))
}

// registerHooksRoutes wires the hooks-management endpoints.
func (h *Hub) registerHooksRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/hooks", h.handleHooksList)
	mux.HandleFunc("POST /api/hooks/{id}/enabled", h.handleHookSetEnabled)
	mux.HandleFunc("POST /api/hooks/{id}/trigger", h.handleHookTrigger)
}
