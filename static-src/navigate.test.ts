// @vitest-environment happy-dom
// ---------------------------------------------------------------------------
// Tests for navigate.ts — the seam router.
//
// Each case pins WHICH surface a subject routes to, because that is the decision
// the module exists to centralise. Four call sites used to pick their own opener
// for the same intent (a changed filename in a tool card, the same filename in
// the turn ledger, a turn-approval row in the dock, and soon a file-browser row)
// and they had already started to disagree.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach } from "vitest";

const calls: string[] = [];
vi.mock("./editor-openers.js", () => ({
  openFile: (p: string, line?: number) => {
    calls.push(`file:${p}:${String(line)}`);
  },
  openFileGitDiff: (p: string, ref: string) => {
    calls.push(`gitdiff:${p}:${ref}`);
  },
}));
vi.mock("./tabs.js", () => ({
  toggleGitView: (tab: string) => {
    calls.push(`gitview:${tab}`);
  },
}));
vi.mock("./git-tabs.js", () => ({
  setGitTab: (tab: string) => {
    calls.push(`gittab:${tab}`);
  },
}));

import { openChange, openChangeSet, openAtLine, openExternal } from "./navigate.js";

beforeEach(() => {
  calls.length = 0;
});

describe("openChange", () => {
  it("opens the file's diff against HEAD", () => {
    // vs HEAD is the honest source: the write already landed, so the working
    // tree IS the after state and git holds the before.
    openChange("src/a.ts");
    expect(calls).toEqual(["gitdiff:src/a.ts:HEAD"]);
  });

  it("honours an explicit ref", () => {
    openChange("src/a.ts", "origin/main");
    expect(calls).toEqual(["gitdiff:src/a.ts:origin/main"]);
  });

  it("does nothing for an empty path", () => {
    openChange("");
    expect(calls).toEqual([]);
  });
});

describe("openChangeSet", () => {
  it("routes the multi-file review to the git view's changes tab", () => {
    // Not a bespoke turn-scoped viewer: the ladder's rule is that depth 2 lands
    // in a surface that already exists, and the git view already lists every
    // changed file and opens each one's diff.
    openChangeSet();
    expect(calls).toEqual(["gittab:changes", "gitview:changes"]);
  });
});

describe("openAtLine", () => {
  it("passes the line through", () => {
    openAtLine("src/a.ts", 42);
    expect(calls).toEqual(["file:src/a.ts:42"]);
  });

  it("opens without a line when none is given", () => {
    openAtLine("src/a.ts");
    expect(calls).toEqual(["file:src/a.ts:undefined"]);
  });

  it("does nothing for an empty path", () => {
    openAtLine("");
    expect(calls).toEqual([]);
  });
});

describe("openExternal", () => {
  it("refuses a URL that is not http(s), and says so", () => {
    const open = vi.fn();
    vi.stubGlobal("open", open);
    expect(openExternal("javascript:alert(1)")).toBe(false);
    expect(open).not.toHaveBeenCalled();
  });

  it("opens an https URL with noopener", () => {
    const open = vi.fn();
    vi.stubGlobal("open", open);
    expect(openExternal("https://example.com")).toBe(true);
    expect(open).toHaveBeenCalledWith("https://example.com", "_blank", "noopener,noreferrer");
  });
});
