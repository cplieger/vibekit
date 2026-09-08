// The resume control, docked at the foot of the timeline rail's own column.
//
// It sits IN that column and was the one thing in it drawn as a floating pill: a
// raised surface with a visible border, `--btn-h` tall, sans at `--fs-sm`, no
// tabular figures under a label that is mostly a count, and its label revealed by a
// WIDTH query, so at any width past 70rem it was permanently expanded. Measured on
// the shipped stylesheet before the change, at 1200px of chat area: 121.05 x 36
// against the marker's 24 x 24, `oklch(0.275 0.03 284.1)` against
// `oklch(0.15 0.022 283.9)`, `oklch(1 0 none / 0.16)` border against a transparent
// one, `system-ui, sans-serif` 12px against mono 11px, `normal` figures and a
// `normal` line height.
//
// Three claims here, and each needs a different instrument.
//
// COMPUTED PARITY, against the real assembled cascade: the control and a real
// `.rail-marker` resolve the SAME value for every property that was aligned. Read
// off two live elements rather than compared against literals, because the
// vocabulary is 29-turns.css's and a retune there has to move both or fail here.
//
// REAL HOVER, through `userEvent`: the control is icon-only at rest and grows only
// on hover, and it grows RIGHT — its inline-start edge is where the reveal is
// bounded from. A synthetic event cannot drive `:hover`, so this is the only way to
// assert the state the reader actually sees.
//
// REAL LAYOUT: revealing the label moves no other box. The control is
// `position: absolute` in `#messages-wrap-outer`, a SIBLING of the scroller, so
// `#messages`' scrollHeight — and therefore `scrollableBy()`, the rail's own
// navigability gate — is out of reach. That is the one coupling a growing control
// could break, and it is measured rather than reasoned about.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { userEvent } from "vitest/browser";

import { loadCSS, mountAppCSS, ruleBody, ruleContaining } from "./__test-helpers__/css-rules.js";

const messagesCSS = loadCSS("13-messages.css");
const REVEAL_QUERY = "any-hover: hover";
/** The width at which the docked variant applies, and the width at which the gutter
 *  can hold the label. Both are the stylesheet's own thresholds. */
const DOCKED_PX = 920;
const LABEL_PX = 1120;

let style: HTMLStyleElement;
let area: HTMLElement | undefined;

interface Fixture {
  messages: HTMLElement;
  scroller: HTMLElement;
  card: HTMLElement;
  /** The last transcript control, so a case can reach the resume button by TAB from
   *  inside the transcript the way a reader would. */
  lastCardButton: HTMLElement;
  rail: HTMLElement;
  marker: HTMLElement;
  resume: HTMLButtonElement;
  label: HTMLElement;
}

/** The real transcript nesting, because every fact here is a consequence of it: the
 *  container query needs `#chat-area` to BE the container, the docked position needs
 *  `#messages-wrap-outer`'s `--rail-inset-*`, and the scrollHeight claim needs the
 *  control to be outside `#messages-wrap`.
 *
 *  Pinned at the viewport's origin with no margin so the page cannot scroll: a
 *  document that overflows makes `userEvent.hover` scroll it into view first, which
 *  moves every rect by a scrollbar's width and turns a position assertion into
 *  noise (measured, twice, while writing this). */
function build(chatWidth: number): Fixture {
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
  // Enough turns to overflow, so `scrollHeight` and the scroller's scroll room are
  // real numbers rather than zero. Each card carries a real control, which a turn
  // card does and which this fixture needs for its own reason: Chromium makes a
  // scroll container tabbable only when it holds NO focusable child, so a transcript
  // of bare divs puts `#messages-wrap` itself in the tab order and the keyboard case
  // below would land there instead of on the button it is about.
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

  const resume = document.createElement("button");
  resume.type = "button";
  resume.id = "scroll-bottom";
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("class", "ic-ui");
  svg.setAttribute("viewBox", "0 0 24 24");
  resume.appendChild(svg);
  const label = document.createElement("span");
  // The dynamic label, not the "Latest" fallback: the count is the fact the control
  // exists to carry, and it is also the widest thing it renders.
  label.textContent = "3 new blocks";
  resume.appendChild(label);
  outer.appendChild(resume);

  const rail = document.createElement("nav");
  rail.className = "turn-rail";
  const marker = document.createElement("button");
  marker.className = "rail-marker";
  marker.type = "button";
  marker.textContent = "7";
  rail.appendChild(marker);
  outer.appendChild(rail);

  view.appendChild(outer);
  area.appendChild(view);
  document.body.appendChild(area);
  if (first === undefined || lastButton === undefined) {
    throw new Error("no transcript card");
  }
  return {
    messages,
    scroller,
    card: first,
    lastCardButton: lastButton,
    rail,
    marker,
    resume,
    label,
  };
}

beforeAll(() => {
  style = mountAppCSS();
});

afterEach(() => {
  area?.remove();
  area = undefined;
});

afterAll(() => {
  style.remove();
});

/** The properties the control was brought into line with. Each one was a measured
 *  gap against `.rail-marker`; `color` is deliberately NOT among them — the marker
 *  rests at `--c-text-tertiary` because it is a passive mark, and this is a control
 *  the reader is meant to click. */
const ALIGNED = [
  "background-color",
  "border-top-color",
  "border-right-color",
  "border-bottom-color",
  "border-left-color",
  "border-radius",
  "min-height",
  "font-family",
  "font-size",
  "font-variant-numeric",
  "line-height",
] as const;

function styles(el: Element): Record<string, string> {
  const cs = getComputedStyle(el);
  return Object.fromEntries(ALIGNED.map((p) => [p, cs.getPropertyValue(p)]));
}

describe("the reveal gate is live in this browser", () => {
  it("matches any-hover, so every hover case below measures the gated rule", () => {
    // The premise. Under `(any-hover: none)` the label is always visible by design,
    // so the icon-only case would fail for the wrong reason and the always-visible
    // case would pass for the wrong one.
    expect(window.matchMedia(`(${REVEAL_QUERY})`).matches).toBe(true);
  });
});

describe("the docked control reads as a rail row", () => {
  it("resolves the rail marker's own values for every property that was aligned", () => {
    const { marker, resume } = build(LABEL_PX);
    expect(styles(resume)).toEqual(styles(marker));
  });

  it("and those values are the marker's rather than two elements agreeing on nothing", () => {
    // The control. Equality above is satisfied by two unstyled boxes, which is
    // exactly what a deleted rule looks like — so pin the marker against the page
    // it is NOT: an unclassed button in the same tree.
    const { marker, resume } = build(LABEL_PX);
    const plain = document.createElement("button");
    plain.type = "button";
    plain.textContent = "7";
    area?.appendChild(plain);
    expect(styles(marker)).not.toEqual(styles(plain));
    expect(styles(resume)).not.toEqual(styles(plain));
  });

  it("keeps the app's hit floor rather than the marker's literal 24px", () => {
    // `--hit-floor` and the marker's `1.5rem` are the same 24px on a fine pointer,
    // which is why the parity case above passes. They diverge on a coarse one, where
    // this control keeps a 44px target instead of inheriting a trade the rail's own
    // rows made for themselves.
    const { resume } = build(LABEL_PX);
    const probe = document.createElement("span");
    probe.style.setProperty("min-height", "var(--hit-floor)");
    area?.appendChild(probe);
    expect(getComputedStyle(resume).minHeight).toBe(getComputedStyle(probe).minHeight);
  });
});

describe("icon-only at rest, the label on hover", () => {
  it("is the rail's own column wide at rest, with no label in the box", () => {
    // Compared against the RAIL, which is `--rail-w` wide by declaration, rather than
    // against a probe reading that token: it is declared on `#messages-wrap-outer`,
    // so a probe mounted anywhere else resolves nothing and stretches (measured).
    const { resume, label, rail } = build(LABEL_PX);
    expect(getComputedStyle(label).display).toBe("none");
    expect(resume.getBoundingClientRect().width).toBe(rail.getBoundingClientRect().width);
  });

  it("shows the label on a real hover, and grows only to the RIGHT", async () => {
    const { resume, label } = build(LABEL_PX);
    const before = resume.getBoundingClientRect();

    await userEvent.hover(resume);

    const after = resume.getBoundingClientRect();
    expect(getComputedStyle(label).display).toBe("block");
    expect(after.width).toBeGreaterThan(before.width);
    // Pinned by its inline-START edge, which is what keeps the growth in the gutter
    // instead of back over the turn cards.
    expect(after.left).toBe(before.left);
    // And bounded: `max-inline-size` keeps it clear of the wrapper's own end.
    expect(after.right).toBeLessThan(area?.getBoundingClientRect().right ?? 0);
  });

  it("shows the label on keyboard focus too", async () => {
    // Hover alone would hide the count from a keyboard user permanently. Reached by a
    // real TAB out of the transcript, which is the gesture, rather than by `focus()`:
    // programmatic focus does not set the focus-visible flag on a fresh page, so a
    // `focus()`-driven case would fail for a reason that is not the rule.
    const { resume, label, lastCardButton, card } = build(LABEL_PX);
    // THE POINTER IS PARKED FIRST, and this is not hygiene. The mouse position
    // survives between tests in one page while the fixture is rebuilt, so the previous
    // case's hover lands on the new control at the same coordinates and this case
    // passes on `:hover` with `:focus-visible` deleted from the rule.
    await userEvent.hover(card);
    expect(resume.matches(":hover")).toBe(false);

    lastCardButton.focus();
    await userEvent.tab();
    expect(document.activeElement).toBe(resume);
    expect(resume.matches(":focus-visible")).toBe(true);
    expect(resume.matches(":hover")).toBe(false);
    expect(getComputedStyle(label).display).toBe("block");
  });

  it("withholds the label where the gutter cannot hold it", async () => {
    // At the docked threshold the leftover gutter is `--rail-w` and the label wants
    // about 80px more, so hovering could only produce a clipped or empty expansion.
    // The width query is a FLOOR on the reveal, not the reveal itself.
    const { resume, label } = build(DOCKED_PX);
    const before = resume.getBoundingClientRect().width;
    await userEvent.hover(resume);
    expect(getComputedStyle(label).display).toBe("none");
    expect(resume.getBoundingClientRect().width).toBe(before);
  });
});

describe("revealing the label moves nothing else", () => {
  it("leaves the transcript's scrollHeight and its own cards where they were", async () => {
    // The control is `position: absolute` in `#messages-wrap-outer`, a SIBLING of
    // `#messages-wrap`, so this is out of reach by construction — which is the whole
    // reason a geometric reveal is admissible here and is not in the turn footer.
    // Asserted rather than argued: `scrollableBy()` is content height minus viewport
    // height on the scroller, and the rail hides itself below MIN_SCROLL_PX.
    const { resume, messages, scroller, card } = build(LABEL_PX);
    // The two facts the claim RESTS on, asserted rather than assumed: the control is
    // out of flow, and it is not inside the scroller. Without them the numbers below
    // are true by accident of this fixture and could not fail.
    expect(getComputedStyle(resume).position).toBe("absolute");
    expect(scroller.contains(resume)).toBe(false);

    const scrollHeightBefore = messages.scrollHeight;
    const scrollableBefore = scroller.scrollHeight - scroller.clientHeight;
    const cardBefore = card.getBoundingClientRect();
    expect(scrollableBefore).toBeGreaterThan(100);

    await userEvent.hover(resume);

    expect(messages.scrollHeight).toBe(scrollHeightBefore);
    expect(scroller.scrollHeight - scroller.clientHeight).toBe(scrollableBefore);
    expect(card.getBoundingClientRect().toJSON()).toEqual(cardBefore.toJSON());
  });
});

describe("the reveal, read as source", () => {
  it("is gated on any-hover, never on hover", () => {
    // Those queries report only the PRIMARY input, and iPadOS answers `hover: none`
    // with a trackpad attached, so a `hover: hover` gate drops the rule on every
    // touch-primary device. Only source can answer which query a rule sits in.
    const rest = ruleContaining(messagesCSS, '[id="scroll-bottom"] > span', REVEAL_QUERY);
    expect(rest.body).toMatch(/display:\s*none/u);
    expect(messagesCSS).not.toContain("@media (hover: hover)");
  });

  it("leaves the label visible where there is no hover to reveal it with", () => {
    // Outside the query the width-gated rule stands unchanged: a touch device has no
    // gesture to ask with, so always-on is the right default rather than a fallback.
    // Read as source because a test page cannot answer `(any-hover: none)`.
    //
    // Scoped to the part of the container block ABOVE its nested hover query, because
    // one selector legitimately appears in both and the point is that the two bodies
    // differ — the same reason `ruleContaining` takes a scope at all.
    const container = ruleBody(messagesCSS, "@container chat-area (width >= 70rem)");
    const beforeHoverGate = container.split("@media")[0] ?? "";
    expect(beforeHoverGate).not.toBe("");
    const shown = ruleContaining(beforeHoverGate, '[id="scroll-bottom"] > span', "top");
    expect(shown.body).toMatch(/display:\s*block/u);
  });

  it("reveals on focus-visible as well as hover", () => {
    const shown = ruleContaining(messagesCSS, '[id="scroll-bottom"]:hover > span', REVEAL_QUERY);
    expect(shown.selector).toContain('[id="scroll-bottom"]:focus-visible > span');
    expect(shown.body).toMatch(/display:\s*block/u);
  });
});
