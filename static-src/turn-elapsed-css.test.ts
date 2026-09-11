// The turn's time slot: quiet at rest on EVERY device, revealed by hover or focus,
// in a box that is RESERVED at rest.
//
// COMPUTED, against the real assembled cascade: invisible at rest under the bundle
// as shipped AND under one with the hover query stripped, which is what a device
// answering `any-hover: none` computes; the box still occupies space; and revealing
// it moves NOTHING. That last one is the design constraint and cannot be reasoned
// about — a geometric expansion changes the card's height, which changes
// `#messages`' `scrollHeight`, which feeds `scrollableBy()`, the timeline rail's own
// navigability gate at MIN_SCROLL_PX.
//
// SOURCE, because computed style cannot answer it: which QUERY each of the three
// rules sits in. A synthetic hover drives no style recalc and `CSS.forcePseudoState`
// is a devtools protocol call a test page cannot make.
import { describe, it, expect, beforeAll, afterAll, vi } from "vitest";

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

/** A delegate's footer: the same builder's output, reused inside `.subagent-foot`
 *  where there is no `.turn` ancestor to reveal from. */
function mountDelegate(): { card: HTMLElement; slot: HTMLElement } {
  const card = document.createElement("div");
  card.className = "subagent-block";
  const foot = document.createElement("div");
  foot.className = "subagent-foot";
  const footer = document.createElement("div");
  footer.className = "turn-footer";
  const slot = document.createElement("time");
  slot.className = "turn-elapsed";
  slot.textContent = "12.0s";
  footer.appendChild(slot);
  foot.appendChild(footer);
  card.appendChild(foot);
  host.replaceChildren(card);
  return { card, slot };
}

/** The bundle a device with no hover computes: every `@media (any-hover: hover)`
 *  block dropped, nested ones included. Comments go first, so a rule's own prose
 *  naming the query cannot be mistaken for the query. */
function withoutHoverBlocks(css: string): string {
  const marker = `@media (${REVEAL_QUERY})`;
  let out = css.replace(/\/\*[\s\S]*?\*\//gu, " ");
  for (;;) {
    const at = out.indexOf(marker);
    if (at < 0) {
      return out;
    }
    const open = out.indexOf("{", at);
    let depth = 0;
    let end = out.length - 1;
    for (let i = open; i < out.length; i++) {
      if (out[i] === "{") {
        depth++;
      } else if (out[i] === "}") {
        depth--;
        if (depth === 0) {
          end = i;
          break;
        }
      }
    }
    out = out.slice(0, at) + out.slice(end + 1);
  }
}

/** Run one case against that bundle instead of the shipped one. */
async function withNoHoverCSS(fn: () => Promise<void> | void): Promise<void> {
  const stripped = document.createElement("style");
  stripped.textContent = withoutHoverBlocks(style.textContent ?? "");
  style.remove();
  document.head.appendChild(stripped);
  try {
    await fn();
  } finally {
    stripped.remove();
    document.head.appendChild(style);
  }
}

/** The reveal is a 0.2s opacity transition, so the value one tick after focus is
 *  still the resting one. Poll for the settled end rather than the first frame. */
async function expectRevealed(slot: HTMLElement): Promise<void> {
  await vi.waitFor(() => {
    expect(getComputedStyle(slot).opacity).toBe("1");
  });
}

describe("the reveal gate is live in this browser", () => {
  it("matches any-hover, so every computed case below is measuring the gated rule", () => {
    // The premise. Without it a `(any-hover: none)` runtime would report `opacity: 1`
    // at rest and the rest-state case would pass for the wrong reason — reading as
    // "the reveal is broken" rather than "this browser has no hover".
    expect(window.matchMedia(`(${REVEAL_QUERY})`).matches).toBe(true);
  });

  it("and the emulated bundle really drops that query", () => {
    // The other premise: without it the no-hover cases measure the shipped cascade
    // twice and prove nothing about a device that matches no hover query.
    const shipped = style.textContent ?? "";
    const stripped = withoutHoverBlocks(shipped);
    expect(shipped).toContain(`@media (${REVEAL_QUERY})`);
    expect(stripped).not.toContain(`@media (${REVEAL_QUERY})`);
    expect(stripped.length).toBeLessThan(shipped.length);
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

  it("is invisible on a device with no hover at all", async () => {
    // The rest state sits outside every media query, so a device that matches none
    // of them still paints nothing — which is the whole of the defect: with the rest
    // state inside the hover query, a phone showed every turn's duration at rest.
    await withNoHoverCSS(() => {
      const { slot } = mountCard();
      expect(getComputedStyle(slot).opacity).toBe("0");
    });
  });
});

describe("the keyboard path", () => {
  it("lifts the slot when focus enters the card", async () => {
    const { footer, slot } = mountCard();
    footer.querySelector<HTMLButtonElement>(".turn-ledger-summary")?.focus();
    await expectRevealed(slot);
  });

  it("still lifts it on a device with no hover at all", async () => {
    // The reason the focus reveal is ungated: a keyboard exists where a pointer does
    // not, and inside the hover query this path would be withdrawn on exactly the
    // devices where the ledger disclosure is the only other one.
    await withNoHoverCSS(async () => {
      const { footer, slot } = mountCard();
      footer.querySelector<HTMLButtonElement>(".turn-ledger-summary")?.focus();
      await expectRevealed(slot);
    });
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
  it("declares the rest state outside every media query", () => {
    // Unscoped and ungated, so no device and no footer inherits a duration painted
    // at rest. `"top"` demands exactly one match outside every at-rule.
    const rest = ruleContaining(turns, ".turn-footer > .turn-elapsed", "top");
    expect(rest.body).toMatch(/opacity:\s*0/u);
  });

  it("gates only the hover reveal on any-hover", () => {
    // `hover` and `pointer` report only the PRIMARY input, and iPadOS answers
    // `hover: none` with a trackpad attached — so a `hover: hover` gate silently
    // drops the rule on every touch-primary device.
    const shown = ruleContaining(turns, ".turn:hover > .turn-footer > .turn-elapsed", REVEAL_QUERY);
    expect(shown.body).toMatch(/opacity:\s*1/u);
    expect(turns).not.toContain("@media (hover: hover)");
  });

  it("declares the focus reveal outside every media query", () => {
    const focus = ruleContaining(turns, ".turn:focus-within > .turn-footer > .turn-elapsed", "top");
    expect(focus.body).toMatch(/opacity:\s*1/u);
  });

  it("reveals on focus-within as well as hover", () => {
    // The pair, and both halves name BOTH footers: `turn-footer.ts` is reused inside
    // `.subagent-foot`, whose copy has no `.turn` ancestor to reveal from.
    const hover = ruleContaining(turns, ".turn:hover > .turn-footer > .turn-elapsed", REVEAL_QUERY);
    expect(hover.selector).toContain(".subagent-block:hover .turn-footer > .turn-elapsed");
    const focus = ruleContaining(turns, ".turn:focus-within > .turn-footer > .turn-elapsed", "top");
    expect(focus.selector).toContain(".subagent-block:focus-within .turn-footer > .turn-elapsed");
  });

  it("names no layout property in the rest state or the transition", () => {
    const rest = ruleContaining(turns, ".turn-footer > .turn-elapsed", "top");
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
});

describe("the delegate footer's copy", () => {
  it("hides at rest and reveals on the delegate card's hover", () => {
    // The rest state is unscoped, so the copy `turn-footer.ts` builds inside
    // `.subagent-foot` is quiet too. The gesture that lifts it is the delegate
    // CARD's own hover.
    const { slot } = mountDelegate();
    expect(getComputedStyle(slot).opacity).toBe("0");
    const hover = ruleContaining(
      turns,
      ".subagent-block:hover .turn-footer > .turn-elapsed",
      REVEAL_QUERY,
    );
    expect(hover.body).toMatch(/opacity:\s*1/u);
  });

  it("and the TURN footer's copy still reveals, which is that case's control", async () => {
    // Paired deliberately: a rule that hid BOTH copies would satisfy the case above
    // on its own. Focus is the half a test page can drive.
    const { footer, slot } = mountCard();
    footer.querySelector<HTMLButtonElement>(".turn-ledger-summary")?.focus();
    await expectRevealed(slot);
  });
});
