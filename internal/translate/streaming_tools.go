package translate

// Tool call streaming handlers: tool_call, tool_call_update, ext session update.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/cplieger/pathinside/v2"
	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/sanitize"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// HandleToolCall adds a tool call to the current assistant message buffer and
// broadcasts it. A v3 (KAS) subagent is an ordinary tool call tagged
// _meta.kiro.kind=="agent-subtask"; its AgentSubtaskID is what nests its chunks.
func (t *Translator) HandleToolCall(ctx context.Context, chatID vibekit.ChatID, raw json.RawMessage, attr FrameAttribution) {
	var tc ACPToolCallWire
	if json.Unmarshal(raw, &tc) != nil {
		return
	}
	// A pre-tool-use hook's ask-permission gate arrives as a kind:"other" call
	// tagged _meta.kiro.hookAsk; drop the card when hooks.showStatus is off.
	if len(tc.Meta.Kiro.HookAsk) > 0 && !t.hookStatus.IsHookStatusEnabled() {
		return
	}
	// Internal engine bookkeeping never reaches the transcript. Dropped before
	// TurnFoldTarget, which would open a wire turn and split the user's own.
	if isInternalTool(tc.Meta.Kiro.ToolID) {
		t.suppressed.add(tc.ToolCallID)
		return
	}
	buf := t.buffers.TurnFoldTarget(ctx, chatID, foldSource(attr.Step))
	t.ensureTurnStarted(ctx, chatID, buf)
	// A step's tool frames carry KAS's own agentSubtaskId while its TEXT is keyed
	// by nodePath, so the step's workflow identity wins or its work splits in two.
	subtask := tc.Meta.Kiro.AgentSubtaskID
	if wf := tc.Meta.Kiro.Workflow.SubtaskID(); wf != "" {
		subtask = wf
		t.countStepTurn(tc.Meta.Kiro.Workflow, wf)
	}
	// One parser for both frames. The create frame deliberately does NOT adopt
	// content.output: the following update repeats it, so it would double-render.
	content := t.parseToolUpdateContent(tc.ToolCallID, tc.Content)
	diffs := content.diffs
	call := toolCallFromWire(&tc, subtask, attr.SubSessionID, content)
	turn := buf.AppendToolCall(&call) + 1
	// Anchor the tool in the chronological block array. Always a NEW block, so
	// back-to-back tool calls each get their own.
	blockIndex := buf.AppendToolUseBlock(call.ID, subtask)
	buf.RecordToolStart(tc.ToolCallID)
	if len(diffs) > 0 {
		isNew := tc.Kind == vibekit.ToolKindEdit && tc.Status == vibekit.ToolPending
		buf.TrackFileChanges(diffs, isNew)
		t.lines.RecordFromDiffs(chatID, diffs, turn, string(tc.Kind))
	}
	t.emit(ctx, buf, vibekit.NewEvent(vibekit.EventToolCall, chatID,
		vibekit.ToolCallPayload{MessageID: buf.MessageID, ToolCall: call, BlockIndex: blockIndex}))
	t.emit(ctx, buf, vibekit.NewEvent(vibekit.EventWorkingLabel, chatID,
		vibekit.WorkingLabelPayload{Label: vibekit.WorkingLabelForKind(tc.Kind, tc.Title)}))
}

// toolCallFromWire builds the domain tool call a `tool_call` frame describes.
//
// Extracted so the CHAT path above and the run-bridge path
// (workflow_step_content.go) decode one frame one way: a second copy of this
// literal is a second place a field can be forgotten, which is how a run card
// ends up with no diffs while a chat's card has them.
func toolCallFromWire(
	tc *ACPToolCallWire, subtask, subSessionID string, content toolUpdateContent,
) vibekit.ToolCall {
	return vibekit.ToolCall{
		ID:             tc.ToolCallID,
		Title:          tc.Title,
		Kind:           tc.Kind,
		Status:         tc.Status,
		Input:          tc.RawInput,
		SubSessionID:   subSessionID,
		AgentSubtaskID: subtask,
		TerminalID:     content.terminalID,
		Locations:      tc.Locations,
		Diffs:          content.diffs,
		Disclosed:      disclosedFrom(tc.Meta.Kiro.DisclosedContext),
		Denial:         denialFrom(tc.Meta.Kiro.PolicyDenial),
		Ts:             time.Now().UnixMilli(),
	}
}

// HandleToolCallUpdate mutates an in-flight tool call's status and
// appends any new output chunks.
func (t *Translator) HandleToolCallUpdate(ctx context.Context, chatID vibekit.ChatID, raw json.RawMessage, attr FrameAttribution) {
	var tu ACPToolCallUpdateWire
	if json.Unmarshal(raw, &tu) != nil {
		return
	}
	// A suppressed internal tool's completion. Dropped BEFORE TurnFoldTarget,
	// which would otherwise open a wire turn for a frame nothing renders.
	if t.suppressed.take(tu.ToolCallID) {
		return
	}
	content := t.parseToolUpdateContent(tu.ToolCallID, tu.Content)
	buf := t.buffers.TurnFoldTarget(ctx, chatID, foldSource(attr.Step))
	// A COPY, folded locally and written back: the fold reaches the terminal
	// registry, the line tracker and the event bus, none of which may hold the mutex.
	tc, idx, ok := buf.ToolCall(tu.ToolCallID)
	if !ok {
		return
	}
	// The pre-fold value, kept so the frame can carry the fold's INPUTS rather than
	// its result: comparing before against after derives the delta from the fold
	// instead of restating the fold's rules here — including the one a pure-append
	// wire cannot express, adoptTerminalOutput replacing the output at completion. A
	// struct copy is enough: every field the fold writes is replaced or appended to.
	before := tc
	t.applyToolCallUpdate(ctx, chatID, buf, &tc, &tu, content, attr.SubSessionID)
	buf.SetToolCall(idx, &tc)
	t.emit(ctx, buf, vibekit.NewEvent(vibekit.EventToolCallUpdate, chatID,
		toolCallDelta(buf.MessageID, &before, &tc)))
}

// toolCallDelta describes what one fold changed about a tool call.
//
// Sending the whole accumulated ToolCall re-sent output and diffs on every later
// frame for it — megabytes behind a few open tabs — and Input is not here at all,
// because an update never changes it.
//
// Every omitted field means "unchanged", so applying this to `before` reconstructs
// `after` exactly. That is what lets the client keep no accumulation rules.
func toolCallDelta(messageID string, before, after *vibekit.ToolCall) vibekit.ToolCallUpdatePayload {
	d := vibekit.ToolCallUpdatePayload{MessageID: messageID, ToolCallID: after.ID}
	if after.Title != before.Title {
		d.Title = after.Title
	}
	if after.Kind != before.Kind {
		d.Kind = after.Kind
	}
	if after.Status != before.Status {
		d.Status = after.Status
	}
	if after.DurationMs != before.DurationMs {
		d.DurationMs = after.DurationMs
	}
	d.OutputDelta, d.OutputReplace = outputDelta(before.Output, after.Output)
	deltaContent(&d, before, after)
	deltaAttachments(&d, before, after)
	return d
}

// deltaContent carries the three collections. Only Diffs accumulates; the other
// two are absolute and go entire whenever they change.
func deltaContent(d *vibekit.ToolCallUpdatePayload, before, after *vibekit.ToolCall) {
	if !slices.Equal(after.OutputSpans, before.OutputSpans) {
		d.OutputSpans = after.OutputSpans
	}
	// Diffs only ever append (applyToolCallDiffs), so the tail is the whole change.
	// Guarded on the length, because a frame that appended nothing must send nothing.
	if len(after.Diffs) > len(before.Diffs) {
		d.DiffsAppended = after.Diffs[len(before.Diffs):]
	}
	if !slices.Equal(after.Locations, before.Locations) {
		d.Locations = after.Locations
	}
}

// deltaAttachments carries the four late identity ids and the three metadata
// blocks. Every one is adopted once and never overwritten, so each appears on at
// most one frame per call — which is what makes a set-if-present fold correct.
func deltaAttachments(d *vibekit.ToolCallUpdatePayload, before, after *vibekit.ToolCall) {
	if after.TerminalID != before.TerminalID {
		d.TerminalID = after.TerminalID
	}
	if after.SubSessionID != before.SubSessionID {
		d.SubSessionID = after.SubSessionID
	}
	if after.AgentSubtaskID != before.AgentSubtaskID {
		d.AgentSubtaskID = after.AgentSubtaskID
	}
	if after.WorkflowID != before.WorkflowID {
		d.WorkflowID = after.WorkflowID
	}
	if after.Checkpoint != nil && *after.Checkpoint != derefCheckpoint(before.Checkpoint) {
		d.Checkpoint = after.Checkpoint
	}
	if before.Disclosed == nil && after.Disclosed != nil {
		d.Disclosed = after.Disclosed
	}
	if before.Denial == nil && after.Denial != nil {
		d.Denial = after.Denial
	}
}

// outputDelta describes the change from one accumulated output to the next: the
// appended tail, or the whole new value when it is not an extension of the old.
//
// The replace arm is adoptTerminalOutput: at completion a terminal's full stream
// wins over the ACP fragments already on the card. Detected by asking whether the
// new value EXTENDS the old, so the rule lives in the fold and this reports it.
func outputDelta(before, after string) (delta string, replace bool) {
	if after == before {
		return "", false
	}
	if strings.HasPrefix(after, before) {
		return after[len(before):], false
	}
	return after, true
}

// derefCheckpoint returns the checkpoint's value, or the zero value for nil, so
// a nil-to-set transition compares as a change without a second nil branch.
func derefCheckpoint(c *vibekit.ToolCheckpoint) vibekit.ToolCheckpoint {
	if c == nil {
		return vibekit.ToolCheckpoint{}
	}
	return *c
}

// parseToolUpdateContent extracts the sanitized output delta, any file diffs, and
// the terminal id from a tool_call_update's content blocks. Diff paths are
// normalized to workspace-relative form.
//
// A type:"terminal" block is ACP's statement that this call's output is a
// terminal's stream: its text is deliberately not folded in, because the bytes
// arrive on the terminal/* surface instead. toolCallID is carried for the two Debug
// lines, where a content block vibekit does not model disappears.
func (t *Translator) parseToolUpdateContent(toolCallID string, items []ACPToolCallContentBlock) toolUpdateContent {
	var out toolUpdateContent
	var outputDelta strings.Builder
	for _, item := range items {
		switch {
		case item.Type == ContentTypeContent && item.Content.Text != "":
			outputDelta.WriteString(sanitize.Output(item.Content.Text))
			outputDelta.WriteByte('\n')
		case item.Type == ContentTypeDiff && item.Path != "":
			out.diffs = append(out.diffs, vibekit.ToolDiff{
				Path: t.relPath(item.Path), OldText: item.OldText, NewText: item.NewText,
			})
		case item.Type == ContentTypeTerminal && item.TerminalID != "":
			out.terminalID = item.TerminalID
		case !knownToolContentType(item.Type):
			// The TYPE is what is unmodelled, so this arm is guarded on the type rather
			// than left as a bare default: a default would also catch a known type whose
			// payload arm did not match, which is a normal frame, and bury this line.
			slog.Debug("tool call content block of an unmodelled type, dropped",
				"tool_call_id", toolCallID, "type", item.Type)
		}
	}
	out.output = outputDelta.String()
	// The observable SYMPTOM, logged once per frame rather than per block: content
	// arrived and none of it reached the card. Guarded on all three outputs so a
	// legitimately claim-only tool logs nothing.
	if len(items) > 0 && out.output == "" && out.diffs == nil && out.terminalID == "" {
		slog.Debug("tool call carried content blocks that produced nothing to render",
			"tool_call_id", toolCallID, "blocks", len(items))
	}
	return out
}

// knownToolContentType reports whether the ACP content-block discriminator is one
// parseToolUpdateContent models.
//
// A closed set beside the switch rather than inside it: the switch's arms pair a
// type WITH a payload condition, so it cannot answer "is this type known" on its
// own. Both have to move together when a member is adopted.
func knownToolContentType(t string) bool {
	switch t {
	case ContentTypeContent, ContentTypeDiff, ContentTypeTerminal:
		return true
	default:
		return false
	}
}

// toolUpdateContent is one tool_call_update's parsed content blocks. A struct
// rather than three return values, because the set grows with the ACP union.
type toolUpdateContent struct {
	output     string
	terminalID string
	diffs      []vibekit.ToolDiff
}

// applyToolCallUpdate folds a parsed tool_call_update into the buffered tool call:
// status (emitting a working label on a terminal status), appended output, replaced
// locations, appended diffs with line tracking, and a first-seen subsession id.
func (t *Translator) applyToolCallUpdate(ctx context.Context, chatID vibekit.ChatID, buf *buffer.Buffer, tc *vibekit.ToolCall, tu *ACPToolCallUpdateWire, content toolUpdateContent, subSessionID string) {
	// A mid-flight update may refine title/kind (KAS sends them nullish), so apply
	// only when present, or an update that omits them wipes the initial values.
	if tu.Title != "" {
		tc.Title = tu.Title
	}
	if tu.Kind != "" {
		tc.Kind = tu.Kind
	}
	// Adopt the terminal link BEFORE the status fold. One frame can carry both the
	// link and `completed`, and with the status fold first that frame's adoption
	// looked up an id the call did not yet have, so the output was never persisted.
	if tc.TerminalID == "" && content.terminalID != "" {
		tc.TerminalID = content.terminalID
	}
	t.applyToolCallStatus(ctx, chatID, buf, tc, tu)
	applyToolCallOutput(tc, tu, content)
	if len(tu.Locations) > 0 {
		tc.Locations = tu.Locations
	}
	t.applyToolCallDiffs(chatID, buf, tc, content.diffs)
	if tc.SubSessionID == "" && subSessionID != "" {
		tc.SubSessionID = subSessionID
	}
	if tc.AgentSubtaskID == "" {
		// Late adoption mirrors the create path, workflow identity first.
		if wf := tu.Meta.Kiro.Workflow.SubtaskID(); wf != "" {
			tc.AgentSubtaskID = wf
		} else if tu.Meta.Kiro.AgentSubtaskID != "" {
			tc.AgentSubtaskID = tu.Meta.Kiro.AgentSubtaskID
		}
	}
	// The run a `run_workflow` invocation started. Adopted once and never
	// overwritten: a later frame for the same call cannot name a different run.
	if tc.WorkflowID == "" {
		tc.WorkflowID = rawOutputWorkflowID(tu.RawOutput)
	}
	mergeCheckpoint(tc, tu.Meta.Kiro.Checkpoint)
	mergeToolMeta(tc, tu)
}

// applyToolCallOutput folds an update's output text onto the card.
//
// A failed tool's reason rides `rawOutput` and nothing else: for an edit KAS puts a
// diff in the content blocks and the reason in no block at all. Gated on an empty
// Output so a command's own output wins.
func applyToolCallOutput(tc *vibekit.ToolCall, tu *ACPToolCallUpdateWire, content toolUpdateContent) {
	if content.output != "" {
		tc.Output += content.output
	}
	if tc.Status != vibekit.ToolFailed || tc.Output != "" {
		return
	}
	if reason := rawOutputFailureText(tu.RawOutput); reason != "" {
		tc.Output = sanitize.Output(reason)
	}
}

// applyToolCallStatus folds an update's status in, and on a terminal status stamps
// the duration and takes the terminal's output for keeping.
func (t *Translator) applyToolCallStatus(
	ctx context.Context, chatID vibekit.ChatID, buf *buffer.Buffer,
	tc *vibekit.ToolCall, tu *ACPToolCallUpdateWire,
) {
	if tu.Status == "" {
		return
	}
	tc.Status = tu.Status
	if tu.Status != vibekit.ToolCompleted && tu.Status != vibekit.ToolFailed {
		return
	}
	// KAS can send more than one terminal status frame for one tool call, and
	// ComputeDuration CONSUMES its start time, so the second read answers 0.
	// Assigning it unconditionally wrote that 0 over a correct duration, and the
	// markdown export then dropped the line, which is gated on a positive value.
	if tc.DurationMs == 0 {
		tc.DurationMs = buf.ComputeDuration(tu.ToolCallID)
	}
	t.adoptTerminalOutput(chatID, tc)
	t.emit(ctx, buf, vibekit.NewEvent(vibekit.EventWorkingLabel, chatID,
		vibekit.WorkingLabelPayload{Label: vibekit.WorkingLabelThinking}))
}

// applyToolCallDiffs appends an update's diffs to the card and, once the tool has
// succeeded, records the file changes they describe.
//
// Only a `completed` status feeds the ledger: KAS repeats a write's diff block on
// every streaming frame, on the approval frame, and from the write tool's own
// catch, so tracking each arrival would claim a file changed when the write failed.
// The card keeps every diff regardless — a pending write's is what a reader approves.
func (t *Translator) applyToolCallDiffs(
	chatID vibekit.ChatID, buf *buffer.Buffer, tc *vibekit.ToolCall, diffs []vibekit.ToolDiff,
) {
	if len(diffs) == 0 {
		return
	}
	tc.Diffs = append(tc.Diffs, diffs...)
	if tc.Status != vibekit.ToolCompleted {
		return
	}
	buf.TrackFileChanges(diffs, false)
	t.lines.RecordFromDiffs(chatID, diffs, buf.ToolCallCount(), string(tc.Kind))
}

// adoptTerminalOutput copies a finished terminal's output onto its tool call, so
// the command's output survives a page reload.
//
// A copy at one moment rather than incremental bookkeeping: a terminal's first
// output arrives before the update that names it, so no earlier point could be
// both correct and complete. The terminal's output wins over anything already on
// the call, since an ACP content block is a fragment of what it holds in full. A
// miss is logged, because a card that renders empty looks like a silent command.
func (t *Translator) adoptTerminalOutput(chatID vibekit.ChatID, tc *vibekit.ToolCall) {
	if tc.TerminalID == "" {
		return
	}
	text, spans, ok := t.terminals.Output(tc.TerminalID)
	if !ok {
		slog.Warn("terminal output missing at completion",
			"chat_id", chatID, "tool_call_id", tc.ID,
			"terminal_id", tc.TerminalID, "status", tc.Status,
			"output_bytes", len(tc.Output))
		return
	}
	if text == "" {
		return
	}
	tc.Output = text
	tc.OutputSpans = spans
}

// mergeToolMeta folds a tool_call_update's disclosure and denial metadata into the
// buffered call. Late adoption: a denial is decided when the call is ATTEMPTED, so
// it can arrive on the update rather than the create. Never overwrites.
func mergeToolMeta(tc *vibekit.ToolCall, tu *ACPToolCallUpdateWire) {
	if tc.Disclosed == nil {
		tc.Disclosed = disclosedFrom(tu.Meta.Kiro.DisclosedContext)
	}
	if tc.Denial == nil {
		tc.Denial = denialFrom(tu.Meta.Kiro.PolicyDenial)
	}
}

// disclosedFrom maps KAS's disclosedContext block onto the domain type. Nil for
// every tool call that is not a disclose_context, which is nearly all of them.
func disclosedFrom(in *ACPDisclosedContext) *vibekit.ToolDisclosed {
	if in == nil {
		return nil
	}
	return &vibekit.ToolDisclosed{Type: in.Type, DisplayName: in.DisplayName, URI: in.URI}
}

// denialFrom maps KAS's policyDenial block onto the domain type. The outer
// `effect` is always the literal "deny" so it is dropped; the matched rule's own
// effect is kept, because an "ask" rule that nobody answered also arrives here.
func denialFrom(in *ACPPolicyDenial) *vibekit.ToolDenial {
	if in == nil {
		return nil
	}
	out := &vibekit.ToolDenial{
		Capability: in.Capability,
		Resource:   in.Resource,
		Scope:      in.Scope,
		Source:     in.Source,
	}
	if in.MatchedRule != nil {
		out.Rule = &vibekit.ToolDenialRule{
			Capability: in.MatchedRule.Capability,
			Effect:     in.MatchedRule.Effect,
			Match:      in.MatchedRule.Match,
			Exclude:    in.MatchedRule.Exclude,
		}
	}
	return out
}

// mergeCheckpoint folds a tool_call_update's _meta.kiro.checkpoint into the
// buffered tool call, field by field so a frame omitting a key cannot erase one an
// earlier frame supplied.
//
// Per-field rather than wholesale because the key set genuinely varies between
// frames: KAS sends {modified, local} for a file creation and adds `original` only
// when there was a pre-image, so replacing the struct would be lossy.
func mergeCheckpoint(tc *vibekit.ToolCall, in *ACPCheckpointMeta) {
	if in == nil || (in.Original == "" && in.Modified == "" && in.Local == "") {
		return
	}
	if tc.Checkpoint == nil {
		tc.Checkpoint = &vibekit.ToolCheckpoint{}
	}
	if in.Original != "" {
		tc.Checkpoint.Original = in.Original
	}
	if in.Modified != "" {
		tc.Checkpoint.Modified = in.Modified
	}
	if in.Local != "" {
		tc.Checkpoint.Local = in.Local
	}
}

// localPath converts a wire path reference to a local filesystem path.
//
// KAS sends some tool-call paths as file:// URIs, and every consumer downstream
// treats the value as a path while none survive a URI (filepath.Rel refuses it),
// so the symptom was a changed-file row whose diff could never load. Anything that
// is not a LOCAL file:// URI is returned unchanged: a remote authority names a file
// this process cannot open, so the outside-the-workspace handling rejects it.
func localPath(ref string) string {
	// Cheap gate: a filesystem path has no scheme, and this runs on every path.
	if !strings.Contains(ref, "://") {
		return ref
	}
	u, err := url.Parse(ref)
	if err != nil || u.Scheme != "file" || u.Path == "" {
		return ref
	}
	if u.Host != "" && u.Host != "localhost" {
		return ref
	}
	// u.Path is percent-DECODED, which is what a filesystem call needs:
	// "file:///workspace/hello%20world.sh" is the file "hello world.sh".
	return u.Path
}

// relPath strips the workspace root prefix from an absolute path. A path that is
// not under the workspace is returned unchanged.
//
// The funnel every ACP-supplied path crosses on the way to the client, so
// normalising the wire's URI spelling belongs here rather than at each call site —
// and it has to happen FIRST, or the not-under-the-workspace branch returns the raw
// URI. The escape test is pathinside.RelEscapes, not a leading-".." prefix: the
// separator-precise rule keeps a file under a directory whose name starts with two.
func (t *Translator) relPath(ref string) string {
	abs := localPath(ref)
	workDir := t.workDir
	if workDir == "" {
		return abs
	}
	clean := filepath.Clean(abs)
	root := filepath.Clean(workDir)
	rel, err := filepath.Rel(root, clean)
	if err != nil || pathinside.RelEscapes(rel) {
		return abs
	}
	return filepath.ToSlash(rel)
}

// ensureTurnStarted initializes the buffer for a new turn if not already started:
// assigns the message id and broadcasts message_created.
//
// It owns no crash durability: a turn interrupted mid-flight is rebuilt from KAS's
// own log by the session/load replay projection.
func (t *Translator) ensureTurnStarted(ctx context.Context, chatID vibekit.ChatID, buf *buffer.Buffer) {
	if !buf.StartTurn(t.newMsgID()) {
		return
	}
	// Fallback attribution only. A prompt latches the model at dispatch, closing the
	// window where a fast switch lands before the old model's first frame. This read
	// stays for turns nobody dispatched, where the chat record is the only evidence.
	if !buf.HasModel() {
		if c, ok := t.chats.Get(ctx, chatID); ok {
			buf.SetModel(c.Model)
		}
	}
	t.emit(ctx, buf, vibekit.NewEvent(vibekit.EventMessageCreated, chatID,
		vibekit.Message{ID: buf.MessageID, Role: vibekit.RoleAssistant, Ts: time.Now().UnixMilli()}))
}
