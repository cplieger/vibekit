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

const { buildAssistantBody, updateAssistantBody, finalizeAssistantBody } =
  await import("./messages-blocks.js");

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

// ---------------------------------------------------------------------------
// A mounted block tracks the store's text whether or not it was mounted live.
//
// The fast path for a live turn is the per-block signal effect: a chunk writes
// the signal and the DOM updates with no reconcile. That effect only exists for
// a block the renderer judged LIVE, and `store.appendChunk` falls back to
// scheduling a full repaint for any block with no signal — so the repaint is the
// only channel left for a block whose liveness was misjudged, and it used to
// mount new blocks and never revisit a mounted one. The measured symptom was an
// assistant reply frozen at its first streamed chunk (`<p>Here's v</p>`), with
// no ellipsis, healed only by reloading the page.
//
// Liveness is misjudged cheaply: `isLikelyLiveStreaming` requires the chat's
// `thinking` flag, and any mid-turn event that clears it (a `.kiro/agents` parse
// error arriving at session construction was the real one) makes every later
// block of that turn mount for replay.
// ---------------------------------------------------------------------------

describe("a mounted block picks up text that arrived after it mounted", () => {
  function growingMessage(blocks: Record<string, unknown>[]): Message {
    return {
      id: "m-grow",
      role: "assistant",
      content: "",
      blocks,
      tool_calls: [],
    } as unknown as Message;
  }

  it("renders a text block's tail on the next repaint, mounted for REPLAY", () => {
    const wrap = document.createElement("div");
    const blocks = [text("Here's v")];
    const m = growingMessage(blocks);
    // live: false — the frozen case.
    buildAssistantBody(wrap, m, false);
    expect(wrap.querySelector(".message.assistant")?.textContent).toBe("Here's v");

    blocks[0]!["text"] = "Here's version two of the plan.";
    updateAssistantBody(wrap, m, false);
    // The grown bubble is on the incremental renderer now, which holds its last
    // character provisionally until the next write or the finalize — the same
    // contract a live turn has. What matters here is that the tail arrived at all.
    expect(wrap.querySelector(".message.assistant")?.textContent).toContain(
      "Here's version two of the plan",
    );

    finalizeAssistantBody(m.id);
    expect(wrap.querySelector(".message.assistant")?.textContent).toBe(
      "Here's version two of the plan.",
    );
  });

  it("renders a thinking block's tail the same way", () => {
    const wrap = document.createElement("div");
    const blocks: Record<string, unknown>[] = [
      { type: "thinking", thinking: "I", agent_subtask_id: "" },
    ];
    const m = growingMessage(blocks);
    buildAssistantBody(wrap, m, false);
    expect(wrap.querySelector(".reasoning-body")?.textContent).toBe("I");

    blocks[0]!["thinking"] = "I should read the file first.";
    updateAssistantBody(wrap, m, false);
    expect(wrap.querySelector(".reasoning-body")?.textContent).toBe(
      "I should read the file first.",
    );
  });

  it("adds nothing when the block did not grow", () => {
    // The sweep runs on every repaint of every mounted block, so a settled
    // transcript must cost a length comparison and produce no DOM churn.
    const wrap = document.createElement("div");
    const blocks = [text("settled")];
    const m = growingMessage(blocks);
    buildAssistantBody(wrap, m, false);
    const before = wrap.querySelector(".message.assistant")?.innerHTML;

    updateAssistantBody(wrap, m, false);
    updateAssistantBody(wrap, m, false);
    expect(wrap.querySelector(".message.assistant")?.innerHTML).toBe(before);
  });

  it("reaches a block inside a subagent card too", () => {
    const wrap = document.createElement("div");
    const blocks = [text("delegate ", "sub-A")];
    const m = growingMessage(blocks);
    buildAssistantBody(wrap, m, false);

    blocks[0]!["text"] = "delegate finished its walk.";
    updateAssistantBody(wrap, m, false);
    expect(wrap.querySelector(".subagent-body")?.textContent).toContain(
      "delegate finished its walk",
    );
  });
});
