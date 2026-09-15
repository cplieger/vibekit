// The editor pane's position cues, measured against the shipped stylesheet in a
// real browser: the flash, the find's mark, and the instant scroll a find step
// takes. Geometry is asserted against the browser's own glyph boxes (a Range
// over the rendered text), never against the arithmetic under test.
import { describe, it, expect, beforeAll, afterAll, beforeEach, afterEach, vi } from "vitest";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import {
  clearEditorMark,
  flashEditorLine,
  markEditorSpan,
  scrollToEditorLine,
} from "./editor-scroll.js";

let sheet: HTMLStyleElement;
let sizing: HTMLStyleElement;

beforeAll(() => {
  sheet = mountAppCSS();
  // The view is a flex child of the app shell; here it needs a box of its own for
  // `.editor-body` to have anything to scroll inside.
  sizing = document.createElement("style");
  sizing.textContent = `[id="editor-view"] { width: 480px; height: 240px; }`;
  document.head.appendChild(sizing);
});

afterAll(() => {
  sheet.remove();
  sizing.remove();
});

beforeEach(() => {
  document.body.innerHTML = `
    <div id="editor-view" data-tab-view>
      <div class="editor-page">
        <div id="editor-error" class="editor-error hidden"></div>
        <div id="editor-conflict-overlay" class="editor-conflict-overlay hidden"></div>
        <div class="editor-body">
          <pre id="editor-gutter" class="editor-gutter" aria-hidden="true"></pre>
          <pre id="editor-highlight" class="editor-highlight"><code id="editor-code"></code></pre>
          <textarea id="editor-content" class="editor-content hidden" aria-label="File editor"></textarea>
          <div id="editor-markdown" class="editor-markdown hidden"></div>
          <div id="editor-image" class="editor-image hidden"></div>
          <div id="editor-diff-pane" class="editor-diff-pane hidden"></div>
        </div>
      </div>
    </div>`;
});

afterEach(() => {
  clearEditorMark();
  vi.useRealTimers();
});

function body(): HTMLElement {
  return document.querySelector<HTMLElement>(".editor-body")!;
}

function code(): HTMLElement {
  return document.getElementById("editor-code")!;
}

function textarea(): HTMLTextAreaElement {
  return document.getElementById("editor-content") as HTMLTextAreaElement;
}

/** Paint `buffer` on the read surface as one text node, and hand back the
 *  browser's own box for `[from, to)` of it. */
function paint(buffer: string): (from: number, to: number) => DOMRect {
  const node = document.createTextNode(buffer);
  code().replaceChildren(node);
  return (from, to) => {
    const r = document.createRange();
    r.setStart(node, from);
    r.setEnd(node, to);
    return r.getBoundingClientRect();
  };
}

function markRect(): DOMRect {
  const mark = document.querySelector<HTMLElement>(".editor-find-mark");
  if (mark === null) {
    throw new Error("no mark on the pane");
  }
  return mark.getBoundingClientRect();
}

function showEdit(buffer: string): void {
  document.getElementById("editor-highlight")!.classList.add("hidden");
  const ta = textarea();
  ta.classList.remove("hidden");
  ta.value = buffer;
}

describe("markEditorSpan", () => {
  it("covers the matched glyphs on the read surface, by the browser's own measure", () => {
    const buffer = "package main\n\nfunc target() {}\n// target again\n";
    const box = paint(buffer);
    markEditorSpan(3, "func ", "target");
    const want = box(19, 25);
    const got = markRect();
    expect(Math.abs(got.left - want.left)).toBeLessThan(1);
    expect(Math.abs(got.width - want.width)).toBeLessThan(1);
    // The mark is the line's full height; the glyph box sits centred inside it.
    expect(got.top).toBeLessThanOrEqual(want.top + 0.5);
    expect(got.bottom).toBeGreaterThanOrEqual(want.bottom - 0.5);
    expect(Math.abs((got.top + got.bottom) / 2 - (want.top + want.bottom) / 2)).toBeLessThan(1);
  });

  it("expands tabs from the line start, so an indented match lands on its glyphs", () => {
    const buffer = "x\n\t\tfoo bar\n";
    const box = paint(buffer);
    markEditorSpan(2, "\t\tfoo ", "bar");
    const want = box(8, 11);
    const got = markRect();
    expect(Math.abs(got.left - want.left)).toBeLessThan(1);
    expect(Math.abs(got.width - want.width)).toBeLessThan(1);
  });

  it("moves one element between steps rather than adding one per step", () => {
    paint("a\nb\nc\n");
    markEditorSpan(1, "", "a");
    const first = markRect();
    markEditorSpan(3, "", "c");
    expect(document.querySelectorAll(".editor-find-mark")).toHaveLength(1);
    expect(markRect().top).toBeGreaterThan(first.top);
    clearEditorMark();
    expect(document.querySelectorAll(".editor-find-mark")).toHaveLength(0);
  });

  it("sits over the textarea's glyphs in edit mode, past its start border", () => {
    const buffer = "package main\n\nfunc target() {}\n";
    paint(buffer);
    markEditorSpan(3, "func ", "target");
    const read = markRect();
    showEdit(buffer);
    markEditorSpan(3, "func ", "target");
    const edit = markRect();
    // Same line, same width; the textarea draws a start border the pre does not.
    expect(edit.top).toBe(read.top);
    expect(Math.abs(edit.width - read.width)).toBeLessThan(1);
    expect(edit.left - read.left).toBe(textarea().clientLeft);
    expect(textarea().clientLeft).toBeGreaterThan(0);
  });

  it("pans the textarea's own scroller to a far column, and the mark follows", () => {
    const line = `${"x".repeat(200)} needle`;
    showEdit(`${line}\n`);
    const ta = textarea();
    expect(ta.scrollLeft).toBe(0);
    markEditorSpan(1, `${"x".repeat(200)} `, "needle");
    expect(ta.scrollLeft).toBeGreaterThan(0);
    const got = markRect();
    const view = ta.getBoundingClientRect();
    expect(got.left).toBeGreaterThanOrEqual(view.left);
    expect(got.right).toBeLessThanOrEqual(view.right);
  });

  it("pans the pane to a far column on the read surface", () => {
    const prefix = `${"x".repeat(200)} `;
    paint(`${prefix}needle\n`);
    const scroller = body();
    expect(scroller.scrollLeft).toBe(0);
    markEditorSpan(1, prefix, "needle");
    expect(scroller.scrollLeft).toBeGreaterThan(0);
    const got = markRect();
    const view = scroller.getBoundingClientRect();
    expect(got.left).toBeGreaterThanOrEqual(view.left);
    expect(got.right).toBeLessThanOrEqual(view.right);
  });
});

describe("flashEditorLine", () => {
  it("re-positions ONE element across a burst of steps", () => {
    vi.useFakeTimers();
    paint("a\n".repeat(50));
    for (let line = 1; line <= 41; line++) {
      flashEditorLine(line);
    }
    const flashes = document.querySelectorAll<HTMLElement>(".editor-line-flash");
    expect(flashes).toHaveLength(1);
    const lh = parseFloat(getComputedStyle(code()).lineHeight);
    const pad = parseFloat(
      getComputedStyle(document.getElementById("editor-highlight")!).paddingTop,
    );
    expect(parseFloat(flashes[0]!.style.top)).toBeCloseTo(pad + 40 * lh, 3);
    // The LAST step's flash gets its whole 1.2s: an earlier step's timer must not
    // take the shared element away under it.
    vi.advanceTimersByTime(1000);
    flashEditorLine(7);
    vi.advanceTimersByTime(250);
    expect(document.querySelectorAll(".editor-line-flash")).toHaveLength(1);
    vi.advanceTimersByTime(1000);
    expect(document.querySelectorAll(".editor-line-flash")).toHaveLength(0);
  });
});

describe("scrollToEditorLine", () => {
  it("lands at once when asked to be instant", () => {
    paint("a\n".repeat(400));
    const scroller = body();
    scrollToEditorLine(300, "instant");
    expect(scroller.scrollTop).toBeGreaterThan(0);
    const lh = parseFloat(getComputedStyle(code()).lineHeight);
    const pad = parseFloat(
      getComputedStyle(document.getElementById("editor-highlight")!).paddingTop,
    );
    expect(scroller.scrollTop).toBeCloseTo(pad + 299 * lh - scroller.clientHeight / 3, 0);
  });
});
