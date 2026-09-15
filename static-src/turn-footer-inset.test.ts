// THE TURN FOOTER'S BAND IS ITS LEDGER BUTTON'S HIT BOX.
//
// The band carries no block padding: `.turn-ledger-summary` declares the height
// (`max(--hit-floor, --ctl-h-dense)`) and the grid's own `align-items: center` lands
// that box on both edges, so the target a finger aims at IS the strip rather than a
// box sitting inside one. Before that, `.turn-footer`'s own
// `padding: var(--sp-1) var(--sp-3)` put 4px above and below a target that paints
// nothing — 52px of band under a finger for a 44px target, reported as the button
// having gaps around it and the footer being too tall on a phone.
//
// Four things have to hold together, and each is a separate case below:
//
//   1. the band equals the declared height and the target fills it, at both tiers;
//   2. the `i` still starts on the card's ink gutter, level with the header, even
//      though the footer no longer carries that gutter — the button does;
//   3. `.turn-rewind` does NOT go flush, because it is the only footer child with a
//      RESTING border, and its target still reaches the hit floor past its paint;
//   4. the trailing gutter EQUALS that leading one, whatever the trailing child is
//      and on either card that mounts this row — reported as the two insets not
//      matching, and as the delegate card's `i` not lining up with the turn card's.
//
// Measured in real layout because the numbers are what the claim is about. The `…`
// collapse and Rewind's word both live behind `width <= 40rem`, so the phone case is
// measured in an IFRAME; the desktop case is measured in the page, whose viewport is
// pinned at 1280x720.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;
let frame: HTMLIFrameElement;
let phone: Document;

type Trailing = "actions" | "rewind";

/** The card this helper last mounted, per document. */
const mounted = new WeakMap<Document, HTMLElement>();

interface Mounted {
  card: HTMLElement;
  footer: HTMLElement;
  ledger: HTMLElement;
  /** The `i`, whose leading edge is the ink the gutter is about. */
  info: HTMLElement;
  /** The header's request text, the ink the footer's has to line up with. */
  headerText: HTMLElement;
  last: HTMLElement;
}

/** A turn card with its header and its footer, as `buildTurnHeader`,
 *  `buildTurnFooter`, `mountTurnActions` and `mountRewind` assemble one: the
 *  ledger button (glyph, mark, text), the fact slot, then whichever trailing
 *  control the case is about. */
function mountFooter(doc: Document, trailing: Trailing): Mounted {
  const card = doc.createElement("div");
  card.className = "turn";

  const header = doc.createElement("div");
  header.className = "turn-header";
  const req = doc.createElement("div");
  req.className = "turn-req";
  const headerText = doc.createElement("span");
  headerText.className = "turn-req-text";
  headerText.textContent = "What the reader asked for.";
  req.appendChild(headerText);
  header.appendChild(req);

  const footer = doc.createElement("div");
  footer.className = "turn-footer";

  const ledger = doc.createElement("button");
  ledger.type = "button";
  ledger.className = "turn-ledger-summary";
  const info = doc.createElement("span");
  info.className = "turn-ledger-info";
  const infoGlyph = doc.createElementNS("http://www.w3.org/2000/svg", "svg");
  infoGlyph.setAttribute("class", "ic-ui");
  info.appendChild(infoGlyph);
  const mark = doc.createElement("span");
  mark.className = "turn-ledger-glyph";
  const text = doc.createElement("span");
  text.className = "turn-ledger-text";
  text.textContent = "Failed";
  ledger.append(info, mark, text);

  const fact = doc.createElement("span");
  fact.className = "turn-fact";
  fact.textContent = "12.0s";

  footer.append(ledger, fact);

  let last: HTMLElement = fact;
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

  card.append(header, footer);
  // Only this helper's PREVIOUS card goes, never `body.replaceChildren`: the phone
  // frame is a child of the page's body, so wiping it detaches the iframe and every
  // later phone measurement reads 0 against a blank document.
  mounted.get(doc)?.remove();
  mounted.set(doc, card);
  doc.body.appendChild(card);
  return { card, footer, ledger, info, headerText, last };
}

interface MountedDelegate {
  footer: HTMLElement;
  ledger: HTMLElement;
  /** The `i`, whose leading edge is the ink the gutter is about. */
  info: HTMLElement;
  /** The header's identity glyph, the ink this card's foot has to line up with. */
  headerInk: Element;
  last: HTMLElement;
}

/** A delegate card with its header and its foot, as `buildSubagentCard` assembles
 *  one: the same `.turn-footer` row nested inside `.subagent-foot`, so the gutters
 *  under test are the ones 29-turns.css declares rather than a copy. */
function mountDelegate(doc: Document): MountedDelegate {
  const card = doc.createElement("div");
  card.className = "subagent-block";

  const header = doc.createElement("a");
  header.className = "subagent-header";
  header.href = "#";
  const icon = doc.createElement("span");
  icon.className = "subagent-icon tool-icon";
  const headerInk = doc.createElementNS("http://www.w3.org/2000/svg", "svg");
  headerInk.setAttribute("class", "ic-ui");
  icon.appendChild(headerInk);
  const name = doc.createElement("span");
  name.className = "subagent-name";
  name.textContent = "context-gatherer";
  header.append(icon, name);

  const foot = doc.createElement("div");
  foot.className = "subagent-foot";
  const footer = doc.createElement("div");
  footer.className = "turn-footer subagent-footer";

  const ledger = doc.createElement("button");
  ledger.type = "button";
  ledger.className = "turn-ledger-summary";
  const info = doc.createElement("span");
  info.className = "turn-ledger-info";
  const infoGlyph = doc.createElementNS("http://www.w3.org/2000/svg", "svg");
  infoGlyph.setAttribute("class", "ic-ui");
  info.appendChild(infoGlyph);
  const mark = doc.createElement("span");
  mark.className = "turn-ledger-glyph";
  const text = doc.createElement("span");
  text.className = "turn-ledger-text";
  ledger.append(info, mark, text);

  const fact = doc.createElement("span");
  fact.className = "turn-fact";
  fact.textContent = "42.0s";

  footer.append(ledger, fact);
  foot.appendChild(footer);
  card.append(header, foot);

  mounted.get(doc)?.remove();
  mounted.set(doc, card);
  doc.body.appendChild(card);
  return { footer, ledger, info, headerInk, last: fact };
}

/** Computed style resolved in the element's OWN realm. `window.getComputedStyle`
 *  hands back an EMPTY declaration for a node in another document in Chromium, so
 *  every read below would be `""` for the iframe cases and every derived number
 *  `NaN` — which reads as a layout finding rather than a cross-realm call. */
function cs(el: Element, pseudo?: string): CSSStyleDeclaration {
  const view = el.ownerDocument.defaultView ?? window;
  return view.getComputedStyle(el, pseudo ?? null);
}

/** The band, in CSS px. It IS the footer's box: both helpers here used to subtract
 *  the row's own `border-top`, which was the seam between the body and the ledger
 *  rather than part of the row — and the card draws no internal seam any more
 *  (29-turns.css's file header), so the term was structurally zero and went. The
 *  numbers below are unchanged by that: the border was inside this border box, so
 *  its removal left the box's top edge where it was and took 1px off the height,
 *  which is what the subtraction had been correcting for. */
function band(footer: HTMLElement): number {
  return +footer.getBoundingClientRect().height.toFixed(2);
}

/** The gaps between a child's box and the band it sits in. */
function insets(
  footer: HTMLElement,
  child: HTMLElement,
): { top: number; bottom: number; end: number } {
  const f = footer.getBoundingClientRect();
  const c = child.getBoundingClientRect();
  return {
    top: +(c.top - f.top).toFixed(2),
    bottom: +(f.bottom - c.bottom).toFixed(2),
    end: +(f.right - c.right).toFixed(2),
  };
}

/** A length token off the document the case is measured in. */
function token(doc: Document, name: string): number {
  const probe = doc.createElement("div");
  probe.style.setProperty("block-size", `var(${name})`);
  doc.body.appendChild(probe);
  const px = probe.getBoundingClientRect().height;
  probe.remove();
  return px;
}

/** The height a control's target reaches, paint plus whatever its `::after`
 *  expander adds on the block axis. The pseudo has no rect of its own, so this
 *  reads the resolved inset — negative where it overhangs. */
function targetHeight(el: HTMLElement): number {
  const paint = el.getBoundingClientRect().height;
  const after = cs(el, "::after");
  const start = parseFloat(after.insetBlockStart);
  const end = parseFloat(after.insetBlockEnd);
  if (Number.isNaN(start) || Number.isNaN(end)) {
    return paint;
  }
  return +(paint - start - end).toFixed(2);
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

describe("the band is the ledger's hit box", () => {
  it("is on the tier the complaint was made on", () => {
    expect(phone.defaultView?.innerWidth).toBeLessThanOrEqual(640);
    expect(cs(phone.documentElement).getPropertyValue("--hit-floor").trim()).toBe("2.75rem");
  });

  it("gives a finger a 44px band with the target filling it", () => {
    const { footer, ledger } = mountFooter(phone, "rewind");
    const floor = token(phone, "--hit-floor");
    expect(floor).toBeCloseTo(44, 0);
    // The band IS the floor here: --ctl-h-dense is 40 on this tier, so the `max()`
    // resolves to the target rather than to the dense control height.
    expect(band(footer)).toBeCloseTo(floor, 0);
    expect(ledger.getBoundingClientRect().height).toBeCloseTo(floor, 0);
    const i = insets(footer, ledger);
    expect(i.top, `top ${i.top}px`).toBeCloseTo(0, 1);
    expect(i.bottom, `bottom ${i.bottom}px`).toBeCloseTo(0, 1);
  });

  it("keeps the mouse tier at the dense control height, target filling it", () => {
    // The other arm of the `max()`, and the reason it is a `max()`: with a mouse the
    // floor is 24 and the dense row is 32, so the band must NOT shrink to the target.
    expect(window.innerWidth).toBeGreaterThan(640);
    const { footer, ledger } = mountFooter(document, "rewind");
    const floor = token(document, "--hit-floor");
    const dense = token(document, "--ctl-h-dense");
    expect(floor).toBeCloseTo(24, 0);
    expect(dense).toBeCloseTo(32, 0);
    expect(band(footer)).toBeCloseTo(dense, 0);
    const h = ledger.getBoundingClientRect().height;
    expect(h).toBeCloseTo(dense, 0);
    expect(h).toBeGreaterThanOrEqual(floor);
    const i = insets(footer, ledger);
    expect(i.top, `top ${i.top}px`).toBeCloseTo(0, 1);
    expect(i.bottom, `bottom ${i.bottom}px`).toBeCloseTo(0, 1);
  });

  it("reaches the band's leading edge while the `i` keeps the card's ink gutter", () => {
    // The gutter moved from the footer onto the button, so the box reaches the edge
    // and the ink does not. Compared against the HEADER's own text rather than a
    // literal, because lining up with the rest of the card is the whole property.
    const { footer, ledger, info, headerText } = mountFooter(document, "rewind");
    expect(cs(footer).paddingInlineStart).toBe("0px");
    expect(ledger.getBoundingClientRect().left).toBeCloseTo(footer.getBoundingClientRect().left, 1);
    expect(info.getBoundingClientRect().left).toBeCloseTo(
      headerText.getBoundingClientRect().left,
      1,
    );
  });
});

describe("the trailing control", () => {
  it("keeps Rewind off the band's edges, because it paints a resting border", () => {
    // The one child that may not go flush. Both tiers, because its border is there
    // at every width and the band is tight on the mouse tier too.
    for (const [where, doc] of [
      ["phone", phone],
      ["desktop", document],
    ] as const) {
      const { footer, last } = mountFooter(doc, "rewind");
      expect(cs(last).borderTopWidth, `${where} resting border`).not.toBe("0px");
      const i = insets(footer, last);
      expect(i.top, `${where} top ${i.top}px`).toBeGreaterThan(1);
      expect(i.bottom, `${where} bottom ${i.bottom}px`).toBeGreaterThan(1);
      expect(i.end, `${where} end ${i.end}px`).toBeGreaterThan(1);
    }
  });

  it("gives Rewind's target the hit floor back past its paint", () => {
    // What the inset above costs, and the expander is what pays it: the paint is
    // smaller than the floor, the target is not.
    for (const [where, doc] of [
      ["phone", phone],
      ["desktop", document],
    ] as const) {
      const { last } = mountFooter(doc, "rewind");
      const floor = token(doc, "--hit-floor");
      const paint = last.getBoundingClientRect().height;
      expect(paint, `${where} paint ${paint}px against floor ${floor}px`).toBeLessThan(floor);
      expect(targetHeight(last), `${where} target`).toBeGreaterThanOrEqual(floor);
      // Width stays on the floor, so the expander only grows the block axis and
      // cannot reach the control 8px to its left.
      expect(cs(last, "::after").insetInlineStart).toBe("0px");
      expect(last.getBoundingClientRect().width).toBeGreaterThanOrEqual(floor);
    }
  });

  it("lets the … trigger fill the band, because it paints nothing at rest", () => {
    // The other trailing child, and the opposite call: a hover-only wash filling
    // the row reads as a row-height hover (`.turn-header`, `.ev-row-main`), so it
    // stays on the floor and needs no inset of its own.
    const { footer, last } = mountFooter(phone, "actions");
    const trigger = last.querySelector<HTMLElement>(".turn-action-more");
    expect(trigger).not.toBeNull();
    if (trigger === null) {
      return;
    }
    expect(cs(trigger).backgroundColor).toBe("rgba(0, 0, 0, 0)");
    expect(cs(trigger).borderTopWidth).toBe("0px");
    expect(trigger.getBoundingClientRect().height).toBeCloseTo(band(footer), 0);
  });

  it("ends on the SAME gutter the `i` starts on, whatever the trailing child is", () => {
    // The retired `:has()` rule tightened this edge to 4px for a row ending in a
    // control, which put Rewind's box 4px from the card while the `i`'s ink sat 12px
    // from the other side — reported as the two insets not matching. Held against
    // the LEADING declaration rather than a literal, and over both trailing
    // children, because a value that only matched for the text case is the shape
    // being removed. The fact slot is not a trailing child any more — it sits beside
    // the `i` — so the two controls are the whole population.
    for (const trailing of ["actions", "rewind"] as const) {
      const { footer, ledger, last } = mountFooter(document, trailing);
      const lead = parseFloat(cs(ledger).paddingInlineStart);
      expect(lead, `${trailing} leading`).toBeGreaterThan(0);
      expect(parseFloat(cs(footer).paddingInlineEnd), `${trailing} trailing`).toBeCloseTo(lead, 1);
      expect(insets(footer, last).end, `${trailing} last child`).toBeCloseTo(lead, 1);
    }
  });
});

describe("the delegate card mounts this row and gets the same gutters", () => {
  it("lines the `i` up with its own header's ink, on the card's own 12px", () => {
    // `.subagent-header` is `padding: var(--sp-2) var(--sp-3)`, so a delegate's
    // identity ink starts on the same gutter every other card's does — which is what
    // the retired 8px override in 14-tools.css was wrong about: the FOOT was the
    // outlier inside its own card, not a denser surface. Compared against the header
    // glyph rather than a literal, the way the turn card's case compares against its
    // header text.
    const { headerInk, info } = mountDelegate(document);
    expect(info.getBoundingClientRect().left).toBeCloseTo(
      headerInk.getBoundingClientRect().left,
      1,
    );
  });

  it("ends its fact slot on that same gutter, having no trailing control", () => {
    // The slot spans the flexible track, so on a footer with nothing after it the
    // slot's box is what ends on the gutter — the row's symmetric insets, measured.
    const { footer, ledger, last } = mountDelegate(document);
    const lead = parseFloat(cs(ledger).paddingInlineStart);
    expect(lead).toBeGreaterThan(0);
    expect(parseFloat(cs(footer).paddingInlineEnd)).toBeCloseTo(lead, 1);
    expect(insets(footer, last).end).toBeCloseTo(lead, 1);
  });
});
