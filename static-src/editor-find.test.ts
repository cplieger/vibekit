// @vitest-environment happy-dom
// Find in the open file's buffer.
//
// The gap this closes was a trap, not an omission: Ctrl-F on a file tab routed to
// find-in-FILES, which activates the browser view — so the chord every editor
// binds to "search this document" navigated away from the document. What matters
// most here is the DECLINE: over a diff pane, an image or rendered markdown there
// is no line geometry, so a match this find counted could not be reached, and it
// hands the key back rather than shipping a control that does nothing.
import { describe, it, expect, vi, beforeEach } from "vitest";

const scrollToLine = vi.fn();
const flashLine = vi.fn();
vi.mock("./editor-scroll.js", () => ({
  scrollToEditorLine: (n: number) => scrollToLine(n),
  flashEditorLine: (n: number) => flashLine(n),
}));

import { findInBuffer, formatBufferCount } from "./editor-find.js";
import type * as EditorFind from "./editor-find.js";

type EditorFindModule = typeof EditorFind;

describe("findInBuffer", () => {
  it("reports the 1-based line of every hit", () => {
    const text = "alpha\nbeta target\ngamma\ntarget again";
    expect(findInBuffer(text, "target", false)).toEqual([
      { line: 2, offset: 11 },
      { line: 4, offset: 24 },
    ]);
  });

  it("counts lines across a hit that is not on line 1", () => {
    // The running newline count is the part worth guarding: it advances a cursor
    // rather than splitting the buffer, so an off-by-one shows up as a jump to
    // the wrong line — which is the whole point of the feature.
    expect(findInBuffer("a\n\n\nz", "z", false)).toEqual([{ line: 4, offset: 4 }]);
  });

  it("folds by default and matches verbatim under case sensitivity", () => {
    expect(findInBuffer("TODO todo", "todo", false)).toHaveLength(2);
    expect(findInBuffer("TODO todo", "todo", true)).toEqual([{ line: 1, offset: 5 }]);
    expect(findInBuffer("TODO todo", "TODO", true)).toEqual([{ line: 1, offset: 0 }]);
  });

  it("finds OVERLAPPING occurrences, the convention a text editor's find uses", () => {
    // Advancing past the whole match would report one hit in "aaa"; a reader
    // stepping through expects two.
    expect(findInBuffer("aaa", "aa", false)).toEqual([
      { line: 1, offset: 0 },
      { line: 1, offset: 1 },
    ]);
  });

  it("treats an empty needle as no matches, never as every position", () => {
    expect(findInBuffer("anything", "", false)).toEqual([]);
  });

  it("finds nothing in an empty buffer", () => {
    expect(findInBuffer("", "x", false)).toEqual([]);
  });
});

describe("formatBufferCount", () => {
  it("is empty for an empty query and 1-based for humans", () => {
    expect(formatBufferCount(0, -1, "")).toBe("");
    expect(formatBufferCount(3, 0, "x")).toBe("1 of 3");
    expect(formatBufferCount(3, 2, "x")).toBe("3 of 3");
  });

  it("says No matches rather than 0 of 0", () => {
    expect(formatBufferCount(0, -1, "x")).toBe("No matches");
  });
});

// ---------------------------------------------------------------------------
// The bar, against a real editor DOM and a real FileState.
// ---------------------------------------------------------------------------

const BUFFER = "package main\n\nfunc target() {}\n// target again\n";

function editorDOM(): void {
  document.body.innerHTML = `
    <div id="editor-view" data-tab-view>
      <div class="editor-page">
        <div id="editor-error" class="editor-error hidden"></div>
        <div id="editor-conflict-overlay" class="editor-conflict-overlay hidden"></div>
        <div class="editor-body">
          <pre id="editor-gutter"></pre>
          <pre id="editor-highlight"><code id="editor-code"></code></pre>
          <textarea id="editor-content" class="hidden"></textarea>
          <div id="editor-markdown" class="hidden"></div>
          <div id="editor-image" class="hidden"></div>
          <div id="editor-diff-pane" class="hidden"></div>
        </div>
      </div>
    </div>`;
}

/** Register one open file in the editor's real state, in the given mode. */
async function openFile(
  path: string,
  mode: "edit" | "editing" | "diff" | "image",
  content = BUFFER,
): Promise<void> {
  const types = await import("./editor-types.js");
  const state = types.freshState(path);
  state.loaded = true;
  state.current.value = content;
  state.original.value = content;
  switch (mode) {
    case "edit":
      state.mode.value = { kind: "edit", editing: false };
      break;
    case "editing":
      state.mode.value = { kind: "edit", editing: true };
      break;
    case "diff":
      state.mode.value = {
        kind: "diff",
        // fromGit false: both sides are in memory here. True would send the left
        // pane to GET /api/git/show, which this suite neither needs nor stubs.
        diffSource: {
          oldContent: "",
          newContent: content,
          oldLabel: "a",
          newLabel: "b",
          fromGit: false,
        },
      };
      break;
    case "image":
      state.mode.value = { kind: "image" };
      break;
  }
  types.fileStates.set(path, state);
  types.setActiveFilePath(path);
}

describe("the in-file find bar", () => {
  let mod: EditorFindModule;

  beforeEach(async () => {
    vi.resetModules();
    scrollToLine.mockReset();
    flashLine.mockReset();
    editorDOM();
    mod = await import("./editor-find.js");
  });

  function input(): HTMLInputElement | null {
    return document.getElementById("editor-find-input") as HTMLInputElement | null;
  }

  function count(): string {
    return document.getElementById("editor-find-count")?.textContent ?? "";
  }

  function type(value: string): void {
    const el = input();
    if (el === null) {
      throw new Error("find input not built");
    }
    el.value = value;
    el.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }),
    );
  }

  it("docks IN FLOW between the conflict overlay and the scroller", async () => {
    // Not floating: `.editor-body` is the scroller, so a bar in the flex column
    // shrinks it and covers no line — where a floating box would sit over the
    // first lines, which on a jump-to-match is exactly where the reader looks.
    await openFile("/workspace/a.go", "edit");
    expect(mod.openEditorFind()).toBe(true);
    const bar = document.querySelector(".editor-find");
    expect(bar).not.toBeNull();
    expect(bar?.previousElementSibling?.id).toBe("editor-conflict-overlay");
    expect(bar?.nextElementSibling?.classList.contains("editor-body")).toBe(true);
  });

  it("is a role=search landmark and finds the buffer's matches", async () => {
    await openFile("/workspace/a.go", "edit");
    mod.openEditorFind();
    expect(document.querySelector(".editor-find")?.getAttribute("role")).toBe("search");
    type("target");
    expect(count()).toBe("1 of 2");
  });

  it("takes the cursor to the matched LINE, and steps through", async () => {
    await openFile("/workspace/a.go", "edit");
    mod.openEditorFind();
    type("target");
    // Two frames of wait: the surface must be laid out before its geometry is
    // read, the same reason editor-ui.ts's applyPendingLine defers.
    await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(() => r(null))));
    expect(scrollToLine).toHaveBeenLastCalledWith(3);
    expect(flashLine).toHaveBeenLastCalledWith(3);

    type("target"); // same query -> step
    await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(() => r(null))));
    expect(count()).toBe("2 of 2");
    expect(scrollToLine).toHaveBeenLastCalledWith(4);
  });

  it("wraps at the end and steps backwards on Shift+Enter", async () => {
    await openFile("/workspace/a.go", "edit");
    mod.openEditorFind();
    type("target");
    type("target");
    type("target"); // wraps
    expect(count()).toBe("1 of 2");
    const el = input();
    el?.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "Enter",
        shiftKey: true,
        bubbles: true,
        cancelable: true,
      }),
    );
    expect(count()).toBe("2 of 2");
  });

  it("marks the no-results state rather than saying nothing", async () => {
    await openFile("/workspace/a.go", "edit");
    mod.openEditorFind();
    type("nowhere");
    expect(count()).toBe("No matches");
    expect(
      document.querySelector(".editor-find")?.classList.contains("editor-find-no-results"),
    ).toBe(true);
  });

  it("searches the UNSAVED buffer, which is what the reader is looking at", async () => {
    // A server-side grep would answer about the saved file. `FileState.current`
    // is the live buffer, so an edit is findable before it is written.
    await openFile("/workspace/a.go", "editing", "one\ntwo\n");
    const types = await import("./editor-types.js");
    types.fileStates.get("/workspace/a.go")!.current.value = "one\ntwo\ninserted\n";
    mod.openEditorFind();
    type("inserted");
    expect(count()).toBe("1 of 1");
  });

  it("finds in a .md file being EDITED, where the source is on screen", async () => {
    await openFile("/workspace/notes.md", "editing", "# Title\nfindme\n");
    expect(mod.openEditorFind()).toBe(true);
    type("findme");
    expect(count()).toBe("1 of 1");
  });

  it("DECLINES over rendered markdown, a diff pane and an image", async () => {
    // No fixed line height in any of the three, so a counted match could not be
    // reached — and the browser's own find reads all three perfectly well.
    const cases: { path: string; mode: "edit" | "diff" | "image"; why: string }[] = [
      { path: "/workspace/notes.md", mode: "edit", why: "rendered markdown reflows" },
      { path: "/workspace/a.go", mode: "diff", why: "the diff pane has its own rows" },
      { path: "/workspace/pic.png", mode: "image", why: "an image has no lines" },
    ];
    for (const c of cases) {
      vi.resetModules();
      editorDOM();
      const fresh = await import("./editor-find.js");
      await openFile(c.path, c.mode);
      expect(fresh.openEditorFind(), c.why).toBe(false);
      expect(document.querySelector(".editor-find"), c.why).toBeNull();
    }
  });

  it("declines with no file open at all", async () => {
    const types = await import("./editor-types.js");
    types.setActiveFilePath("");
    expect(mod.openEditorFind()).toBe(false);
  });

  it("declines for a file whose content has not loaded yet", async () => {
    const types = await import("./editor-types.js");
    const state = types.freshState("/workspace/slow.go");
    state.loaded = false;
    types.fileStates.set("/workspace/slow.go", state);
    types.setActiveFilePath("/workspace/slow.go");
    expect(mod.openEditorFind()).toBe(false);
  });

  it("hands the chord back when it declines, so native find opens", async () => {
    await openFile("/workspace/pic.png", "image");
    const e = new KeyboardEvent("keydown", { key: "f", ctrlKey: true, cancelable: true });
    expect(mod.handleEditorFindHotkey(e)).toBe(false);
    expect(e.defaultPrevented).toBe(false);
  });

  it("claims the chord and pre-empts native find over a source buffer", async () => {
    await openFile("/workspace/a.go", "edit");
    const e = new KeyboardEvent("keydown", { key: "f", ctrlKey: true, cancelable: true });
    expect(mod.handleEditorFindHotkey(e)).toBe(true);
    expect(e.defaultPrevented).toBe(true);
    expect(mod._isEditorFindOpen()).toBe(true);
  });

  it("lets a SECOND press fall through, the escape hatch every destination owns", async () => {
    await openFile("/workspace/a.go", "edit");
    mod.openEditorFind();
    input()?.focus();
    const second = new KeyboardEvent("keydown", { key: "f", ctrlKey: true, cancelable: true });
    expect(mod.handleEditorFindHotkey(second)).toBe(true);
    expect(second.defaultPrevented, "a repeat press must reach the browser").toBe(false);
  });

  it("ignores a chord that is not Ctrl/Cmd-F", async () => {
    await openFile("/workspace/a.go", "edit");
    for (const e of [
      new KeyboardEvent("keydown", { key: "g", ctrlKey: true, cancelable: true }),
      new KeyboardEvent("keydown", { key: "f", cancelable: true }),
      new KeyboardEvent("keydown", { key: "f", ctrlKey: true, shiftKey: true, cancelable: true }),
      new KeyboardEvent("keydown", { key: "f", ctrlKey: true, altKey: true, cancelable: true }),
    ]) {
      expect(mod.handleEditorFindHotkey(e)).toBe(false);
      expect(e.defaultPrevented).toBe(false);
    }
  });

  it("closes on Escape, on the ×, and on a TAB SWITCH", async () => {
    for (const how of ["escape", "button", "tab"] as const) {
      vi.resetModules();
      editorDOM();
      const fresh = await import("./editor-find.js");
      const bus = await import("./bus.js");
      await openFile("/workspace/a.go", "edit");
      fresh.openEditorFind();
      const el = document.getElementById("editor-find-input") as HTMLInputElement;
      el.value = "target";
      el.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }),
      );
      expect(fresh._isEditorFindOpen(), how).toBe(true);

      if (how === "escape") {
        el.dispatchEvent(
          new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
        );
      } else if (how === "button") {
        document
          .querySelector<HTMLButtonElement>('.editor-find [aria-label="Close find"]')
          ?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      } else {
        bus.emitBus(bus.BUS_TAB_CHANGED, { to: "__files__", kind: "files" });
      }
      expect(fresh._isEditorFindOpen(), how).toBe(false);
      expect(document.getElementById("editor-find-count")?.textContent, how).toBe("");
    }
  });

  it("toggles: the button closes an open bar rather than re-running it", async () => {
    await openFile("/workspace/a.go", "edit");
    mod.toggleEditorFind();
    expect(mod._isEditorFindOpen()).toBe(true);
    mod.toggleEditorFind();
    expect(mod._isEditorFindOpen()).toBe(false);
  });

  it("re-runs on the Aa toggle without retyping, and honours case", async () => {
    await openFile("/workspace/a.go", "edit", "Target\ntarget\n");
    mod.openEditorFind();
    type("target");
    expect(count()).toBe("1 of 2");
    document.querySelector<HTMLButtonElement>(".editor-find-case")?.click();
    expect(count()).toBe("1 of 1");
  });
});
