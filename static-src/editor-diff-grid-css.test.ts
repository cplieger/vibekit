// The diff pane occupies `.editor-body`'s FLEXIBLE track, so its width cannot
// depend on its own content.
//
// Measured against real layout rather than read out of the CSS, because the
// defect this pins is arithmetic over boxes: `diff-pane.ts`'s `measure()` writes
// `--diff-hspan` from a column's `scrollWidth` back into every row's
// `min-inline-size`, so a pane sitting in the `auto` (max-content) track feeds its
// own measurement — the `ResizeObserver` on the column re-fires and the pane
// ratchets 2px per frame from ~78% to full width over ~2.8s. A source read cannot
// see it: the two grid declarations look inert beside the pane's own `flex: 1`.
import { describe, it, expect, beforeAll, afterAll, beforeEach } from "vitest";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

/** Enough rows to give the inline axis somewhere to go, one of them long. */
const ROWS = 20;
const LONG_ROW = 7;

let sheet: HTMLStyleElement;
let host: HTMLDivElement;

beforeAll(() => {
  sheet = mountAppCSS();
});

afterAll(() => {
  sheet.remove();
  host?.remove();
});

/** The editor view in DIFF mode as `showDiffMode` leaves it: the gutter and the
 *  highlight hidden, the diff pane the only surface in the grid. */
function mount(): void {
  host?.remove();
  host = document.createElement("div");
  host.id = "app";
  const row = (n: number): string => {
    const text = n === LONG_ROW ? "y".repeat(56) : `line ${String(n)}`;
    return `<div class="diff-row diff-row-ctx"><span class="diff-gutter">${String(n)}</span><span class="diff-content"><span class="diff-marker"> </span><span class="diff-line-text">${text}</span></span></div>`;
  };
  const rows = Array.from({ length: ROWS }, (_, i) => row(i + 1)).join("");
  host.innerHTML = `
    <main id="chat-area">
      <div id="editor-view" data-tab-view>
        <div class="editor-page">
          <div class="editor-body">
            <pre id="editor-gutter" class="editor-gutter hidden"></pre>
            <pre id="editor-highlight" class="editor-highlight hidden"><code id="editor-code"></code></pre>
            <div id="editor-diff-pane" class="editor-diff-pane">
              <div class="diff-pane diff-pane-mapped">
                <div class="diff-pane-toolbar"></div>
                <div class="diff-pane-viewport">
                  <div class="diff-pane-body diff-pane-split">
                    <div class="diff-pane-header"></div>
                    <div class="diff-col diff-col-old" tabindex="0">${rows}</div>
                    <div class="diff-col diff-col-new" tabindex="0">${rows}</div>
                  </div>
                  <div class="diff-pane-hbar"><div class="diff-pane-hbar-spacer"></div></div>
                  <div class="diff-map"></div>
                </div>
              </div>
            </div>
          </div>
          <div class="editor-toolbar bottom-bar"></div>
        </div>
      </div>
    </main>`;
  document.body.appendChild(host);
}

/** `diff-pane.ts` `measure()`, in the one respect that reaches layout: the span
 *  is the wider column's `scrollWidth`, published on the VIEWPORT. */
function publishSpan(): number {
  const left = document.querySelector(".diff-col-old");
  const right = document.querySelector(".diff-col-new");
  const viewport = document.querySelector<HTMLElement>(".diff-pane-viewport");
  if (left === null || right === null || viewport === null) {
    throw new Error("the diff pane is not mounted");
  }
  const span = Math.max(left.scrollWidth, right.scrollWidth);
  viewport.style.setProperty("--diff-hspan", `${String(span)}px`);
  return span;
}

function paneWidth(): number {
  const pane = document.querySelector("#editor-diff-pane");
  if (pane === null) {
    throw new Error("the diff pane is not mounted");
  }
  return pane.getBoundingClientRect().width;
}

describe("the diff pane's grid placement", () => {
  beforeEach(() => {
    mount();
  });

  it("leaves the max-content track empty and takes the flexible one", () => {
    const body = document.querySelector(".editor-body");
    const pane = document.querySelector("#editor-diff-pane");
    expect(body, "the grid is mounted").not.toBeNull();
    if (body === null || pane === null) {
      return;
    }
    // The gutter and the highlight are hidden in diff mode, so the `auto` track
    // has no content: a pane auto-placed into it would be sized by its own
    // max-content instead, which is the ratchet's first ingredient.
    const [autoTrack] = getComputedStyle(body).gridTemplateColumns.split(" ");
    expect(autoTrack, "the max-content track carries nothing").toBe("0px");

    // Everything but the pane's own margin.
    const margin = parseFloat(getComputedStyle(pane).marginInlineStart);
    expect(margin, "the pane declares an inline margin").toBeGreaterThan(0);
    expect(paneWidth()).toBeCloseTo(body.clientWidth - 2 * margin, 0);
  });

  it("does not widen when the measured span is fed back as the row minimum", () => {
    const first = paneWidth();
    expect(first, "the pane has a box to measure").toBeGreaterThan(0);

    // The feedback loop, run by hand: each pass publishes the span the previous
    // pass's layout produced, which is exactly what the ResizeObserver does one
    // frame apart. A pane in the max-content track gains 2px per pass.
    const widths: number[] = [];
    for (let pass = 0; pass < 6; pass++) {
      publishSpan();
      widths.push(paneWidth());
    }
    expect(widths, "every pass measures the same pane").toEqual(widths.map(() => first));
  });

  // The PREMISE of the case above rather than a second ratchet case: it holds
  // whether or not the pane is pinned, and it fails if the feedback path stops
  // reaching layout — which would make that case pass for no reason at all.
  it("feeds the published span into every row's minimum", () => {
    const span = publishSpan();
    const row = document.querySelector(".diff-pane-split .diff-row");
    expect(row, "a row is mounted").not.toBeNull();
    if (row === null) {
      return;
    }
    expect(getComputedStyle(row).minInlineSize).toBe(`${String(span)}px`);
  });
});
