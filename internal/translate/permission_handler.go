package translate

import (
	"context"
	"log/slog"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// permOptionWire decodes one inbound permission option. ACP sends the id
// as camelCase `optionId`; the SSE-facing vibekit.PermissionOption tags it
// `option_id`, and Go's case-insensitive match does not bridge the
// underscore — so we decode from this wire struct and map to the SSE type.
// approvalTypeTurn is the `_meta.kiro.type` marking a turn approval.
const approvalTypeTurn = "turn_approval"

type permOptionWire struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// HandlePermissionRequest processes session/request_permission from kiro-cli.
//
// The v3 params object is FLAT — {sessionId, toolCall{...}, options[]} —
// and the JSON-RPC correlation id is on the envelope (msg.ID), NOT inside
// params. unmarshalParams decodes msg.Params directly, so the decode struct
// must match those fields at top level; a `params`-wrapped struct (or an
// `id` field read from params) decodes to all-zero, yielding an empty dialog
// and request_id=0 (the outcome would then be answered on id 0, wedging the
// tool call and disabling the shell auto-policy). Mirror HandleElicitationCreate,
// which decodes flat and reads *msg.ID.
func (t *Translator) HandlePermissionRequest(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) {
	if msg.ID == nil {
		// Without an id we cannot route the outcome back to the agent, so
		// drop rather than show a dialog whose answer can never arrive.
		slog.Warn("permission request missing id", "chat_id", chatID)
		return
	}
	// Field order is fieldalignment's rather than the wire's: Meta leads because
	// its Consent block carries the struct's only pointer, and the slice trails
	// because its len/cap are two scalars the GC would otherwise have to scan
	// past to reach a later pointer.
	type permReq struct {
		// Meta carries the turn-approval discriminator, its file list, and
		// 2.19.1's persistability verdict. Named block rather than an inline
		// struct — see ACPPermissionMeta for why this frame in particular earns
		// one.
		Meta      ACPPermissionMeta `json:"_meta"`
		SessionID string            `json:"sessionId"`
		ToolCall  struct {
			ToolCallID string           `json:"toolCallId"`
			Title      string           `json:"title"`
			Kind       vibekit.ToolKind `json:"kind"`
		} `json:"toolCall"`
		Options []permOptionWire `json:"options"`
	}
	req, ok := unmarshalParams[permReq](msg, "session/request_permission")
	if !ok {
		return
	}
	reqID := *msg.ID

	subSessionID := t.deriveSubSession(chatID, req.SessionID)

	options := make([]vibekit.PermissionOption, len(req.Options))
	for i, o := range req.Options {
		// Name is the text ON the button the user clicks, so it is as much a
		// decision surface as the title: a card whose title is safe and whose
		// "Reject" button reads "Allow" is deceived just the same. OptionID and
		// Kind are opaque identifiers the client echoes back, not display text,
		// so they are left exactly as received — sanitizing an identifier would
		// change what the answer means.
		options[i] = vibekit.PermissionOption{OptionID: o.OptionID, Name: displayText(o.Name), Kind: o.Kind}
	}

	// No secondary shell classifier: kiro-cli's native Cedar policy
	// already auto-resolved everything it could — a request that
	// reaches vibekit is a genuine ask and always surfaces to the user.

	// A turn approval's files, workspace-relative. relPath because every other
	// path vibekit puts on the wire is relative and a client that had to handle
	// both would get it wrong somewhere.
	var files []vibekit.ApprovalFile
	if req.Meta.Kiro.Type == approvalTypeTurn {
		files = make([]vibekit.ApprovalFile, 0, len(req.Meta.Kiro.Files))
		for _, f := range req.Meta.Kiro.Files {
			files = append(files, vibekit.ApprovalFile{
				Path:        t.relPath(f.Path),
				SnapshotURI: f.SnapshotURI,
				ActionID:    f.ToolCallID,
			})
		}
	}

	// Whether the card may offer to persist a rule for this command, translated
	// to a vibekit CODE rather than forwarded as KAS's reason string. Absent
	// means persistable, so nil is a yes and only present-and-false blocks the
	// offer — a plain bool here would read false for every pre-2.19.1 frame and
	// suppress the Always-allow row everywhere.
	//
	// This replaces the client's own guess: static-src/permission.ts used to
	// test the command against a shell-metacharacter regex, which was wrong in
	// both directions — it suppressed `git commit -m "fix"` (a quote, though
	// `git *` matches it) and it offered the row for a command KAS cannot parse
	// at all, where the click wrote a rule that could never match.
	var alwaysAllowBlocked vibekit.AlwaysAllowBlock
	if c := req.Meta.Kiro.Consent.PersistableConsent; c != nil && !*c {
		alwaysAllowBlocked = vibekit.AlwaysAllowBlockUnparseable
	}

	// A workflow STEP's ask is attributed to its run, whichever bridge it
	// arrived on: the launching chat's for an agent-launched run, the run
	// bridge for a manual one. The run id is what lets a run tab render an ask
	// keyed to a different surface, and the node id is what makes the card say
	// WHO is asking.
	step := t.steps.refFor(req.SessionID)
	evt := vibekit.NewEvent(vibekit.EventPermissionNeeded, chatID, vibekit.PermissionNeededPayload{
		RequestID:  reqID,
		ToolCallID: req.ToolCall.ToolCallID,
		// THE TITLE IS A DECISION SURFACE, which is why it is the one string on
		// this payload that gets defused. It is composed upstream of the tool
		// call — reachable by an agent that read a poisoned file — and it is the
		// only description of the action the human is approving. A Bidi override
		// in it renders `rm -rf /workspace` as an innocuous find command while
		// the approved action is unchanged, so the card lies about what
		// pressing Allow does. See displayText for the measured before/after.
		Title:              displayText(req.ToolCall.Title),
		Kind:               req.ToolCall.Kind,
		SubSessionID:       subSessionID,
		RunID:              step.WorkflowID,
		NodeID:             step.NodeID,
		Options:            options,
		Files:              files,
		AlwaysAllowBlocked: alwaysAllowBlocked,
	})
	t.bus.Broadcast(ctx, evt)
	t.pendingPerms.PendingPermsAdd(reqID, evt)
	t.bus.Broadcast(ctx, vibekit.NewEvent(vibekit.EventWorkingLabel, chatID, vibekit.WorkingLabelPayload{Label: vibekit.WorkingLabelApproval}))
	t.push.NotifyPush(ctx, "Permission needed", vibekit.PushKindPermission, chatID)
}
