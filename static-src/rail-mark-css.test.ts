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
import { describe, it, expect, vi, beforeAll, afterAll, afterEach } from "vitest";
import { userEvent } from "vitest/browser";

import { loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";

const turns = loadCSS("29-turns.css");
const REVEAL_QUERY = "any-hover: hover";
/** The widths the marker's duration slot turns on: the rail is shown from 57.5rem of
 *  chat area, and the slot needs the wider gutter the resume label also derives. */
const RAIL_PX = 992;
const SLOT_PX = 1120;

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

// ---------------------------------------------------------------------------
// THE MARKER'S DURATION SLOT: the "expand right on hover" the reader asked for.
//
// The reveal rule this app has recorded is an OPACITY change in a box that was
// already reserved, never a geometric expansion — because a growing box changes the
// card's height, which changes `#messages`' scrollHeight, which feeds
// `scrollableBy()`, which is this rail's own navigability gate at MIN_SCROLL_PX.
// Absolute positioning is what lets the slot be BOTH: out of flow, so it carries its
// whole geometry at rest and cannot move anything by revealing itself, while still
// growing rightwards out of a 24px box into the gutter.
//
// So the assertions are the two halves of that claim. The box is reserved and the
// reveal is opacity (computed, over real layout, through a REAL hover — a synthetic
// event drives no style recalc). And nothing else moves: the marker, the rail, the
// transcript's cards, `#messages`' scrollHeight and the scroller's own scroll room
// are byte-identical either way.
// ---------------------------------------------------------------------------

let area: HTMLElement | undefined;

interface RailFixture {
  messages: HTMLElement;
  scroller: HTMLElement;
  card: HTMLElement;
  rail: HTMLElement;
  marker: HTMLElement;
  /** A second marker with NO slot, which is what makes "the slot costs the marker
   *  nothing" falsifiable rather than a claim about one element. */
  bare: HTMLElement;
  slot: HTMLElement;
  /** The last transcript control, so a case can reach the rail by TAB the way a
   *  reader would. */
  lastCardButton: HTMLElement;
}

/** The real transcript nesting: the container query needs `#chat-area` to BE the
 *  container, the rail's offset needs `#messages-wrap-outer`'s `--rail-inset-end`,
 *  and the scrollHeight claim needs the rail to sit OUTSIDE `#messages-wrap`.
 *
 *  Pinned at the viewport origin with no margin so the page cannot scroll: a
 *  document that overflows makes a real hover scroll it into view first, which moves
 *  every rect and turns a geometry assertion into noise. */
function buildRail(chatWidth: number): RailFixture {
  document.body.style.margin = "0";
  area = document.createElement("div");
  area.id = "chat-area";
  area.style.cssText = `position:fixed;top:0;left:0;margin:0;width:${String(chatWidth)}px;height:600px;`;
  const view = document.createElement("div");
  view.id = "chat-view";
  const outer = document.createElement("div");
  outer.id = "messages-wrap-outer";
  const scroller = document.createElement("div");
  scroller.id = "messages-wrap";
  const messages = document.createElement("div");
  messages.id = "messages";
  // Each card carries a real control, which a turn card does and which this fixture
  // needs for its own reason: Chromium makes a scroll container tabbable only when it
  // holds NO focusable child, so a transcript of bare divs puts `#messages-wrap` in
  // the tab order and the keyboard case below lands there instead of on the rail.
  let first: HTMLElement | undefined;
  let lastButton: HTMLElement | undefined;
  for (let i = 0; i < 12; i++) {
    const card = document.createElement("div");
    card.className = "turn";
    card.style.cssText = "block-size:120px;";
    card.textContent = `turn ${String(i + 1)}`;
    const action = document.createElement("button");
    action.type = "button";
    action.className = "turn-action-btn";
    action.textContent = "copy";
    card.appendChild(action);
    messages.appendChild(card);
    first ??= card;
    lastButton = action;
  }
  scroller.appendChild(messages);
  outer.appendChild(scroller);

  const rail = document.createElement("nav");
  rail.className = "turn-rail";
  const marker = document.createElement("button");
  marker.className = "rail-marker";
  marker.type = "button";
  marker.textContent = "7";
  const slot = document.createElement("time");
  slot.className = "rail-marker-time";
  slot.dateTime = "PT1M32S";
  slot.textContent = "1m 32s";
  marker.appendChild(slot);
  const bare = document.createElement("button");
  bare.className = "rail-marker";
  bare.type = "button";
  bare.textContent = "8";
  rail.append(marker, bare);
  outer.appendChild(rail);

  view.appendChild(outer);
  area.appendChild(view);
  document.body.appendChild(area);
  if (first === undefined || lastButton === undefined) {
    throw new Error("no transcript card");
  }
  return { messages, scroller, card: first, rail, marker, bare, slot, lastCardButton: lastButton };
}

afterEach(() => {
  area?.remove();
  area = undefined;
});

describe("the marker's duration slot", () => {
  it("is measuring the gated rule, because this browser reports any-hover", () => {
    // The premise. Under `(any-hover: none)` the slot is not rendered at all, so the
    // reserved-box case below would fail for a reason that is not the rule.
    expect(window.matchMedia(`(${REVEAL_QUERY})`).matches).toBe(true);
  });

  it("has its whole box at rest, invisible", () => {
    const { slot } = buildRail(SLOT_PX);
    const cs = getComputedStyle(slot);
    expect(cs.opacity).toBe("0");
    expect(cs.display).not.toBe("none");
    expect(cs.visibility).toBe("visible");
    // The assertion a DOM emulator structurally cannot make.
    expect(slot.getBoundingClientRect().width).toBeGreaterThan(0);
    expect(slot.getBoundingClientRect().height).toBeGreaterThan(0);
  });

  it("costs the marker nothing, because it is out of flow", () => {
    // THE LOAD-BEARING ONE. In flow the `<time>` is a second item in a 24px flex row:
    // measured at 44.5px wide against 24px with the rule in place, which is the rail's
    // column silently becoming a table. `position: absolute` is what buys the reserved
    // box for free, so it is asserted as the mechanism AND as its consequence.
    const { marker, bare, slot } = buildRail(SLOT_PX);
    expect(getComputedStyle(slot).position).toBe("absolute");
    expect(marker.getBoundingClientRect().width).toBe(bare.getBoundingClientRect().width);
    expect(marker.getBoundingClientRect().height).toBe(bare.getBoundingClientRect().height);
  });

  it("grows to the RIGHT of the marker, inside the gutter", () => {
    const { marker, slot, rail } = buildRail(SLOT_PX);
    expect(slot.getBoundingClientRect().left).toBeGreaterThanOrEqual(
      marker.getBoundingClientRect().right,
    );
    // Clear of the chat area's own end, which is what the container floor buys: at
    // this width the gutter past the rail is 108px and the slot wants about 60.
    expect(slot.getBoundingClientRect().right).toBeLessThan(
      area?.getBoundingClientRect().right ?? 0,
    );
    expect(rail.getBoundingClientRect().right).toBeLessThan(
      area?.getBoundingClientRect().right ?? 0,
    );
  });

  it("reveals on a real hover and moves nothing at all", async () => {
    const { marker, slot, rail, messages, scroller, card } = buildRail(SLOT_PX);
    // The two facts the scrollHeight claim RESTS on, asserted rather than assumed: the
    // rail is out of flow, and it is not inside the scroller. Without them the numbers
    // below are true by accident of this fixture and could not fail.
    expect(getComputedStyle(rail).position).toBe("absolute");
    expect(scroller.contains(rail)).toBe(false);

    const slotBefore = slot.getBoundingClientRect().toJSON();
    const markerBefore = marker.getBoundingClientRect().toJSON();
    const railBefore = rail.getBoundingClientRect().toJSON();
    const cardBefore = card.getBoundingClientRect().toJSON();
    const scrollHeightBefore = messages.scrollHeight;
    // `scrollableBy()` is exactly this: content height minus viewport height on the
    // scroller, and the rail withholds every row below MIN_SCROLL_PX. A feedback loop
    // here would flip the rail on and off under the reader's own pointer.
    const scrollableBefore = scroller.scrollHeight - scroller.clientHeight;
    expect(scrollableBefore).toBeGreaterThan(100);

    await userEvent.hover(marker);
    // The reveal transitions, so the computed value is mid-flight for `--dur-micro`
    // after the pointer lands; polling is what makes this measure the end state
    // rather than the animation.
    await vi.waitFor(() => {
      expect(getComputedStyle(slot).opacity).toBe("1");
    });

    expect(slot.getBoundingClientRect().toJSON()).toEqual(slotBefore);
    expect(marker.getBoundingClientRect().toJSON()).toEqual(markerBefore);
    expect(rail.getBoundingClientRect().toJSON()).toEqual(railBefore);
    expect(card.getBoundingClientRect().toJSON()).toEqual(cardBefore);
    expect(messages.scrollHeight).toBe(scrollHeightBefore);
    expect(scroller.scrollHeight - scroller.clientHeight).toBe(scrollableBefore);
  });

  it("reveals on keyboard focus, so tabbing the rail reaches the value", async () => {
    // Hover alone would hide it from a keyboard user permanently. A real TAB rather
    // than `focus()`: programmatic focus does not set the focus-visible flag on a
    // fresh page, so a `focus()`-driven case would fail for the wrong reason.
    const { marker, slot, lastCardButton, card } = buildRail(SLOT_PX);
    // THE POINTER IS PARKED FIRST, and this is not hygiene. The mouse position
    // survives between tests in one page while the fixture is rebuilt, so the previous
    // case's hover lands on the new marker at the same coordinates — measured: with
    // `:focus-visible` deleted from the rule this case still passed, on `:hover`.
    await userEvent.hover(card);
    expect(marker.matches(":hover")).toBe(false);

    lastCardButton.focus();
    await userEvent.tab();
    expect(document.activeElement).toBe(marker);
    expect(marker.matches(":focus-visible")).toBe(true);
    expect(marker.matches(":hover")).toBe(false);
    await vi.waitFor(() => {
      expect(getComputedStyle(slot).opacity).toBe("1");
    });
  });

  it("is withheld where the gutter cannot hold it, with the rail still shown", async () => {
    // The rail appears from 57.5rem, where the leftover gutter is one `--rail-w`. A
    // duration would be clipped by `#chat-area` there, so it is not rendered at all
    // rather than truncated, and hovering produces nothing.
    const { marker, slot, rail } = buildRail(RAIL_PX);
    expect(getComputedStyle(rail).display).not.toBe("none");
    expect(getComputedStyle(slot).display).toBe("none");
    await userEvent.hover(marker);
    expect(getComputedStyle(slot).display).toBe("none");
  });
});

describe("the marker's duration slot, read as source", () => {
  it("is gated on any-hover, never on hover", () => {
    // Those queries report only the PRIMARY input and iPadOS answers `hover: none`
    // with a trackpad attached. Which query a rule sits in is only readable as text.
    const rest = ruleContaining(turns, ".rail-marker > .rail-marker-time", REVEAL_QUERY);
    expect(rest.body).toMatch(/opacity:\s*0/u);
    expect(turns).not.toContain("@media (hover: hover)");
  });

  it("pairs :focus-visible with :hover in one rule", () => {
    const shown = ruleContaining(turns, ".rail-marker:hover > .rail-marker-time", REVEAL_QUERY);
    expect(shown.selector).toContain(".rail-marker:focus-visible > .rail-marker-time");
    expect(shown.body).toMatch(/opacity:\s*1/u);
  });

  it("names no layout property in the rest state or the transition", () => {
    // The other half of "the reveal is opacity": a `block-size` or `inline-size` in
    // the transition would make it geometric whatever the rest state measures.
    const rest = ruleContaining(turns, ".rail-marker > .rail-marker-time", REVEAL_QUERY);
    // Read as the whole DECLARATION, across newlines: a `transition:` matched with `.`
    // stops at the first line break, so a second property listed underneath the first
    // slipped past an earlier spelling of this case (measured).
    const transition = /transition:([\s\S]*?);/u.exec(rest.body)?.[1] ?? "";
    expect(transition).toContain("opacity");
    expect(transition).not.toMatch(/block-size|inline-size|height|width|padding|margin/u);
    expect(rest.body).not.toMatch(/visibility:/u);
  });

  it("says nothing about prefers-reduced-motion", () => {
    // 40-a11y.css's global sweep zeroes the duration, and a zeroed transition still
    // RUNS — so the reveal becomes instant rather than suppressed, which is correct.
    // A second rule here would be redundant, and this is the guard against one.
    const rest = ruleContaining(turns, ".rail-marker > .rail-marker-time", REVEAL_QUERY);
    expect(rest.body).not.toContain("prefers-reduced-motion");
    // The AT-RULE form, not the words: the file names the sweep in prose where the
    // landing flash records the same reasoning, and that sentence is not a rule.
    expect(turns).not.toMatch(/@media[^{]*prefers-reduced-motion/u);
  });
});

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
