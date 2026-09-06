// Real-layout CSS guards for the exec view's disclosures and the run card's
// clamped step output.
//
// Every fact here was reported as a defect and measured before it was fixed, and
// each one is invisible to the type checker, the linter and to a source-reading
// test: a phantom indent, a composed rotation, a missing glyph and a clamp that
// clips inside its own padding are all GEOMETRY. `mountAppCSS` assembles the
// sheet in `css/MANIFEST` order the way `cmd/bundle` concatenates it, because
// equal-specificity ties in this app are decided by that order rather than by
// the selectors, and the browser project computes real boxes.
//
// Markup is hand-built to mirror the builders (`exec-view/tree.ts` `buildRow`,
// `exec-view/page.ts` `resultsHead`, `fundamentals/run-card.ts`'s step row)
// rather than driven through them: the subject is the stylesheet, and importing
// the builders drags `chat.ts` and the run store in behind them for facts they
// do not decide.
import { describe, it, expect, beforeAll, afterAll } from "vitest";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { chevronEl } from "./chevron.js";

let styleEl: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  styleEl = mountAppCSS();
  host = document.createElement("div");
  // The panes are flex children of a sized page in production; a definite width
  // here is what makes a clamped line box have somewhere to wrap.
  host.style.inlineSize = "480px";
  document.body.appendChild(host);
});

afterAll(() => {
  styleEl?.remove();
  host?.remove();
});

function mount(node: HTMLElement): HTMLElement {
  host.replaceChildren(node);
  return node;
}

function css(el: Element, prop: string): string {
  return getComputedStyle(el).getPropertyValue(prop);
}

/** One `.ev-row` as `buildRow` assembles it: the twist wrapper carrying the
 *  shared chevron, the state mark, the kind slot, the text and the duration.
 *  `kids` gives the row children, which is what decides whether it is a leaf. */
function evRow(opts: { depth: number; kids: boolean; expanded?: boolean }): HTMLElement {
  const chevron = document.createElement("span");
  chevron.className = "ev-twist";
  chevron.setAttribute("aria-hidden", "true");
  chevron.appendChild(chevronEl());

  const glyph = document.createElement("span");
  glyph.className = "ev-state";
  const kindSlot = document.createElement("span");
  kindSlot.className = "ev-kind";
  kindSlot.hidden = true;
  const text = document.createElement("span");
  text.className = "ev-text";
  const label = document.createElement("span");
  label.className = "ev-label";
  label.textContent = "review_gpt";
  text.appendChild(label);
  const dur = document.createElement("span");
  dur.className = "ev-dur";

  const main = document.createElement("div");
  main.className = "ev-row-main";
  main.append(chevron, glyph, kindSlot, text, dur);

  const row = document.createElement("div");
  row.className = "ev-row";
  row.setAttribute("role", "treeitem");
  row.dataset["state"] = "running";
  row.dataset["kind"] = "step";
  row.style.setProperty("--ev-depth", String(opts.depth));
  row.appendChild(main);

  // `paint` hides the twist and removes `aria-expanded` on a leaf, and calls
  // `applyCollapse` (which writes both) on a row with children.
  if (opts.kids) {
    chevron.hidden = false;
    const collapsed = opts.expanded !== true;
    row.classList.toggle("ev-collapsed", collapsed);
    row.setAttribute("aria-expanded", String(!collapsed));
    const kids = document.createElement("div");
    kids.className = "ev-kids";
    kids.setAttribute("role", "group");
    kids.hidden = collapsed;
    row.appendChild(kids);
  } else {
    chevron.hidden = true;
    row.removeAttribute("aria-expanded");
  }
  return row;
}

/** The tree pane wrapper, so the row's own rules apply in the box they ship in. */
function evTree(...rows: HTMLElement[]): HTMLElement {
  const tree = document.createElement("div");
  tree.className = "ev-tree";
  tree.setAttribute("role", "tree");
  tree.append(...rows);
  const pane = document.createElement("div");
  pane.className = "ev-pane ev-pane-tree";
  pane.appendChild(tree);
  return pane;
}

describe("a tree row with no disclosure keeps no phantom indent", () => {
  it("removes the hidden twist from layout rather than emptying it", () => {
    const row = evRow({ depth: 0, kids: false });
    mount(evTree(row));
    const twist = row.querySelector<HTMLElement>(".ev-twist")!;
    // `visibility: hidden` was the shipped answer and it cannot collapse the
    // row's own flex `gap`, which is half of the 20px.
    expect(css(twist, "display")).toBe("none");
    expect(css(twist, "visibility")).not.toBe("hidden");
    expect(twist.getBoundingClientRect().width).toBe(0);
  });

  it("puts the activity mark on the row's own content edge", () => {
    const row = evRow({ depth: 0, kids: false });
    mount(evTree(row));
    const main = row.querySelector<HTMLElement>(".ev-row-main")!;
    const glyph = row.querySelector<HTMLElement>(".ev-state")!;
    // The content edge, not the border box: the row's leading padding IS its
    // depth indent and is not the defect. Measured at 20px of gap before the
    // fix (a 1rem twist box plus the row's 0.25rem gap). The border term is not
    // padding for the arithmetic's sake — `70-selection.css` reserves a 1px
    // edge on `.ev-row-main` so the selected border is a colour change rather
    // than a layout shift, and it sits outside the padding box.
    const contentEdge =
      main.getBoundingClientRect().x +
      Number.parseFloat(css(main, "border-left-width")) +
      Number.parseFloat(css(main, "padding-left"));
    expect(glyph.getBoundingClientRect().x - contentEdge).toBeCloseTo(0, 1);
  });

  it("still indents a row that HAS a disclosure by its twist", () => {
    // The other direction: collapsing the empty box must not collapse the real
    // one, or a container's own glyph would sit where its children's do.
    const leaf = evRow({ depth: 0, kids: false });
    const container = evRow({ depth: 0, kids: true });
    mount(evTree(container, leaf));
    const cx = container.querySelector<HTMLElement>(".ev-state")!.getBoundingClientRect().x;
    const lx = leaf.querySelector<HTMLElement>(".ev-state")!.getBoundingClientRect().x;
    expect(cx).toBeGreaterThan(lx);
  });
});

describe("the tree's disclosure arrow follows the app's direction convention", () => {
  it("points DOWN when open and RIGHT when closed, with no wrapper transform", () => {
    for (const expanded of [true, false]) {
      const row = evRow({ depth: 0, kids: true, expanded });
      mount(evTree(row));
      const twist = row.querySelector<HTMLElement>(".ev-twist")!;
      const glyph = twist.querySelector<HTMLElement>(".disclosure-chevron")!;
      const state = expanded ? "open" : "closed";

      // The wrapper's own `rotate(-90deg)` is what COMPOSED with the chevron's
      // closed -90deg to make -180deg (UP when closed, a bare RIGHT when open).
      expect(css(twist, "transform"), `${state}: wrapper`).toBe("none");
      expect(css(glyph, "--chev-turn").trim(), `${state}: turn`).toBe(expanded ? "0deg" : "-90deg");
      expect(css(glyph, "transform"), `${state}: glyph`).toBe(
        expanded ? "matrix(1, 0, 0, 1, 0, 0)" : "matrix(0, -1, 1, 0, 0, 0)",
      );
    }
  });

  it("flips only the row that owns the chevron, never its subtree", () => {
    // `.ev-kids` nests INSIDE `.ev-row`, so a descendant selector would turn a
    // collapsed child's arrow down from its expanded ancestor. Same defect the
    // `.ev-state` rules record, which is why both use child combinators.
    const parent = evRow({ depth: 0, kids: true, expanded: true });
    const child = evRow({ depth: 1, kids: true, expanded: false });
    parent.querySelector<HTMLElement>(".ev-kids")!.appendChild(child);
    mount(evTree(parent));

    const parentGlyph = parent.querySelector<HTMLElement>(
      ":scope > .ev-row-main > .ev-twist > .disclosure-chevron",
    )!;
    const childGlyph = child.querySelector<HTMLElement>(
      ":scope > .ev-row-main > .ev-twist > .disclosure-chevron",
    )!;
    expect(css(parentGlyph, "--chev-turn").trim()).toBe("0deg");
    expect(css(childGlyph, "--chev-turn").trim()).toBe("-90deg");
  });
});

describe("the results roll-up carries the standard disclosure arrow", () => {
  function results(collapsed: boolean): HTMLElement {
    const twist = document.createElement("span");
    twist.className = "ev-r-twist";
    twist.setAttribute("aria-hidden", "true");
    twist.appendChild(chevronEl());
    const title = document.createElement("span");
    title.className = "ev-r-title";
    title.textContent = "Results";
    const count = document.createElement("span");
    count.className = "ev-r-count";
    count.textContent = "3";

    const head = document.createElement("div");
    head.className = "ev-r-head";
    head.setAttribute("role", "button");
    head.tabIndex = 0;
    // `createDisclosure` writes `aria-expanded` on the TRIGGER, and that is what
    // the flip keys on; `page.ts`'s own `.collapsed` class on the root is its
    // `onToggle` bookkeeping. Both are set here because production sets both.
    head.setAttribute("aria-expanded", String(!collapsed));
    head.append(twist, title, count);

    const body = document.createElement("div");
    body.className = "ev-r-body";

    const root = document.createElement("div");
    root.className = collapsed ? "ev-results collapsed" : "ev-results";
    root.append(head, body);
    return root;
  }

  it("renders exactly one chevron, and no nested interactive element", () => {
    const box = mount(results(true));
    const head = box.querySelector<HTMLElement>(".ev-r-head")!;
    expect(head.querySelectorAll(".disclosure-chevron")).toHaveLength(1);
    // `.ev-r-head` is `role="button"`, so a `<button>` or `<a>` inside it is
    // axe's `nested-interactive` and `aria-hidden` does not clear it.
    expect(head.querySelectorAll("button, a, input, [tabindex]:not([tabindex='-1'])")).toHaveLength(
      0,
    );
  });

  it("points RIGHT while collapsed and DOWN once open", () => {
    for (const collapsed of [true, false]) {
      const box = mount(results(collapsed));
      const glyph = box.querySelector<HTMLElement>(".ev-r-twist > .disclosure-chevron")!;
      expect(css(glyph, "--chev-turn").trim(), collapsed ? "collapsed" : "open").toBe(
        collapsed ? "-90deg" : "0deg",
      );
    }
  });

  it("centres the glyph on the header rather than riding its baseline", () => {
    // `.ev-r-head` is `align-items: baseline` and an SVG-only grid box has no
    // useful baseline, which is what `align-self: center` answers.
    const box = mount(results(true));
    const head = box.querySelector<HTMLElement>(".ev-r-head")!;
    const twist = box.querySelector<HTMLElement>(".ev-r-twist")!;
    const h = head.getBoundingClientRect();
    const t = twist.getBoundingClientRect();
    expect(Math.abs(t.y + t.height / 2 - (h.y + h.height / 2))).toBeLessThanOrEqual(1);
  });
});

describe("the run's task instructions read as a card", () => {
  function inputs(hidden: boolean): HTMLElement {
    const dl = document.createElement("dl");
    dl.className = "ev-inputs";
    dl.hidden = hidden;
    const k = document.createElement("dt");
    k.className = "ev-in-k";
    k.textContent = "task";
    const v = document.createElement("dd");
    v.className = "ev-in-v";
    v.textContent = "converge the chevrons";
    dl.append(k, v);
    return dl;
  }

  it("takes the same fill, radius and padding as the timeline card", () => {
    // Read off `.ev-tl` rather than hardcoded, so the claim is that the two are
    // the SAME card and not that either is a given colour.
    const tl = document.createElement("div");
    tl.className = "ev-tl";
    const reference = mount(tl);
    const want = {
      bg: css(reference, "background-color"),
      radius: css(reference, "border-top-left-radius"),
      pad: css(reference, "padding-top"),
    };
    expect(want.bg).not.toBe("rgba(0, 0, 0, 0)");

    const box = mount(inputs(false));
    expect(css(box, "background-color")).toBe(want.bg);
    expect(css(box, "border-top-left-radius")).toBe(want.radius);
    expect(css(box, "padding-top")).toBe(want.pad);
  });

  it("disappears entirely when hidden, rather than becoming an empty card", () => {
    // There is no author `[hidden]` rule in this tree, so the UA sheet's
    // `[hidden] { display: none }` LOSES the specificity tie to the bare class
    // and a hidden `.ev-inputs` computed `display: grid` at 17px tall.
    const box = mount(inputs(true));
    expect(css(box, "display")).toBe("none");
    expect(box.getBoundingClientRect().height).toBe(0);
  });
});

describe("a run step's clamped output does not paint past its own clamp", () => {
  function step(text: string): HTMLElement {
    const cap = document.createElement("div");
    cap.className = "run-step-capture";
    cap.textContent = text;

    const row = document.createElement("div");
    row.className = "run-step";
    row.dataset["status"] = "completed";
    // An ANCHOR, as the builder makes it: the row is a door into `/run/{id}`.
    const head = document.createElement("a");
    head.className = "run-step-head";
    head.href = "/run/wf_1";
    row.append(head, cap);

    const card = document.createElement("div");
    card.className = "run-card";
    const steps = document.createElement("div");
    steps.className = "run-steps";
    steps.appendChild(row);
    card.appendChild(steps);
    return card;
  }

  it("keeps a navigator row's own ink rather than taking the reset's link ink", () => {
    // The reset layer carries `a { color: var(--c-link) }`, and this app spends link
    // ink on TEXT controls; a row is a navigator, like `.tab` and `.ev-row-main`. So
    // the row has to opt out, or every step in the card reads as a hyperlink.
    const card = mount(step("captured"));
    const head = card.querySelector<HTMLElement>(".run-step-head")!;
    expect(css(head, "text-decoration-line")).toBe("none");

    // The oracle is the LIVE token, not a literal: `--c-link` is theme-split, and a
    // hard-coded swatch would pass on one theme and lie on the other.
    const probe = document.createElement("a");
    probe.href = "/run/wf_1";
    probe.textContent = "link";
    card.appendChild(probe);
    expect(css(head, "color")).not.toBe(css(probe, "color"));
    // …and it is the inherited ink, which is what `color: inherit` means.
    expect(css(head, "color")).toBe(css(card, "color"));
  });

  const overflowing = Array.from(
    { length: 40 },
    (_, i) => `captured output line fragment number ${String(i)}`,
  ).join(" ");

  it("moves the trailing space to a margin, outside the clip region", () => {
    const card = mount(step(overflowing));
    const cap = card.querySelector<HTMLElement>(".run-step-capture")!;
    // `overflow: hidden` clips at the PADDING box, so bottom padding on a
    // `-webkit-line-clamp` box reveals the clipped line inside that band.
    expect(css(cap, "padding-bottom")).toBe("0px");
    expect(Number.parseFloat(css(cap, "margin-bottom"))).toBeGreaterThan(0);
    // The clamp itself is untouched.
    expect(css(cap, "-webkit-line-clamp")).toBe("2");
    expect(css(cap, "overflow-y")).toBe("hidden");
  });

  /** Every line box the capture's text produced, clipped ones included. */
  function lineRects(cap: HTMLElement): DOMRect[] {
    const range = document.createRange();
    range.selectNodeContents(cap);
    return [...range.getClientRects()];
  }

  it("paints no partial line under the ellipsis", () => {
    const card = mount(step(overflowing));
    const cap = card.querySelector<HTMLElement>(".run-step-capture")!;
    // The clip edge IS the element's own bottom, because `overflow: hidden`
    // clips at the padding box.
    //
    // "No line below the edge" is the WRONG claim, and asserting it fails on the
    // fix: `getClientRects()` reports every line the text produced, and leaving
    // lines below the edge is the clamp's whole job. The defect is a line that
    // STRADDLES the edge — part painted, part cut. With block-end padding the
    // third line began 8px above the edge and 7px of it rendered, directly under
    // the ellipsis; with the padding at 0 that line begins exactly ON the edge,
    // so none of it paints.
    const clip = cap.getBoundingClientRect().bottom;
    const rects = lineRects(cap);
    // The text really does overflow, or this asserts nothing.
    expect(rects.length).toBeGreaterThan(2);
    const straddling = rects
      .filter((r) => r.top < clip - 0.5 && r.bottom > clip + 0.5)
      .map((r) => `${r.top - clip}..${r.bottom - clip}`);
    expect(straddling).toEqual([]);
  });

  it("keeps the visible gap the padding used to give it", () => {
    // The fix must not tighten the row, and the plan's claim is that the visible
    // gap is IDENTICAL either way — not that it is any particular number. So the
    // oracle is a second card carrying the pre-fix declaration inline, rather
    // than arithmetic over the box model, which would only restate it.
    // `.run-step` is `overflow: hidden`, so it establishes a BFC and the margin
    // cannot collapse out of the row.
    const visibleGap = (card: HTMLElement): number => {
      const row = card.querySelector<HTMLElement>(".run-step")!;
      const cap = card.querySelector<HTMLElement>(".run-step-capture")!;
      const clip = cap.getBoundingClientRect().bottom;
      // A line box is not one rect: `white-space: pre-wrap` plus
      // `overflow-wrap: anywhere` splits a line into a rect per text fragment
      // (measured: 4 rects for the 2 visible lines), so take the lowest visible
      // EDGE rather than counting rects.
      const shown = lineRects(cap)
        .map((r) => r.bottom)
        .filter((b) => b <= clip + 0.5);
      expect(shown.length).toBeGreaterThan(0);
      return row.getBoundingClientRect().bottom - Math.max(...shown);
    };

    const fixed = visibleGap(mount(step(overflowing)));

    const legacy = step(overflowing);
    const legacyCap = legacy.querySelector<HTMLElement>(".run-step-capture")!;
    legacyCap.style.paddingBlockEnd = "var(--sp-2)";
    legacyCap.style.marginBlockEnd = "0";
    expect(visibleGap(mount(legacy))).toBeCloseTo(fixed, 1);
  });
});
