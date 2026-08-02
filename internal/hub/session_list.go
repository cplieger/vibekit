package hub

// Previous-session picker: GET /api/sessions serves the workspace's stored
// KAS sessions so the UI can offer "load a previous session" the way
// kiro-cli's own picker does.
//
// This is the ADOPTION of kiro-cli's `/chat` capability, with a UI instead of
// an ANSI list. Read out of the v3 TUI source (tui.js, 2.16.0) rather than
// guessed:
//
//	{name:"/chat", description:"Load a previous session, save, or start a new
//	 one", meta:{inputType:"selection", local:true,
//	 subcommands:["new","save","load"]}}
//
// `local:true` marks it TUI-side, and its `save`/`load` subcommands take a
// FILE PATH — they are not the session picker. The picker is
// `--resume-picker`, whose list comes from `Vd()`:
//
//	let a = ["chat","--list-sessions","--format","json"];   // spawns kiro-cli
//
// and whose selection resolves to a sessionId that is then resumed. So the
// native shape is: list the sessions for THIS DIRECTORY, pick one, resume it.
//
// vibekit uses the ACP verb rather than that shell-out, deliberately. The TUI
// spawns a subprocess because at --resume-picker time it has no ACP session
// yet; vibekit has the utility bridge. Measured 2026-08-02, both views agree
// on the same 2 rows for one workspace, and the ACP row is strictly richer:
// the shell-out carries {sessionId, source, title, updatedAt} while
// session/list adds status, createdAt, agentMode, description — and the
// `workflow` marker this file filters on.
//
// One vocabulary trap: `source` means different things in the two views. The
// shell-out's is a FORMAT tag ("v3"); ACP's `_meta.kiro.source` is a LOCALITY
// tag ("local"). Do not treat them as the same field.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/cplieger/vibekit/internal/api"
)

// sessionListTimeout bounds the round-trip. The first call may lazily start
// the utility bridge (session/new plus the auth handshake), so this matches
// configTemplateTimeout rather than a bare read timeout.
const sessionListTimeout = 45 * time.Second

// kasSessionList is the session/list result shape.
type kasSessionList struct {
	Sessions []kasSessionRow `json:"sessions"`
}

// kasSessionRow is one stored session as session/list reports it. Every field
// here was present on all 399 rows of the measurement except the _meta
// entries noted at their declarations.
type kasSessionRow struct {
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
	Title     string `json:"title"`
	UpdatedAt string `json:"updatedAt"`
	Meta      struct {
		Kiro struct {
			CreatedAt string `json:"createdAt"`
			AgentMode string `json:"agentMode"`
			Status    string `json:"status"`
			// Description was present on 88 of 399 rows: the agent's
			// self-declared "what I'm working on" for that session.
			Description string `json:"description"`
			// Workflow is present ONLY on a workflow-step session (93 of 399,
			// co-occurring exactly with modelId and shellType). It is the
			// discriminator that keeps run machinery out of a chat picker.
			//
			// This corrects the design doc, which declined session/list partly
			// because `createdReason` is null on every row and concluded no
			// discriminator existed. createdReason IS still null on all 399 —
			// but this field does the job.
			Workflow json.RawMessage `json:"workflow"`
		} `json:"kiro"`
	} `json:"_meta"`
}

// handleSessionList: GET /api/sessions → the workspace's resumable sessions,
// newest first.
//
// Degrades to an empty list on every failure, matching /api/config-template:
// the picker is an affordance, not a correctness surface, and an empty list
// reads as "nothing to resume" rather than breaking the view.
func (h *Hub) handleSessionList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		api.MethodNotAllowed(w, http.MethodGet)
		return
	}
	rows, err := h.resumableSessions(r.Context())
	if err != nil {
		slog.Warn("session list failed", "error", err)
		api.WriteJSON(w, map[string]any{"sessions": []api.ResumableSession{}})
		return
	}
	api.WriteJSON(w, map[string]any{"sessions": rows})
}

// resumableSessions fetches and filters the workspace's stored sessions.
func (h *Hub) resumableSessions(ctx context.Context) ([]api.ResumableSession, error) {
	u := h.ensureUtility()
	cctx, cancel := context.WithTimeout(ctx, sessionListTimeout)
	defer cancel()

	// cwd scopes the answer to this workspace. Unscoped, the call returns
	// every session on the box: 399 rows across 55 directories in the
	// measurement, against 2 for the workspace. `sessionListScopes` advertises
	// only "workspace", so this is the intended narrowing.
	raw, err := u.session.rawCall(cctx, "session list call", api.MethodSessionList,
		callerParams(map[string]any{"cwd": h.lifecycle.workDir}))
	if err != nil {
		return nil, err
	}
	var list kasSessionList
	if uErr := json.Unmarshal(raw, &list); uErr != nil {
		return nil, uErr
	}
	return h.toResumable(ctx, list.Sessions), nil
}

// toResumable filters the raw rows down to what belongs in a chat picker and
// marks the ones a vibekit chat already owns.
func (h *Hub) toResumable(ctx context.Context, rows []kasSessionRow) []api.ResumableSession {
	// Every session already claimed by a chat record, so the picker can say so
	// instead of offering a duplicate of a chat the user can just open. A chat
	// owns its whole CHAIN, not only its current session (see
	// Chat.SessionChain), which is why this is a set rather than a field read.
	claimed := map[string]api.ChatID{}
	// Indexed: api.ChatHeader is 304 bytes, which gocritic's rangeValCopy flags.
	headers := h.chatStore.List(ctx)
	for i := range headers {
		for _, sid := range headers[i].SessionChain() {
			claimed[sid] = api.ChatID(headers[i].ID)
		}
	}

	out := make([]api.ResumableSession, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		if row.SessionID == "" {
			continue
		}
		// Workflow steps are not chats. They are the majority of the raw list
		// in a workspace that runs them, so an unfiltered picker would bury
		// the user's conversations in run machinery.
		if len(row.Meta.Kiro.Workflow) > 0 {
			continue
		}
		out = append(out, api.ResumableSession{
			SessionID:   row.SessionID,
			Title:       row.Title,
			UpdatedAt:   parseKASTime(row.UpdatedAt),
			CreatedAt:   parseKASTime(row.Meta.Kiro.CreatedAt),
			AgentMode:   row.Meta.Kiro.AgentMode,
			Status:      row.Meta.Kiro.Status,
			Description: row.Meta.Kiro.Description,
			ChatID:      string(claimed[row.SessionID]),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out
}

// parseKASTime converts KAS's RFC3339 timestamps to the epoch millis the rest
// of the wire uses. Zero on absence or a parse failure — the caller sorts on
// it, so a bad value sinks rather than jumping to "now".
func parseKASTime(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}
