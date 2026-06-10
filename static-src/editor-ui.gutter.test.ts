// @vitest-environment happy-dom
import { describe, it, expect, vi } from "vitest";

// updateGutter is the sole owner of the gutter DOM via a keyed reconcile.
// These mocks isolate it from the editor's heavy import graph; only
// $.editorGutter matters for the gutter path. agentLines is injected
// explicitly (the optional second param) so the reconcile behaviour is
// observable without seeding module-private agent-line caches.

vi.mock("./dom.js", () => ({
  $: { editorGutter: document.createElement("pre") },
}));

vi.mock("./highlight.js", () => ({
  highlight: () => "",
}));

vi.mock("./store.js", () => ({
  getActiveId: () => "",
}));

vi.mock("./actions/editor.js", () => ({
  fetchAgentLines: {
    cancel: () => {
      /* noop */
    },
    dispatch: () => Promise.resolve(null),
  },
}));

vi.mock("./editor-scroll.js", () => ({
  scrollToEditorLine: () => {
    /* noop */
  },
  flashEditorLine: () => {
    /* noop */
  },
}));

import { $ } from "./dom.js";
import { updateGutter } from "./editor-ui.js";

function content(lineCount: number): string {
  return Array.from({ length: lineCount }, (_, i) => `L${String(i + 1)}`).join("\n");
}

function rows(): NodeListOf<HTMLElement> {
  return $.editorGutter.querySelectorAll<HTMLElement>(".gutter-line");
}

describe("updateGutter file-switch reconcile (no manual clear)", () => {
  it("shrinks rows, clears stale agent markers, and preserves row identity", () => {
    // File A: 100 lines, agent-modified on 5 and 6.
    updateGutter(content(100), new Set([5, 6]));

    const rowsA = rows();
    expect(rowsA.length).toBe(100);
    expect(rowsA.item(4).classList.contains("gutter-agent-modified")).toBe(true); // line 5
    expect(rowsA.item(5).classList.contains("gutter-agent-modified")).toBe(true); // line 6
    const row1Before = rowsA.item(0); // capture persisting row identity

    // Switch to File B: 80 lines, agent-modified on 5 only. No manual clear.
    updateGutter(content(80), new Set([5]));

    const rowsB = rows();
    // Surplus rows (81..100) removed by reconcile.
    expect(rowsB.length).toBe(80);
    expect(rowsB.item(79).textContent).toBe("80");
    // Line 5 still marked; line 6's stale marker is cleared by the
    // unconditional `update` toggle (the whole point of dropping
    // replaceChildren and rebuildGutter).
    expect(rowsB.item(4).classList.contains("gutter-agent-modified")).toBe(true); // line 5
    expect(rowsB.item(5).classList.contains("gutter-agent-modified")).toBe(false); // line 6 cleared
    // Persisting row (line 1) is the SAME DOM node across both calls.
    expect(rowsB.item(0)).toBe(row1Before);
  });
});
