import { describe, it, expect, vi } from "vitest";

// updateGutter is the sole owner of the gutter DOM via a keyed reconcile.
// These mocks isolate it from the editor's heavy import graph; only
// $.editorGutter matters for the gutter path. agentLines is injected
// explicitly (the optional second param) so the reconcile behaviour is
// observable without seeding module-private agent-line caches.

// The gutter is what these tests are about; the other five surfaces are here so
// the exported mode helpers can be driven, since each one decides whether the
// gutter belongs beside what it is showing.
vi.mock("./dom.js", () => ({
  $: {
    editorGutter: document.createElement("pre"),
    editorHighlight: document.createElement("pre"),
    editorContent: document.createElement("textarea"),
    editorDiffPane: document.createElement("div"),
    editorMarkdown: document.createElement("div"),
    editorImage: document.createElement("div"),
  },
}));

vi.mock("./highlight.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  highlightByLang: undefined,
  normalizeLang: undefined,
  highlight: () => "",
}));

// The tab projection widened this graph: the tab factory reads the chat store for a
// chat tab's display NAME, and chat.ts's store effect reads the rest. Present-but-
// inert so real-ESM linking succeeds — no tab is materialized here.
vi.mock("./store.js", () => ({
  getActiveId: () => "",
  get: vi.fn(() => undefined),
  getActive: vi.fn(() => undefined),
  getSessions: vi.fn(() => []),
  tabStatusFor: vi.fn(() => ""),
  setActive: vi.fn(),
  removeChat: vi.fn(),
  upsertHeader: vi.fn(),
  clearTurnDone: vi.fn(),
  activeSession: { value: undefined },
}));

vi.mock("./actions/editor.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  loadDiff: undefined,
  suggestResolution: undefined,
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
import { updateGutter, showReadMode, showEditMode, showDiffMode } from "./editor-ui.js";

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

// Which surfaces the gutter belongs beside. The diff pane renders its own
// old/new line numbers per row, so leaving the source gutter up put a second,
// stale column next to them counting the file as it was before the comparison.
// showDiffMode was the one mode helper that never touched the gutter at all.
describe("the gutter's visibility per mode", () => {
  const gutterHidden = (): boolean => $.editorGutter.classList.contains("hidden");

  it("shows the gutter beside source, read and edit alike", () => {
    showDiffMode();
    showReadMode();
    expect(gutterHidden()).toBe(false);
    showDiffMode();
    showEditMode();
    expect(gutterHidden()).toBe(false);
  });

  it("hides the gutter in diff mode", () => {
    showReadMode();
    expect(gutterHidden()).toBe(false);
    showDiffMode();
    expect(gutterHidden()).toBe(true);
  });

  it("brings the gutter back when the diff is left", () => {
    showDiffMode();
    expect(gutterHidden()).toBe(true);
    showReadMode();
    expect(gutterHidden()).toBe(false);
  });

  it("leaves the diff pane the only visible surface", () => {
    showDiffMode();
    expect($.editorDiffPane.classList.contains("hidden")).toBe(false);
    for (const surface of [$.editorHighlight, $.editorContent, $.editorMarkdown, $.editorImage]) {
      expect(surface.classList.contains("hidden")).toBe(true);
    }
  });
});
