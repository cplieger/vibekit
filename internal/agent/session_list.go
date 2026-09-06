package agent

// Previous-session picker: GET /api/sessions serves the workspace's stored KAS
// sessions through the ACP `session/list` verb, not the `--resume-picker` shell-out.
// Vocabulary trap: that shell-out's `source` is a FORMAT tag ("v3") while ACP's
// `_meta.kiro.source` is a LOCALITY tag ("local").

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

// sessionListTimeout bounds the round-trip. The first call may lazily start the
// utility bridge, so this matches configTemplateTimeout, not a bare read timeout.
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
			// Workflow is present ONLY on a workflow-step session — the discriminator
			// that keeps run machinery out of a chat picker.
			Workflow json.RawMessage `json:"workflow"`
		} `json:"kiro"`
	} `json:"_meta"`
}

// handleSessionList: GET /api/sessions → the workspace's resumable sessions, newest
// first. Degrades to an empty list on every failure: the picker is an affordance, and
// an empty list reads as "nothing to resume" rather than breaking the view.
func (rt *Runtime) handleSessionList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	// Chats and runs degrade INDEPENDENTLY: separate verbs on the same bridge, so a
	// workflow-list failure must not blank the chat list.
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

func (rt *Runtime) resumableSessions(ctx context.Context, claimed map[string]vibekit.ChatID) ([]vibekit.ResumableSession, error) {
	u := rt.utility.get()
	cctx, cancel := context.WithTimeout(ctx, sessionListTimeout)
	defer cancel()

	// cwd scopes the answer to this workspace; unscoped, the call returns every
	// session on the box, which is the "user" scope sessionListScopes also advertises.
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

// claimedSessions maps every KAS session a vibekit chat owns to that chat. Keyed on
// the whole session CHAIN, not the current id: a chat changes session on a failed
// session/load, a model-switch fallback and empty-turn recovery, so its retired
// sessions would otherwise look unowned and be offered as separate conversations.
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

// toResumable keeps a row only when a vibekit chat claims its session, dropping
// workflow-step sessions, vibekit's own utility session and subagent sessions. The
// chat store decides WHAT is offered; session/list decides whether KAS can resume it.
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
	// Stable: ties must keep insertion order or the list reshuffles between polls.
	slices.SortStableFunc(out, func(a, b vibekit.ResumableSession) int {
		return cmp.Compare(b.UpdatedAt, a.UpdatedAt)
	})
	return out
}

// collapseClaimedByChat keeps ONE row per owning chat, the most recently updated; a
// chat with several chain members otherwise produces one row per member. Newest wins
// because UpdatedAt is what the row displays and sorts on; ties: livelierThan.
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

// livelierThan reports whether a should keep its chat's single row over b. An
// UpdatedAt tie is reachable (parseKASTime sinks a bad timestamp to 0), so CreatedAt
// breaks it, and SessionID makes the order total so ties do not reshuffle.
func livelierThan(a, b *vibekit.ResumableSession) bool {
	return cmp.Or(
		cmp.Compare(a.UpdatedAt, b.UpdatedAt),
		cmp.Compare(a.CreatedAt, b.CreatedAt),
		cmp.Compare(a.SessionID, b.SessionID),
	) > 0
}

// parseKASTime converts KAS's RFC3339 timestamps to epoch millis. Zero on absence or
// a parse failure — the caller sorts on it, so a bad value sinks rather than jumps.
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

// --- Workflow runs ---
//
// Runs do NOT come from session/list: its workflow-tagged rows are STEP sessions and
// their status is always idle. _kiro/workflow/list is the run-level inventory.

// kasWorkflowRuns is the _kiro/workflow/list result.
type kasWorkflowRuns struct {
	Runs []kasWorkflowRun `json:"runs"`
}

// kasWorkflowRun is one run as _kiro/workflow/list reports it.
type kasWorkflowRun struct {
	WorkflowID string `json:"workflowId"`
	Name       string `json:"name"`
	// Status is RUN-level, unlike the step sessions' status, which is always idle.
	Status    string `json:"status"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
	StartedAt string `json:"startedAt"`
	// ParentSessionID attributes the run back to the chat that launched it.
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

// toWire maps the raw run inventory to the wire rows, dropping the ones that do not
// belong in a history list. Split out of list because the filtering is the part worth
// testing and the RPC is not.
func (rs *Runs) toWire(claimed map[string]vibekit.ChatID, runs []kasWorkflowRun) []vibekit.WorkflowRun {
	out := make([]vibekit.WorkflowRun, 0, len(runs))
	for i := range runs {
		r := &runs[i]
		if r.WorkflowID == "" {
			continue
		}
		// Attributed through the chain, so a chat that has changed session resolves.
		parentChatID := string(claimed[r.ParentSessionID])
		out = append(out, vibekit.WorkflowRun{
			WorkflowID: r.WorkflowID,
			Name:       r.Name,
			Status:     r.Status,
			CreatedAt:  parseKASTime(r.CreatedAt),
			UpdatedAt:  parseKASTime(r.UpdatedAt),
			StartedAt:  parseKASTime(r.StartedAt),
			// The launching chat, empty for a manual or scheduled run.
			ParentChatID: parentChatID,
			// KAS has no end-reason field, so the deciding host owns it: run_bounds.go.
			EndReason: rs.endReason(r.WorkflowID),
		})
	}
	// Stable: ties must keep insertion order or the list reshuffles between polls.
	slices.SortStableFunc(out, func(a, b vibekit.WorkflowRun) int {
		return cmp.Compare(b.UpdatedAt, a.UpdatedAt)
	})
	return out
}
