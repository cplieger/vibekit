// A COLLAPSED TOOL BOX IS ONE HEIGHT, whichever kind it is.
//
// The transcript puts two box kinds side by side in one turn: a `.tool-group`
// collapsed to its summary line ("Ran 2 commands") and a claim-only `.tool-call`
// ("Read file"). Both are a header row inside a 1px-bordered card, both headers
// declare `min-height: var(--btn-h)` and the same padding, so they must measure
// the same — and they did not, which reads as the boxes being sized at random.
//
// Measured rather than reasoned, because the two numbers a reader reports are
// OUTER heights and the divergence was in neither header: `.tool-call` declares
// `contain-intrinsic-size` for its `content-visibility: auto`, and that value is
// what an off-screen card renders at. A placeholder disagreeing with the real
// collapsed height makes a card change size as it scrolls into view.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
  host?.remove();
});

/** The two collapsed shapes as the transcript builds them, in one container so
 *  they share a width and a font. */
function mountBoxes(): { group: HTMLElement; card: HTMLElement } {
  host?.remove();
  host = document.createElement("div");
  host.className = "msg-wrap";
  host.style.inlineSize = "760px";
  document.body.appendChild(host);

  host.innerHTML = `
    <div class="tool-group">
      <div class="tool-group-header" role="button" aria-expanded="false">
        <span class="tool-group-icon"></span>
        <span class="tool-group-summary">Ran 2 commands</span>
      </div>
      <div class="tool-group-body" hidden></div>
    </div>
    <div class="tool-call">
      <div class="tool-summary">
        <div class="tool-header">
          <span class="tool-icon"></span>
          <span class="tool-title">Read File</span>
          <span class="tool-subject">auth.go</span>
        </div>
      </div>
    </div>`;

  return {
    group: host.querySelector<HTMLElement>(".tool-group")!,
    card: host.querySelector<HTMLElement>(".tool-call")!,
  };
}

/** The control-height token in pixels. A custom property reads back as its raw
 *  token (`2.25rem`), so the only honest way to get the length is to let the
 *  engine resolve it on a real box. */
function controlHeight(): number {
  const probe = document.createElement("div");
  probe.style.blockSize = "var(--btn-h)";
  host.appendChild(probe);
  const h = probe.getBoundingClientRect().height;
  probe.remove();
  return h;
}

describe("a collapsed tool box", () => {
  it("measures the same whichever kind it is", () => {
    const { group, card } = mountBoxes();
    // Rects rather than offsetHeight: a fractional line box is the thing that
    // made these disagree by less than a pixel before the floor landed.
    const g = group.getBoundingClientRect().height;
    const c = card.getBoundingClientRect().height;
    expect(c).toBeCloseTo(g, 1);
  });

  it("puts both headers on the control-height floor", () => {
    mountBoxes();
    const floor = controlHeight();
    expect(floor).toBeGreaterThan(0);

    for (const sel of [".tool-group-header", ".tool-header"] as const) {
      const el = host.querySelector<HTMLElement>(sel)!;
      expect(el.getBoundingClientRect().height, sel).toBeCloseTo(floor, 1);
    }
  });

  // The placeholder a card renders at while `content-visibility: auto` skips its
  // layout. A value that disagrees with the real collapsed height is a resize on
  // scroll, not a static mismatch — and `contain-intrinsic-size` sizes the
  // CONTENT box, so the card's own borders must NOT be in it.
  it("reserves the header floor, borders excluded, on every contained card", () => {
    mountBoxes();
    const floor = controlHeight();

    for (const sel of [".tool-call", ".subagent-block"] as const) {
      const el = document.createElement("div");
      el.className = sel.slice(1);
      host.appendChild(el);
      const declared = getComputedStyle(el).containIntrinsicSize;
      // `auto <length>`; the length is what is reserved before first render.
      const reserved = Number.parseFloat(declared.replace(/^auto\s+/, ""));
      expect(Number.isNaN(reserved), `${sel} containIntrinsicSize was ${declared}`).toBe(false);
      expect(reserved, sel).toBeCloseTo(floor, 1);
      el.remove();
    }
  });
});
