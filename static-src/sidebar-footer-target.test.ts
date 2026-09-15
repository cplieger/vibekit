// THE ACCOUNT ROW'S TARGET IS THE SIDEBAR FOOTER'S WHOLE BAND.
//
// The defect this pins: `.sidebar-email` took `align-self: center` from the
// footer's `align-items`, so its box was the trimmed cap band plus 0.35em —
// measured 16.61px against the 55px band the footer declared at the time, before
// both of the panel's ends moved onto `--sidebar-band-h` — and iOS painted its
// touch-and-hold highlight
// as a thin strip floating in the middle of the row. Reported as the footer's touch
// area being a weird rectangle rather than the whole footer.
//
// THE SUBJECT MOVED, and the defect's shape is what carried over. The mark and the
// address are ONE `<button id="account-btn">` now — the popup's trigger, so a
// reader presses the row rather than an 8px disc — and it is the BUTTON's box that
// has to fill the band. The mechanism moved with it: `align-self: stretch` on the
// button against `.sidebar-footer`'s `min-height`, where the address used to carry
// the stretch plus an `align-content: center` to put its line back in the middle.
// Both of those left `.sidebar-email`, which is a plain content-height flex item of
// an `align-items: center` button now.
//
// Two claims, and the second is why this is a layout test rather than a style read:
// the button's box has to FILL the band, and the address inside it has to stay
// vertically centred and still ellipsise. A computed-style assertion on
// `align-self` would pass for a box that renders anywhere.
//
// Real layout in the page's own document: nothing here is behind a media query —
// the thin strip is the same defect with a mouse, where it is the hover and
// focus-visible region.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;

/** The footer as `static/index.html` authors it: the anchor holding the merged
 *  trigger, the trigger holding the mark, the address and the `.sr-only` subject,
 *  the card as the trigger's SIBLING, then the trailing action cluster. */
function mountFooter(email: string): {
  footer: HTMLElement;
  btn: HTMLButtonElement;
  addr: HTMLElement;
  dot: HTMLElement;
  logout: HTMLElement;
} {
  const sidebar = document.createElement("nav");
  sidebar.id = "sidebar";
  const footer = document.createElement("div");
  footer.className = "sidebar-footer";

  const anchor = document.createElement("div");
  anchor.className = "popup-anchor";

  const btn = document.createElement("button");
  btn.type = "button";
  btn.id = "account-btn";
  btn.className = "account-btn pill-expandable";

  const dot = document.createElement("span");
  dot.id = "status-dot";
  dot.className = "status-dot connected";
  dot.setAttribute("aria-hidden", "true");

  const addr = document.createElement("span");
  addr.id = "user-email";
  addr.className = "sidebar-email";
  addr.textContent = email;

  // Out of flow (`position: absolute`), so the button has exactly TWO flex items.
  const subject = document.createElement("span");
  subject.className = "sr-only";
  subject.textContent = "Account and connection status";

  btn.append(dot, addr, subject);

  const card = document.createElement("span");
  card.id = "status-card";
  card.className = "pill-expand-content pill-status-content hidden";

  anchor.append(btn, card);

  const actions = document.createElement("div");
  actions.className = "sidebar-footer-actions";
  const logout = document.createElement("button");
  logout.type = "button";
  logout.id = "logout-btn";
  logout.className = "icon-btn";
  actions.appendChild(logout);

  footer.append(anchor, actions);
  sidebar.appendChild(footer);
  document.body.replaceChildren(sidebar);
  return { footer, btn, addr, dot, logout };
}

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
});

describe("the account row's box", () => {
  it("fills the footer's whole content band", () => {
    const { footer, btn } = mountFooter("someone@example.invalid");
    const f = footer.getBoundingClientRect();
    const b = btn.getBoundingClientRect();
    // The footer declares no block padding, so its CONTENT band is its box less the
    // dotted `border-block-start` item 3 added — which is spent out of the band
    // rather than added to it (`box-sizing: border-box`).
    expect(getComputedStyle(footer).paddingBlockStart).toBe("0px");
    const border = parseFloat(getComputedStyle(footer).borderTopWidth);
    expect(border, "the footer carries the dotted divider").toBeCloseTo(1, 1);
    expect(b.height, `the trigger is ${b.height}px in a ${f.height}px footer`).toBeCloseTo(
      f.height - border,
      0,
    );
    expect(b.top).toBeCloseTo(f.top + border, 0);
    expect(b.bottom).toBeCloseTo(f.bottom, 0);
  });

  it("answers a hit at the band's top and bottom edges, and across its whole width", () => {
    // The measurement a style read cannot make. Both axes are probed, because the
    // fix grows only ONE of them and a test at the centre alone would pass for a
    // full-height box 1px wide.
    //
    // Each probe stays off the CORNERS: `border-radius` is honoured by hit
    // testing, so a point 1px in from both a side and an edge at once lands
    // outside the 6px arc and answers the footer. Vertical reach is read at
    // mid-row, horizontal reach at mid-height.
    // The VERTICAL probes are taken at the FOOTER's own band edges rather than the
    // trigger's, which is the whole claim: a trigger that filled only its content
    // height would answer inside itself at every probe and tell us nothing. The
    // horizontal probes are the trigger's, because the footer's inline padding is
    // deliberately outside every control.
    const { footer, btn } = mountFooter("someone@example.invalid");
    const f = footer.getBoundingClientRect();
    const b = btn.getBoundingClientRect();
    const border = parseFloat(getComputedStyle(footer).borderTopWidth);
    const midX = b.left + b.width / 2;
    const midY = f.top + border + (f.height - border) / 2;
    for (const [name, x, y] of [
      ["top edge", midX, f.top + border + 1],
      ["bottom edge", midX, f.bottom - 1],
      ["leading edge", b.left + 1, midY],
      ["trailing edge", b.right - 1, midY],
    ] as const) {
      // The mark and the address are non-interactive spans INSIDE the trigger, so a
      // probe legitimately answers one of them; what must not happen is a probe
      // landing outside the control.
      expect(btn.contains(document.elementFromPoint(x, y)), `${name} of the band`).toBe(true);
    }
  });

  it("leaves the logout button its own target", () => {
    // The stretch must not reach over its neighbour: that is the footer's other
    // control and a 44px target of its own on a finger. The mark is INSIDE the
    // trigger now, so the old "leaves the status dot its own target" half is gone
    // with the button it described.
    const { btn, dot, logout } = mountFooter("someone@example.invalid");
    const b = btn.getBoundingClientRect();
    const d = dot.getBoundingClientRect();
    const l = logout.getBoundingClientRect();
    expect(b.right).toBeLessThanOrEqual(l.left);
    expect(d.left, "the mark sits inside the trigger").toBeGreaterThanOrEqual(b.left);
    expect(d.right).toBeLessThanOrEqual(b.right);
  });
});

describe("what filling the band must not cost", () => {
  it("keeps the address vertically centred in it", () => {
    // The button's `align-items: center` rather than the address's own
    // `align-content: center`, which left with the stretch it corrected. Measured as
    // the ink's own centre against the band's, which is the property the cap-band
    // trim exists to make exact (label-centring.test.ts owns the trim itself).
    const { footer, addr } = mountFooter("someone@example.invalid");
    const f = footer.getBoundingClientRect();
    const border = parseFloat(getComputedStyle(footer).borderTopWidth);
    const range = document.createRange();
    const text = addr.firstChild;
    if (text === null) {
      throw new Error("the address has no text node");
    }
    range.selectNodeContents(text);
    const ink = range.getBoundingClientRect();
    const inkCentre = ink.top + ink.height / 2;
    const bandCentre = f.top + border + (f.height - border) / 2;
    expect(
      Math.abs(inkCentre - bandCentre),
      `ink centre ${inkCentre} against the band's ${bandCentre}`,
    ).toBeLessThan(1.5);
  });

  it("still clips an address too long for the row, on one line", () => {
    const { addr } = mountFooter(
      "a-very-long-address-that-cannot-possibly-fit-in-this-row@example.invalid",
    );
    expect(getComputedStyle(addr).textOverflow).toBe("ellipsis");
    expect(getComputedStyle(addr).overflowX).toBe("hidden");
    expect(addr.scrollWidth).toBeGreaterThan(addr.clientWidth);
    // ONE LINE, so the clip is horizontal. Measured against a SHORT address's box
    // rather than against the parent's height: the address is content-height now
    // (the stretch moved to the button), so a wrap shows up as this box being
    // taller than one line rather than as it exceeding the band.
    const long = addr.getBoundingClientRect().height;
    const { addr: shortAddr } = mountFooter("a@b.invalid");
    expect(long).toBeCloseTo(shortAddr.getBoundingClientRect().height, 0);
  });

  it("keeps the box a BLOCK container, which is what the ellipsis needs", () => {
    // The regression the assertions above CANNOT see, checked by planting it:
    // `display: flex` with `align-items: center` fills the band and centres the
    // ink just as well, and Chromium still reports `text-overflow: ellipsis`,
    // `overflow-x: hidden` and `scrollWidth > clientWidth` — every one of those
    // passes. What changes is where the text lives: `text-overflow` applies to a
    // block container and is not inherited, so a flex or grid container moves the
    // address into an anonymous item that has neither the property nor the clip,
    // and the address hard-cuts mid glyph instead of ellipsising.
    //
    // Still the address's own subject after the merge: it is a flex ITEM of
    // `.account-btn`, so it is blockified to `display: block` and the property
    // still applies.
    //
    // Stated as "not a flex or grid container" rather than "is `block`", because
    // `inline-block` and `flow-root` are block containers too and would be fine.
    const { addr } = mountFooter("someone@example.invalid");
    const display = getComputedStyle(addr).display;
    expect(
      ["flex", "inline-flex", "grid", "inline-grid"],
      `the address resolves to display: ${display}`,
    ).not.toContain(display);
  });
});
