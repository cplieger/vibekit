// The turn's time slot: revealed on hover, in a box that is RESERVED at rest.
//
// Two kinds of claim, which is why this file needs both halves of the css-rules
// helper (the shape `turn-dot-visibility-css.test.ts` states).
//
// COMPUTED, against the real assembled cascade: that the slot is invisible at rest,
// that its box still occupies space, and that revealing it moves NOTHING. The last
// one is the whole design constraint and it cannot be reasoned about — a geometric
// expansion would change the card's height, which changes `#messages`'
// `scrollHeight`, which feeds `scrollableBy()`, which is the timeline rail's own
// navigability gate at MIN_SCROLL_PX. On a short chat sitting near that threshold,
// hovering a turn could flip the rail on and off.
//
// SOURCE, because computed style cannot answer it: that the gate is `any-hover` and
// not `hover` (a synthetic hover drives no style recalc, and `CSS.forcePseudoState`
// is a devtools protocol call a test page cannot make, so which QUERY the rule sits
// in is only readable as text), and that the rest state and the transition name no
// layout property.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";

const turns = loadCSS("29-turns.css");
const REVEAL_QUERY = "any-hover: hover";

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

describe("the reveal gate is live in this browser", () => {
  it("matches any-hover, so every computed case below is measuring the gated rule", () => {
    // The premise. Without it a `(any-hover: none)` runtime would report `opacity: 1`
    // at rest and the rest-state case would pass for the wrong reason — reading as
    // "the reveal is broken" rather than "this browser has no hover".
    expect(window.matchMedia(`(${REVEAL_QUERY})`).matches).toBe(true);
  });
});

describe("the time slot at rest", () => {
  it("is invisible", () => {
    const { slot } = mountCard();
    expect(getComputedStyle(slot).opacity).toBe("0");
  });

  it("still occupies its box, which is what makes the reveal shift nothing", () => {
    // The assertion a DOM emulator could not make: happy-dom reports every layout
    // box as 0, so "the space is reserved" is unfalsifiable there.
    const { slot } = mountCard();
    expect(slot.getBoundingClientRect().width).toBeGreaterThan(0);
    expect(slot.getBoundingClientRect().height).toBeGreaterThan(0);
  });

  it("is neither display:none nor visibility:hidden", () => {
    // Both take the box with them, so the row's right edge would move the moment a
    // pointer arrived — the layout shift this reveal was required not to cause.
    const { slot } = mountCard();
    const cs = getComputedStyle(slot);
    expect(cs.display).not.toBe("none");
    expect(cs.visibility).toBe("visible");
  });
});

describe("revealing the time moves nothing", () => {
  it("leaves the card's height byte-identical", () => {
    // The reveal's ONE declaration is `opacity` (asserted as a source fact below),
    // so applying it is the honest way to measure the geometry it produces.
    const { card, slot } = mountCard();
    const before = card.offsetHeight;
    expect(before).toBeGreaterThan(0);
    slot.style.opacity = "1";
    expect(card.offsetHeight).toBe(before);
  });

  it("leaves the footer's own height and the slot's box identical", () => {
    const { footer, slot } = mountCard();
    const footerBefore = footer.offsetHeight;
    const slotBefore = slot.getBoundingClientRect();
    slot.style.opacity = "1";
    const slotAfter = slot.getBoundingClientRect();
    expect(footer.offsetHeight).toBe(footerBefore);
    expect(slotAfter.width).toBe(slotBefore.width);
    expect(slotAfter.right).toBe(slotBefore.right);
  });

  it("leaves the container's scrollHeight alone, which is the rail's gate", () => {
    // `scrollableBy()` is content height minus viewport height, and the rail hides
    // itself below MIN_SCROLL_PX. This is the one coupling a height change would
    // break, so it is measured rather than reasoned about.
    const scroller = document.createElement("div");
    scroller.style.cssText = "height:80px;overflow-y:auto;";
    host.replaceChildren(scroller);
    const inner = document.createElement("div");
    scroller.appendChild(inner);
    const saved = host;
    // Re-mount the card inside the scroller by hand, since mountCard owns `host`.
    const card = document.createElement("div");
    card.className = "turn";
    const footer = document.createElement("div");
    footer.className = "turn-footer";
    const slot = document.createElement("time");
    slot.className = "turn-elapsed";
    slot.textContent = "1m 32s";
    footer.appendChild(slot);
    card.appendChild(footer);
    inner.appendChild(card);
    // Enough content to overflow, so scrollHeight is a real number.
    const filler = document.createElement("div");
    filler.style.cssText = "height:400px;";
    inner.appendChild(filler);

    const before = scroller.scrollHeight;
    slot.style.opacity = "1";
    expect(scroller.scrollHeight).toBe(before);
    expect(saved).toBe(host);
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

describe("the reveal, read as source", () => {
  it("is gated on any-hover, never on hover", () => {
    // `hover` and `pointer` report only the PRIMARY input, and iPadOS answers
    // `hover: none` with a trackpad attached — so a `hover: hover` gate silently
    // drops the rule on every touch-primary device. Both directions are asserted:
    // the rule is in the right query AND `29-turns.css` grew no `(hover: hover)`
    // block for it.
    const rest = ruleContaining(turns, ".turn > .turn-footer > .turn-elapsed", REVEAL_QUERY);
    expect(rest.body).toMatch(/opacity:\s*0/u);
    expect(turns).not.toContain("@media (hover: hover)");
  });

  it("reveals on focus-within as well as hover", () => {
    // Not decoration: a keyboard user tabbing into the ledger button or a file row
    // must see the time, and hover alone would hide it from them permanently.
    const shown = ruleContaining(turns, ".turn:hover > .turn-footer > .turn-elapsed", REVEAL_QUERY);
    expect(shown.selector).toContain(".turn:focus-within > .turn-footer > .turn-elapsed");
    expect(shown.body).toMatch(/opacity:\s*1/u);
  });

  it("names no layout property in the rest state or the transition", () => {
    const rest = ruleContaining(turns, ".turn > .turn-footer > .turn-elapsed", REVEAL_QUERY);
    expect(rest.body).not.toMatch(/display:/u);
    expect(rest.body).not.toMatch(/visibility:/u);
    // The transition is the other half: a `block-size` or `width` here would make
    // the reveal geometric whatever the rest state says.
    expect(rest.body).toMatch(/transition:\s*opacity/u);
    expect(rest.body).not.toMatch(/transition:.*(block-size|height|width|padding|margin)/u);
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

  it("leaves the DELEGATE footer's copy of the slot always visible", () => {
    // `turn-footer.ts` is reused inside `.subagent-foot` (14-tools.css). The reveal
    // is scoped to `.turn > .turn-footer`, so a delegate's duration stays on screen
    // — it only exists once its card is expanded, and hiding it behind a second
    // hover would take away a value that is visible today.
    const rest = ruleContaining(turns, ".turn > .turn-footer > .turn-elapsed", REVEAL_QUERY);
    expect(rest.selector).not.toContain(".subagent");
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
