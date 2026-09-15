// The footer's fact slot: one outcome fact beside the `i`, clipped rather than
// wrapped, in the row's only flexible track. What has to hold is that the text never
// wraps, never pushes the `…` trigger or Rewind off the row, and is gated on no
// pointer state anywhere in the shipped bundle — a hover cannot carry a value, so a
// hover gate on it would take the run time away from every touch device.
//
// Two kinds of claim, which is why this file needs both halves of the css-rules
// helper (the shape `turn-dot-visibility-css.test.ts` states).
//
// COMPUTED, against the real assembled cascade: where the slot lands beside the
// button on desktop, and what a long fact does at 360px with the coarse tier — the
// phone case is measured in an IFRAME, because the `…` collapse and Rewind's word
// both live behind `width <= 40rem`.
//
// SOURCE, because computed style cannot answer it: that the inline gap is this
// element's own margin rather than a grid column-gap, which column it takes, and
// that no rule in the bundle makes it conditional on a pointer. Which query a rule
// sits in is only readable as text or through the CSSOM.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";

const turns = loadCSS("29-turns.css");

let style: HTMLStyleElement;
let frame: HTMLIFrameElement;
let phone: Document;

/** The card this helper last mounted, per document. */
const mounted = new WeakMap<Document, HTMLElement>();

/** A fact that no phone-width track can hold, so the ellipsis has to fire. */
const LONG_FACT = "12 files +1234 \u22125678 and a great deal more than one track can hold";

interface Mounted {
  footer: HTMLElement;
  ledger: HTMLElement;
  slot: HTMLElement;
  actions: HTMLElement | null;
  rewind: HTMLElement | null;
}

/** A turn card with the footer parts that decide the row's layout, as
 *  `buildTurnFooter`, `mountTurnActions` and `mountRewind` assemble one. */
function mountCard(
  doc: Document,
  opts: { fact: string; word?: string; actions?: boolean; rewind?: boolean },
): Mounted {
  const card = doc.createElement("div");
  card.className = "turn";
  const body = doc.createElement("div");
  body.className = "turn-body";
  body.textContent = "some output";
  card.appendChild(body);

  const footer = doc.createElement("div");
  footer.className = "turn-footer";
  // The two attributes `updateTurnFooter` writes; a clean turn hides its glyph.
  const clean = (opts.word ?? "") === "";
  footer.dataset["outcome"] = clean ? "completed" : "unknown";
  footer.dataset["severity"] = clean ? "clean" : "stopped";
  const ledger = doc.createElement("button");
  ledger.type = "button";
  ledger.className = "turn-ledger-summary";
  const info = doc.createElement("span");
  info.className = "turn-ledger-info";
  const infoGlyph = doc.createElementNS("http://www.w3.org/2000/svg", "svg");
  infoGlyph.setAttribute("class", "ic-ui");
  info.appendChild(infoGlyph);
  const glyph = doc.createElement("span");
  glyph.className = "turn-ledger-glyph";
  const text = doc.createElement("span");
  text.className = "turn-ledger-text";
  text.textContent = opts.word ?? "";
  ledger.append(info, glyph, text);
  footer.appendChild(ledger);

  const slot = doc.createElement("span");
  slot.className = "turn-fact";
  slot.textContent = opts.fact;
  footer.appendChild(slot);

  let actions: HTMLElement | null = null;
  if (opts.actions === true) {
    actions = doc.createElement("span");
    actions.className = "turn-actions-buttons";
    const more = doc.createElement("details");
    more.className = "turn-actions-more";
    const summary = doc.createElement("summary");
    summary.className = "turn-action-btn turn-action-more";
    const moreGlyph = doc.createElementNS("http://www.w3.org/2000/svg", "svg");
    moreGlyph.setAttribute("class", "ic-ui");
    summary.appendChild(moreGlyph);
    const group = doc.createElement("span");
    group.className = "turn-actions-group";
    more.append(summary, group);
    actions.appendChild(more);
    footer.appendChild(actions);
  }

  let rewind: HTMLButtonElement | null = null;
  if (opts.rewind === true) {
    rewind = doc.createElement("button");
    rewind.type = "button";
    rewind.className = "turn-rewind";
    const rewindGlyph = doc.createElementNS("http://www.w3.org/2000/svg", "svg");
    rewindGlyph.setAttribute("class", "ic-ui");
    const label = doc.createElement("span");
    label.className = "turn-rewind-label";
    label.textContent = "Rewind";
    rewind.append(rewindGlyph, label);
    footer.appendChild(rewind);
  }

  card.appendChild(footer);
  // Only this helper's PREVIOUS card goes, never `body.replaceChildren`: the phone
  // frame is a child of the page's body, so wiping it detaches the iframe.
  mounted.get(doc)?.remove();
  mounted.set(doc, card);
  doc.body.appendChild(card);
  return { footer, ledger, slot, actions, rewind };
}

/** The delegate card's copy of the row, nested in `.subagent-foot`. */
function mountDelegate(doc: Document, fact: string): Mounted {
  const delegate = doc.createElement("div");
  delegate.className = "subagent-block";
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
  const glyph = doc.createElement("span");
  glyph.className = "turn-ledger-glyph";
  const text = doc.createElement("span");
  text.className = "turn-ledger-text";
  ledger.append(info, glyph, text);
  const slot = doc.createElement("span");
  slot.className = "turn-fact";
  slot.textContent = fact;
  footer.append(ledger, slot);
  foot.appendChild(footer);
  delegate.appendChild(foot);
  mounted.get(doc)?.remove();
  mounted.set(doc, delegate);
  doc.body.appendChild(delegate);
  return { footer, ledger, slot, actions: null, rewind: null };
}

/** Computed style resolved in the element's OWN realm; the page's
 *  `getComputedStyle` hands back an empty declaration for an iframe's node. */
function cs(el: Element): CSSStyleDeclaration {
  const view = el.ownerDocument.defaultView ?? window;
  return view.getComputedStyle(el);
}

/** One entry per style rule in the SHIPPED bundle, carrying the selector chain that
 *  reaches it and every condition wrapping it — which is what `allRules` cannot
 *  report, because it flattens at-rule bodies into their contents and drops the
 *  prelude that gated them. */
function shippedRules(sheet: CSSStyleSheet): { chain: string; conditions: string }[] {
  const out: { chain: string; conditions: string }[] = [];
  const walk = (rules: CSSRuleList, chain: string, conditions: string): void => {
    for (let i = 0; i < rules.length; i++) {
      const rule = rules.item(i);
      if (rule === null) {
        continue;
      }
      let innerConditions = conditions;
      if (rule instanceof CSSConditionRule) {
        innerConditions = `${conditions} ${rule.conditionText}`;
      }
      let innerChain = chain;
      if (rule instanceof CSSStyleRule) {
        innerChain = `${chain} ${rule.selectorText}`;
        out.push({ chain: innerChain, conditions: innerConditions });
      }
      if (rule instanceof CSSGroupingRule) {
        walk(rule.cssRules, innerChain, innerConditions);
      }
    }
  };
  walk(sheet.cssRules, "", "");
  return out;
}

beforeAll(() => {
  style = mountAppCSS();
  frame = document.createElement("iframe");
  frame.width = "360";
  frame.height = "800";
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

describe("the fact sits beside the `i`", () => {
  it("starts one inline gap after the outcome word, not at the far end of a track", () => {
    // Against the INK rather than the button's box: a stretched button in a flexible
    // track would put its box edge beside the fact whatever the word's width.
    const { ledger, slot } = mountCard(document, { fact: "3 files +42 \u221217", word: "Failed" });
    const word = ledger.querySelector<HTMLElement>(".turn-ledger-text");
    if (word === null) {
      throw new Error("no .turn-ledger-text");
    }
    const gap = slot.getBoundingClientRect().left - word.getBoundingClientRect().right;
    expect(gap).toBeGreaterThan(0);
    expect(gap).toBeLessThanOrEqual(16);
  });

  it("is opaque with no pointer interaction at all", () => {
    const { slot } = mountCard(document, { fact: "5 commands" });
    const style = cs(slot);
    expect(style.opacity).toBe("1");
    expect(style.display).not.toBe("none");
    expect(style.visibility).toBe("visible");
  });

  it("inherits the row's secondary ink rather than the hint tier", () => {
    // Tertiary on this tinted band is under AA for 12px text (vibekit-ui.md), and
    // this readout is the row's main text now.
    const { footer, slot } = mountCard(document, { fact: "5 commands" });
    expect(cs(slot).color).toBe(cs(footer).color);
  });

  it("stays beside the `i` on a clean turn, where the word is empty", () => {
    // An empty `.turn-ledger-text` used to charge the button's flex gap, which would
    // sit between the `i` and the fact as dead space.
    const { ledger, slot } = mountCard(document, { fact: "5 commands", word: "" });
    const text = ledger.querySelector<HTMLElement>(".turn-ledger-text");
    const info = ledger.querySelector<HTMLElement>(".turn-ledger-info");
    if (text === null || info === null) {
      throw new Error("no ledger parts");
    }
    expect(cs(text).display).toBe("none");
    const gap = slot.getBoundingClientRect().left - info.getBoundingClientRect().right;
    expect(gap).toBeGreaterThan(0);
    expect(gap).toBeLessThanOrEqual(16);
  });
});

describe("at 360px with a finger", () => {
  it("is on the tier the constraint is about", () => {
    // The frame's own border takes 2px a side off the 360 it was given.
    expect(phone.defaultView?.innerWidth).toBeLessThanOrEqual(360);
    expect(cs(phone.documentElement).getPropertyValue("--hit-floor").trim()).toBe("2.75rem");
  });

  it("clips a long fact with an ellipsis on ONE line", () => {
    const { slot } = mountCard(phone, {
      fact: LONG_FACT,
      word: "Outcome unknown",
      actions: true,
      rewind: true,
    });
    const style = cs(slot);
    expect(style.textOverflow).toBe("ellipsis");
    expect(style.overflow).toBe("hidden");
    expect(slot.scrollWidth).toBeGreaterThan(slot.clientWidth);
    const lineHeight = parseFloat(style.fontSize) * 1.6;
    expect(slot.getBoundingClientRect().height).toBeLessThanOrEqual(lineHeight);
  });

  it("pushes neither the … trigger nor Rewind off the row", () => {
    const { footer, slot, actions, rewind } = mountCard(phone, {
      fact: LONG_FACT,
      word: "Outcome unknown",
      actions: true,
      rewind: true,
    });
    if (actions === null || rewind === null) {
      throw new Error("no trailing controls mounted");
    }
    const row = slot.getBoundingClientRect();
    // One grid row: every trailing control is centred on the slot's own centre.
    const mid = (r: DOMRect): number => r.top + r.height / 2;
    expect(Math.abs(mid(actions.getBoundingClientRect()) - mid(row))).toBeLessThanOrEqual(1);
    expect(Math.abs(mid(rewind.getBoundingClientRect()) - mid(row))).toBeLessThanOrEqual(1);
    // Rewind still ends on the row's own gutter, and the slot ends before the trigger.
    const f = footer.getBoundingClientRect();
    const gutter = parseFloat(cs(footer).paddingInlineEnd);
    expect(Math.abs(f.right - rewind.getBoundingClientRect().right - gutter)).toBeLessThanOrEqual(
      1,
    );
    expect(row.right).toBeLessThanOrEqual(actions.getBoundingClientRect().left);
    expect(rewind.getBoundingClientRect().right).toBeLessThanOrEqual(f.right);
  });

  it("keeps the outcome word on one line beside the `i`", () => {
    // The button's track is `auto`, which can shrink below max-content under
    // pressure; `nowrap` on the word is what stops it folding onto two lines.
    const { ledger } = mountCard(phone, {
      fact: LONG_FACT,
      word: "Outcome unknown",
      actions: true,
      rewind: true,
    });
    const text = ledger.querySelector<HTMLElement>(".turn-ledger-text");
    if (text === null) {
      throw new Error("no .turn-ledger-text");
    }
    expect(cs(text).whiteSpace).toBe("nowrap");
    expect(text.getBoundingClientRect().height).toBeLessThanOrEqual(
      parseFloat(cs(text).fontSize) * 1.6,
    );
  });
});

describe("the slot is WITHHELD where the track cannot hold a whole fact", () => {
  /** Squeeze one card until its footer's CONTENT box is `cq` px, which is what the
   *  query measures — 12px under the footer's own `clientWidth`, the row carrying a
   *  trailing gutter and no leading one. */
  function atContainerWidth(cq: number, opts: { word: string; rewind: boolean }): HTMLElement {
    const m = mountCard(phone, {
      fact: LONG_FACT,
      word: opts.word,
      actions: true,
      rewind: opts.rewind,
    });
    const card = m.footer.parentElement;
    if (card === null) {
      throw new Error("footer has no card");
    }
    const gutter = parseFloat(cs(m.footer).paddingInlineEnd);
    card.style.inlineSize = `${String(cq + gutter)}px`;
    return m.slot;
  }

  // 306.4px is 19.15rem, the threshold; a coarse track reaches the 25.3px floor there.
  const BELOW = 300;
  const ABOVE = 312;

  it("withholds it on a non-clean turn with Rewind, and not one pixel wider", () => {
    expect(cs(atContainerWidth(BELOW, { word: "Outcome unknown", rewind: true })).display).toBe(
      "none",
    );
    expect(cs(atContainerWidth(ABOVE, { word: "Outcome unknown", rewind: true })).display).not.toBe(
      "none",
    );
  });

  it("leaves the two shapes that keep a legible track alone", () => {
    // A CLEAN turn's track is the row's main readout and runs 120-212px across these
    // sizes; a non-clean turn with no Rewind has that control's width back. Hiding
    // either is the collateral the scoping exists to avoid, so both are pinned BELOW
    // the threshold where an unscoped rule would take them.
    expect(cs(atContainerWidth(BELOW, { word: "", rewind: true })).display).not.toBe("none");
    expect(
      cs(atContainerWidth(BELOW, { word: "Outcome unknown", rewind: false })).display,
    ).not.toBe("none");
  });
});

describe("the delegate footer's copy", () => {
  it("clips the same way, in the same track", () => {
    // `turn-footer.ts` is reused inside `.subagent-foot` (14-tools.css), so the
    // rules under test are the ones 29-turns.css declares rather than a copy.
    const { slot } = mountDelegate(phone, LONG_FACT);
    const style = cs(slot);
    expect(style.textOverflow).toBe("ellipsis");
    expect(slot.scrollWidth).toBeGreaterThan(slot.clientWidth);
    expect(slot.getBoundingClientRect().height).toBeLessThanOrEqual(
      parseFloat(style.fontSize) * 1.6,
    );
  });
});

describe("the slot, read as source", () => {
  it("is gated on no pointer state anywhere in the shipped bundle", () => {
    // THE GUARD AGAINST A REVEAL, sweeping the whole assembled cascade rather than
    // 29-turns.css: a gesture gate is just as effective from another slice.
    // `any-hover` contains `hover`, so one pattern covers the query form and the
    // pseudo-class form, and `:focus-within` is the keyboard half of the same gate.
    const sheet = style.sheet;
    if (sheet === null) {
      throw new Error("the mounted bundle did not parse");
    }
    const all = shippedRules(sheet);
    const mine = all.filter((r) => r.chain.includes(".turn-fact"));
    expect(mine.length).toBeGreaterThan(0);
    const gated = mine
      .filter((r) => /hover|focus-within/iu.test(`${r.chain} ${r.conditions}`))
      .map((r) => `${r.conditions.trim()} { ${r.chain.trim()} }`);
    expect(gated).toEqual([]);
  });

  it("carries its inline gap as a margin, never as a grid column-gap", () => {
    // A column gap is charged between tracks even when the next one is EMPTY, so a
    // footer with no Rewind would hold its trailing control off the gutter.
    const slot = ruleContaining(turns, ".turn-footer > .turn-fact", "top");
    expect(slot.body).toMatch(/margin-inline-start:/u);
    const footer = ruleContaining(turns, ".turn-footer", "top");
    expect(footer.body).toMatch(/row-gap:/u);
    expect(footer.body).not.toMatch(/column-gap:/u);
    expect(footer.body).not.toMatch(/[^-]gap:/u);
  });

  it("takes the row's only flexible track, between the button and the controls", () => {
    // The button shrink-wraps and the fact takes what is left, which is what makes
    // the trailing controls unpushable by construction.
    expect(ruleContaining(turns, ".turn-footer", "top").body).toMatch(
      /grid-template-columns:\s*auto\s+minmax\(0,\s*1fr\)\s+auto\s+auto/u,
    );
    expect(ruleContaining(turns, ".turn-footer > .turn-fact", "top").body).toMatch(
      /grid-column:\s*2/u,
    );
    expect(ruleContaining(turns, ".turn-footer > .turn-actions-buttons", "top").body).toMatch(
      /grid-column:\s*3/u,
    );
    expect(ruleContaining(turns, ".turn-footer > .turn-rewind", "top").body).toMatch(
      /grid-column:\s*4/u,
    );
  });
});
