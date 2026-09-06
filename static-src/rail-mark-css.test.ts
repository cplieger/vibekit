// The rail's POSITION MARK, and the rule that it is one treatment rather than two.
//
// The rail claims one position per render, and it can claim it through either of two
// attributes: `data-current` is the scroll-derived turn, `data-selected` is the turn
// the reader picked. They stay separate attributes because they answer different
// questions — where the scroll puts you, versus which turn you chose — and exactly
// one is written per render (`turn-rail.ts` `rowNode`), which is what stops the rail
// painting two filled markers.
//
// So they are ONE treatment addressed two ways, and the review finding this file
// answers is what happens when that is written twice: the stylesheet carried two
// rules with four identical declarations each, whose own comment already claimed
// they shared a rule. Two copies of one treatment can drift, and the drift is
// silent — each marker still looks marked, and only a reader seeing both in one
// session would notice they no longer match.
//
// BOTH KINDS OF CLAIM, and neither is sufficient alone. The SOURCE fact (one rule
// lists both selectors) is what makes re-duplication fail; a computed check alone
// would pass for two rules that happen to agree today. The COMPUTED fact (two real
// markers in the real assembled cascade paint identically) is what makes the source
// fact mean something: the merged rule could still be outranked for one attribute
// and not the other by anything later in the bundle, and only the cascade knows.
//
// Deliberately NOT pinning WHICH treatment. The fill, the ink and the weight are a
// design decision that may be retuned; that the two marks agree is the invariant.
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

/** A rail holding one marker per attribute, plus an unmarked one as the control —
 *  without it a cascade that marked EVERY marker would pass the equality below. */
function railWithMarks(): { current: HTMLElement; selected: HTMLElement; plain: HTMLElement } {
  const rail = document.createElement("nav");
  rail.className = "turn-rail";
  const marker = (attr: string | null, text: string): HTMLElement => {
    const btn = document.createElement("button");
    btn.className = "rail-marker";
    btn.type = "button";
    btn.textContent = text;
    if (attr !== null) {
      btn.setAttribute(attr, "");
    }
    rail.appendChild(btn);
    return btn;
  };
  const current = marker("data-current", "2");
  const selected = marker("data-selected", "3");
  const plain = marker(null, "4");
  host.replaceChildren(rail);
  return { current, selected, plain };
}

/** One token's computed value for `prop`, so a comparison is computed-against-
 *  computed. The engine normalises a colour on the way out (`oklch(22% … deg)` is
 *  reported `oklch(0.22 …)`), so the token's authored text is not what any element
 *  resolves to. */
function probe(prop: string, token: string): string {
  const el = document.createElement("span");
  el.style.setProperty(prop, `var(${token})`);
  document.body.appendChild(el);
  const value = getComputedStyle(el).getPropertyValue(prop);
  el.remove();
  return value;
}

/** The four properties the mark is made of. */
const MARK_PROPS = ["background-color", "border-color", "color", "font-weight"] as const;

function marks(el: HTMLElement): Record<string, string> {
  const cs = getComputedStyle(el);
  return Object.fromEntries(MARK_PROPS.map((p) => [p, cs.getPropertyValue(p)]));
}

describe("the rail's two position marks", () => {
  it("are declared by ONE rule, so neither copy can drift from the other", () => {
    const rule = ruleContaining(turns, ".rail-marker[data-current]");
    // `ruleContaining` requires exactly one rule per selector, so a second rule for
    // either attribute fails there rather than here.
    expect(
      rule.selector
        .split(",")
        .map((s) => s.trim())
        .sort(),
    ).toEqual([".rail-marker[data-current]", ".rail-marker[data-selected]"]);
    expect(ruleContaining(turns, ".rail-marker[data-selected]").body).toBe(rule.body);
  });

  it("paint identically in the real assembled cascade", () => {
    const { current, selected } = railWithMarks();
    expect(marks(selected)).toEqual(marks(current));
  });

  it("differ from an unmarked marker, so the mark is a mark", () => {
    // The control. Equality above is satisfied by two markers that are both
    // unstyled, which is exactly what a deleted rule looks like.
    const { current, selected, plain } = railWithMarks();
    expect(marks(current)).not.toEqual(marks(plain));
    expect(marks(selected)).not.toEqual(marks(plain));
  });

  it("resolve to the token pair the contrast record was measured on", () => {
    // The ratios live at the rule (7.736:1 dark / 6.918:1 light for `--c-on-accent`
    // on `--c-accent`) and `rail-mark-contrast.node.test.ts` enforces them against
    // the expression the STYLESHEET declares. What only the cascade can add is that
    // a real marker resolves to that pair — a rule outranked by anything later in
    // the bundle would leave the measurement describing a colour nobody sees.
    //
    // Compared through a PROBE rather than against the token's authored text:
    // Chromium serialises a computed `color` as `oklch(0.22 …)` where the token is
    // authored `oklch(22% … deg)`, so a raw-text comparison fails on the
    // serialisation rather than on the colour.
    const { current } = railWithMarks();
    const cs = getComputedStyle(current);
    expect(cs.backgroundColor).toBe(probe("background-color", "--c-accent"));
    expect(cs.color).toBe(probe("color", "--c-on-accent"));
  });
});
