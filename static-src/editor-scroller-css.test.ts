// The editor pane has ONE scroller, and the gutter shares the code's line grid.
//
// Measured against real layout rather than read out of the CSS: both properties
// are arithmetic over boxes, so a declaration that reads correct in source can
// still resolve to a second scrollbar or a drifting line number.
import { describe, it, expect, beforeAll, afterAll } from "vitest";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

const LINES = 120;
/** A line wider than any pane, so the inline axis has somewhere to go. */
const LONG_LINE = 7;

let sheet: HTMLStyleElement;
let host: HTMLDivElement;

beforeAll(() => {
  sheet = mountAppCSS();
});

afterAll(() => {
  sheet.remove();
});

/** The editor view as `static/index.html` declares it, inside the shell chain
 *  that gives `.editor-body` its definite height. */
function mount(mode: "read" | "edit"): void {
  host?.remove();
  host = document.createElement("div");
  host.id = "app";
  host.innerHTML = `
    <main id="chat-area">
      <div id="editor-view" data-tab-view>
        <div class="editor-page">
          <div class="editor-body">
            <pre id="editor-gutter" class="editor-gutter" aria-hidden="true"></pre>
            <pre id="editor-highlight" class="editor-highlight"><code id="editor-code"></code></pre>
            <textarea id="editor-content" class="editor-content" spellcheck="false"></textarea>
          </div>
          <div class="editor-toolbar bottom-bar"></div>
        </div>
      </div>
    </main>`;
  document.body.appendChild(host);

  const lineText = (n: number): string =>
    n === LONG_LINE ? `\tlong := ${"x".repeat(400)}` : `\tline ${String(n)}`;
  const text = Array.from({ length: LINES }, (_, i) => lineText(i + 1)).join("\n");

  // One `.gutter-line` div per line, the shape `updateGutter` reconciles to.
  const gutter = document.querySelector("#editor-gutter");
  const frag = document.createDocumentFragment();
  for (let n = 1; n <= LINES; n++) {
    const row = document.createElement("div");
    row.className = "gutter-line";
    row.textContent = String(n);
    frag.append(row);
  }
  gutter?.replaceChildren(frag);

  const code = document.querySelector("#editor-code");
  if (code !== null) {
    code.textContent = text;
  }
  const area = document.querySelector<HTMLTextAreaElement>("#editor-content");
  if (area !== null) {
    area.value = text;
  }

  // showReadMode / showEditMode: exactly one text surface is visible.
  document.querySelector("#editor-highlight")?.classList.toggle("hidden", mode === "edit");
  area?.classList.toggle("hidden", mode === "read");
}

/** Every element under the view that paints a scrollbar the reader can drag. */
function scrollers(axis: "y" | "x"): string[] {
  const out: string[] = [];
  for (const el of document.querySelectorAll("#editor-view, #editor-view *")) {
    const cs = getComputedStyle(el);
    const scrolls =
      axis === "y"
        ? /auto|scroll/u.test(cs.overflowY) && el.scrollHeight > el.clientHeight
        : /auto|scroll/u.test(cs.overflowX) && el.scrollWidth > el.clientWidth;
    if (scrolls) {
      out.push(el.id !== "" ? `#${el.id}` : `.${el.className}`);
    }
  }
  return out;
}

describe("the editor pane's scrollers", () => {
  for (const mode of ["read", "edit"] as const) {
    it(`gives a long file exactly one vertical scrollbar in ${mode} mode`, () => {
      mount(mode);
      expect(scrollers("y")).toEqual([".editor-body"]);
    });
  }

  it("keeps the horizontal bar inside the scrollport, not at the end of the file", () => {
    mount("read");
    const body = document.querySelector(".editor-body");
    expect(body, "the scroller is mounted").not.toBeNull();
    if (body === null) {
      return;
    }
    // A bar sits at the bottom of its OWNER's box, so an owner taller than the
    // scrollport puts it off screen.
    expect(scrollers("x")).toEqual([".editor-body"]);
    for (const sel of scrollers("x")) {
      const owner = document.querySelector(sel);
      expect(
        owner?.clientHeight ?? 0,
        `${sel} is no taller than the scrollport`,
      ).toBeLessThanOrEqual(body.clientHeight);
    }
  });

  it("holds the gutter in place while the code pans sideways", () => {
    mount("read");
    const body = document.querySelector(".editor-body");
    const gutter = document.querySelector("#editor-gutter");
    expect(body?.scrollWidth ?? 0, "the long line overflows the pane").toBeGreaterThan(
      body?.clientWidth ?? 0,
    );
    if (body === null || gutter === null) {
      return;
    }
    const before = gutter.getBoundingClientRect().left;
    body.scrollLeft = 400;
    expect(body.scrollLeft, "the pane panned").toBeGreaterThan(0);
    expect(gutter.getBoundingClientRect().left).toBeCloseTo(before, 0);
  });

  it("clips no line of text in either mode", () => {
    for (const mode of ["read", "edit"] as const) {
      mount(mode);
      const surface = document.querySelector(
        mode === "read" ? "#editor-highlight" : "#editor-content",
      );
      if (surface === null) {
        continue;
      }
      const cs = getComputedStyle(surface);
      const hidden = /hidden|clip/u.test(cs.overflowY);
      const over = Math.max(0, surface.scrollHeight - surface.clientHeight);
      // A clip is admissible only inside the trailing padding: the horizontal
      // bar a textarea must own costs its own height out of this box.
      expect(hidden ? over : 0, `${mode}: a clip eats no text`).toBeLessThanOrEqual(
        parseFloat(cs.paddingBottom),
      );
    }
  });
});

describe("the gutter's line grid", () => {
  it("numbers each line at the line's own height", () => {
    mount("read");
    const gutterRow = document.querySelector(".gutter-line");
    const highlight = document.querySelector("#editor-highlight");
    expect(gutterRow, "a gutter row is mounted").not.toBeNull();
    if (gutterRow === null || highlight === null) {
      return;
    }
    // One line grid: the row and the code it numbers resolve the same height.
    expect(getComputedStyle(gutterRow).lineHeight).toBe(getComputedStyle(highlight).lineHeight);
  });

  it("keeps the last line's number beside the last line of text", () => {
    mount("read");
    const gutter = document.querySelector("#editor-gutter");
    const code = document.querySelector("#editor-code");
    const lastNo = gutter?.lastElementChild;
    const text = code?.firstChild;
    expect(lastNo, "the last number is mounted").not.toBeNull();
    if (lastNo === null || lastNo === undefined || text === null || text === undefined) {
      return;
    }
    const data = (text as Text).data;
    const range = document.createRange();
    range.setStart(text, data.lastIndexOf("\n") + 1);
    range.setEnd(text, data.length);
    // Within one line: sharing a grid leaves half-leading, not a drift that
    // accumulates with the line count.
    const lh = parseFloat(getComputedStyle(code as Element).lineHeight);
    expect(
      Math.abs(range.getBoundingClientRect().top - lastNo.getBoundingClientRect().top),
    ).toBeLessThan(lh);
  });
});
