// THE × 'S INSET FROM THE ROW'S TRAILING EDGE, and why it is not the row's own
// inline padding.
//
// `--sp-2 --sp-3` is this app's row padding — `.fb-row` and `.sidebar-action-btn`
// declare the same pair — and it is authored for TEXT at both ends. A tab row's
// trailing child is not text: `.tab-close` paints a 24px box on hover and on press,
// so its distance from the row's edge is a relationship a reader can see, and at
// `--sp-3` it measured 12px against the 8px the same box has above, below and toward
// the name. Reported as the × looking pushed off-centre, with the 4px wanted back for
// the title — which is the row's ellipsised element, so the space is not cosmetic.
//
// This can only be a LAYOUT measurement. The four gaps come from three different
// mechanisms (`padding-block`, `padding-inline-end` and the flex `gap`), the row's
// reserved 1px transparent border sits inside two of them, and the × 's own box is
// `1.5rem` on a fine pointer and `var(--btn-h)` on a coarse one — so "are these four
// numbers equal" is a used-value question about the assembled cascade, not something
// any source read can answer.
//
// The name's own width is deliberately NOT asserted: it is `flex: 1`, so it takes
// whatever the row does not, and pinning a pixel figure for it would pin the test
// page's font metrics instead of this rule.

import { describe, it, expect, beforeAll, afterAll } from "vitest";
import { page } from "vitest/browser";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;
let host: HTMLElement;
/** `page.viewport` has no getter, so the entry size is read off the frame rather
 *  than copied from `vitest.config.ts`, which would silently leave every later file
 *  measuring at the old size if that config moved. */
let entry: { readonly width: number; readonly height: number };

beforeAll(() => {
  entry = { width: window.innerWidth, height: window.innerHeight };
  style = mountAppCSS();
  host = document.createElement("div");
  // A sidebar-width column, so the name is long enough to be the row's flexible
  // child rather than letting the row shrink-wrap its content.
  host.style.cssText = "inline-size:260px;";
  document.body.appendChild(host);
});

afterAll(async () => {
  style.remove();
  host.remove();
  await page.viewport(entry.width, entry.height);
});

interface Row {
  readonly row: HTMLElement;
  readonly name: HTMLElement;
  readonly close: HTMLElement;
}

/** A chat tab row in the shape `createTabEl` appends: dot, run dot, name, the two
 *  screen-reader spans, pin, ×. */
function tabRow(): Row {
  const list = document.createElement("div");
  list.id = "tab-list";
  const row = document.createElement("div");
  row.className = "tab active";
  row.setAttribute("role", "tab");
  const span = (cls: string, text = ""): HTMLElement => {
    const el = document.createElement("span");
    el.className = cls;
    el.textContent = text;
    return el;
  };
  const name = span("tab-name", "a chat title long enough to be clipped by the row");
  const close = span("tab-close");
  // The glyph, because an empty × would let the box collapse to its padding. SIZED
  // like `iconEl` sizes it: an unsized <svg> takes the UA's default 150px height and
  // overflows 63px above the 24px box (measured), which leaves hit-testable area
  // outside the target and makes any `contains()` probe read long. The box is 24px
  // either way, so this changes no measurement here — it only stops the fixture
  // answering for the expander.
  const glyph = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  glyph.setAttribute("width", "16");
  glyph.setAttribute("height", "16");
  close.appendChild(glyph);
  row.append(
    span("tab-status-dot"),
    span("tab-run-dot"),
    name,
    span("tab-status-dot-sr sr-only"),
    span("tab-run-dot-sr sr-only"),
    span("tab-pin"),
    close,
  );
  list.appendChild(row);
  host.replaceChildren(list);
  return { row, name, close };
}

/** The four distances, named. A `Record<string, number>` would be a weaker type than
 *  the truth — the keys are fixed — and under `noPropertyAccessFromIndexSignature` it
 *  also forces every reader to index with brackets. */
interface Insets {
  readonly top: number;
  readonly bottom: number;
  readonly trailing: number;
  readonly toName: number;
}

/** The four distances that make the × look centred in its corner, in CSS px. The
 *  row's reserved 1px border is subtracted from the three that cross it, so all four
 *  are padding-or-gap and therefore comparable. */
function insets({ row, name, close }: Row): Insets {
  const r = row.getBoundingClientRect();
  const c = close.getBoundingClientRect();
  const n = name.getBoundingClientRect();
  const border = parseFloat(getComputedStyle(row).borderTopWidth);
  return {
    top: c.top - r.top - border,
    bottom: r.bottom - c.bottom - border,
    trailing: r.right - c.right - border,
    toName: c.left - n.right,
  };
}

describe("the × is inset equally on all four sides", () => {
  it("measures one value at the row's own gap, on a fine pointer", () => {
    const row = tabRow();
    const gap = parseFloat(getComputedStyle(row.row).columnGap);
    const i = insets(row);
    expect(gap, "the row's gap is --sp-2").toBe(8);
    expect(i, "trailing was --sp-3 (12px) against 8px on the other three").toEqual({
      top: gap,
      bottom: gap,
      trailing: gap,
      toName: gap,
    });
  });

  it("keeps the trailing rung shorter than the leading one, which is deliberate", () => {
    // The first child is an 8px dot or a nesting arrow, neither of which paints a
    // box, so it has no corner to be centred in — cutting its inset would only crowd
    // the row's own edge. A future edit making the row symmetric again has to answer
    // this rather than reading the asymmetry as an oversight.
    const cs = getComputedStyle(tabRow().row);
    expect(parseFloat(cs.paddingInlineStart)).toBeGreaterThan(parseFloat(cs.paddingInlineEnd));
  });

  // ON A PHONE THE TARGET GROWS AND THE PAINTED BOX DOES NOT, which is the opposite
  // of what this file asserted until 2026-09. `50-mobile.css` used to set
  // `width`/`height: var(--btn-h)` on the ×, and because `.tab` is
  // `min-height: var(--btn-h)` with `padding-block: var(--sp-2)` plus a reserved 1px
  // border, a 44px child STACKED on that chrome and every row rendered 62px against
  // `#new-chat`'s 44px. The × takes the documented expander instead — an absolutely
  // positioned `::after` sized off `--hit-floor`, the escape `.status-dot`,
  // `.shell-resize` and `.tool-file-link` also use — because `.tab-close` is a SPAN
  // (`role="tab"` is Children Presentational, so it cannot be a <button>) and the
  // zero-specificity floor in `61-mcp-tools.css` therefore does not reach it.
  //
  // So the box is the WRONG observable for the target, and a box measurement cannot
  // tell a 44px control from a 24px one wearing a 44px expander. These cases split
  // the two, and the target half is a real hit test past the paint — never a style
  // read, since a rule's presence says nothing about what the assembled cascade
  // actually hits.
  describe("on a phone", () => {
    /** `page.viewport` is per-test in this file (the fine-pointer cases above measure
     *  at the entry size), so each case in here establishes its own. 390x844 puts the
     *  root under `01-tokens.css`'s `width <= 48rem` no-JS fallback, which is what
     *  moves `--hit-floor` to 2.75rem with no `data-pointer` written. */
    async function phoneRow(): Promise<Row> {
      await page.viewport(390, 844);
      expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
        390, 844,
      ]);
      expect(
        getComputedStyle(document.documentElement).getPropertyValue("--hit-floor").trim(),
        "the width fallback moved the floor, so this is the coarse tier",
      ).toBe("2.75rem");
      return tabRow();
    }

    /** `--hit-floor` in CSS px. `getPropertyValue` hands back the AUTHORED text
     *  (`2.75rem`), so it is resolved by measuring an element that consumes the token
     *  rather than by multiplying out a rem — which keeps this correct if the token
     *  ever moves to px, a `clamp()` or anything else. */
    function resolvedFloor(): number {
      const probe = document.createElement("div");
      probe.style.cssText = "position:absolute;visibility:hidden;block-size:var(--hit-floor);";
      host.appendChild(probe);
      const h = probe.getBoundingClientRect().height;
      probe.remove();
      expect(h, "the floor resolved to a real length").toBeGreaterThan(0);
      return h;
    }

    it("keeps the painted box at its 24px desktop size", async () => {
      // The whole point of the expander: growing this box is what made the row 62px.
      const row = await phoneRow();
      const box = row.close.getBoundingClientRect();
      expect(box.width, "the box must NOT take the touch floor").toBe(24);
      expect(box.height).toBe(24);
    });

    it("leaves the row sitting on the floor rather than outgrowing it", async () => {
      // The regression the expander fixes, asserted at the row rather than at the ×:
      // a 44px child inside 8px padding and a 1px border is a 62px row.
      const row = await phoneRow();
      expect(row.row.getBoundingClientRect().height, "the row is the floor, not more").toBe(
        resolvedFloor(),
      );
    });

    it("still lands the finger on the × ten pixels outside the paint", async () => {
      // The expander is 44px CENTRED on the 24px box, so it reaches (44-24)/2 = 10px
      // past each edge. Probed 5px out — inside the expander, outside the paint — on
      // all four sides, and asserted by IDENTITY: a hit on the × itself is the
      // ::after, where a hit on a DESCENDANT would be the glyph overflowing and would
      // pass just as well with no expander at all.
      const row = await phoneRow();
      const box = row.close.getBoundingClientRect();
      const cx = box.left + box.width / 2;
      const cy = box.top + box.height / 2;
      const out = box.width / 2 + 5;
      for (const [name, x, y] of [
        ["above", cx, cy - out],
        ["below", cx, cy + out],
        ["leading", cx - out, cy],
        ["trailing", cx + out, cy],
      ] as const) {
        expect(document.elementFromPoint(x, y), `the × owns the point 5px ${name} its paint`).toBe(
          row.close,
        );
      }
    });

    it("keeps the inline clearance at the row's own gap", async () => {
      // The two numbers the expander does not move: it is absolutely positioned, so
      // it takes no layout space and the × sits where it sat.
      const row = await phoneRow();
      const gap = parseFloat(getComputedStyle(row.row).columnGap);
      const i = insets(row);
      expect(gap, "the row's gap is --sp-2").toBe(8);
      expect({ trailing: i.trailing, toName: i.toName }).toEqual({ trailing: gap, toName: gap });
    });

    it("centres the × in a row the floor made taller than its content", async () => {
      // The block axis stops matching the gap here, and that is centring rather than a
      // clearance decision. On the fine tier the 24px box EXCEEDS the row's content
      // box and drives its height, so the inset is the padding exactly; under the
      // floor the row is 44px, its content box is 26px, and the box centres with 1px
      // of slack on each side. Asserted as symmetry plus a derived figure, so a change
      // to the floor or the padding moves the expectation with it rather than failing.
      const row = await phoneRow();
      const cs = getComputedStyle(row.row);
      const r = row.row.getBoundingClientRect();
      const box = row.close.getBoundingClientRect();
      const i = insets(row);
      expect(i.top, "symmetric about the row's centre").toBe(i.bottom);
      expect(box.top + box.height / 2, "the × 's centre IS the row's centre").toBeCloseTo(
        r.top + r.height / 2,
        5,
      );
      const contentH =
        r.height - 2 * parseFloat(cs.paddingBlockStart) - 2 * parseFloat(cs.borderTopWidth);
      const slack = (contentH - box.height) / 2;
      expect(slack, "the floor left 1px on each side").toBe(1);
      expect(i.top).toBe(parseFloat(cs.paddingBlockStart) + slack);
    });
  });
});
