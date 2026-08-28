// The agent-terminal → tool-card seam: what happens to live chunks that arrive
// before a card has claimed their terminal id.
//
// Two defects live here and both are ORDERING defects, so they are only
// reachable through the real mount path — the hold, the card build and the flush
// have to run in the order the reconciler runs them.
import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderOutput } from "./output-render.js";
import type { TextSpan, ToolCall } from "./types.js";

// The signal layer hands back the same snapshot it was given, so mount's effect
// sees `next === lastApplied` and does not re-enter applyToolCallUpdate. The real
// store-signals module would drag the whole chat store in.
vi.mock("./store-signals.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  blockKey: undefined,
  blockTextSigs: undefined,
  blockThinkingSigs: undefined,
  streamingReasoningSigs: undefined,
  streamingTextSigs: undefined,
  toolCallSigs: undefined,
  toolCallSigKey: undefined,
  ensureToolCallSig: vi.fn((_chat: string, _id: string, tc: unknown) => ({ value: tc })),
  clearToolCallSig: vi.fn(),
}));
vi.mock("./tool-group.js", () => ({
  maybeCollapseGroup: vi.fn(),
  formatDuration: vi.fn(() => ""),
  untrackInProgress: vi.fn(),
}));
vi.mock("./tool-schema.js", () => ({
  isToolDone: vi.fn(() => false),
  // Present-but-undefined, same reason as the mocks above: another module in
  // this graph imports the name and no path under test calls it.
  isToolActive: undefined,
}));

// A card double that does the ONE thing these tests depend on the real builder
// doing: render `opts.output` into the output region at BUILD time, through the
// same painter (`renderOutput`) tool-card.ts uses. That is what makes a
// subsequent flush a duplicate rather than a first paint. The real module is not
// imported because it reaches scroll.ts's self-initialising singleton and the
// editor openers, none of which this seam touches.
vi.mock("./tool-card.js", () => ({
  buildToolCard: vi.fn((opts: { output?: string; outputSpans?: TextSpan[] }) => {
    const card = document.createElement("div");
    card.className = "tool-call";
    card.dataset["depth1"] = "output";
    const out = document.createElement("div");
    out.className = "tool-output";
    card.appendChild(out);
    if (opts.output !== undefined && opts.output !== "") {
      const pre = document.createElement("pre");
      renderOutput(pre, opts.output, opts.outputSpans ?? []);
      out.appendChild(pre);
    }
    return card;
  }),
  insertDiffPreview: vi.fn(),
  expandToolDetails: vi.fn(),
  applyOutcome: vi.fn(),
}));

import {
  toolSpecFor,
  updateToolCall,
  appendTerminalChunk,
  forgetTerminal,
  disposeAllToolEffects,
} from "./messages-tools.js";

/** The spec every mount in this file goes through. The chat id is part of the
 *  per-tool signal key, so a mount and its writer have to name the same one. */
const spec = toolSpecFor("c-terminal");

/** A minimal ToolCall. Cast at the boundary: the wire type carries a dozen
 *  fields this seam never reads, and spelling them out would obscure the two
 *  that matter (`terminal_id` and `output`). */
function toolCall(over: Record<string, unknown>): ToolCall {
  return {
    id: "tc-1",
    title: "npm test",
    kind: "execute",
    status: "in_progress",
    ts: 0,
    ...over,
  } as ToolCall;
}

function pre(card: Element): string {
  return card.querySelector(".tool-output pre")?.textContent ?? "";
}

beforeEach(() => {
  disposeAllToolEffects();
  document.body.replaceChildren();
});

describe("a completion snapshot supersedes the hold", () => {
  it("does not print the opening output twice when the first frame already carries it", () => {
    // The reachable sequence: a fast command's chunks land before any card owns
    // the terminal, then its FIRST tool_call frame arrives already completed with
    // the server's whole-stream output on it.
    appendTerminalChunk("term-1", "hello\n", [], 0);
    appendTerminalChunk("term-1", "world\n", [], 6);

    const card = spec.mount(
      toolCall({ terminal_id: "term-1", status: "completed", output: "hello\nworld\n" }),
    );

    // Flushing the hold on top of the snapshot yields "hello\nworld\nhello\nworld\n".
    expect(pre(card)).toBe("hello\nworld\n");
  });

  it("still flushes the hold when the tool call carries no output of its own", () => {
    // The other half, and the reason the hold exists at all: without it the
    // opening lines of every command are missing from the live view until
    // completion replaces the whole thing.
    appendTerminalChunk("term-2", "starting\n", [], 0);
    const card = spec.mount(toolCall({ id: "tc-2", terminal_id: "term-2" }));
    expect(pre(card)).toBe("starting\n");
  });

  it("treats an empty-string output as no output", () => {
    // `output: ""` is what a pending tool call carries, and it is not a snapshot.
    appendTerminalChunk("term-3", "early\n", [], 0);
    const card = spec.mount(toolCall({ id: "tc-3", terminal_id: "term-3", output: "" }));
    expect(pre(card)).toBe("early\n");
  });

  it("is idempotent — a later update carrying the same link re-flushes nothing", () => {
    appendTerminalChunk("term-4", "a\n", [], 0);
    const tc = toolCall({ id: "tc-4", terminal_id: "term-4" });
    const card = spec.mount(tc);
    expect(pre(card)).toBe("a\n");
    // The same link arrives on every later update; the discard must be permanent
    // in both directions, so neither a re-link nor a snapshot re-flushes.
    updateToolCall(card, tc);
    expect(pre(card)).toBe("a\n");
  });

  it("keeps live chunks flowing after the link, snapshot or not", () => {
    const card = spec.mount(
      toolCall({ id: "tc-5", terminal_id: "term-5", status: "completed", output: "snap\n" }),
    );
    appendTerminalChunk("term-5", "after\n", [], 5);
    expect(pre(card)).toBe("snap\nafter\n");
  });
});

describe("the hold is a contiguous prefix", () => {
  // 64 KB, PENDING_CHARS_CAP. Not exported: the cap is an internal budget, and a
  // test that reads it would pass whatever it was set to.
  const CAP = 64 * 1024;

  it("stops accepting after the first chunk that does not fit, leaving no hole", () => {
    // Accepting a later SMALLER chunk after dropping an oversized one produces a
    // silent gap: every chunk is rebased by its own `base`, so the two sides of
    // the hole render as though they were adjacent and nothing marks the missing
    // middle.
    appendTerminalChunk("term-6", "head\n", [], 0);
    appendTerminalChunk("term-6", "M".repeat(CAP), [], 5);
    appendTerminalChunk("term-6", "tail\n", [], 5 + CAP);

    const card = spec.mount(toolCall({ id: "tc-6", terminal_id: "term-6" }));
    // "head\ntail\n" is the gap: two non-adjacent slices printed as neighbours.
    expect(pre(card)).toBe("head\n");
  });

  it("takes every chunk that fits", () => {
    for (let i = 0; i < 5; i++) {
      appendTerminalChunk("term-7", `line${String(i)}\n`, [], i * 6);
    }
    const card = spec.mount(toolCall({ id: "tc-7", terminal_id: "term-7" }));
    expect(pre(card)).toBe("line0\nline1\nline2\nline3\nline4\n");
  });

  it("releases an unclaimed hold on terminal_exited", () => {
    appendTerminalChunk("term-8", "orphan\n", [], 0);
    forgetTerminal("term-8");
    const card = spec.mount(toolCall({ id: "tc-8", terminal_id: "term-8" }));
    expect(pre(card)).toBe("");
  });

  it("evicts the oldest hold once too many terminals are unclaimed", () => {
    // PENDING_TERMINALS_CAP is 16. The 17th terminal to be held evicts the first,
    // because the newest is the one most likely still to find its card.
    for (let i = 0; i < 17; i++) {
      appendTerminalChunk(`bulk-${String(i)}`, `t${String(i)}\n`, [], 0);
    }
    const first = spec.mount(toolCall({ id: "tc-b0", terminal_id: "bulk-0" }));
    expect(pre(first)).toBe("");
    const last = spec.mount(toolCall({ id: "tc-b16", terminal_id: "bulk-16" }));
    expect(pre(last)).toBe("t16\n");
  });
});

describe("a flushed chunk keeps its styling", () => {
  it("rebases held spans by each chunk's own base", () => {
    // The hold stores `base` per chunk precisely so a flush is not one big
    // concatenation: the second chunk's spans address the accumulated stream.
    appendTerminalChunk("term-9", "ok\n", [{ start: 0, end: 2, fg: 2, bg: -1, attrs: 0 }], 0);
    appendTerminalChunk("term-9", "bad\n", [{ start: 3, end: 6, fg: 1, bg: -1, attrs: 0 }], 3);
    const card = spec.mount(toolCall({ id: "tc-9", terminal_id: "term-9" }));
    const spans = card.querySelectorAll(".tool-output pre span");
    expect([...spans].map((s) => [s.textContent, s.className])).toEqual([
      ["ok", "ansi-green-fg"],
      ["bad", "ansi-red-fg"],
    ]);
  });
});
