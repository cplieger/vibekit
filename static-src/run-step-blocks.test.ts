// ---------------------------------------------------------------------------
// The live step transcript's ordering rules.
//
// Three of them, and each is a rule the SERVER cannot enforce because a parentless
// run has no buffer at that end: a run of same-kind deltas has to extend one
// element, a tool call has to close the run of text above it, and a tool UPDATE has
// to replace its card rather than append a second one. Every one of those is what
// the chat path gets for free from `Buffer.AppendTextDelta`, so this side owning
// them is exactly the seam worth pinning.
//
// The tool card is mocked. What is under test is the ORDER and the identity of the
// elements in a step's body, not what a card looks like — tool-card.test.ts owns
// that, and linking the real builder would drag the diff renderer, the highlighter
// and the editor openers in for nothing.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach, beforeAll } from "vitest";
import type { RunStepPayload, ToolCall } from "./types.js";

// The markdown bubble's graph touches these at module level. The transcript's own
// suites do the same (tool-card.test.ts), and the alternative would be mocking the
// bubble — which is the element whose IDENTITY across deltas is the thing under
// test here, so it has to be the real one.
beforeAll(() => {
  for (const id of ["messages", "messages-wrap", "banner-stack"]) {
    if (document.getElementById(id) === null) {
      const node = document.createElement("div");
      node.id = id;
      document.body.appendChild(node);
    }
  }
});

vi.mock("./scroll.js", () =>
  import("./__test-helpers__/scroll-mock.js").then((mm) => mm.scrollMock),
);

const m = vi.hoisted(() => ({ built: 0 }));

vi.mock("./tool-card.js", () => ({
  buildToolCard: vi.fn((opts: { id: string; status: string }) => {
    m.built++;
    const node = document.createElement("div");
    node.className = "tool-call";
    node.dataset["id"] = opts.id;
    node.dataset["status"] = opts.status;
    return node;
  }),
  expandToolDetails: vi.fn(),
}));

const { createRunStepStream } = await import("./run-step-blocks.js");

function tool(id: string, status: string): ToolCall {
  return { id, title: "Run tests", kind: "execute", status } as ToolCall;
}

function text(delta: string): RunStepPayload {
  return { workflow_id: "wf_1", node_path: "seq/coder", kind: "text", delta };
}

function thinking(delta: string): RunStepPayload {
  return { workflow_id: "wf_1", node_path: "seq/coder", kind: "thinking", delta };
}

function toolFrame(tc: ToolCall): RunStepPayload {
  return { workflow_id: "wf_1", node_path: "seq/coder", kind: "tool", tool_call: tc };
}

/** A fresh body per step path, so the stream's host contract is exercised rather
 *  than assumed: the run card hands out one element per node path. */
function harness(): { apply: (p: RunStepPayload) => void; body: (path: string) => HTMLElement } {
  const bodies = new Map<string, HTMLElement>();
  const stream = createRunStepStream((path) => {
    let host = bodies.get(path);
    if (host === undefined) {
      host = document.createElement("div");
      bodies.set(path, host);
    }
    return host;
  });
  return {
    apply: (p) => {
      stream.apply(p);
    },
    body: (path) => {
      const host = bodies.get(path);
      if (host === undefined) {
        throw new Error(`no body for ${path}`);
      }
      return host;
    },
  };
}

/** The element kinds in a body, in order. */
function shape(host: HTMLElement): string[] {
  return [...host.children].map((c) => {
    if (c.classList.contains("tool-call")) {
      return "tool";
    }
    if (c.tagName === "DETAILS") {
      return "thinking";
    }
    return "text";
  });
}

beforeEach(() => {
  m.built = 0;
});

describe("run step stream", () => {
  // The rule `Buffer.AppendTextDelta` implements server-side for a chat: a run of
  // same-kind deltas is ONE block. Without it a step streaming a paragraph would
  // produce one bubble per token.
  it("extends one bubble across a run of text deltas", () => {
    const h = harness();
    for (const d of ["All ", "ten ", "commands ", "exit 0."]) {
      h.apply(text(d));
    }
    // ONE element for four deltas is the assertion. The text itself is not read
    // back: the bubble reveals through a rAF cursor (see fundamentals/text-bubble
    // on the reveal buffer), so its `textContent` is legitimately behind the
    // parser at this instant and forcing it forward would be testing the reveal.
    expect(shape(h.body("seq/coder"))).toEqual(["text"]);
  });

  // Reasoning is a different kind, so it opens its own block — and the previous
  // text block closes rather than absorbing it.
  it("opens a new block when the kind changes", () => {
    const h = harness();
    h.apply(text("looking"));
    h.apply(thinking("the suite is red"));
    h.apply(text("fixed"));
    expect(shape(h.body("seq/coder"))).toEqual(["text", "thinking", "text"]);
  });

  // A tool call between two text runs is what makes the order on screen the order
  // the step did things in. Folding the second run back into the first would put
  // the step's conclusion above the work that produced it.
  it("closes the text run when a tool call lands", () => {
    const h = harness();
    h.apply(text("running the battery"));
    h.apply(toolFrame(tool("t1", "in_progress")));
    h.apply(text("all green"));
    expect(shape(h.body("seq/coder"))).toEqual(["text", "tool", "text"]);
  });

  // The server folds every update into the call and sends the WHOLE thing, so the
  // newest frame is the complete truth and the card is rebuilt from it. What must
  // not happen is a second card: an `in_progress` and a `completed` frame for one
  // id are one tool.
  it("replaces a tool card on update rather than appending a second", () => {
    const h = harness();
    h.apply(toolFrame(tool("t1", "in_progress")));
    h.apply(toolFrame(tool("t1", "completed")));
    const host = h.body("seq/coder");
    expect(shape(host)).toEqual(["tool"]);
    expect(host.querySelector<HTMLElement>(".tool-call")?.dataset["status"]).toBe("completed");
    expect(m.built).toBe(2);
  });

  // Two steps are two bodies. A repeat's iterations share a node ID and differ only
  // in their PATH, which is why the payload carries the path — so this is the case
  // that would silently merge two passes of one loop body.
  it("keeps each node path's content in its own body", () => {
    const h = harness();
    h.apply(text("first pass"));
    h.apply(toolFrame(tool("t1", "completed")));
    h.apply({ workflow_id: "wf_1", node_path: "loop#1/iter", kind: "text", delta: "second pass" });
    // Each body holds only its own step's blocks. Asserted on the shape rather
    // than the prose for the reveal reason above; the tool card is what makes the
    // two bodies distinguishable synchronously.
    expect(shape(h.body("seq/coder"))).toEqual(["text", "tool"]);
    expect(shape(h.body("loop#1/iter"))).toEqual(["text"]);
  });

  // A frame with no address has no row to go in. Dropping it beats guessing a
  // step, which would attribute one step's work to another.
  it("drops a frame with no node path", () => {
    const h = harness();
    h.apply({ workflow_id: "wf_1", node_path: "", kind: "text", delta: "orphan" });
    expect(() => h.body("")).toThrow();
  });

  // An empty delta is not content, and neither is a tool frame with no call. The
  // server already declines to send either, so this is the belt-and-braces half:
  // an empty bubble would still be an element in the step's body.
  it("opens no block for an empty delta or a call-less tool frame", () => {
    const h = harness();
    h.apply(text(""));
    h.apply(thinking(""));
    h.apply({ workflow_id: "wf_1", node_path: "seq/coder", kind: "tool" });
    expect(shape(h.body("seq/coder"))).toEqual([]);
  });
});
