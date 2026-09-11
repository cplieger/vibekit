// The turn's time slot: painted unconditionally, in a box that is RESERVED.
//
// The hover reveal this file used to be about is DELETED. A hover cannot carry a
// value — it does not exist on a touch device, and a reader who never moves a
// pointer over the card never learns how long the turn took — so the slot is simply
// always visible and the rail's second copy of the same number went with the gesture
// that revealed it (`turn-rail.ts`, `rail-labels.ts`).
//
// The RESERVED BOX survives the reveal's deletion on its own terms, which is why it
// is still the subject here. A geometric change to this slot moves `#messages`'
// `scrollHeight`, which feeds `scrollableBy()`, which is the timeline rail's own
// navigability gate at MIN_SCROLL_PX — so `display: none` or `visibility: hidden` on
// a slot that HAS a duration would take the box with it and, on a short chat sitting
// near that threshold, decide whether the rail exists.
//
// Two kinds of claim, which is why this file needs both halves of the css-rules
// helper (the shape `turn-dot-visibility-css.test.ts` states).
//
// COMPUTED, against the real assembled cascade: the slot is opaque with no pointer
// interaction of any kind, in the turn card's footer and in the delegate's copy of
// it, and its box occupies space.
//
// SOURCE, because computed style cannot answer it: that the inline gap is this
// element's own margin rather than a grid column-gap, which column it takes, and —
// the guard against the reveal coming back in a different stylesheet — that NO rule
// anywhere in the shipped bundle makes this slot conditional on a pointer. Which
// query a rule sits in is only readable as text or through the CSSOM; a synthetic
// hover drives no style recalc, and `CSS.forcePseudoState` is a devtools protocol
// call a test page cannot make.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";

const turns = loadCSS("29-turns.css");

let style: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  style = mountAppCSS();
  host = document.createElement("div");
  document.body.appendChild(host);
});

afterAll(() => {
  style.remove();
  host.remove();
});

/** A turn card with the footer parts that decide the row's layout: the ledger
 *  button, the time slot, and (optionally) the trailing Rewind whose empty column
 *  is what used to charge a phantom gap against the readout's right edge. */
function mountCard(opts: { elapsed: string; rewind?: boolean } = { elapsed: "1m 32s" }): {
  card: HTMLElement;
  footer: HTMLElement;
  slot: HTMLElement;
} {
  const card = document.createElement("div");
  card.className = "turn";
  const body = document.createElement("div");
  body.className = "turn-body";
  body.textContent = "some output";
  card.appendChild(body);

  const footer = document.createElement("div");
  footer.className = "turn-footer";
  const summary = document.createElement("button");
  summary.className = "turn-ledger-summary";
  summary.type = "button";
  const glyph = document.createElement("span");
  glyph.className = "turn-ledger-glyph";
  const text = document.createElement("span");
  text.className = "turn-ledger-text";
  text.textContent = "2 files +12 \u22124 \u00b7 2 cmds \u00b7 1.42 cr";
  summary.append(glyph, text);
  footer.appendChild(summary);

  const slot = document.createElement("time");
  slot.className = "turn-elapsed";
  slot.textContent = opts.elapsed;
  footer.appendChild(slot);

  if (opts.rewind === true) {
    const rewind = document.createElement("button");
    rewind.className = "turn-rewind";
    rewind.type = "button";
    rewind.textContent = "Rewind";
    footer.appendChild(rewind);
  }

  card.appendChild(footer);
  host.replaceChildren(card);
  return { card, footer, slot };
}

/** One entry per style rule in the SHIPPED bundle, carrying the selector chain that
 *  reaches it and every condition wrapping it — which is what `allRules` cannot
 *  report, because it flattens at-rule bodies into their contents and drops the
 *  prelude that gated them. Read through the CSSOM rather than by parsing text, so
 *  the answer comes from the engine that will apply these rules. */
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
        // A nested rule's own `selectorText` is relative (`&:hover`), so the chain
        // rather than the leaf is what names the element a nested gate would reach.
        innerChain = `${chain} ${rule.selectorText}`;
        out.push({ chain: innerChain, conditions: innerConditions });
      }
      // CSS nesting makes a `CSSStyleRule` a grouping rule too, so this descends
      // into `&` blocks as well as into `@media` / `@container` / `@supports`.
      if (rule instanceof CSSGroupingRule) {
        walk(rule.cssRules, innerChain, innerConditions);
      }
    }
  };
  walk(sheet.cssRules, "", "");
  return out;
}

describe("the time slot", () => {
  it("is opaque with no pointer interaction at all", () => {
    // The inverse of the case this replaced. Nothing is hovered, nothing is focused,
    // and the value is on screen — which is the whole of the change: a duration is
    // information, and a gesture is not a channel every reader has.
    const { slot } = mountCard();
    expect(getComputedStyle(slot).opacity).toBe("1");
  });

  it("occupies its box, which is what keeps the rail's gate out of this", () => {
    // The assertion a DOM emulator could not make: happy-dom reports every layout
    // box as 0, so "the space is reserved" is unfalsifiable there.
    const { slot } = mountCard();
    expect(slot.getBoundingClientRect().width).toBeGreaterThan(0);
    expect(slot.getBoundingClientRect().height).toBeGreaterThan(0);
  });

  it("is neither display:none nor visibility:hidden", () => {
    // Both take the box with them, and the box is what `#messages`' scrollHeight is
    // priced off. `syncElapsed` still hides a slot with NO duration — an empty
    // `<time>` makes no claim and has no box to reserve — which is a different rule
    // from hiding one that has a value.
    const { slot } = mountCard();
    const cs = getComputedStyle(slot);
    expect(cs.display).not.toBe("none");
    expect(cs.visibility).toBe("visible");
  });

  it("is equally visible in the DELEGATE footer's copy", () => {
    // `turn-footer.ts` is reused inside `.subagent-foot` (14-tools.css). The reveal
    // that existed here was scoped to `.turn > .turn-footer` precisely to spare this
    // copy; with the reveal gone the two footers need no scoping rule to agree, and
    // this is the case that would notice a new one.
    const delegate = document.createElement("div");
    delegate.className = "subagent-foot";
    const footer = document.createElement("div");
    footer.className = "turn-footer subagent-footer";
    const slot = document.createElement("time");
    slot.className = "turn-elapsed";
    slot.textContent = "12.0s";
    footer.appendChild(slot);
    delegate.appendChild(footer);
    host.replaceChildren(delegate);
    expect(getComputedStyle(slot).opacity).toBe("1");
  });
});

describe("the readout ends on the card's own gutter", () => {
  it("with a Rewind beside it", () => {
    const { footer, slot } = mountCard({ elapsed: "1m 32s", rewind: true });
    // The time is not the last column when Rewind is present, so it is Rewind that
    // owns the gutter — the check here is that the row's LAST item does.
    const rewind = footer.querySelector<HTMLElement>(".turn-rewind");
    expect(rewind).not.toBeNull();
    const cs = getComputedStyle(footer);
    const contentRight =
      footer.getBoundingClientRect().right - Number.parseFloat(cs.paddingRight || "0");
    expect(
      Math.abs((rewind?.getBoundingClientRect().right ?? 0) - contentRight),
    ).toBeLessThanOrEqual(1);
    expect(slot.getBoundingClientRect().right).toBeLessThan(contentRight);
  });

  it("and on its own when nothing follows it", () => {
    // The measured defect this repeats: an EMPTY trailing column still charges the
    // grid's column gap beside it, which held `2 cmds` 28px off a `--sp-3` gutter.
    // The slot's gap is its own `margin-inline-start`, so with no Rewind the time
    // lands exactly on the content edge.
    const { footer, slot } = mountCard({ elapsed: "1m 32s" });
    const cs = getComputedStyle(footer);
    const contentRight =
      footer.getBoundingClientRect().right - Number.parseFloat(cs.paddingRight || "0");
    expect(Math.abs(slot.getBoundingClientRect().right - contentRight)).toBeLessThanOrEqual(1);
  });
});

describe("the slot, read as source", () => {
  it("is gated on no pointer state anywhere in the shipped bundle", () => {
    // THE GUARD AGAINST THE REVEAL COMING BACK, and it deliberately sweeps the whole
    // assembled cascade rather than 29-turns.css: a rule reintroducing the gesture
    // gate is just as effective from another slice of the bundle, and keying on one
    // file would not see it. `any-hover` contains `hover`, so one pattern covers the
    // query form and the pseudo-class form, and `:focus-within` is in the pattern
    // because the deleted reveal used it as the keyboard half of the same gate.
    const sheet = style.sheet;
    if (sheet === null) {
      // Stated as a throw rather than an `expect`, so the walk below takes the sheet
      // itself rather than a maybe — the shape `css-rules.ts` uses for the same reason.
      throw new Error("the mounted bundle did not parse");
    }
    const all = shippedRules(sheet);
    // The premise: the sweep is reading a bundle that actually contains this slot,
    // so an empty result below means "not gated" rather than "not found".
    const mine = all.filter((r) => r.chain.includes(".turn-elapsed"));
    expect(mine.length).toBeGreaterThan(0);
    const gated = mine
      .filter((r) => /hover|focus-within/iu.test(`${r.chain} ${r.conditions}`))
      .map((r) => `${r.conditions.trim()} { ${r.chain.trim()} }`);
    expect(gated).toEqual([]);
  });

  it("carries its inline gap as a margin, never as a grid column-gap", () => {
    // The recorded measurement on this exact row: a column gap is charged between
    // tracks even when the next one is EMPTY, so a footer with no Rewind held its
    // readout off the gutter.
    const slot = ruleContaining(turns, ".turn-footer > .turn-elapsed", "top");
    expect(slot.body).toMatch(/margin-inline-start:/u);
    const footer = ruleContaining(turns, ".turn-footer", "top");
    expect(footer.body).toMatch(/row-gap:/u);
    expect(footer.body).not.toMatch(/column-gap:/u);
    // A bare `gap` would reintroduce the column gap through the shorthand.
    expect(footer.body).not.toMatch(/[^-]gap:/u);
  });

  it("takes a column of its own, ahead of the actions and Rewind", () => {
    // The grid grew from three tracks to four; the two trailing items moved with it,
    // and asserting the numbers is what stops a future edit stacking two items in
    // one cell.
    expect(ruleContaining(turns, ".turn-footer", "top").body).toMatch(
      /grid-template-columns:\s*minmax\(0,\s*1fr\)\s+auto\s+auto\s+auto/u,
    );
    expect(ruleContaining(turns, ".turn-footer > .turn-elapsed", "top").body).toMatch(
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
