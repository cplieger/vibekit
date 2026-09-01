package agent

// The unattended floor: a permission request raised by a SCHEDULED run has
// nobody to answer it, so it is refused on a short budget instead of parking the
// run forever.
//
// Why this exists: the one-live-run-per-recipe rule means a parked run blocks
// every later run of the same recipe, so an unanswered prompt at 03:00 silently
// stops the schedule until someone notices.
//
// Scope is deliberately narrow (user decision): SCHEDULED runs only. A manually
// launched run is attended by definition, and an agent-launched run is the
// agent's own on its chat's bridge. Neither is marked, so neither is ever
// auto-refused.
//
// DENY by default, with an explicit opt-out (`scheduled_auto_approve`,
// Settings → Permissions, off by default): "I approve this while watching" and
// "approve this unattended at 03:00" are different consents, and inheriting the
// first as the second is how a scheduler quietly acquires privileges nobody
// granted.

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/cplieger/runesafe/v2"
	"github.com/cplieger/vibekit/internal/settings"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// unattendedApprovalBudget is how long a scheduled run's permission request
// waits before it is refused.
//
// 180 seconds, copied from KiroCrew's `_BACKGROUND_APPROVAL_TIMEOUT_SECS`:
// background sources have no human responder, so waiting the full interactive
// window burns hours on every unattended approval. Not zero because the user
// may have the run's page open and can still answer.
const unattendedApprovalBudget = 180 * time.Second

// maxToolNameBytes bounds the tool name on its two human surfaces, the log
// attribute and the schedule row's sentence. Deliberately not the permission
// card's 512: this value is CONCATENATED into a one-line sentence an operator
// reads and is persisted into schedules.json on every fire.
const maxToolNameBytes = 128

// approvalTypeTurn is the `_meta.kiro.type` marking a TURN APPROVAL.
const approvalTypeTurn = "turn_approval"

// turnApprovalName is vibekit's own name for a turn approval, which carries no
// tool name and titles itself the literal "Review changes".
const turnApprovalName = "this turn's file changes"

// The two option kinds an unattended answer may select. Both are one-shot: the
// `_always` twins persist a rule, which is what optionIDByKind refuses to reach.
const (
	optionKindAllowOnce  = "allow_once"
	optionKindRejectOnce = "reject_once"
)

// logMsgUnattendedPermission is the message the unattended answer logs under.
//
// A CONSTANT because a homelab Loki rule alerts on it: a scheduled run refused a
// permission is the app's one genuinely unattended failure, and the schedule row
// only tells the user once they look. Changing this string breaks that rule
// silently, so change both together.
const logMsgUnattendedPermission = "unattended permission answered with no user present"

// The two outcomes, as they appear in the log's `outcome` field. A homelab Loki
// rule matches outcomeRefused, so these are values with a consumer, not labels.
const (
	outcomeRefused  = "refused"
	outcomeApproved = "approved"
)

// logMsgRunOverran is the message a run cancelled at its own repeat interval logs
// under. A CONSTANT for the same reason as logMsgUnattendedPermission: a homelab
// Loki rule matches this string, and it is the ONLY signal that a schedule stopped
// producing rather than merely running long. Changing it breaks that rule
// silently, so change both together.
const logMsgRunOverran = "scheduled run still going when its next slot came due; cancelling"

// outcomeOverran is what the schedule's row reads afterwards. Written for the
// person looking at the Workflows tab, not for a matcher: it has to say what
// happened AND what to do, because the row is where they will look first.
const outcomeOverran = "failed: still running when its next slot came due, so it was cancelled — " +
	"give the schedule a longer interval, or make the workflow finish inside it"

// permissionWithUnattendedFloor wraps the ordinary permission handler.
//
// The request still reaches the client exactly as it would otherwise; what the
// wrapper adds is a deadline after which vibekit answers for them.
func (rs *Runs) permissionWithUnattendedFloor(inner chatHandler) chatHandler {
	return func(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
		inner(ctx, chatID, msg)

		// The lease's own mark, which is why the floor now survives a restart.
		// The lookup key strips the `run:` prefix and yields "" for any other
		// chat id, so the floor reaches only a parentless run on its own bridge.
		l, held := rs.lease(workflowIDOf(chatID))
		if !held || !l.Unattended || msg.ID == nil {
			return
		}
		scheduleID := l.ScheduleID
		requestID := *msg.ID
		tool := permissionToolName(msg.Params)
		// AfterFunc parks no goroutine while waiting, and it is a no-op once the
		// request has been answered: answerUnattended claims the request from
		// the tracker, and the ordinary response path has already claimed it.
		params := msg.Params
		time.AfterFunc(unattendedApprovalBudget, func() {
			rs.answerUnattended(chatID, requestID, scheduleID, tool, params)
		})
	}
}

// answerUnattended settles a still-pending request for an absent
// user: refuse by default, or approve when the operator opted in.
func (rs *Runs) answerUnattended(chatID vibekit.ChatID, requestID int64, scheduleID, tool string, msgParams json.RawMessage) {
	ctx, cancel := rs.lifecycle.derivedContext()
	defer cancel()

	sb := rs.bridges.get(chatID)
	if sb == nil {
		return
	}
	approve := scheduledAutoApprove(ctx, rs.lifecycle.configDir)
	// Refuse with the reject option the request ADVERTISED — answer with a
	// choice the request offered, same rule the approve side below follows.
	// Cancelled is the FALL-BACK for a request advertising none.
	outcome := vibekit.PermissionOutcomeCancelled()
	if opt := optionIDByKind(msgParams, optionKindRejectOnce); opt != "" {
		outcome = vibekit.PermissionOutcomeSelected(opt)
	}
	verb := outcomeRefused
	if approve {
		opt := optionIDByKind(msgParams, optionKindAllowOnce)
		if opt == "" {
			// Nothing to select: inventing an id would answer with a choice the
			// request never offered. Fall back to the refusal.
			slog.Warn("unattended auto-approve: request offered no allow option, refusing instead",
				"chat_id", chatID, "tool", tool)
		} else {
			outcome = vibekit.PermissionOutcomeSelected(opt)
			verb = outcomeApproved
		}
	}
	// Claim the request, and give up when the claim fails. This is
	// load-bearing: the floor races a human who has the run's page open, and
	// the budget expires at a moment nobody chose. Before the claim was atomic,
	// both answers went out and kiro-cli kept whichever arrived first, so the
	// user's decision could be overruled by a timer with no trace of it.
	//
	// Taking it also retires the entry, and announces the answer as the
	// MACHINE's: a card collapsing under a reader who was deciding must say
	// that a deadline answered it, and which way.
	if !rs.perms.TakePendingPerm(chatID, requestID, vibekit.SettledByUnattended) {
		return
	}
	// A FIXED message with the outcome as a field, not a message built from the
	// verb: a Loki rule matches on the message, and a concatenated one cannot
	// be matched reliably.
	slog.Warn(logMsgUnattendedPermission,
		"outcome", verb, "chat_id", chatID, "tool", tool,
		"budget", unattendedApprovalBudget, "schedule_id", scheduleID)
	if err := sb.Respond(ctx, requestID, outcome, nil); err != nil {
		slog.Error("unattended permission answer failed", "chat_id", chatID, "error", err)
		return
	}
	if verb == outcomeApproved {
		// An approval is not a failure: the run continues, so the schedule's
		// outcome stays whatever the run itself reports.
		return
	}

	// Surface it. Without this the schedule row still reads "started" while the
	// run fails the same way every night, which is exactly the silent-repeat
	// failure this floor exists to make visible.
	if rs.schedules == nil || scheduleID == "" {
		return
	}
	reason := "failed: needed approval for " + tool + " with nobody watching — add a permission rule to allow it"
	if tool == "" {
		reason = "failed: needed an approval with nobody watching — add a permission rule to allow it"
	}
	if err := rs.schedules.RecordOutcome(ctx, scheduleID, reason); err != nil {
		slog.Warn("could not record the schedule's outcome", "schedule_id", scheduleID, "error", err)
	}
}

// permissionToolName names what a request is asking about, for the log line and
// the schedule row. Best-effort: an unnamed request still gets denied.
//
// Machine-authored names first, the model's prose last. A PRECEDENCE rather
// than a gate on toolId: only the ordinary tool approval carries one, three of
// the backend's six ask kinds carry no `_meta` at all, and a hook approval
// carries a different shape.
func permissionToolName(params json.RawMessage) string {
	var p struct {
		ToolCall struct {
			Title string `json:"title"`
			Kind  string `json:"kind"`
		} `json:"toolCall"`
		Meta struct {
			// Decoded here rather than through translate.ACPPermissionKiroBlock:
			// that block carries the turn-approval discriminator and the file
			// list, not these two names.
			Kiro struct {
				// ToolID is KAS's own tool id (`execute_bash`, `fs_write`) and is
				// unconditional on an ordinary tool approval.
				ToolID string `json:"toolId"`
				// HookName comes from a hook file on disk, so it is
				// user-authored rather than the model's.
				HookName string `json:"hookName"`
				Type     string `json:"type"`
			} `json:"kiro"`
		} `json:"_meta"`
	}
	if json.Unmarshal(params, &p) != nil {
		return ""
	}
	kiro := p.Meta.Kiro
	switch {
	case kiro.ToolID != "":
		return safeToolName(kiro.ToolID)
	case kiro.HookName != "":
		return safeToolName(kiro.HookName)
	case kiro.Type == approvalTypeTurn:
		return turnApprovalName
	}
	if p.ToolCall.Title != "" {
		return safeToolName(p.ToolCall.Title)
	}
	return safeToolName(p.ToolCall.Kind)
}

// safeToolName defuses one wire-supplied name for a single-line human surface.
//
// The title is composed upstream by the MODEL, so an agent that read a poisoned
// file can reach both the log line and the schedule row through it. The
// sanitizer replaces rather than deletes, so a legitimate name is byte-identical
// and a bidi reversal becomes visible whitespace.
func safeToolName(s string) string {
	return runesafe.SanitizeSingleLineBounded(s, maxToolNameBytes)
}

// scheduledAutoApprove reads the opt-out. Absent or unreadable means OFF, so a
// missing settings file can never widen what an unattended run may do.
func scheduledAutoApprove(ctx context.Context, configDir string) bool {
	var b bool
	if !settings.FieldInto(ctx, configDir, settings.KeyScheduledAutoApprove, &b) {
		return false
	}
	return b
}

// optionIDByKind picks the request's advertised option of one EXACT kind.
//
// Exact, never a prefix: `allow_always`/`reject_always` are advertised whenever
// consent is persistable, and selecting either makes the backend PERSIST a
// rule — turning one automated answer into a standing grant nobody wrote. A
// one-shot answer expires with the turn.
func optionIDByKind(params json.RawMessage, kind string) string {
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
		if o.Kind == kind {
			return o.OptionID
		}
	}
	return ""
}
