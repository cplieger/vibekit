// THE ACCOUNT ROW'S TARGET IS THE SIDEBAR FOOTER'S WHOLE BAND.
//
// The defect this pins: `.sidebar-email` took `align-self: center` from the
// footer's `align-items`, so its box was the trimmed cap band plus 0.35em —
// measured 16.61px in a 55px footer — and iOS painted its touch-and-hold highlight
// as a thin strip floating in the middle of the row. Reported as the footer's touch
// area being a weird rectangle rather than the whole footer.
//
// Two claims, and the second is why this is a layout test rather than a style read:
// the box has to FILL the band, and the address inside it has to stay vertically
// centred and still ellipsise. `align-content: center` on a block container is what
// buys both, and a flex or grid container would silently drop the ellipsis
// (`text-overflow` is not inherited, so an anonymous item does not get it). A
// computed-style assertion on `align-content` would pass for the flex shape too.
//
// Real layout in the page's own document: nothing here is behind a media query —
// the thin strip is the same defect with a mouse, where it is the hover and
// focus-visible region.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;

/** The footer as `static/index.html` authors it: the status dot's anchor, the
 *  address, then the trailing action cluster. */
function mountFooter(email: string): {
  footer: HTMLElement;
  addr: HTMLAnchorElement;
  dot: HTMLElement;
  logout: HTMLElement;
} {
  const sidebar = document.createElement("nav");
  sidebar.id = "sidebar";
  const footer = document.createElement("div");
  footer.className = "sidebar-footer";

  const anchor = document.createElement("div");
  anchor.className = "popup-anchor";
  const dot = document.createElement("button");
  dot.type = "button";
  dot.id = "status-dot";
  dot.className = "status-dot pill-expandable connected";
  anchor.appendChild(dot);

  const addr = document.createElement("a");
  addr.id = "user-email";
  addr.className = "sidebar-email";
  addr.href = "https://example.invalid/account";
  addr.textContent = email;

  const actions = document.createElement("div");
  actions.className = "sidebar-footer-actions";
  const logout = document.createElement("button");
  logout.type = "button";
  logout.id = "logout-btn";
  logout.className = "icon-btn";
  actions.appendChild(logout);

  footer.append(anchor, addr, actions);
  sidebar.appendChild(footer);
  document.body.replaceChildren(sidebar);
  return { footer, addr, dot, logout };
}

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
});

describe("the account row's box", () => {
  it("fills the footer's whole content band", () => {
    const { footer, addr } = mountFooter("someone@example.invalid");
    const f = footer.getBoundingClientRect();
    const a = addr.getBoundingClientRect();
    // The footer declares no block padding, so its content band IS its box.
    expect(getComputedStyle(footer).paddingBlockStart).toBe("0px");
    expect(a.height, `the address is ${a.height}px in a ${f.height}px footer`).toBeCloseTo(
      f.height,
      0,
    );
    expect(a.top).toBeCloseTo(f.top, 0);
    expect(a.bottom).toBeCloseTo(f.bottom, 0);
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
    const { footer, addr } = mountFooter("someone@example.invalid");
    const a = addr.getBoundingClientRect();
    const f = footer.getBoundingClientRect();
    const midX = a.left + a.width / 2;
    const midY = f.top + f.height / 2;
    for (const [name, x, y] of [
      ["top edge", midX, f.top + 1],
      ["bottom edge", midX, f.bottom - 1],
      ["leading edge", a.left + 1, midY],
      ["trailing edge", a.right - 1, midY],
    ] as const) {
      expect(document.elementFromPoint(x, y), `${name} of the band`).toBe(addr);
    }
  });

  it("leaves the status dot and the logout button their own targets", () => {
    // The stretch must not reach over its neighbours: those are the footer's two
    // other controls and each is a 44px target of its own on a finger.
    const { addr, dot, logout } = mountFooter("someone@example.invalid");
    const d = dot.getBoundingClientRect();
    const l = logout.getBoundingClientRect();
    const a = addr.getBoundingClientRect();
    expect(a.left).toBeGreaterThan(d.right);
    expect(a.right).toBeLessThan(l.left);
  });
});

describe("what filling the band must not cost", () => {
  it("keeps the address vertically centred in it", () => {
    // `align-content: center` rather than a taller box with the line at its top.
    // Measured as the ink's own centre against the band's, which is the property
    // the cap-band trim exists to make exact (label-centring.test.ts owns the
    // trim itself).
    const { footer, addr } = mountFooter("someone@example.invalid");
    const f = footer.getBoundingClientRect();
    const range = document.createRange();
    const text = addr.firstChild;
    if (text === null) {
      throw new Error("the address has no text node");
    }
    range.selectNodeContents(text);
    const ink = range.getBoundingClientRect();
    const inkCentre = ink.top + ink.height / 2;
    const bandCentre = f.top + f.height / 2;
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
    // One line, so the clip is horizontal: a wrap would make the box taller than
    // the band it was stretched to.
    const f = addr.parentElement?.getBoundingClientRect().height ?? 0;
    expect(addr.getBoundingClientRect().height).toBeCloseTo(f, 0);
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
