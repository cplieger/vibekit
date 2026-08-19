package chat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cplieger/vibekit/internal/sanitize"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// renderChatMarkdown renders a persisted chat as a self-contained Markdown
// transcript: a title + metadata header, then one section per message with a
// role heading, timestamp, collapsible reasoning, content, a plan checklist,
// and collapsible tool-call summaries. It is pure (no I/O) so it is
// unit-testable and safe to call inline on the chat read path.
//
// Tool input/output is passed through sanitize.Output before embedding —
// the same ANSI-strip + hidden-codepoint scrub the store applies when
// persisting tool output — so a hostile tool result can't smuggle terminal
// escapes or prompt-injection codepoints into the exported file.
func renderChatMarkdown(c *vibekit.Chat) string {
	var b strings.Builder
	title := oneLine(c.Name)
	if title == "" {
		title = "Untitled chat"
	}
	fmt.Fprintf(&b, "# %s\n\n", title)
	writeChatMetadata(&b, c)
	if len(c.Messages) == 0 {
		b.WriteString("_No messages._\n")
		return b.String()
	}
	for i := range c.Messages {
		writeMessageMarkdown(&b, &c.Messages[i])
	}
	return b.String()
}

// writeChatMetadata emits the header bullet list (id, model, mode,
// timestamps, message count). Empty fields are skipped.
func writeChatMetadata(b *strings.Builder, c *vibekit.Chat) {
	if c.ID != "" {
		fmt.Fprintf(b, "- **Chat ID:** `%s`\n", oneLine(c.ID))
	}
	if c.Model != "" {
		fmt.Fprintf(b, "- **Model:** %s\n", oneLine(c.Model))
	}
	if c.CurrentModeID != "" {
		fmt.Fprintf(b, "- **Mode:** %s\n", oneLine(c.CurrentModeID))
	}
	if ts := mdTimestamp(c.CreatedAt); ts != "" {
		fmt.Fprintf(b, "- **Created:** %s\n", ts)
	}
	if ts := mdTimestamp(c.UpdatedAt); ts != "" {
		fmt.Fprintf(b, "- **Updated:** %s\n", ts)
	}
	fmt.Fprintf(b, "- **Messages:** %d\n\n", len(c.Messages))
}

// writeMessageMarkdown renders one message: heading, timestamp, reasoning
// (collapsible), content, plan checklist, and tool calls, then a rule.
func writeMessageMarkdown(b *strings.Builder, m *vibekit.Message) {
	b.WriteString(messageHeading(m))
	if ts := mdTimestamp(m.Ts); ts != "" {
		fmt.Fprintf(b, "_%s_\n\n", ts)
	}
	if reasoning := strings.TrimSpace(m.Reasoning); reasoning != "" {
		b.WriteString("<details>\n<summary>Reasoning</summary>\n\n")
		b.WriteString(reasoning)
		b.WriteString("\n\n</details>\n\n")
	}
	if content := strings.TrimSpace(m.Content); content != "" {
		b.WriteString(content)
		b.WriteString("\n\n")
	}
	if len(m.Plan) > 0 {
		writePlanMarkdown(b, m.Plan)
	}
	for i := range m.ToolCalls {
		writeToolCallMarkdown(b, &m.ToolCalls[i])
	}
	b.WriteString("---\n\n")
}

// messageHeading returns the "## Role" heading (with the event kind for
// event messages) including its trailing blank line.
func messageHeading(m *vibekit.Message) string {
	switch m.Role {
	case vibekit.RoleUser:
		return "## User\n\n"
	case vibekit.RoleAssistant:
		return "## Assistant\n\n"
	case vibekit.RoleEvent:
		kind := oneLine(string(m.EventKind))
		if kind == "" {
			kind = "event"
		}
		return fmt.Sprintf("## Event: %s\n\n", kind)
	default:
		role := oneLine(string(m.Role))
		if role == "" {
			role = "message"
		}
		return fmt.Sprintf("## %s\n\n", role)
	}
}

// writePlanMarkdown renders the plan as a GitHub task-list checklist.
// Completed → [x]; in-progress → [ ] with a suffix (GFM has no third box).
func writePlanMarkdown(b *strings.Builder, plan []vibekit.PlanEntry) {
	b.WriteString("**Plan**\n\n")
	for i := range plan {
		box, suffix := "[ ]", ""
		switch plan[i].Status {
		case vibekit.PlanCompleted:
			box = "[x]"
		case vibekit.PlanInProgress:
			suffix = " _(in progress)_"
		case vibekit.PlanPending:
			// leave the default unchecked box
		}
		fmt.Fprintf(b, "- %s %s%s\n", box, oneLine(plan[i].Content), suffix)
	}
	b.WriteString("\n")
}

// writeToolCallMarkdown renders one tool call as a collapsible block:
// "Tool: <title> — <status>" summary, then duration, locations, and the
// sanitised input/output in fenced code blocks.
func writeToolCallMarkdown(b *strings.Builder, tc *vibekit.ToolCall) {
	title := oneLine(tc.Title)
	if title == "" {
		title = oneLine(string(tc.Kind))
	}
	if title == "" {
		title = "tool"
	}
	status := string(tc.Status)
	if status == "" {
		status = "unknown"
	}
	fmt.Fprintf(b, "<details>\n<summary>Tool: %s — %s</summary>\n\n", title, status)
	if tc.DurationMs > 0 {
		fmt.Fprintf(b, "Duration: %dms\n\n", tc.DurationMs)
	}
	writeToolLocations(b, tc.Locations)
	if in := formatToolInput(tc.Input); in != "" {
		b.WriteString("Input:\n\n")
		b.WriteString(fencedCode(sanitize.Output(in), "json"))
	}
	if out := strings.TrimSpace(tc.Output); out != "" {
		b.WriteString("Output:\n\n")
		b.WriteString(fencedCode(sanitize.Output(out), ""))
	}
	b.WriteString("</details>\n\n")
}

// writeToolLocations renders the tool call's file locations as a list.
func writeToolLocations(b *strings.Builder, locs []vibekit.ToolLocation) {
	if len(locs) == 0 {
		return
	}
	b.WriteString("Locations:\n\n")
	for _, loc := range locs {
		if loc.Line > 0 {
			fmt.Fprintf(b, "- `%s:%d`\n", oneLine(loc.Path), loc.Line)
		} else {
			fmt.Fprintf(b, "- `%s`\n", oneLine(loc.Path))
		}
	}
	b.WriteString("\n")
}

// formatToolInput pretty-prints a tool call's raw JSON input, or returns
// the trimmed raw string when it is not valid JSON. Empty/null yields "".
func formatToolInput(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err == nil {
		return pretty.String()
	}
	return s
}

// mdTimestamp formats a millisecond epoch as a UTC datetime, or "" for a
// zero/negative timestamp.
func mdTimestamp(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04:05 UTC")
}

// oneLine collapses CR/LF to spaces and trims, so a value can't break a
// heading, list item, or table cell it is interpolated into.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

// fencedCode wraps content in a Markdown code fence whose backtick run is
// always longer than the longest backtick run inside content, so embedded
// triple-backticks can't prematurely close the block.
func fencedCode(content, lang string) string {
	longest, run := 0, 0
	for _, r := range content {
		if r == '`' {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	fenceLen := 3
	if longest >= fenceLen {
		fenceLen = longest + 1
	}
	fence := strings.Repeat("`", fenceLen)
	var b strings.Builder
	b.WriteString(fence)
	b.WriteString(lang)
	b.WriteByte('\n')
	b.WriteString(strings.TrimRight(content, "\n"))
	b.WriteByte('\n')
	b.WriteString(fence)
	b.WriteString("\n\n")
	return b.String()
}
