// ---------------------------------------------------------------------------
// Tests for fundamentals/text-bubble.ts — the assistant prose container, and
// specifically its wiring to the reveal cursor.
//
// `reveal.test.ts` covers the controller's arithmetic against an injected clock.
// What is left to prove is the WIRING, which the controller's own tests cannot
// see: that a live bubble defers growth and a replay bubble does not, that text
// present at mount paints immediately either way, and that the caret's lifetime
// follows the reveal rather than the turn.
//
// These use the real requestAnimationFrame, because the reveal's whole subject
// is frames and a fake one would prove the wiring against a clock production
// never uses. Each wait is a bounded poll, never a fixed sleep.
// ---------------------------------------------------------------------------

import { describe, expect, it } from "vitest";
import { buildAssistantBubble } from "./text-bubble.js";
import { FRAME_BUDGET_MS, testTimeoutFor } from "../__test-helpers__/frame-budget.js";

/** Resolve once `cond` holds, or throw after `budget` ms. */
async function until(cond: () => boolean, budget = FRAME_BUDGET_MS): Promise<void> {
  const deadline = performance.now() + budget;
  while (!cond()) {
    if (performance.now() > deadline) {
      throw new Error("condition never held");
    }
    await new Promise((r) => {
      requestAnimationFrame(() => r(null));
    });
  }
}

describe("buildAssistantBubble", { timeout: testTimeoutFor(FRAME_BUDGET_MS) }, () => {
  it("paints the text it was mounted with, live or replay", () => {
    // A mid-turn connect and a repaint both arrive holding text. Deferring it
    // would blank a transcript the reader is already looking at.
    //
    // The live case is one character short until the next write or the finalize:
    // the incremental parser holds its last codepoint provisionally, which is a
    // pre-existing contract of every streaming path and not the reveal's doing.
    for (const live of [true, false]) {
      const b = buildAssistantBubble("already on screen", live);
      expect(b.root.textContent).toContain("already on scree");
      b.finishNow();
      expect(b.root.textContent).toContain("already on screen");
    }
  });

  it("defers growth on a live bubble and lands all of it", async () => {
    const b = buildAssistantBubble("", true);
    const body = "Paragraph one of the answer, long enough to take several frames to reveal.";
    b.setText(body);
    // The reveal trails the live edge, so the whole string cannot be on screen
    // in the same task the caller published it.
    expect(b.root.textContent ?? "").not.toContain("several frames");
    await until(() => (b.root.textContent ?? "").includes("several frames"));
    b.end();
    await until(() => !b.root.classList.contains("streaming"));
    expect(b.root.textContent).toContain(body);
  });

  it("does NOT defer growth on a replay bubble", () => {
    // A replay bubble that grows is a misjudged-liveness repair, not a token
    // arriving now: it re-renders through a flushed stream in the same task,
    // holding only the provisional last character back.
    const b = buildAssistantBubble("first", false);
    b.setText("first and then some more");
    expect(b.root.textContent).toContain("and then some mor");
    b.finishNow();
    expect(b.root.textContent).toContain("first and then some more");
  });

  it("keeps the caret until the reveal catches up, then drops it", async () => {
    // The class is the caret and the streaming wash. Text is still appearing for
    // the reveal's lag after `turn_ended`, so dropping it at end() would leave
    // prose arriving under a settled turn.
    const b = buildAssistantBubble("", true);
    b.setText("a".repeat(400));
    expect(b.root.classList.contains("streaming")).toBe(true);
    b.end();
    expect(b.root.classList.contains("streaming")).toBe(true);
    await until(() => !b.root.classList.contains("streaming"));
    expect(b.root.textContent).toContain("a".repeat(400));
  });

  it("ends immediately when there is nothing left to reveal", () => {
    // The common shape: a short block that was already fully revealed, or a
    // block sealed because the tail moved to a tool call. No frame is coming, so
    // end() has to seal itself rather than wait for one.
    const b = buildAssistantBubble("short answer", true);
    b.end();
    expect(b.root.classList.contains("streaming")).toBe(false);
    expect(b.root.textContent).toContain("short answer");
  });

  it("finishNow reveals the remainder and settles in the same task", () => {
    // The teardown path: a chat switch discards the DOM, so the reveal must not
    // be left holding a frame loop over a detached node.
    const b = buildAssistantBubble("", true);
    const body = "b".repeat(600);
    b.setText(body);
    b.finishNow();
    expect(b.root.textContent).toContain(body);
    expect(b.root.classList.contains("streaming")).toBe(false);
  });

  it("append and setText reach the same place", async () => {
    const b = buildAssistantBubble("", true);
    b.append("one ");
    b.append("two ");
    b.setText("one two three");
    b.finishNow();
    expect(b.root.textContent).toContain("one two three");
    await until(() => true);
  });

  it("ignores a target that did not grow", () => {
    const b = buildAssistantBubble("settled", true);
    b.setText("settled");
    b.setText("short");
    b.end();
    expect(b.root.textContent).toContain("settled");
    expect(b.root.textContent).not.toContain("short");
  });

  it("renders markdown structure, not a flat string", async () => {
    // The reveal feeds the incremental parser rather than a text node, so the
    // slicing must not cost the markup. This is the guard against a future
    // "simplification" that writes revealed text straight into the DOM.
    const b = buildAssistantBubble("", true);
    b.setText("# Heading\n\nBody text with `code` in it.\n");
    b.finishNow();
    expect(b.root.querySelector("h1")?.textContent).toBe("Heading");
    expect(b.root.querySelector("code")?.textContent).toBe("code");
  });

  it("reveals a fenced block's chrome and its final highlighting", async () => {
    // A fence arriving through the reveal is the case where the two buffers meet:
    // the per-slice sweep gives an open fence its label, and end() finalizes it.
    const b = buildAssistantBubble("", true);
    b.setText("```js\nconst x = 1;\n```\n");
    b.finishNow();
    expect(b.root.querySelector("pre")).not.toBeNull();
    expect(b.root.textContent).toContain("const x = 1;");
  });
});

// ---------------------------------------------------------------------------
// The caret's two exits, which are not the same exit.
//
// `messages-blocks.ts` has two reasons to stop a bubble and they want different
// endings. The tail MOVING to another block means the model is already producing
// something else, so that bubble settles at once (finishNow) — otherwise its
// caret would run alongside the new tail's for the reveal's lag, and "exactly one
// streaming caret" is a pinned invariant. The TURN ending is the opposite case:
// the residue is the last text the model really produced, so it keeps flowing
// (end). These tests pin the primitive's half of that contract; the dispatcher's
// half is in messages-blocks.test.ts.
// ---------------------------------------------------------------------------

describe("the caret's two exits", { timeout: testTimeoutFor(FRAME_BUDGET_MS) }, () => {
  it("end() keeps the caret while a backlog remains", async () => {
    const b = buildAssistantBubble("", true);
    b.setText("d".repeat(400));
    b.end();
    expect(b.root.classList.contains("streaming")).toBe(true);
    await until(() => !b.root.classList.contains("streaming"));
  });

  it("finishNow() drops the caret in the same task", () => {
    const b = buildAssistantBubble("", true);
    b.setText("d".repeat(400));
    b.finishNow();
    expect(b.root.classList.contains("streaming")).toBe(false);
  });
});
