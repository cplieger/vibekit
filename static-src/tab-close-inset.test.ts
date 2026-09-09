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
  // The glyph, because an empty × would let the box collapse to its padding.
  close.appendChild(document.createElementNS("http://www.w3.org/2000/svg", "svg"));
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

/** The four distances that make the × look centred in its corner, in CSS px. The
 *  row's reserved 1px border is subtracted from the three that cross it, so all four
 *  are padding-or-gap and therefore comparable. */
function insets({ row, name, close }: Row): Record<string, number> {
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

  it("holds on a phone, where the × box grows and the insets do not", async () => {
    // `50-mobile.css` takes the × to `var(--btn-h)` for the touch hit floor, which
    // moves the BOX and not its clearance — so the same four numbers have to come
    // back off a 44px control in a taller row.
    await page.viewport(390, 844);
    expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
      390, 844,
    ]);
    const row = tabRow();
    const box = row.close.getBoundingClientRect();
    expect(box.width, "the mobile hit target, not the 24px desktop box").toBeGreaterThan(24);
    expect(box.width).toBe(box.height);
    const gap = parseFloat(getComputedStyle(row.row).columnGap);
    expect(insets(row)).toEqual({
      top: gap,
      bottom: gap,
      trailing: gap,
      toName: gap,
    });
  });
});
