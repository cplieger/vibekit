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
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/httpreply"
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
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	// Chats and runs degrade INDEPENDENTLY: a workflow-list failure must not
	// blank the chat list, and vice versa. They are separate verbs on the same
	// bridge, so one can fail on its own.
	claimed := h.claimedSessions(r.Context())
	rows, err := h.resumableSessions(r.Context(), claimed)
	if err != nil {
		slog.Warn("session list failed", "error", err)
		rows = []api.ResumableSession{}
	}
	runs, rErr := h.workflowRuns(r.Context(), claimed)
	if rErr != nil {
		slog.Warn("workflow run list failed", "error", rErr)
		runs = []api.WorkflowRun{}
	}
	httpreply.WriteJSON(w, map[string]any{"sessions": rows, "runs": runs})
}

// resumableSessions fetches and filters the workspace's stored sessions.
func (h *Hub) resumableSessions(ctx context.Context, claimed map[string]api.ChatID) ([]api.ResumableSession, error) {
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
	return h.toResumable(claimed, list.Sessions), nil
}

// claimedSessions maps every KAS session a vibekit chat owns to that chat.
//
// Keyed on the whole session CHAIN, not the current id: a chat changes session
// on a failed session/load, a model-switch fallback and empty-turn recovery
// (all via Chat.RecordSession), so the current id alone would leave a chat's
// own retired sessions looking unowned — offered back to the user as separate
// resumable conversations, and leaving a run launched by such a chat
// unattributed.
func (h *Hub) claimedSessions(ctx context.Context) map[string]api.ChatID {
	claimed := map[string]api.ChatID{}
	// Indexed: api.ChatHeader is 304 bytes, which gocritic's rangeValCopy flags.
	headers := h.chatStore.List(ctx)
	for i := range headers {
		for _, sid := range headers[i].SessionChain() {
			claimed[sid] = api.ChatID(headers[i].ID)
		}
	}
	return claimed
}

// toResumable filters the raw rows down to what belongs in a chat picker and
// marks the ones a vibekit chat already owns.
//
// The population is TAB CONVERSATIONS: a row survives only when a vibekit chat
// claims its session. That is the rule, and every other filter here is a
// consequence of it rather than a separate policy:
//
//   - A workflow STEP session is not a chat, and the explicit check stays because
//     it is the cheaper test and it documents the majority case (93 of 399 rows in
//     the measurement).
//   - vibekit's OWN utility session is not a chat either. It reached users as the
//     bug this rule exists to end: the utility bridge's `session/new` is an
//     ordinary session in the workspace cwd, so it sat at the TOP of the History
//     page (newest updatedAt) reading "New Session", permanently, because a live
//     bridge's session is exempt from the orphan sweep too. Clicking it adopted
//     vibekit's own machinery as a chat, whose replay has no user turn, so the
//     page rendered blank and an empty chat was left behind.
//   - A SUBAGENT cannot reach this list at all: KAS models one as a sub-EXECUTION
//     inside its parent session and folds its messages into the parent's stream,
//     so it has no session directory and no index entry.
//
// The chat store is therefore the authority on WHAT is offered, and `session/list`
// on whether KAS can still resume it — a chat whose session KAS no longer holds is
// not restorable, and offering it would be a row that fails on click.
//
// The cost, stated: a session vibekit does not own is no longer adoptable. That
// covers a session started by `kiro-cli` inside the container, and a session whose
// chat was deleted while KAS still held it. Neither is reachable from this UI now,
// and the trade is deliberate — an unclaimed row was indistinguishable from
// vibekit's own machinery, and every instance a user actually met was machinery.
func (h *Hub) toResumable(claimed map[string]api.ChatID, rows []kasSessionRow) []api.ResumableSession {
	out := make([]api.ResumableSession, 0, len(rows))
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
		out = append(out, api.ResumableSession{
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
	slices.SortStableFunc(out, func(a, b api.ResumableSession) int {
		return cmp.Compare(b.UpdatedAt, a.UpdatedAt)
	})
	return out
}

// collapseClaimedByChat keeps ONE row per owning chat, the most recently
// updated.
//
// A row is a door to a chat, and a chat has one identity, so two rows opening the
// same chat is a duplicate by construction — which is exactly what the History
// page showed: the same conversation twice, at two different times. The cause is
// that `claimed` is keyed on the whole session CHAIN (it has to be, or a chat's
// retired sessions read as unowned strangers), and every chat that ever changed
// session has more than one member: a failed session/load, a model-switch
// fallback, or empty-turn recovery. A `/goal` launch used to hit the last of those
// on every send, which is how a one-message-old chat managed to appear twice.
//
// Every row reaching here is claimed (toResumable drops the rest), so there is no
// unclaimed case to exempt: folding on the empty chat id can no longer happen.
//
// Newest wins because UpdatedAt is what the row displays and what the sort
// orders on; it is also the chat's live session in every case that produces a
// chain.
func collapseClaimedByChat(rows []api.ResumableSession) []api.ResumableSession {
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
		if rows[i].UpdatedAt > rows[best].UpdatedAt {
			newestFor[chatID] = i
			drop[best] = true
			continue
		}
		drop[i] = true
	}
	if len(drop) == 0 {
		return rows
	}
	kept := make([]api.ResumableSession, 0, len(rows)-len(drop))
	for i := range rows {
		if !drop[i] {
			kept = append(kept, rows[i])
		}
	}
	return kept
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
// Previous workflow RUNS belong in the same history surface as previous chats,
// but they do NOT come from session/list. That was the trap: session/list's
// workflow-tagged rows are STEP sessions, all 93 of them `type:"step"` across
// only 6 distinct workflowIds — one run's loop alone produced 76 rows
// (`p24-step-parked · tick #17`, `#16`, …). Listing those would put 76 entries
// in the history for a single run, and their `status` is `idle` on every one,
// so a run's real outcome is not even in that data.
//
// _kiro/workflow/list is the run-level inventory: the same workspace that
// yields 93 step rows yields 4 RUNS, each with a run-level status and the
// session that launched it. Both it and _kiro/workflow/inspect work without
// the workflows capability (probed 2026-08-02).

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

// workflowRuns fetches the workspace's workflow runs, newest first.
func (h *Hub) workflowRuns(ctx context.Context, claimed map[string]api.ChatID) ([]api.WorkflowRun, error) {
	u := h.ensureUtility()
	cctx, cancel := context.WithTimeout(ctx, sessionListTimeout)
	defer cancel()

	// workspacePaths is an ARRAY and is required — see methodKiroWorkflowList.
	raw, err := u.session.rawCall(cctx, "workflow list call", methodKiroWorkflowList,
		callerParams(map[string]any{keyWorkspacePaths: []string{h.lifecycle.workDir}}))
	if err != nil {
		return nil, err
	}
	var list kasWorkflowRuns
	if uErr := json.Unmarshal(raw, &list); uErr != nil {
		return nil, uErr
	}
	return h.toWorkflowRuns(claimed, list.Runs), nil
}

// toWorkflowRuns maps the raw run inventory to the wire rows, dropping the ones
// that do not belong in a history list. Split out of workflowRuns for the reason
// toResumable is split out of resumableSessions: the filtering is the part worth
// testing and the RPC is not.
func (h *Hub) toWorkflowRuns(claimed map[string]api.ChatID, runs []kasWorkflowRun) []api.WorkflowRun {
	out := make([]api.WorkflowRun, 0, len(runs))
	for i := range runs {
		r := &runs[i]
		if r.WorkflowID == "" {
			continue
		}
		// Attributed through the launching session's chain, so a run launched by
		// a chat that has since changed session still resolves.
		parentChatID := string(claimed[r.ParentSessionID])
		// PARENTLESS runs only. A run a chat launched is that conversation's
		// work: it already renders in the chat's transcript, its outcome is the
		// agent's to handle, and recovering it is the agent's job — so a second
		// row here duplicates a thing the user reaches by opening the chat, and
		// the run rows a user can actually act on get buried under it. Six of
		// the six runs in the live workspace were agent-launched `/goal` runs
		// off three chats.
		//
		// A manual or scheduled run has no other home: nothing pushes on a
		// finished run (api.PushKind has no member for it), so this page is the
		// only place its outcome is ever read.
		if parentChatID != "" {
			continue
		}
		out = append(out, api.WorkflowRun{
			WorkflowID: r.WorkflowID,
			Name:       r.Name,
			Status:     r.Status,
			CreatedAt:  parseKASTime(r.CreatedAt),
			UpdatedAt:  parseKASTime(r.UpdatedAt),
			StartedAt:  parseKASTime(r.StartedAt),
			// Always empty now, and kept on the wire rather than removed: the
			// client reads it to decide the outcome glyph and the Retry
			// affordance, both of which are "parentless" questions, and a field
			// that says so explicitly beats a client that infers it from the
			// row's presence.
			ParentChatID: parentChatID,
			// Joined from the host, because KAS has no field for it: both run
			// bounds stop a run through the same cancel a person does, so the
			// reason has to come from the side that decided. "" for everything
			// else, including a user cancel. See run_bounds.go.
			EndReason: h.runEndReason(r.WorkflowID),
		})
	}
	// Stable: ties must keep insertion order or the run list reshuffles
	// between polls.
	slices.SortStableFunc(out, func(a, b api.WorkflowRun) int {
		return cmp.Compare(b.UpdatedAt, a.UpdatedAt)
	})
	return out
}
