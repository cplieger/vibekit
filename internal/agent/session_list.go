package agent

// Previous-session picker: GET /api/sessions serves the workspace's stored
// KAS sessions so the UI can offer "load a previous session" the way
// kiro-cli's own picker does — the ACP `session/list` verb, not the
// `--resume-picker` shell-out, since vibekit already has the utility bridge.
//
// Vocabulary trap: `source` means different things in the two views. The
// shell-out's is a FORMAT tag ("v3"); ACP's `_meta.kiro.source` is a LOCALITY
// tag ("local"). Do not treat them as the same field.

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/webhttp/v2"
)

// sessionListTimeout bounds the round-trip. The first call may lazily start
// the utility bridge (session/new plus the auth handshake), so this matches
// configTemplateTimeout rather than a bare read timeout.
const sessionListTimeout = 45 * time.Second

// kasSessionList is the session/list result shape.
type kasSessionList struct {
	Sessions []kasSessionRow `json:"sessions"`
}

// kasSessionRow is one stored session as session/list reports it.
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
			// Description is the agent's self-declared "what I'm working on".
			Description string `json:"description"`
			// Workflow is present ONLY on a workflow-step session — the
			// discriminator that keeps run machinery out of a chat picker.
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
func (rt *Runtime) handleSessionList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	// Chats and runs degrade INDEPENDENTLY: a workflow-list failure must not
	// blank the chat list, and vice versa. They are separate verbs on the same
	// bridge, so one can fail on its own.
	claimed := rt.claimedSessions(r.Context())
	rows, err := rt.resumableSessions(r.Context(), claimed)
	if err != nil {
		slog.Warn("session list failed", "error", err)
		rows = []vibekit.ResumableSession{}
	}
	runs, rErr := rt.runs.list(r.Context(), claimed)
	if rErr != nil {
		slog.Warn("workflow run list failed", "error", rErr)
		runs = []vibekit.WorkflowRun{}
	}
	webhttp.WriteJSON(w, map[string]any{"sessions": rows, "runs": runs})
}

// resumableSessions fetches and filters the workspace's stored sessions.
func (rt *Runtime) resumableSessions(ctx context.Context, claimed map[string]vibekit.ChatID) ([]vibekit.ResumableSession, error) {
	u := rt.utility.get()
	cctx, cancel := context.WithTimeout(ctx, sessionListTimeout)
	defer cancel()

	// cwd scopes the answer to this workspace. Unscoped, the call returns
	// every session on the box: 399 rows across 55 directories in the
	// measurement, against 2 for the workspace. `sessionListScopes` advertises
	// ["workspace", "user"], and "user" IS that unscoped answer, so this is the
	// intended narrowing rather than the only scope offered.
	raw, err := u.session.rawCall(cctx, "session list call", vibekit.MethodSessionList,
		callerParams(map[string]any{"cwd": rt.lifecycle.workDir}))
	if err != nil {
		return nil, err
	}
	var list kasSessionList
	if uErr := json.Unmarshal(raw, &list); uErr != nil {
		return nil, uErr
	}
	return toResumable(claimed, list.Sessions), nil
}

// claimedSessions maps every KAS session a vibekit chat owns to that chat.
//
// Keyed on the whole session CHAIN, not the current id: a chat changes session
// on a failed session/load, a model-switch fallback and empty-turn recovery
// (all via Chat.RecordSession), so the current id alone would leave a chat's
// own retired sessions looking unowned — offered back to the user as separate
// resumable conversations, and leaving a run launched by such a chat
// unattributed.
func (rt *Runtime) claimedSessions(ctx context.Context) map[string]vibekit.ChatID {
	claimed := map[string]vibekit.ChatID{}
	// Indexed: vibekit.ChatHeader is 304 bytes, which gocritic's rangeValCopy flags.
	headers := rt.chatStore.List(ctx)
	for i := range headers {
		for _, sid := range headers[i].SessionChain() {
			claimed[sid] = vibekit.ChatID(headers[i].ID)
		}
	}
	return claimed
}

// toResumable filters the raw rows down to what belongs in a chat picker: a
// row survives only when a vibekit chat claims its session. This drops
// workflow-step sessions, vibekit's own utility session (which otherwise sits
// at the top of History as "New Session" forever), and subagent sessions
// (KAS folds those into their parent's stream, so they have no row at all).
// The chat store decides WHAT is offered; session/list decides whether KAS
// can still resume it, so an unresumable chat is never offered.
func toResumable(claimed map[string]vibekit.ChatID, rows []kasSessionRow) []vibekit.ResumableSession {
	out := make([]vibekit.ResumableSession, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		if row.SessionID == "" {
			continue
		}
		if len(row.Meta.Kiro.Workflow) > 0 {
			continue
		}
		chatID := claimed[row.SessionID]
		if chatID == "" {
			continue
		}
		out = append(out, vibekit.ResumableSession{
			SessionID:   row.SessionID,
			Title:       row.Title,
			UpdatedAt:   parseKASTime(row.UpdatedAt),
			CreatedAt:   parseKASTime(row.Meta.Kiro.CreatedAt),
			AgentMode:   row.Meta.Kiro.AgentMode,
			Status:      row.Meta.Kiro.Status,
			Description: row.Meta.Kiro.Description,
			ChatID:      string(chatID),
		})
	}
	out = collapseClaimedByChat(out)
	// Stable: ties must keep insertion order or the session list reshuffles
	// between polls.
	slices.SortStableFunc(out, func(a, b vibekit.ResumableSession) int {
		return cmp.Compare(b.UpdatedAt, a.UpdatedAt)
	})
	return out
}

// collapseClaimedByChat keeps ONE row per owning chat, the most recently
// updated. A chat with more than one chain member (a failed session/load, a
// model-switch fallback, empty-turn recovery) otherwise produces one row per
// member. Newest wins because UpdatedAt is what the row displays and sorts
// on; tie-break rule: livelierThan.
func collapseClaimedByChat(rows []vibekit.ResumableSession) []vibekit.ResumableSession {
	newestFor := map[string]int{}
	drop := map[int]bool{}
	for i := range rows {
		chatID := rows[i].ChatID
		if chatID == "" {
			continue
		}
		best, seen := newestFor[chatID]
		if !seen {
			newestFor[chatID] = i
			continue
		}
		if livelierThan(&rows[i], &rows[best]) {
			newestFor[chatID] = i
			drop[best] = true
			continue
		}
		drop[i] = true
	}
	if len(drop) == 0 {
		return rows
	}
	kept := make([]vibekit.ResumableSession, 0, len(rows)-len(drop))
	for i := range rows {
		if !drop[i] {
			kept = append(kept, rows[i])
		}
	}
	return kept
}

// livelierThan reports whether a should keep its chat's single row over b,
// for two rows opening the SAME chat. Key: (UpdatedAt, CreatedAt, SessionID),
// all descending. An UpdatedAt tie is reachable (parseKASTime sinks a bad
// timestamp to 0), so CreatedAt breaks it toward the later-created session —
// a chain is produced by retiring a session for a fresh one. SessionID makes
// the order total so ties do not reshuffle between polls.
func livelierThan(a, b *vibekit.ResumableSession) bool {
	return cmp.Or(
		cmp.Compare(a.UpdatedAt, b.UpdatedAt),
		cmp.Compare(a.CreatedAt, b.CreatedAt),
		cmp.Compare(a.SessionID, b.SessionID),
	) > 0
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

// --- Workflow runs -------------------------------------------------------
//
// Runs do NOT come from session/list: its workflow-tagged rows are STEP
// sessions (many per run, one per iteration), and their status is always
// idle regardless of the run's real outcome. _kiro/workflow/list is the
// run-level inventory instead, and works without the workflows capability.

// kasWorkflowRuns is the _kiro/workflow/list result.
type kasWorkflowRuns struct {
	Runs []kasWorkflowRun `json:"runs"`
}

// kasWorkflowRun is one run as _kiro/workflow/list reports it.
type kasWorkflowRun struct {
	WorkflowID string `json:"workflowId"`
	Name       string `json:"name"`
	// Status is RUN-level (e.g. paused / completed / failed), unlike the step
	// sessions' status, which is idle regardless of what the run did.
	Status    string `json:"status"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
	StartedAt string `json:"startedAt"`
	// ParentSessionID is the session that launched the run, which is how a run
	// is attributed back to the chat that started it.
	ParentSessionID string `json:"parentSessionId"`
}

// list fetches the workspace's workflow runs, newest first.
func (rs *Runs) list(ctx context.Context, claimed map[string]vibekit.ChatID) ([]vibekit.WorkflowRun, error) {
	u := rs.utility()
	cctx, cancel := context.WithTimeout(ctx, sessionListTimeout)
	defer cancel()

	// workspacePaths is an ARRAY and is required — see methodKiroWorkflowList.
	raw, err := u.session.rawCall(cctx, "workflow list call", methodKiroWorkflowList,
		callerParams(map[string]any{keyWorkspacePaths: []string{rs.lifecycle.workDir}}))
	if err != nil {
		return nil, err
	}
	var list kasWorkflowRuns
	if uErr := json.Unmarshal(raw, &list); uErr != nil {
		return nil, uErr
	}
	return rs.toWire(claimed, list.Runs), nil
}

// toWire maps the raw run inventory to the wire rows, dropping the ones
// that do not belong in a history list. Split out of list for the reason
// toResumable is split out of resumableSessions: the filtering is the part worth
// testing and the RPC is not.
func (rs *Runs) toWire(claimed map[string]vibekit.ChatID, runs []kasWorkflowRun) []vibekit.WorkflowRun {
	out := make([]vibekit.WorkflowRun, 0, len(runs))
	for i := range runs {
		r := &runs[i]
		if r.WorkflowID == "" {
			continue
		}
		// Attributed through the launching session's chain, so a run launched by
		// a chat that has since changed session still resolves.
		parentChatID := string(claimed[r.ParentSessionID])
		// PARENTLESS runs only: a chat-launched run already renders in that
		// chat's transcript, and a manual/scheduled run has no other home.
		if parentChatID != "" {
			continue
		}
		out = append(out, vibekit.WorkflowRun{
			WorkflowID: r.WorkflowID,
			Name:       r.Name,
			Status:     r.Status,
			CreatedAt:  parseKASTime(r.CreatedAt),
			UpdatedAt:  parseKASTime(r.UpdatedAt),
			StartedAt:  parseKASTime(r.StartedAt),
			// Always empty here (parentless-only), kept explicit on the wire
			// rather than inferred from row presence: the client reads it for
			// the outcome glyph and the Retry affordance.
			ParentChatID: parentChatID,
			// KAS has no end-reason field; both run bounds stop a run via the
			// same cancel a person does, so the reason must come from the
			// host that decided. See run_bounds.go.
			EndReason: rs.endReason(r.WorkflowID),
		})
	}
	// Stable: ties must keep insertion order or the run list reshuffles
	// between polls.
	slices.SortStableFunc(out, func(a, b vibekit.WorkflowRun) int {
		return cmp.Compare(b.UpdatedAt, a.UpdatedAt)
	})
	return out
}
