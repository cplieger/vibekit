// Find in the open file.
//
// The gap this closes was a trap, not an omission: Ctrl-F on a file tab routed to
// find-in-FILES, which activates the browser view — so the chord every editor
// binds to "search this document" navigated away from the document. Two things
// matter most here. The DECLINE: over an image there is no text, so the chord is
// handed back and native find opens. And the ANSWER: a hit over source is marked
// on its glyphs and selected in the textarea, a hit over a diff pane or rendered
// prose is a real <mark>, a buffer that has not arrived says nothing yet, and one
// that could not be read says so rather than "No matches".
import { describe, it, expect, vi, beforeEach } from "vitest";

const scrollToLine = vi.fn();
const flashLine = vi.fn();
const markSpan = vi.fn();
const clearMark = vi.fn();
vi.mock("./editor-scroll.js", () => ({
  scrollToEditorLine: (n: number, behavior: string) => scrollToLine(n, behavior),
  flashEditorLine: (n: number) => flashLine(n),
  markEditorSpan: (line: number, prefix: string, match: string) => markSpan(line, prefix, match),
  clearEditorMark: () => clearMark(),
}));

import { findInBuffer } from "./editor-find.js";
import type * as EditorFind from "./editor-find.js";
import type * as Bus from "./bus.js";
import { lineDiff } from "./diff.js";
import { renderDiffPane } from "./diff-pane.js";

/** Cache-buster for the re-imports below.
 *
 * `vi.resetModules()` does not re-evaluate a module in Browser Mode: the module
 * map is URL-keyed, so a following `await import()` hands back the CACHED
 * instance and every test after the first observes stale module state. Busting
 * the specifier per evaluation is what actually mints a fresh instance. The `.ts`
 * extension is load-bearing — written `.js` the suite still passes while coverage
 * silently attributes every evaluation to a file that does not exist.
 *
 * Only the module under test is busted. Its own dependencies keep their plain
 * specifiers, so `vi.mock` still intercepts them and a shared module the test
 * also imports is the same instance the fresh module got.
 */
let bootSeq = 0;

type EditorFindModule = typeof EditorFind;
type BusModule = typeof Bus;

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

  it("does not overlap occurrences, the one rule every search surface shares", () => {
    // `aa` in `aaa` is one hit here as it is in the transcript, the file search
    // and the server: a reader stepping through sees the same count everywhere.
    expect(findInBuffer("aaa", "aa", false)).toEqual([{ line: 1, offset: 0 }]);
  });

  it("keeps its offsets and lines under a fold that changes string length", () => {
    // U+0130 lowercases to two code units under `toLowerCase`, so a fold of the
    // whole buffer put every later hit N units off — ten of them walked the line
    // counter past a newline and the bar jumped to the wrong LINE.
    const text = `${"\u0130".repeat(10)}\n\ntarget`;
    expect(findInBuffer(text, "target", false)).toEqual([{ line: 3, offset: 12 }]);
    expect(findInBuffer("\u0130stanbul", "istanbul", false)).toEqual([{ line: 1, offset: 0 }]);
  });

  it("treats an empty needle as no matches, never as every position", () => {
    expect(findInBuffer("anything", "", false)).toEqual([]);
  });

  it("finds nothing in an empty buffer", () => {
    expect(findInBuffer("", "x", false)).toEqual([]);
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
  mode: "edit" | "editing" | "diff" | "image" | "conflict",
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
    case "conflict": {
      const { parseConflicts } = await import("./conflict.js");
      state.mode.value = { kind: "conflict", conflict: parseConflicts(content), editing: true };
      break;
    }
  }
  types.fileStates.set(path, state);
  types.setActiveFilePath(path);
}

/** What `showEditMode` does for the textarea: reveal it over the buffer. */
function showTextarea(content: string): HTMLTextAreaElement {
  document.getElementById("editor-highlight")?.classList.add("hidden");
  const ta = document.getElementById("editor-content") as HTMLTextAreaElement;
  ta.classList.remove("hidden");
  ta.value = content;
  return ta;
}

/** The editor's diff pane over a one-line change, with the option set
 *  editor-diff.ts passes, mounted into the pane's real host and revealed. */
function showDiffPane(): HTMLElement {
  const oldText = ["func alpha() {", '\treturn "old"', "}", ""].join("\n");
  const newText = ["func alpha() {", '\treturn "new"', "}", ""].join("\n");
  const host = document.getElementById("editor-diff-pane");
  if (host === null) {
    throw new Error("missing pane");
  }
  host.classList.remove("hidden");
  host.replaceChildren(
    renderDiffPane(lineDiff(oldText, newText), {
      oldLabel: "before",
      newLabel: "after",
      lineNumbers: true,
      syncScroll: true,
      lang: "x.go",
      source: { oldText, newText },
    }),
  );
  return host;
}

describe("the in-file find bar", () => {
  let mod: EditorFindModule;
  let bus: BusModule;

  beforeEach(async () => {
    vi.resetModules();
    bootSeq++;
    scrollToLine.mockReset();
    flashLine.mockReset();
    markSpan.mockReset();
    clearMark.mockReset();
    editorDOM();
    mod = (await import(
      /* @vite-ignore */ `./editor-find.ts?boot=${bootSeq}`
    )) as typeof EditorFind;
    // The SAME module registry `mod` came from, or the emit reaches a second copy
    // of the bus that this bar never subscribed to.
    bus = await import("./bus.js");
  });

  function input(): HTMLInputElement | null {
    return document.getElementById("editor-find-input") as HTMLInputElement | null;
  }

  function count(): string {
    return document.getElementById("editor-find-count")?.textContent ?? "";
  }

  function noResults(): boolean {
    return (
      document.querySelector(".editor-find")?.classList.contains("editor-find-no-results") ?? false
    );
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

  function enter(shift = false): void {
    input()?.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "Enter",
        shiftKey: shift,
        bubbles: true,
        cancelable: true,
      }),
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
    // An empty box is not a query: no count, no no-results tint.
    expect(count()).toBe("");
    expect(noResults()).toBe(false);
  });

  it("is a role=search landmark and finds the buffer's matches", async () => {
    await openFile("/workspace/a.go", "edit");
    mod.openEditorFind();
    expect(document.querySelector(".editor-find")?.getAttribute("role")).toBe("search");
    type("target");
    expect(count()).toBe("1 of 2");
  });

  it("takes the cursor to the matched LINE, marks the match, and steps through", async () => {
    await openFile("/workspace/a.go", "edit");
    mod.openEditorFind();
    type("target");
    // Synchronous: the surface is laid out under an open bar, and a held Enter
    // has to settle on the hit it stopped at rather than on a frame behind it.
    expect(scrollToLine).toHaveBeenLastCalledWith(3, "instant");
    expect(flashLine).toHaveBeenLastCalledWith(3);
    expect(markSpan).toHaveBeenLastCalledWith(3, "func ", "target");

    type("target"); // same query -> step
    expect(count()).toBe("2 of 2");
    expect(scrollToLine).toHaveBeenLastCalledWith(4, "instant");
    expect(markSpan).toHaveBeenLastCalledWith(4, "// ", "target");
  });

  it("tells two hits on ONE line apart by their column", async () => {
    // The old reveal flashed the line and nothing else, so Next between two hits
    // on one line moved nothing a reader could see.
    await openFile("/workspace/a.go", "edit", "alpha beta alpha\n");
    mod.openEditorFind();
    type("alpha");
    expect(count()).toBe("1 of 2");
    expect(markSpan).toHaveBeenLastCalledWith(1, "", "alpha");
    enter();
    expect(count()).toBe("2 of 2");
    expect(markSpan).toHaveBeenLastCalledWith(1, "alpha beta ", "alpha");
  });

  it("selects the match in the textarea while EDITING, so leaving the box lands on it", async () => {
    await openFile("/workspace/a.go", "editing");
    const ta = showTextarea(BUFFER);
    mod.openEditorFind();
    type("target");
    expect([ta.selectionStart, ta.selectionEnd]).toEqual([19, 25]);
    enter();
    expect([ta.selectionStart, ta.selectionEnd]).toEqual([34, 40]);
    // The find box keeps focus: Enter has to keep stepping.
    expect(document.activeElement).toBe(input());
  });

  it("wraps at the end and steps backwards on Shift+Enter", async () => {
    await openFile("/workspace/a.go", "edit");
    mod.openEditorFind();
    type("target");
    type("target");
    type("target"); // wraps
    expect(count()).toBe("1 of 2");
    enter(true);
    expect(count()).toBe("2 of 2");
  });

  it("settles on the hit a burst of steps stopped at", async () => {
    await openFile("/workspace/a.go", "edit", "t\nt\nt\n");
    mod.openEditorFind();
    type("t");
    for (let i = 0; i < 41; i++) {
      enter();
    }
    // 1 + 41 steps over three hits lands on the third.
    expect(count()).toBe("3 of 3");
    expect(markSpan).toHaveBeenCalledTimes(42);
    expect(markSpan).toHaveBeenLastCalledWith(3, "", "t");
    expect(scrollToLine).toHaveBeenLastCalledWith(3, "instant");
  });

  it("marks the no-results state rather than saying nothing", async () => {
    await openFile("/workspace/a.go", "edit");
    mod.openEditorFind();
    type("target");
    clearMark.mockReset();
    markSpan.mockReset();
    type("nowhere");
    expect(count()).toBe("No matches");
    expect(noResults()).toBe(true);
    // The previous query's mark comes down with its hits.
    expect(clearMark).toHaveBeenCalled();
    expect(markSpan).not.toHaveBeenCalled();
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

  it("DECLINES over an image only, because that is the one surface with no text", async () => {
    // It used to decline over rendered markdown and a diff pane too, on the
    // reasoning that neither has a fixed line height — true, and an argument
    // against LINE arithmetic rather than against searching. Both are rendered
    // DOM text, so they take the shared mark engine instead, and the chord stops
    // changing meaning on one tab depending on its mode.
    await openFile("/workspace/pic.png", "image");
    expect(mod.openEditorFind()).toBe(false);
    expect(document.querySelector(".editor-find")).toBeNull();
  });

  it("marks hits in rendered markdown across inline elements", async () => {
    await openFile("/workspace/notes.md", "edit");
    const host = document.getElementById("editor-markdown");
    if (host === null) {
      throw new Error("missing host");
    }
    host.classList.remove("hidden");
    host.innerHTML = "<p>alpha <code>beta alpha</code> gamma</p><p>beta</p>";
    expect(mod.openEditorFind()).toBe(true);
    type("alpha");
    expect(host.querySelectorAll("mark.find-hit")).toHaveLength(2);
    expect(count()).toBe("1 of 2");
    // A phrase across the inline boundary is one hit.
    type("alpha beta");
    expect(count()).toBe("1 of 1");
    expect([...host.querySelectorAll("mark.find-hit")].map((m) => m.textContent)).toEqual([
      "alpha ",
      "beta",
    ]);
    // The marks go with the box: one left behind is welded into the pane for the
    // rest of the session, and the next render would reconcile around it.
    mod.closeEditorFind();
    expect(host.querySelectorAll("mark.find-hit")).toHaveLength(0);
    expect(host.textContent, "the text comes back intact").toBe("alpha beta alpha gammabeta");
  });

  it("walks the real diff pane: token spans join, both columns count", async () => {
    await openFile("/workspace/a.go", "diff");
    const host = showDiffPane();
    expect(mod.openEditorFind()).toBe(true);
    // The highlighter splits `func alpha() {` into a keyword span, a text node and
    // punctuation spans; the line reads as one line, so the phrase is found.
    type("func alpha");
    // A context line is rendered in both columns, and the walker's principle, in
    // the user's words: we find text hits, we do not filter; it must be
    // predictable. Two columns, two hits.
    expect(count()).toBe("1 of 2");
    // The gutter's line numbers and the +/- markers are chrome, not content.
    type("1");
    expect(count()).toBe("No matches");
    type("new");
    expect(count()).toBe("1 of 1");
    expect(host.querySelector("mark.find-hit-current")?.closest(".diff-col-new")).not.toBeNull();

    // A mode swap under the open bar: the pane's marks go before the buffer is
    // searched, or they are welded into a pane the next render reconciles around.
    const types = await import("./editor-types.js");
    types.fileStates.get("/workspace/a.go")!.mode.value = { kind: "edit", editing: false };
    host.classList.add("hidden");
    type("target");
    expect(count()).toBe("1 of 2");
    expect(host.querySelectorAll("mark.find-hit")).toHaveLength(0);
    mod.closeEditorFind();
  });

  it("steps between marks on a rendered surface, wrapping like the transcript", async () => {
    await openFile("/workspace/a.go", "diff");
    const host = showDiffPane();
    mod.openEditorFind();
    type("alpha");
    expect(count()).toBe("1 of 2");
    const marks = [...host.querySelectorAll<HTMLElement>("mark.find-hit")];
    expect(marks).toHaveLength(2);
    expect(marks[0]?.classList.contains("find-hit-current")).toBe(true);
    // Enter on the query already searched steps rather than re-running.
    enter();
    expect(count()).toBe("2 of 2");
    expect(marks.map((m) => m.classList.contains("find-hit-current"))).toEqual([false, true]);
    enter();
    expect(count()).toBe("1 of 2");
    enter(true);
    expect(count()).toBe("2 of 2");
    expect(host.querySelectorAll("mark.find-hit-current")).toHaveLength(1);
    // No line geometry over a diff pane: the browser scrolls the mark itself.
    expect(scrollToLine).not.toHaveBeenCalled();
    expect(markSpan).not.toHaveBeenCalled();
    mod.closeEditorFind();
  });

  it("honours Match case over a rendered surface too", async () => {
    await openFile("/workspace/notes.md", "edit");
    const host = document.getElementById("editor-markdown");
    if (host === null) {
      throw new Error("missing host");
    }
    host.classList.remove("hidden");
    host.innerHTML = "<p>Target <em>target</em></p>";
    mod.openEditorFind();
    type("target");
    expect(count()).toBe("1 of 2");
    document.querySelector<HTMLButtonElement>(".editor-find-case")?.click();
    expect(count()).toBe("1 of 1");
    expect(host.querySelector("mark.find-hit")?.textContent).toBe("target");
  });

  it("searches the conflict overlay and the buffer as ONE list, in reading order", async () => {
    const conflicted = "a\n<<<<<<< HEAD\ntarget one\n=======\ntarget two\n>>>>>>> incoming\n";
    await openFile("/workspace/a.go", "conflict", conflicted);
    const ta = showTextarea(conflicted);
    const overlay = document.getElementById("editor-conflict-overlay");
    if (overlay === null) {
      throw new Error("missing overlay");
    }
    overlay.classList.remove("hidden");
    overlay.innerHTML =
      '<div class="conflict-status">1 unresolved conflict</div>' +
      '<div class="conflict-hunk-row"><span class="conflict-hunk-title">Line 2: HEAD vs incoming</span>' +
      '<span class="conflict-suggest-pill">AI suggestion</span></div>' +
      '<pre class="conflict-suggest-preview">merged Target here</pre>';
    mod.openEditorFind();

    // The merge suggestion is rendered text nobody could search before.
    type("merged");
    expect(count()).toBe("1 of 1");
    expect(overlay.querySelectorAll("mark.find-hit-current")).toHaveLength(1);
    expect(markSpan).not.toHaveBeenCalled();

    // One overlay hit sits above two buffer hits: the overlay comes first.
    type("target");
    expect(count()).toBe("1 of 3");
    expect(overlay.querySelector("mark.find-hit-current")?.textContent).toBe("Target");
    expect(markSpan).not.toHaveBeenCalled();
    enter();
    expect(count()).toBe("2 of 3");
    // The overlay keeps its highlight and loses its "current": one cursor.
    expect(overlay.querySelectorAll("mark.find-hit")).toHaveLength(1);
    expect(overlay.querySelectorAll("mark.find-hit-current")).toHaveLength(0);
    expect(markSpan).toHaveBeenLastCalledWith(3, "", "target");
    expect([ta.selectionStart, ta.selectionEnd]).toEqual([15, 21]);
    enter();
    expect(count()).toBe("3 of 3");
    expect(markSpan).toHaveBeenLastCalledWith(5, "", "target");
    clearMark.mockReset();
    enter();
    expect(count()).toBe("1 of 3");
    expect(overlay.querySelectorAll("mark.find-hit-current")).toHaveLength(1);
    // Back on the overlay, the buffer's mark is taken down.
    expect(clearMark).toHaveBeenCalledTimes(1);

    // Match case reaches the overlay's engine as it reaches the buffer's.
    document.querySelector<HTMLButtonElement>(".editor-find-case")?.click();
    expect(count()).toBe("1 of 2");
    expect(overlay.querySelectorAll("mark.find-hit")).toHaveLength(0);
    expect(markSpan).toHaveBeenLastCalledWith(3, "", "target");

    mod.closeEditorFind();
    expect(overlay.querySelectorAll("mark.find-hit")).toHaveLength(0);
    expect(overlay.textContent).toContain("merged Target here");
  });

  it("declines with no file open at all", async () => {
    const types = await import("./editor-types.js");
    types.setActiveFilePath("");
    expect(mod.openEditorFind()).toBe(false);
  });

  it("OPENS over a file whose bytes have not arrived, says nothing, and answers when they land", async () => {
    // Refusing to open was the old answer, and it made the chord do nothing at all
    // while a file read was in flight. "No matches" was the next answer, and it
    // was a claim about a file nobody had read. A load in flight is not an
    // answer, so the counter stays blank until the load path says the bytes are
    // in — and then the open box answers for them.
    const types = await import("./editor-types.js");
    const state = types.freshState("/workspace/slow.go");
    state.loaded = false;
    types.fileStates.set("/workspace/slow.go", state);
    types.setActiveFilePath("/workspace/slow.go");
    expect(mod.openEditorFind()).toBe(true);
    type("anything");
    expect(count()).toBe("");
    expect(noResults()).toBe(false);

    state.current.value = "here is anything\n";
    state.loaded = true;
    // Another file's load says nothing about this one.
    bus.emitBus(bus.BUS_EDITOR_FILE_LOADED, { path: "/workspace/other.go" });
    expect(count()).toBe("");
    bus.emitBus(bus.BUS_EDITOR_FILE_LOADED, { path: "/workspace/slow.go" });
    expect(count()).toBe("1 of 1");
    expect(markSpan).toHaveBeenLastCalledWith(1, "here is ", "anything");
    mod.closeEditorFind();
  });

  it("says the file was not read when the load failed, rather than No matches", async () => {
    // `failBufferLoad` sets the error AND `loaded`, with the buffer never adopted,
    // so the old `loaded` guard passed and the bar searched an empty string.
    const types = await import("./editor-types.js");
    const state = types.freshState("/workspace/big.bin");
    state.loaded = true;
    state.error.value = "Failed to load file";
    types.fileStates.set("/workspace/big.bin", state);
    types.setActiveFilePath("/workspace/big.bin");
    expect(mod.openEditorFind()).toBe(true);
    type("anything");
    expect(count()).toBe("File not read");
    expect(noResults()).toBe(true);
    expect(markSpan).not.toHaveBeenCalled();
    mod.closeEditorFind();
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

  it("closes on Escape, on the ×, and on a TAB SWITCH, taking the mark with it", async () => {
    for (const how of ["escape", "button", "tab"] as const) {
      vi.resetModules();
      bootSeq++;
      editorDOM();
      clearMark.mockReset();
      const fresh = (await import(
        /* @vite-ignore */ `./editor-find.ts?boot=${bootSeq}`
      )) as typeof EditorFind;
      const freshBus = await import("./bus.js");
      await openFile("/workspace/a.go", "edit");
      fresh.openEditorFind();
      const el = document.getElementById("editor-find-input") as HTMLInputElement;
      el.value = "target";
      el.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }),
      );
      expect(fresh._isEditorFindOpen(), how).toBe(true);
      clearMark.mockReset();

      if (how === "escape") {
        el.dispatchEvent(
          new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
        );
      } else if (how === "button") {
        document
          .querySelector<HTMLButtonElement>('.editor-find [aria-label="Close find"]')
          ?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      } else {
        freshBus.emitBus(freshBus.BUS_TAB_CHANGED, { to: "__files__", kind: "files" });
      }
      expect(fresh._isEditorFindOpen(), how).toBe(false);
      expect(document.getElementById("editor-find-count")?.textContent, how).toBe("");
      expect(clearMark, how).toHaveBeenCalled();
    }
  });

  it("toggles: the button closes an open bar rather than re-running it", async () => {
    await openFile("/workspace/a.go", "edit");
    mod.toggleEditorFind();
    expect(mod._isEditorFindOpen()).toBe(true);
    mod.toggleEditorFind();
    expect(mod._isEditorFindOpen()).toBe(false);
  });

  it("FORGETS the query on a tab switch, so the next file's find opens empty", async () => {
    // One bar serves every editor tab, so a retained query searched the NEXT file
    // for a string typed against the previous one and reported a count for it.
    // Closing the bar alone left the text in place, and the open path re-runs.
    await openFile("/workspace/a.go", "edit", "target here\n");
    mod.openEditorFind();
    type("target");
    const el = document.getElementById("editor-find-input") as HTMLInputElement;
    expect(el.value).toBe("target");
    bus.emitBus(bus.BUS_TAB_CHANGED, { to: "editor:/workspace/b.go", kind: "editor" });
    expect(el.value, "a retained query is inherited by the next editor tab").toBe("");
  });

  it("re-runs on the Aa toggle without retyping, and honours case", async () => {
    await openFile("/workspace/a.go", "edit", "Target\ntarget\n");
    mod.openEditorFind();
    type("target");
    expect(count()).toBe("1 of 2");
    document.querySelector<HTMLButtonElement>(".editor-find-case")?.click();
    expect(count()).toBe("1 of 1");
    expect(markSpan).toHaveBeenLastCalledWith(2, "", "target");
  });
});
