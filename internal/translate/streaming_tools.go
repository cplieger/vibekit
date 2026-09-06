package translate

// Tool call streaming handlers: tool_call, tool_call_update, ext session update.

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

// HandleToolCall adds a tool call to the current assistant message
// buffer and broadcasts it. On v3 (KAS) a subagent is an ordinary tool
// call tagged _meta.kiro.kind=="agent-subtask"; AgentSubtaskID is threaded
// onto the domain ToolCall so the client can render a subagent card and
// nest the subagent's chunks (which carry the same id) under it.
func (t *Translator) HandleToolCall(ctx context.Context, chatID vibekit.ChatID, raw json.RawMessage, attr FrameAttribution) {
	var tc ACPToolCallWire
	if json.Unmarshal(raw, &tc) != nil {
		return
	}
	// The run's idle window, reported ABOVE EVERY GUARD BELOW: all of those are
	// RENDERING decisions (do not draw a hook card, do not fold engine bookkeeping
	// into a turn) and this is ENFORCEMENT, so sat under them a display preference
	// decides a cancellation — with hooks.showStatus off, a step whose frames are hook
	// asks stops refilling its run's window. It is DELIBERATELY apart from
	// countStepTurn below, which needs the step key this does not: they are two bounds
	// talking to one host, and folding them together would also skip progress for a
	// step whose key is empty, which countStepTurn tolerates.
	t.reportRunProgress(tc.Meta.Kiro.Workflow)
	// Hook status suppression. On v3 (KAS) a pre-tool-use hook's
	// ask-permission gate arrives as a kind:"other" tool call tagged
	// _meta.kiro.hookAsk. When hooks.showStatus is off, drop the
	// hook-ask card; its follow-up tool_call_update drops too, since
	// HandleToolCallUpdate early-returns when the id was never buffered.
	if len(tc.Meta.Kiro.HookAsk) > 0 && !t.hookStatus.IsHookStatusEnabled() {
		return
	}
	// Internal engine bookkeeping never reaches the transcript. The
	// cloud-config fetch runs during session creation, before the
	// prompt's turn opens, so its frame used to open a wire turn the
	// prompt then displaced — a fragment message that split the user's
	// turn in two on every fresh session. Dropping the frame before
	// ensureTurnStarted prevents that turn from opening. The update must
	// be dropped too and not only by the id-not-buffered fallback:
	// TurnFoldTarget opens a wire turn for any frame it is asked about.
	if isInternalTool(tc.Meta.Kiro.ToolID) {
		t.suppressed.add(tc.ToolCallID)
		return
	}
	buf := t.buffers.TurnFoldTarget(ctx, chatID, foldSource(attr.Step))
	t.ensureTurnStarted(ctx, chatID, buf)
	// A workflow STEP's tool frames carry KAS's own agentSubtaskId (or none),
	// while the step's TEXT is keyed by its nodePath — so without this override
	// one step's work fragments across two boxes. Same rule as the chunk path:
	// the step's workflow identity wins.
	subtask := tc.Meta.Kiro.AgentSubtaskID
	if wf := tc.Meta.Kiro.Workflow.SubtaskID(); wf != "" {
		subtask = wf
		t.countStepTurn(tc.Meta.Kiro.Workflow, wf)
	}
	// One parser for both frames, so the ACP content union is decoded in one
	// place. The create frame deliberately does NOT adopt content.output: the
	// initial tool_call has never fed Output, and folding it in here would
	// double-render whatever the following update repeats.
	content := t.parseToolUpdateContent(tc.ToolCallID, tc.Content)
	diffs := content.diffs
	call := toolCallFromWire(&tc, subtask, attr.SubSessionID, content)
	turn := buf.AppendToolCall(&call) + 1
	// Anchor the tool in the chronological block array. Always a new
	// block — back-to-back tool calls each get their own tool_use
	// block (the next text chunk after this will also start a new
	// block since the trailing block is now tool_use, not text).
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
// Extracted so the CHAT path above and the run-bridge path (workflow_step_content.go)
// decode one frame one way. A parentless run's steps run the same tools with the
// same wire shape, and a second copy of this literal is a second place a field can
// be forgotten — which is how a run card would end up with no diffs or no denial
// notice while a chat's card had both.
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
	// A COPY, folded locally and written back, because the fold below reaches the
	// terminal registry, the line tracker and the event bus — none of which may
	// run under the buffer's mutex.
	tc, idx, ok := buf.ToolCall(tu.ToolCallID)
	if !ok {
		return
	}
	t.applyToolCallUpdate(ctx, chatID, buf, &tc, &tu, content, attr.SubSessionID)
	buf.SetToolCall(idx, &tc)
	t.emit(ctx, buf, vibekit.NewEvent(vibekit.EventToolCallUpdate, chatID, vibekit.ToolCallUpdatePayload{MessageID: buf.MessageID, ToolCall: tc}))
}

// parseToolUpdateContent extracts the sanitized output delta, any file
// diffs, and the terminal id from a tool_call_update's content blocks.
// Diff paths are normalized to workspace-relative form.
//
// A type:"terminal" block is ACP's statement that this tool call's output
// is a terminal's stream. Its text is deliberately not folded into the
// output delta: the bytes arrive on the terminal/* surface instead, and
// the id is what lets the tool card subscribe to that stream and later
// persist it.
//
// toolCallID is carried for diagnostics only. The two Debug lines exist
// because this switch is where a content block vibekit does not model
// disappears — kiro-cli 2.19.2 added structuredContent to the result
// union, and a structured-only result renders a claim-only card with an
// empty details region, with no error and no log line otherwise.
//
// Debug rather than Warn on both: an unmodelled content type is an
// upstream addition on a wire that gains members between releases, not a
// fault in this deployment.
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
			// The TYPE is what is unmodelled, so this arm is guarded on the type
			// rather than left as a bare default. A default would also catch a
			// known type whose payload arm did not match — an empty-text content
			// block, a diff with no path — which is a normal frame rather than a
			// gap in what vibekit decodes, and logging those would bury this line.
			slog.Debug("tool call content block of an unmodelled type, dropped",
				"tool_call_id", toolCallID, "type", item.Type)
		}
	}
	out.output = outputDelta.String()
	// The observable SYMPTOM, logged once per frame rather than per block: content
	// arrived and none of it reached the card, which is exactly the empty details
	// region a reader reports. Guarded on all three outputs so a legitimately
	// claim-only tool logs nothing.
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
// own, and the diagnostic needs exactly that question. Both have to move together
// when a member is adopted, which is what the shared use of the ContentType
// constants makes visible.
func knownToolContentType(t string) bool {
	switch t {
	case ContentTypeContent, ContentTypeDiff, ContentTypeTerminal:
		return true
	default:
		return false
	}
}

// toolUpdateContent is one tool_call_update's parsed content blocks. A struct
// rather than three return values because applyToolCallUpdate already carries
// enough parameters, and because the set grows with the ACP content union.
type toolUpdateContent struct {
	output     string
	terminalID string
	diffs      []vibekit.ToolDiff
}

// applyToolCallUpdate folds a parsed tool_call_update into the buffered
// tool call at idx: status (emitting a working label on terminal
// status), appended output, replaced locations, appended diffs (with
// line tracking), and a first-seen subsession id.
func (t *Translator) applyToolCallUpdate(ctx context.Context, chatID vibekit.ChatID, buf *buffer.Buffer, tc *vibekit.ToolCall, tu *ACPToolCallUpdateWire, content toolUpdateContent, subSessionID string) {
	// A mid-flight update may refine the card's title/kind (KAS sends them
	// nullish on tool_call_update); apply only when present so an update
	// that omits them doesn't wipe the values set on the initial tool_call.
	if tu.Title != "" {
		tc.Title = tu.Title
	}
	if tu.Kind != "" {
		tc.Kind = tu.Kind
	}
	// Adopt the terminal link BEFORE the status fold. Order matters: a single
	// frame can carry both the link and `completed`, and with the status fold
	// first that frame's adoption looked up an id the tool call did not yet
	// have, so the output was never persisted and never would be — the update
	// carrying the link had already gone by.
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
	// overwritten: KAS reports it on the terminal update, and a later frame for
	// the same call cannot name a different run.
	if tc.WorkflowID == "" {
		tc.WorkflowID = rawOutputWorkflowID(tu.RawOutput)
	}
	mergeCheckpoint(tc, tu.Meta.Kiro.Checkpoint)
	mergeToolMeta(tc, tu)
}

// applyToolCallOutput folds an update's output text onto the card.
//
// A failed tool's reason rides `rawOutput` and nothing else: for an edit KAS puts
// a diff in the content blocks and the reason in no block at all. Gated on an
// empty Output so a command's own output wins — the status fold ran first, so
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

// applyToolCallStatus folds an update's status in, and on a terminal status
// stamps the duration and takes the terminal's output for keeping. Split out of
// applyToolCallUpdate to keep that function a flat list of per-field folds.
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
	// Assigning it unconditionally wrote that 0 over a correct duration, the SSE
	// carried it, the turn-end persist made it durable, and the markdown export
	// then dropped the line because it is gated on a positive value. The sibling
	// read on the next line was made non-destructive for the same reason.
	if tc.DurationMs == 0 {
		tc.DurationMs = buf.ComputeDuration(tu.ToolCallID)
	}
	t.adoptTerminalOutput(chatID, tc)
	t.emit(ctx, buf, vibekit.NewEvent(vibekit.EventWorkingLabel, chatID,
		vibekit.WorkingLabelPayload{Label: vibekit.WorkingLabelThinking}))
}

// applyToolCallDiffs appends an update's diffs to the card and, once the
// tool has succeeded, records the file changes they describe.
//
// Only a `completed` status feeds the ledger: KAS repeats a write's diff
// block on every streaming frame, on the supervised approval frame, and
// from the write tool's own catch, so tracking each arrival would count
// partial streams and claim a file changed when the write failed. The
// card keeps every diff regardless — a pending write's is what a reader
// approves, a failed one's is what it tried to do.
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

// adoptTerminalOutput copies a finished terminal's output onto its tool
// call, so the command's output survives a page reload.
//
// A copy at one moment rather than incremental bookkeeping: a terminal's
// first output arrives before the update that names it, so there is no
// earlier point where an append could be both correct and complete.
//
// The terminal's output wins over anything already on the tool call: an
// earlier ACP content block is a fragment of what the terminal holds in
// full, and KAS's synthesized explanation only exists when there is no
// terminal, so this never overwrites that case.
//
// A miss is logged rather than swallowed, since a card that renders
// empty looks exactly like a command that printed nothing — the runtime
// distinguishes "no record" from "an empty record", so a silent
// `mkdir -p` does not file a false alarm here.
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

// mergeToolMeta folds a tool_call_update's disclosure and denial metadata into
// the buffered call. Late adoption, for the same reason as the checkpoint fold: a
// denial in particular is decided when the call is ATTEMPTED, so it can arrive on
// the update rather than the create. Never overwrites a value already held.
func mergeToolMeta(tc *vibekit.ToolCall, tu *ACPToolCallUpdateWire) {
	if tc.Disclosed == nil {
		tc.Disclosed = disclosedFrom(tu.Meta.Kiro.DisclosedContext)
	}
	if tc.Denial == nil {
		tc.Denial = denialFrom(tu.Meta.Kiro.PolicyDenial)
	}
}

// disclosedFrom maps KAS's disclosedContext block onto the domain type. Returns
// nil for every tool call that is not a disclose_context, which is nearly all of
// them.
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
// buffered tool call, field by field so that a frame omitting a key cannot
// erase one an earlier frame supplied.
//
// Per-field rather than wholesale because the key set genuinely varies
// between frames for the same tool call: KAS sends {modified, local} for a
// file creation and adds `original` only when there was a pre-image, so
// replacing the struct would be correct today and lossy the moment a second
// frame arrives with a narrower set.
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
// KAS sends some tool-call paths as file:// URIs rather than plain paths.
// Every consumer downstream treats the value as a path, and none survive
// a URI (filepath.Rel refuses it), so the symptom was a changed-file row
// labelled with a URI whose diff could never load.
//
// Anything that is not a local file:// URI is returned unchanged: a
// remote authority ("file://host/share") names a file this process
// cannot open, so leaving it alone lets the normal outside-the-workspace
// handling reject it.
func localPath(ref string) string {
	// Cheap gate: a filesystem path has no scheme, and this runs on every
	// location and diff of every tool call.
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

// relPath strips the workspace root prefix from an absolute path. A path
// that is not under the workspace is returned unchanged.
//
// The funnel every ACP-supplied path crosses on the way to the client, so
// normalising the wire's URI spelling belongs here rather than at each
// call site. Normalising first also matters: the not-under-the-workspace
// branch returns its input, and returning the raw URI there would leak
// the spelling this function exists to remove.
//
// The escape test is pathinside.RelEscapes on the computed rel, not a
// leading-".." string prefix: the separator-precise rule keeps a file
// under a workspace directory whose name merely begins with two dots
// relative, where the string test would leak the absolute path.
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

// ensureTurnStarted initializes the buffer for a new turn if not already
// started: assigns the message id and broadcasts message_created.
//
// It owns no crash durability: a turn interrupted mid-flight is rebuilt
// from KAS's own log by the session/load replay projection, which holds
// each sub-message as it completes.
func (t *Translator) ensureTurnStarted(ctx context.Context, chatID vibekit.ChatID, buf *buffer.Buffer) {
	if !buf.StartTurn(t.newMsgID()) {
		return
	}
	// Fallback attribution only. A prompt latches the model at dispatch,
	// which closes the window where a fast switch lands before the old
	// model's first frame. This read stays for turns nobody dispatched —
	// an agent-opened turn, a priming reply — where the chat record is
	// the only evidence of what is answering.
	if !buf.HasModel() {
		if c, ok := t.chats.Get(ctx, chatID); ok {
			buf.SetModel(c.Model)
		}
	}
	t.emit(ctx, buf, vibekit.NewEvent(vibekit.EventMessageCreated, chatID,
		vibekit.Message{ID: buf.MessageID, Role: vibekit.RoleAssistant, Ts: time.Now().UnixMilli()}))
}
