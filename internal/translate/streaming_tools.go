package translate

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/cplieger/pathinside/v2"
	"github.com/cplieger/vibekit/internal/buffer"
	"github.com/cplieger/vibekit/internal/sanitize"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// HandleToolCall adds a tool call to the current assistant message buffer and
// broadcasts it, threading AgentSubtaskID so the client can nest a subagent's
// chunks (which carry the same id) under its card.
func (t *Translator) HandleToolCall(ctx context.Context, chatID vibekit.ChatID, raw json.RawMessage, attr FrameAttribution) {
	var tc ACPToolCallWire
	if json.Unmarshal(raw, &tc) != nil {
		return
	}
	// Must stay ABOVE every guard below: those are RENDERING decisions and this is
	// ENFORCEMENT, so sat under them a display preference decides a cancellation.
	// Deliberately apart from countStepTurn, which needs a step key this does not.
	t.reportRunProgress(tc.Meta.Kiro.Workflow)
	// A hook's ask-permission gate arrives as a kind:"other" call tagged
	// _meta.kiro.hookAsk. Its follow-up tool_call_update drops too, since
	// HandleToolCallUpdate early-returns when the id was never buffered.
	if len(tc.Meta.Kiro.HookAsk) > 0 && !t.hookStatus.IsHookStatusEnabled() {
		return
	}
	// Dropped BEFORE ensureTurnStarted, or the cloud-config fetch's frame opens a
	// wire turn the prompt then displaces, splitting the user's turn in two. The
	// update must drop too: TurnFoldTarget opens a turn for any frame it is asked about.
	if isInternalTool(tc.Meta.Kiro.ToolID) {
		t.suppressed.add(tc.ToolCallID)
		return
	}
	buf := t.buffers.TurnFoldTarget(ctx, chatID, foldSource(attr.Step))
	t.ensureTurnStarted(ctx, chatID, buf)
	// A step's tool frames carry KAS's own agentSubtaskId while the step's TEXT is
	// keyed by nodePath, so without this override one step's work fragments in two.
	subtask := tc.Meta.Kiro.AgentSubtaskID
	if wf := tc.Meta.Kiro.Workflow.SubtaskID(); wf != "" {
		subtask = wf
		t.countStepTurn(tc.Meta.Kiro.Workflow, wf)
	}
	// The create frame deliberately does NOT adopt content.output: the initial
	// tool_call has never fed Output, and folding it in would double-render
	// whatever the following update repeats.
	content := t.parseToolUpdateContent(tc.ToolCallID, tc.Content)
	diffs := content.diffs
	call := toolCallFromWire(&tc, subtask, attr.SubSessionID, content)
	turn := buf.AppendToolCall(&call) + 1
	// Always a new block: back-to-back tool calls each get their own.
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
// Shared with the run-bridge path (workflow_step_content.go) so one frame decodes
// one way; a second copy of this literal is a second place a field can be
// forgotten, which is how a run card ends up with no diffs or no denial notice.
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

// HandleToolCallUpdate mutates an in-flight tool call's status and appends any new
// output chunks.
func (t *Translator) HandleToolCallUpdate(ctx context.Context, chatID vibekit.ChatID, raw json.RawMessage, attr FrameAttribution) {
	var tu ACPToolCallUpdateWire
	if json.Unmarshal(raw, &tu) != nil {
		return
	}
	// Dropped BEFORE TurnFoldTarget, which would otherwise open a wire turn for a
	// frame nothing renders.
	if t.suppressed.take(tu.ToolCallID) {
		return
	}
	content := t.parseToolUpdateContent(tu.ToolCallID, tu.Content)
	buf := t.buffers.TurnFoldTarget(ctx, chatID, foldSource(attr.Step))
	// Folded on a COPY and written back: the fold reaches the terminal registry, the
	// line tracker and the event bus, none of which may run under the buffer's mutex.
	tc, idx, ok := buf.ToolCall(tu.ToolCallID)
	if !ok {
		return
	}
	t.applyToolCallUpdate(ctx, chatID, buf, &tc, &tu, content, attr.SubSessionID)
	buf.SetToolCall(idx, &tc)
	t.emit(ctx, buf, vibekit.NewEvent(vibekit.EventToolCallUpdate, chatID, vibekit.ToolCallUpdatePayload{MessageID: buf.MessageID, ToolCall: tc}))
}

// parseToolUpdateContent extracts the sanitized output delta, any file diffs, and
// the terminal id from a tool_call_update's content blocks. Diff paths are
// normalized to workspace-relative form.
//
// A type:"terminal" block's text is deliberately not folded into the output delta:
// the bytes arrive on the terminal/* surface instead, and the id is what lets the
// card subscribe to that stream. toolCallID is carried for diagnostics only.
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
			// Guarded on the TYPE rather than left as a bare default, which would also
			// catch a known type whose payload arm did not match and bury this line.
			slog.Debug("tool call content block of an unmodelled type, dropped",
				"tool_call_id", toolCallID, "type", item.Type)
		}
	}
	out.output = outputDelta.String()
	// Logged once per frame rather than per block, and guarded on all three outputs
	// so a legitimately claim-only tool logs nothing.
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
// type WITH a payload condition, so it cannot answer this question on its own.
func knownToolContentType(t string) bool {
	switch t {
	case ContentTypeContent, ContentTypeDiff, ContentTypeTerminal:
		return true
	default:
		return false
	}
}

// toolUpdateContent is one tool_call_update's parsed content blocks. A struct
// rather than three return values because the set grows with the ACP content union.
type toolUpdateContent struct {
	output     string
	terminalID string
	diffs      []vibekit.ToolDiff
}

// applyToolCallUpdate folds a parsed tool_call_update into the buffered tool call
// at idx: status, appended output, replaced locations, appended diffs with line
// tracking, and a first-seen subsession id.
func (t *Translator) applyToolCallUpdate(ctx context.Context, chatID vibekit.ChatID, buf *buffer.Buffer, tc *vibekit.ToolCall, tu *ACPToolCallUpdateWire, content toolUpdateContent, subSessionID string) {
	// KAS sends title and kind nullish on an update, so apply only when present or
	// an update that omits them wipes the initial tool_call's values.
	if tu.Title != "" {
		tc.Title = tu.Title
	}
	if tu.Kind != "" {
		tc.Kind = tu.Kind
	}
	// BEFORE the status fold: one frame can carry both the link and `completed`, and
	// with the fold first that frame's adoption looked up an id the tool call did
	// not yet have, so the output was never persisted and never would be.
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
	// Adopted once and never overwritten: KAS reports the run on the terminal
	// update, and a later frame for the same call cannot name a different one.
	if tc.WorkflowID == "" {
		tc.WorkflowID = rawOutputWorkflowID(tu.RawOutput)
	}
	mergeCheckpoint(tc, tu.Meta.Kiro.Checkpoint)
	mergeToolMeta(tc, tu)
}

// applyToolCallOutput folds an update's output text onto the card.
//
// A failed tool's reason rides `rawOutput` and nothing else. Gated on an empty
// Output so a command's own output wins — the status fold ran first, so
// adoptTerminalOutput may already have filled it.
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
	// KAS can send several terminal status frames for one tool call, and
	// ComputeDuration CONSUMES its start time, so the second read answers 0.
	// Assigning it unconditionally wrote that 0 over a correct duration, which the
	// markdown export then dropped. The sibling read below is non-destructive too.
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
// every streaming frame, so tracking each arrival would count partial streams and
// claim a file changed when the write failed. The card keeps every diff regardless.
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
// output arrives before the update that names it. The terminal's output wins over
// anything already on the tool call, since an earlier ACP content block is a
// fragment of what the terminal holds in full. A miss is logged because a card
// that renders empty looks exactly like a command that printed nothing.
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
// buffered call. A denial is decided when the call is ATTEMPTED, so it can arrive
// on the update rather than the create. Never overwrites a value already held.
func mergeToolMeta(tc *vibekit.ToolCall, tu *ACPToolCallUpdateWire) {
	if tc.Disclosed == nil {
		tc.Disclosed = disclosedFrom(tu.Meta.Kiro.DisclosedContext)
	}
	if tc.Denial == nil {
		tc.Denial = denialFrom(tu.Meta.Kiro.PolicyDenial)
	}
}

// disclosedFrom maps KAS's disclosedContext block onto the domain type. Returns nil
// for every tool call that is not a disclose_context, which is nearly all of them.
func disclosedFrom(in *ACPDisclosedContext) *vibekit.ToolDisclosed {
	if in == nil {
		return nil
	}
	return &vibekit.ToolDisclosed{Type: in.Type, DisplayName: in.DisplayName, URI: in.URI}
}

// denialFrom maps KAS's policyDenial block onto the domain type. The outer `effect`
// is always the literal "deny" so it is dropped; the matched rule's own effect is
// kept, because an "ask" rule that nobody answered also arrives here.
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

// mergeCheckpoint folds a tool_call_update's _meta.kiro.checkpoint into the buffered
// tool call, field by field so a frame omitting a key cannot erase one an earlier
// frame supplied: the key set genuinely varies between frames for one tool call, so
// replacing the struct would be lossy the moment a narrower set arrives.
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
// KAS sends some tool-call paths as file:// URIs. Every consumer downstream treats
// the value as a path and none survive a URI (filepath.Rel refuses it). Anything
// that is not a LOCAL file:// URI is returned unchanged, so a remote authority
// falls through to the normal outside-the-workspace handling.
func localPath(ref string) string {
	// Cheap gate: this runs on every location and diff of every tool call.
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
// Normalising the wire's URI spelling belongs here rather than at each call site:
// the not-under-the-workspace branch returns its input, so a raw URI would leak the
// spelling this function exists to remove. The escape test is separator-precise
// (pathinside.RelEscapes), where a leading-".." string test would leak the path.
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
	// Fallback attribution only: a prompt latches the model at dispatch. This read
	// stays for turns nobody dispatched, where the chat record is the only evidence
	// of what is answering.
	if !buf.HasModel() {
		if c, ok := t.chats.Get(ctx, chatID); ok {
			buf.SetModel(c.Model)
		}
	}
	t.emit(ctx, buf, vibekit.NewEvent(vibekit.EventMessageCreated, chatID,
		vibekit.Message{ID: buf.MessageID, Role: vibekit.RoleAssistant, Ts: time.Now().UnixMilli()}))
}
