// THE TURN FOOTER'S TRAILING CONTROL IS INSET EQUALLY ON ALL FOUR SIDES.
//
// `.turn-footer`'s `padding: var(--sp-1) var(--sp-3)` is authored for INK, and it
// is right at the leading edge where the ledger's words are. At the trailing edge
// the child is `.turn-actions-more`'s `…` trigger or `.turn-rewind`, each floored to
// `--hit-floor` and each painting a real box on hover and on press — so its distance
// from the card's edge is a visible relationship, and 12px beside 4px above and
// below is what was reported as more padding on the right than on the top and
// bottom. `.tab` already carries this rule for the same reason (10-shell-app.css).
//
// Measured in real layout because the numbers are what the claim is about, and
// because the fix is keyed on `:has()`: the footer keeps the ink gutter when it ends
// in `.turn-elapsed` instead, and only a rendered row can tell those two cases
// apart.
//
// The `…` collapse and Rewind's word both live behind `width <= 40rem`, so the
// phone case is measured in an IFRAME; the desktop case is measured in the page,
// whose viewport is pinned at 1280x720.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;
let frame: HTMLIFrameElement;
let phone: Document;

type Trailing = "actions" | "rewind" | "elapsed";

/** A turn card with just its footer, as `mountTurnActions` and `mountRewind`
 *  assemble one: the ledger button, the elapsed readout, then whichever trailing
 *  control the case is about. */
function mountFooter(
  doc: Document,
  trailing: Trailing,
): { footer: HTMLElement; last: HTMLElement } {
  const card = doc.createElement("div");
  card.className = "turn";
  const footer = doc.createElement("div");
  footer.className = "turn-footer";

  const ledger = doc.createElement("button");
  ledger.type = "button";
  ledger.className = "turn-ledger-summary";
  ledger.textContent = "2 cmds · 1 file";

  const elapsed = doc.createElement("time");
  elapsed.className = "turn-elapsed";
  elapsed.dateTime = "PT12S";
  elapsed.textContent = "12s";

  footer.append(ledger, elapsed);

  let last: HTMLElement = elapsed;
  if (trailing === "actions") {
    const slot = doc.createElement("span");
    slot.className = "turn-actions-buttons";
    const more = doc.createElement("details");
    more.className = "turn-actions-more";
    const summary = doc.createElement("summary");
    summary.className = "turn-action-btn turn-action-more";
    const glyph = doc.createElementNS("http://www.w3.org/2000/svg", "svg");
    glyph.setAttribute("class", "ic-ui");
    summary.appendChild(glyph);
    const group = doc.createElement("span");
    group.className = "turn-actions-group";
    more.append(summary, group);
    slot.appendChild(more);
    footer.appendChild(slot);
    last = slot;
  } else if (trailing === "rewind") {
    const btn = doc.createElement("button");
    btn.type = "button";
    btn.className = "turn-rewind";
    const glyph = doc.createElementNS("http://www.w3.org/2000/svg", "svg");
    glyph.setAttribute("class", "ic-ui");
    const label = doc.createElement("span");
    label.className = "turn-rewind-label";
    label.textContent = "Rewind";
    btn.append(glyph, label);
    footer.appendChild(btn);
    last = btn;
  }

  card.appendChild(footer);
  doc.body.replaceChildren(card);
  return { footer, last };
}

/** The four gaps between a child's box and its footer's own box, in CSS px. The
 *  footer's 1px top border is subtracted so `top` is the PADDING above the child,
 *  which is what the other three are. */
function insets(
  footer: HTMLElement,
  child: HTMLElement,
): {
  top: number;
  bottom: number;
  end: number;
} {
  const f = footer.getBoundingClientRect();
  const c = child.getBoundingClientRect();
  const border = parseFloat(getComputedStyle(footer).borderTopWidth);
  return {
    top: +(c.top - f.top - border).toFixed(2),
    bottom: +(f.bottom - c.bottom).toFixed(2),
    end: +(f.right - c.right).toFixed(2),
  };
}

beforeAll(() => {
  style = mountAppCSS();
  frame = document.createElement("iframe");
  frame.width = "390";
  frame.height = "844";
  document.body.appendChild(frame);
  const inner = frame.contentDocument;
  if (inner === null) {
    throw new Error("iframe has no contentDocument");
  }
  phone = inner;
  phone.documentElement.dataset["pointer"] = "coarse";
  const sheet = phone.createElement("style");
  sheet.textContent = style.textContent;
  phone.head.appendChild(sheet);
});

afterAll(() => {
  frame.remove();
  style.remove();
});

describe("a footer ending in a control, on a phone", () => {
  it("is on the tier the complaint was made on", () => {
    expect(phone.defaultView?.innerWidth).toBeLessThanOrEqual(640);
    expect(getComputedStyle(phone.documentElement).getPropertyValue("--hit-floor").trim()).toBe(
      "2.75rem",
    );
  });

  it("insets the … trigger equally above, below and after it", () => {
    const { footer, last } = mountFooter(phone, "actions");
    // The premise: the floor really did grow this box, so the inset is a visible
    // relationship rather than a hairline.
    expect(last.getBoundingClientRect().height).toBeCloseTo(44, 0);
    const i = insets(footer, last);
    expect(i.end, `end ${i.end}px against top ${i.top}px / bottom ${i.bottom}px`).toBeCloseTo(
      i.top,
      1,
    );
    expect(i.end).toBeCloseTo(i.bottom, 1);
  });

  it("insets Rewind the same way", () => {
    const { footer, last } = mountFooter(phone, "rewind");
    const i = insets(footer, last);
    expect(i.end, `end ${i.end}px against top ${i.top}px / bottom ${i.bottom}px`).toBeCloseTo(
      i.top,
      1,
    );
    expect(i.end).toBeCloseTo(i.bottom, 1);
  });
});

describe("a footer ending in a control, on a desktop row", () => {
  it("insets Rewind equally there too", () => {
    // Not a phone-only rule: the floor is 24px with a mouse and `.turn-rewind`
    // still measures taller than the footer's 4px padding, so the same asymmetry
    // was on screen at every width.
    expect(window.innerWidth).toBeGreaterThan(640);
    const { footer, last } = mountFooter(document, "rewind");
    const i = insets(footer, last);
    expect(i.end, `end ${i.end}px against top ${i.top}px / bottom ${i.bottom}px`).toBeCloseTo(
      i.top,
      1,
    );
    expect(i.end).toBeCloseTo(i.bottom, 1);
  });
});

describe("a footer ending in the elapsed readout", () => {
  it("keeps the INK gutter, because the trailing child is text", () => {
    // The other half of the `:has()` rule, and the reason it is keyed rather than
    // applied unconditionally: a right-aligned readout ends on its container's own
    // gutter (`vibekit-ui.md`), so tightening the trailing padding for every
    // footer would pull the duration 8px off the card's edge.
    const { footer, last } = mountFooter(document, "elapsed");
    const gutter = parseFloat(getComputedStyle(footer).paddingInlineStart);
    expect(gutter).toBeGreaterThan(8);
    expect(insets(footer, last).end).toBeCloseTo(gutter, 1);
  });
});
