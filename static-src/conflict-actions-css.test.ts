// ---------------------------------------------------------------------------
// The conflict overlay's actions are the app's shared small button, at the DENSE
// control tier.
//
// `.conflict-btn` used to carry a near-copy of `.btn-small` (the same
// `--c-bg-secondary` fill, the same `--c-bg-tertiary` hover, differing only in
// padding, radius, ink and `display`), so a declaration added to the shared rule
// reached every other neutral ghost button in the app and not these four. The
// copy is gone and the buttons carry `btn-small` from editor-conflict.ts; what
// stays local is the dense tier, because a hunk row is four actions beside a mono
// title inside a strip padded `--sp-1`.
//
// MEASURED rather than reasoned, for two reasons a source read cannot answer.
// `padding-block: 0` has to beat `14-tools.css`'s `padding: var(--sp-2) var(--sp-3)`
// SHORTHAND from a later manifest slice, and this stylesheet set decides
// equal-specificity ties by that order — the class of cross-slice override the
// steering doc says to verify numerically. And `max(var(--ctl-h-dense),
// var(--hit-floor))` resolves differently per POINTER tier, which is the axis the
// universal hit-target floor keys on, so the coarse answer is not derivable from
// the fine one.
//
// The overlay is built by hand rather than through `renderConflictOverlay`: that
// function reaches the `$` DOM registry and a full `FileState`, and every editor
// suite in the tree mocks it out. So the TS side of the class pair — that the
// four buttons carry `btn-small` at all — is deliberately NOT pinned here.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
});

afterEach(() => {
  document.body.replaceChildren();
  document.documentElement.removeAttribute("data-pointer");
});

/** One hunk row as editor-conflict.ts builds it: the mono title, then the three
 *  resolve actions and the accent Suggest. The overlay wrapper is included
 *  because it carries the padding the tier is sized against. */
function mountRow(pointer: "fine" | "coarse"): {
  buttons: HTMLButtonElement[];
  suggest: HTMLButtonElement;
} {
  document.documentElement.setAttribute("data-pointer", pointer);
  const overlay = document.createElement("div");
  overlay.className = "editor-conflict-overlay";
  const row = document.createElement("div");
  row.className = "conflict-hunk-row";
  row.setAttribute("role", "group");
  const title = document.createElement("span");
  title.className = "conflict-hunk-title";
  title.textContent = "Line 12: HEAD vs incoming";
  row.appendChild(title);
  const buttons: HTMLButtonElement[] = [];
  for (const label of ["Ours", "Theirs", "Both"]) {
    const b = document.createElement("button");
    b.className = "btn-small conflict-btn";
    b.textContent = label;
    row.appendChild(b);
    buttons.push(b);
  }
  const suggest = document.createElement("button");
  suggest.className = "btn-small conflict-btn conflict-btn-suggest";
  suggest.textContent = "Suggest";
  row.appendChild(suggest);
  buttons.push(suggest);
  overlay.appendChild(row);
  document.body.replaceChildren(overlay);
  return { buttons, suggest };
}

describe("the shared skin reaches a conflict action", () => {
  // Against a CONTROL `.btn-small` outside the row, which is what makes this a
  // divergence test rather than a restatement of the shared rule: a local copy
  // re-introduced on `.conflict-btn` (the `--r-sm` radius and the tighter padding
  // were the real ones) moves these buttons off the app's box and nothing else
  // would notice. HEIGHT is deliberately excluded — the dense tier below is a
  // divergence this row is entitled to, and the block padding goes with it.
  it("resolves the same box as a btn-small elsewhere on the page", () => {
    const { buttons } = mountRow("fine");
    const control = document.createElement("button");
    control.className = "btn-small";
    control.textContent = "Control";
    document.body.appendChild(control);

    const shape = (b: Element): string => {
      const s = getComputedStyle(b);
      return [
        s.borderTopWidth,
        s.borderTopStyle,
        s.borderTopColor,
        s.borderRadius,
        s.backgroundColor,
        s.color,
        s.fontSize,
        s.paddingLeft,
        s.paddingRight,
      ].join(" / ");
    };
    const want = shape(control);
    // Non-initial by construction, or an unstyled button would satisfy the
    // comparison against another unstyled button.
    expect(want).toContain("1px / solid");
    // The Suggest variant legitimately differs in ink and border colour, so the
    // three mechanical actions are the population here; its own case is below.
    for (const b of buttons.slice(0, 3)) {
      expect(shape(b), b.textContent ?? "").toBe(want);
    }
  });
});

describe("the dense tier, per pointer tier", () => {
  // --ctl-h-dense is 2rem fine / 2.5rem coarse; --hit-floor is 1.5rem / 2.75rem.
  // So the max() is 32px fine and 44px coarse, and --btn-h's own 36/44 is what a
  // bare `.btn-small` would have given — the fine row is what the tier buys.
  it.each([
    ["fine", 32],
    ["coarse", 44],
  ] as const)("holds a %s-pointer action at %ipx", (pointer, expected) => {
    const { buttons } = mountRow(pointer);
    for (const b of buttons) {
      expect(Math.round(b.getBoundingClientRect().height), b.textContent ?? "").toBe(expected);
    }
  });

  it("zeroes the block padding so the floor can bind", () => {
    const { buttons } = mountRow("fine");
    const [first] = buttons;
    expect(first).toBeDefined();
    const cs = getComputedStyle(first!);
    // The cross-slice half: `padding-block: 0` in 60-mcp.css against the shorthand
    // in 14-tools.css. Without it the base's own `--sp-2` plus the label's line box
    // overshoots the tier and the row grows with it.
    expect(cs.paddingTop).toBe("0px");
    expect(cs.paddingBottom).toBe("0px");
    // The inline half of that shorthand still applies, or the labels sit flush
    // against the border.
    expect(cs.paddingLeft).not.toBe("0px");
  });
});

describe("the Suggest variant", () => {
  it("differs from its three neighbours in ink AND border, not fill", () => {
    const { buttons, suggest } = mountRow("fine");
    const [neighbour] = buttons;
    expect(neighbour).toBeDefined();
    const base = getComputedStyle(neighbour!);
    const cs = getComputedStyle(suggest);
    // Compared against a SIBLING rather than against the resolved token: the point
    // of the variant is that it reads differently from the actions beside it, and
    // that is the assertion a retuned accent should not move.
    expect(cs.color).not.toBe(base.color);
    expect(cs.borderTopColor).not.toBe(base.borderTopColor);
    // One hue on both channels, which is what makes it read as a variant of the
    // same button rather than as a second kind of control.
    expect(cs.color).toBe(cs.borderTopColor);
    // The FILL stays the shared one: an accent-filled button is `.btn-small.primary`,
    // a different variant, and this row has no primary action.
    expect(cs.backgroundColor).toBe(base.backgroundColor);
  });

  // SOURCE, not computed: a synthetic hover drives no style recalc here and
  // `CSS.forcePseudoState` is a devtools call a test page cannot make, so the only
  // way to state this is to read the rule. `.btn-small`'s own `&:hover` moves ink
  // to `--c-text-primary` and TIES this rule's specificity, so without the
  // restatement the accent identity drops the moment a pointer arrives.
  it("re-states its ink on hover so the shared rule cannot take it", () => {
    const hover = ruleContaining(loadCSS("60-mcp.css"), ".conflict-btn-suggest:hover", "top");
    expect(hover.body).toMatch(/color:\s*var\(--c-accent\)/);
  });
});
