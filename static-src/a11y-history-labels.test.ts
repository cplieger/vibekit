// A history row's accessible names, in its own file for a MODULE-GRAPH reason:
// this suite mounts the REAL history.js over stubbed actions, and in
// a11y-labels.test.ts that arrangement leaned on `vi.doMock` taking effect for
// modules an earlier sibling had already imported — which it never does (a
// mocked module is evaluated once and cached). The lean broke the day tabs.ts
// grew a real edge to actions/chat.js (the optimistic close's composer
// restore), because the settings-tab case imports tabs.js first. File-level
// mocks are hoisted before any import, so here the stubs always take.
import { describe, it, expect, vi } from "vitest";

// Hoisted alongside the vi.mock factories that reference it — a plain const
// would be read before its initialization when the factories run.
const { noop } = vi.hoisted(() => ({
  noop: (): void => {
    /* noop */
  },
}));

vi.mock("./actions/chat.js", () => ({
  loadSessions: {
    dispatch: () =>
      Promise.resolve({
        sessions: [],
        runs: [{ workflow_id: "wf_a11y", name: "nightly-sweep", status: "failed", updated_at: 1 }],
      }),
    cancel: noop,
  },
  deleteChat: { dispatch: noop },
}));
vi.mock("./actions/chat-search.js", () => ({
  searchChats: { dispatch: noop, cancel: noop },
}));
// The row's delete control: two actions plus the confirm dialog, stubbed for
// the same reason the others are — actions/index.js is reduced to one symbol
// here, so an unmocked action def cannot build.
vi.mock("./actions/runs.js", () => ({ deleteRun: { dispatch: noop } }));
vi.mock("./confirm.js", () => ({ confirm: async () => false }));
vi.mock("./actions/index.js", () => ({ registerCleanup: noop }));
vi.mock("./chat.js", () => ({ openPreviousSession: noop, openChatTab: noop }));
vi.mock("./run-view.js", () => ({ openRunView: noop }));
vi.mock("./tabs.js", () => ({
  // The toggle takes no callback now: the tab factory owns the page's loader.
  // The suite mounts the page itself, so this only has to resolve.
  toggleHistoryView: () => Promise.resolve(),
  hasTab: () => false,
}));
vi.mock("@cplieger/ui-primitives/skeleton", () => ({
  skeletonTiming: () => ({ cancel: noop }),
}));
vi.mock("./editor-openers.js", () => ({
  openFileDiff: noop,
  openFile: undefined,
  openFileGitDiff: undefined,
}));
vi.mock("./navigate.js", () => ({ openChange: noop, openAtLine: noop }));
vi.mock("./scroll.js", () => ({
  setUserScrolledUp: noop,
  preserveReadingPosition: (fn: () => void) => {
    fn();
  },
}));
vi.mock("./tool-group.js", () => ({ trackInProgress: noop }));

import { loadHistoryView } from "./history.js";

describe("a11y: History row accessible names", () => {
  // A history row's OPEN control is a real button whose name says what a click
  // does ("Open X"); the row itself carries no role, because a role="button" on it
  // is Children-Presentational and flattens the delete button beside it out of the
  // accessibility tree (axe nested-interactive, serious, every row).
  // A settled run also states its OUTCOME, and that outcome is a glyph — so the
  // word has exactly one home, that control's accessible name, and must not be
  // duplicated as visible text beside the glyph it replaced.
  it("a settled run row names the outcome once, in the accessible name only", async () => {
    const host = document.createElement("div");
    host.id = "history-table";
    document.body.appendChild(host);

    loadHistoryView();
    await vi.waitFor(() => {
      if (host.querySelector("[data-key]") === null) {
        throw new Error("not rendered");
      }
    });

    const row = host.querySelector<HTMLElement>('[data-key="r:wf_a11y"]')!;
    // The row is a plain container; its two controls are siblings.
    expect(row.getAttribute("role")).toBeNull();
    expect(row.getAttribute("tabindex")).toBeNull();
    const open = row.querySelector<HTMLElement>("button.list-row-name")!;
    // The name still opens with the action, then states the verdict.
    expect(open.getAttribute("aria-label")).toBe("Open nightly-sweep, failed");
    // The slot carrying the mark is decorative: the name already says the word.
    expect(row.querySelector(".tool-icon")?.getAttribute("aria-hidden")).toBe("true");
    expect(row.textContent).not.toContain("failed");

    document.body.removeChild(host);
  });
});
