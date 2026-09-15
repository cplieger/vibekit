// A TURN CARD HAS NO INTERNAL SEAM: each tinted band is separated from the body by
// its own FILL, never by a 1px rule.
//
// The header drew a `border-bottom` and the footer a `border-top` until 2026-09-13,
// while `.turn-face` — a body-coloured region sitting between those same two bands —
// drew neither and named its fill as what separated it. So one card delimited its
// regions two ways, and the reader who reported it had it half right: on a FOLDED
// card the header genuinely had no line under it (`.turn[data-folded] >
// .turn-header` dropped it) while the footer kept one, which is the asymmetry that
// got noticed. Everywhere else both lines were there and identical.
//
// WHAT THIS PINS, and why a style read alone would not: `0px` on both borders is
// satisfied by a card whose bands have stopped painting at all, which loses the
// separation rather than restyling it. So each seam is measured twice — no rule, AND
// the two fills either side of it differ — and the second half is what keeps the
// first honest. The three rules that existed only to stop those lines doubling
// against an adjacent one are asserted GONE for the same reason: each was a
// `border-*: 0` whose absence is indistinguishable from its presence now, so the
// only way to notice one being resurrected with its border is here.
//
// Real layout in a real engine, because "does the reader see a line" is a painted
// question and `getComputedStyle` on a `var()`-driven background answers `""` in a
// DOM emulator (testing-ts.md).
import { afterAll, beforeAll, describe, expect, it } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

/** The two seams, named the way the file header names them. */
const SEAMS = [
  { name: "header/body", band: ".turn-header", side: "borderBottomWidth" },
  { name: "body/footer", band: ".turn-footer", side: "borderTopWidth" },
] as const;

let style: HTMLStyleElement;
const made: HTMLElement[] = [];

/** A settled turn card in `buildTurn`'s own child order. Assembled here rather than
 *  through the builder because the subject is the CASCADE over three class names,
 *  and the builder drags the whole message-rendering graph in to produce the same
 *  three elements. */
function turnCard(opts: { readonly folded?: boolean } = {}): HTMLElement {
  const card = document.createElement("div");
  card.className = "turn";
  card.dataset["outcome"] = "completed";
  card.dataset["severity"] = "clean";
  if (opts.folded === true) {
    card.setAttribute("data-folded", "");
  }

  const header = document.createElement("div");
  header.className = "turn-header";
  const req = document.createElement("div");
  req.className = "turn-req-text";
  req.textContent = "run the build";
  header.append(req);

  const body = document.createElement("div");
  body.className = "turn-body";
  const row = document.createElement("div");
  row.className = "msg-row";
  const msg = document.createElement("div");
  msg.className = "message assistant";
  msg.textContent = "Built.";
  row.append(msg);
  body.append(row);

  const footer = document.createElement("div");
  footer.className = "turn-footer";
  footer.dataset["severity"] = "clean";
  const ledger = document.createElement("button");
  ledger.type = "button";
  ledger.className = "turn-ledger-summary";
  ledger.textContent = "2 cmds";
  footer.append(ledger);

  card.append(header, body, footer);
  document.body.replaceChildren(card);
  made.push(card);
  return card;
}

function el(card: HTMLElement, sel: string): HTMLElement {
  const found = card.querySelector<HTMLElement>(sel);
  if (found === null) {
    throw new Error(`no ${sel}`);
  }
  return found;
}

/** The fill a region actually paints, resolved. `.turn-body` declares none and shows
 *  the CARD's, so an unresolved read would compare a transparent body against a
 *  painted band and pass for the wrong reason. */
function paintedFill(node: HTMLElement): string {
  for (let at: HTMLElement | null = node; at !== null; at = at.parentElement) {
    const fill = getComputedStyle(at).backgroundColor;
    if (fill !== "rgba(0, 0, 0, 0)" && fill !== "transparent") {
      return fill;
    }
  }
  throw new Error("nothing in the ancestor chain paints a fill");
}

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
  for (const node of made.splice(0)) {
    node.remove();
  }
});

describe("a turn card's two band seams", () => {
  it.each(SEAMS)("draws no rule at the $name seam", ({ band, side }) => {
    const card = turnCard();
    expect(getComputedStyle(el(card, band))[side]).toBe("0px");
  });

  it.each(SEAMS)("separates the $name seam by fill instead", ({ band }) => {
    const card = turnCard();
    // `paintedFill` throws rather than returning transparent, so "the band paints"
    // needs no assertion of its own; what can fail is the two sides MATCHING.
    expect(paintedFill(el(card, band)), "the band differs from the body it abuts").not.toBe(
      paintedFill(el(card, ".turn-body")),
    );
  });

  it("keeps the card's own border, which is an outer edge rather than a seam", () => {
    // The differential for both cases above: a change that dropped every border in
    // the file would satisfy them and would take the card's outline with it.
    const card = turnCard();
    expect(getComputedStyle(card).borderBottomWidth).toBe("1px");
  });

  it("leaves the header's rule off a FOLDED card too, with no rule of its own", () => {
    // `.turn[data-folded] > .turn-header { border-bottom: 0 }` was deleted with the
    // seam it existed to de-duplicate. This is what says the deletion changed
    // nothing: a folded header has no line either way, so the rule was dead and not
    // load-bearing. Fails if the base seam comes back, because then only the folded
    // card would still be correct.
    const card = turnCard({ folded: true });
    expect(getComputedStyle(el(card, ".turn-header")).borderBottomWidth).toBe("0px");
  });

  it("leaves the footer's rule off a card whose body is EMPTY", () => {
    // `.turn-body:empty + .turn-footer { border-top: 0 }` was the third de-duplicating
    // rule and went the same way. Same shape of assertion, same reason.
    const card = turnCard();
    el(card, ".turn-body").replaceChildren();
    expect(getComputedStyle(el(card, ".turn-footer")).borderTopWidth).toBe("0px");
  });

  it("leaves the header's rule off a bodyless card, which keeps its radii", () => {
    // `.turn.is-bodyless > .turn-header` lost its `border-bottom: 0` and KEPT the two
    // radii, which are the half that was never about doubling: the header is the
    // card's last painted band there, so it has to round the corners the body was
    // covering. Asserting both is what stops the whole rule being read as dead.
    const card = turnCard();
    card.classList.add("is-bodyless");
    el(card, ".turn-body").replaceChildren();
    const header = getComputedStyle(el(card, ".turn-header"));
    expect(header.borderBottomWidth, "no rule").toBe("0px");
    // Read through `getPropertyValue`: the logical radius longhands are not on
    // TypeScript's `CSSStyleDeclaration`, and the physical ones resolve to the same
    // corner only for `writing-mode: horizontal-tb` + `direction: ltr`.
    expect(
      parseFloat(header.getPropertyValue("border-end-start-radius")),
      "and the corners are rounded",
    ).toBeGreaterThan(0);
  });
});
