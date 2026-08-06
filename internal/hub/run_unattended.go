package hub

// The unattended floor: a permission request raised by a SCHEDULED run has
// nobody to answer it, so it is refused on a short budget instead of parking the
// run forever.
//
// Why this exists at all: the one-live-run-per-recipe rule means a parked run
// blocks every later run of the same recipe. So a single unanswered prompt at
// 03:00 does not fail one night, it silently stops the schedule until someone
// notices — which is the failure this floor removes.
//
// Scope is deliberately narrow (user decision): SCHEDULED runs only. A manually
// launched run is attended by definition — the user clicked Run and can answer —
// and an agent-launched run is the agent's own, on its chat's bridge. Neither is
// marked, so neither is ever auto-refused.
//
// DENY by default, with an explicit opt-out. "I approve this while watching" and
// "approve this unattended at 03:00" are different consents, and INHERITING the
// first as the second is how a scheduler quietly acquires privileges nobody
// granted. A `scheduled_auto_approve` setting (Settings → Permissions, off by
// default) is not that inheritance: it is a decision made on purpose about
// unattended work, so it is offered rather than refused. With it off, a workflow
// needing a permission Cedar does not already allow will not complete unattended
// and the row says why; with it on, the budget still applies and the answer at
// the deadline is approve instead of refuse.

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/settings"
)

// unattendedApprovalBudget is how long a scheduled run's permission request
// waits before it is refused.
//
// 180 seconds, copied from KiroCrew's `_BACKGROUND_APPROVAL_TIMEOUT_SECS`
// (src/kiro_crew/dashboard/state.py), whose own comment records the reasoning:
// background sources have no human responder, so waiting the full interactive
// window burns hours on every unattended approval. It is not zero because the
// user may have the run's page open and can still answer.
const unattendedApprovalBudget = 180 * time.Second

// workflowIDOf recovers a workflow id from its synthetic chat id. Empty for a
// chat id that does not name a run.
func workflowIDOf(chatID api.ChatID) string {
	if !isRunChat(chatID) {
		return ""
	}
	return strings.TrimPrefix(string(chatID), runChatPrefix)
}

// markUnattended records that a workflow run was launched by a schedule, keyed
// so a later denial can be attributed back to the row that asked for it.
func (h *Hub) markUnattended(workflowID, scheduleID string) {
	h.unattendedMu.Lock()
	defer h.unattendedMu.Unlock()
	if h.unattendedRuns == nil {
		h.unattendedRuns = map[string]string{}
	}
	h.unattendedRuns[workflowID] = scheduleID
}

// clearUnattended forgets a run, at terminal status or on a failed launch.
func (h *Hub) clearUnattended(workflowID string) {
	if workflowID == "" {
		return
	}
	h.unattendedMu.Lock()
	delete(h.unattendedRuns, workflowID)
	h.unattendedMu.Unlock()
}

// scheduleForRun returns the schedule id that launched a run, and whether the
// run is unattended at all.
func (h *Hub) scheduleForRun(workflowID string) (string, bool) {
	if workflowID == "" {
		return "", false
	}
	h.unattendedMu.Lock()
	defer h.unattendedMu.Unlock()
	id, ok := h.unattendedRuns[workflowID]
	return id, ok
}

// permissionWithUnattendedFloor wraps the ordinary permission handler.
//
// The request still reaches the client exactly as it would otherwise: the run's
// page shows the card, and a user with it open can answer inside the budget.
// What the wrapper adds is a deadline after which vibekit answers for them.
func (h *Hub) permissionWithUnattendedFloor(inner chatHandler) chatHandler {
	return func(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse) {
		inner(ctx, chatID, msg)

		scheduleID, unattended := h.scheduleForRun(workflowIDOf(chatID))
		if !unattended || msg.ID == nil {
			return
		}
		requestID := *msg.ID
		tool := permissionToolName(msg.Params)
		// AfterFunc parks no goroutine while waiting, and the timer is a no-op
		// once the request has been answered: settle() checks the tracker, which
		// the ordinary response path has already cleared.
		params := msg.Params
		time.AfterFunc(unattendedApprovalBudget, func() {
			h.answerUnattendedPermission(chatID, requestID, scheduleID, tool, params)
		})
	}
}

// answerUnattendedPermission settles a still-pending request for an absent
// user: refuse by default, or approve when the operator opted in.
func (h *Hub) answerUnattendedPermission(chatID api.ChatID, requestID int64, scheduleID, tool string, msgParams json.RawMessage) {
	// Still pending? The tracker holds a request until it resolves, so its
	// absence means the user (or a teardown) already answered.
	if !h.sse.pendingPerms.Has(requestID) {
		return
	}
	ctx, cancel := h.hubContext()
	defer cancel()

	sb := h.bridge.mgr.get(chatID)
	if sb == nil {
		return
	}
	approve := scheduledAutoApprove(ctx, h.lifecycle.configDir)
	outcome := api.PermissionOutcomeCancelled()
	verb := "refusing"
	if approve {
		opt := autoApproveOptionID(msgParams)
		if opt == "" {
			// Nothing to select: an allow option is what makes approval
			// expressible, and inventing an id would answer with a choice the
			// request never offered. Fall back to the refusal.
			slog.Warn("unattended auto-approve: request offered no allow option, refusing instead",
				"chat_id", chatID, "tool", tool)
		} else {
			outcome = api.PermissionOutcomeSelected(opt)
			verb = "approving"
		}
	}
	slog.Warn("unattended run: "+verb+" a permission nobody answered",
		"chat_id", chatID, "tool", tool, "budget", unattendedApprovalBudget,
		"auto_approve", approve)
	if err := sb.Respond(ctx, requestID, outcome, nil); err != nil {
		slog.Error("unattended permission answer failed", "chat_id", chatID, "error", err)
		return
	}
	h.sse.pendingPerms.Remove(requestID)
	if verb == "approving" {
		// An approval is not a failure: the run continues, so the schedule's
		// outcome stays whatever the run itself reports.
		return
	}

	// Surface it. Without this the schedule row still reads "started" while the
	// run fails the same way every night, which is exactly the silent-repeat
	// failure this floor exists to make visible.
	if h.schedules == nil || scheduleID == "" {
		return
	}
	reason := "failed: needed approval for " + tool + " with nobody watching — add a permission rule to allow it"
	if tool == "" {
		reason = "failed: needed an approval with nobody watching — add a permission rule to allow it"
	}
	if err := h.schedules.RecordOutcome(ctx, scheduleID, reason); err != nil {
		slog.Warn("could not record the schedule's outcome", "schedule_id", scheduleID, "error", err)
	}
}

// permissionToolName pulls the tool's display name out of a request, for the log
// line and the schedule row. Best-effort: an unnamed request still gets denied.
func permissionToolName(params json.RawMessage) string {
	var p struct {
		ToolCall struct {
			Title string `json:"title"`
			Kind  string `json:"kind"`
		} `json:"toolCall"`
	}
	if json.Unmarshal(params, &p) != nil {
		return ""
	}
	if p.ToolCall.Title != "" {
		return p.ToolCall.Title
	}
	return p.ToolCall.Kind
}

// scheduledAutoApprove reads the opt-out. Absent or unreadable means OFF, so a
// missing settings file can never widen what an unattended run may do.
func scheduledAutoApprove(ctx context.Context, configDir string) bool {
	var b bool
	if !settings.FieldInto(ctx, configDir, settings.KeyScheduledAutoApprove, settings.KeyScheduledAutoApprove, &b) {
		return false
	}
	return b
}

// autoApproveOptionID picks the request's allow-once option.
//
// `allow_always` is deliberately NOT chosen: it would persist a rule from an
// automated answer, turning one unattended decision into a standing grant the
// user never wrote. A one-shot approval expires with the turn.
func autoApproveOptionID(params json.RawMessage) string {
	var p struct {
		Options []struct {
			OptionID string `json:"optionId"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	if json.Unmarshal(params, &p) != nil {
		return ""
	}
	for _, o := range p.Options {
		if o.Kind == "allow_once" {
			return o.OptionID
		}
	}
	return ""
}
