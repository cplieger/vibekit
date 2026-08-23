// ---------------------------------------------------------------------------
// Two grouping rules of the block dispatcher, which are deliberately OPPOSITE
// and were documented as the same thing.
//
// A tool GROUP is contiguous: `toolGroups` is keyed by container and any text
// block calls `closeToolGroup`, so a run of tool calls broken by prose becomes
// two groups. A SUBAGENT card is not: `st.subagents` is keyed by subtask id and
// nothing closes it, so a delegate's blocks join the card it opened however much
// the parent agent emitted in between.
//
// Four comments and three steering passages all claimed the subagent case was
// contiguous too. It never was, and nothing caught it, which is why these are
// tests rather than a corrected sentence.
// ---------------------------------------------------------------------------

import { describe, it, expect } from "vitest";
import type { Message, ToolCall } from "./types.js";

// The dispatcher's import graph reaches the shared DOM registry, which throws on
// a missing app root. These ids have to exist before the import is evaluated.
for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
]) {
  const d = document.createElement("div");
  d.id = id;
  document.body.appendChild(d);
}

const { buildAssistantBody } = await import("./messages-blocks.js");

function text(t: string, subtask = ""): Record<string, unknown> {
  return { type: "text", text: t, agent_subtask_id: subtask };
}

function toolUse(id: string, subtask = ""): Record<string, unknown> {
  return { type: "tool_use", tool_call_id: id, agent_subtask_id: subtask };
}

function call(id: string, title: string): ToolCall {
  return { id, title, kind: "execute", status: "completed" } as unknown as ToolCall;
}

function render(blocks: Record<string, unknown>[], toolCalls: ToolCall[] = []): HTMLElement {
  const wrap = document.createElement("div");
  buildAssistantBody(
    wrap,
    {
      id: `m-${String(Math.random())}`,
      role: "assistant",
      content: "",
      blocks,
      tool_calls: toolCalls,
    } as unknown as Message,
    false,
  );
  return wrap;
}

/** The `.assistant-blocks` children, as a readable shape. */
function shape(wrap: HTMLElement): string[] {
  return [...(wrap.querySelector(".assistant-blocks")?.children ?? [])].map((e) => {
    if (e.classList.contains("subagent-block")) {
      return `card(${String((e as HTMLElement).dataset["subtask"])})`;
    }
    if (e.classList.contains("tool-group")) {
      return `group(${String(e.querySelectorAll(".tool-call").length)})`;
    }
    return `text(${String(e.textContent).trim().slice(0, 16)})`;
  });
}

describe("subagent grouping is keyed by subtask id, NOT by contiguity", () => {
  it("puts non-adjacent same-subtask blocks in ONE card", () => {
    const wrap = render([
      text("delegate first", "sub-A"),
      text("parent interleaved"),
      text("delegate second", "sub-A"),
    ]);
    const cards = wrap.querySelectorAll(".subagent-block");
    expect(cards).toHaveLength(1);
    const body = cards[0]?.querySelector(".subagent-body")?.textContent ?? "";
    expect(body).toContain("delegate first");
    expect(body).toContain("delegate second");
  });

  it("keeps two DIFFERENT subtasks in two cards", () => {
    const wrap = render([text("a", "sub-A"), text("b", "sub-B"), text("a again", "sub-A")]);
    expect(wrap.querySelectorAll(".subagent-block")).toHaveLength(2);
  });

  it("costs ORDER, not grouping: an interleaved parent block lands after the card", () => {
    // The card is appended at the subtask's FIRST appearance and later blocks are
    // appended into it, so prose that arrived between two of a delegate's halves
    // renders below the whole card. This is the accepted consequence of keying by
    // id; assert it so a future reader meets it as a decision, not a surprise.
    const wrap = render([
      text("delegate first", "sub-A"),
      text("parent interleaved"),
      text("delegate second", "sub-A"),
    ]);
    expect(shape(wrap)).toEqual(["card(sub-A)", "text(parent interleav)"]);
  });
});

describe("tool grouping IS contiguous, which is the contrast", () => {
  it("splits a run of tool calls broken by prose into two groups", () => {
    const wrap = render(
      [toolUse("t1"), toolUse("t2"), text("prose between"), toolUse("t3")],
      [call("t1", "Run Command"), call("t2", "Run Command"), call("t3", "Run Command")],
    );
    expect(shape(wrap)).toEqual(["group(2)", "text(prose between)", "group(1)"]);
  });

  it("keeps an unbroken run in one group", () => {
    const wrap = render(
      [toolUse("t1"), toolUse("t2"), toolUse("t3")],
      [call("t1", "Run Command"), call("t2", "Run Command"), call("t3", "Run Command")],
    );
    expect(shape(wrap)).toEqual(["group(3)"]);
  });
});
