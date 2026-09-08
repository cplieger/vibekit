// EVERY POPUP IS ONE COMPONENT, measured over real layout against the assembled
// bundle.
//
// The four cards a reader opens from the composer and the sidebar — status,
// context, model, mode — already shared `.pill-expand-content`, so background,
// border, radius, shadow and z-index were identical. What made them read as four
// components was four properties diverging per card: padding (8/12 against 8
// against 0), an inline floor of 11 / 14 / 12 / 12rem, a height cap on two of the
// four, and a row size of 11px in two and 12px in the other two.
//
// The table below is the enumeration, so a fifth card added to `static/index.html`
// fails here until it is listed with whatever it departs on and why. A departure
// is declared per property per card: `EXPECTED` is what the shared rule answers,
// and anything else has to name its reason in `overrides`.
//
// The second half is the type floor. There is no coarse tier on the `--fs-*`
// scale; the popup rungs `--fs-popup` / `--fs-popup-meta` (01-tokens.css) carry
// it, on the POINTER tier rather than a width query, and this file measures every
// text-bearing node in every card on both tiers — which is what the block this
// replaced could not do, having enumerated selectors and missed five.

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

/** A departure from the shared answer, and the reason it is allowed to exist. */
interface Override {
  readonly value: string;
  readonly because: string;
}

interface Popup {
  /** The modifier class on the card, "" for the task-list card which has none. */
  readonly cls: string;
  /** What the reader calls it. */
  readonly name: string;
  /** Markup that makes the card's own rows measurable. */
  readonly rows: () => HTMLElement[];
  /** The element whose text is the card's BODY rung. */
  readonly bodyRow: string;
  readonly overrides?: Partial<Record<Prop, Override>>;
}

type Prop = "paddingTop" | "paddingLeft" | "minInlineSize" | "maxBlockSize" | "bodyFontSize";

/** The shared answer per property, in resolved pixels at the fine tier.
 *  --sp-2 is 8px, 14rem is 224px, 16rem is 256px, --fs-base is 13px. */
const EXPECTED: Record<Prop, string> = {
  paddingTop: "8px",
  paddingLeft: "8px",
  minInlineSize: "224px",
  maxBlockSize: "256px",
  bodyFontSize: "13px",
};

function el(tag: string, cls: string, text = ""): HTMLElement {
  const node = document.createElement(tag);
  node.className = cls;
  node.textContent = text;
  return node;
}

function span(cls: string, text: string): HTMLElement {
  return el("span", cls, text);
}

const POPUPS: readonly Popup[] = [
  {
    cls: "pill-status-content",
    name: "the sidebar status card",
    bodyRow: ".pill-detail",
    rows: () => [
      span("pill-detail", "Ready"),
      el("span", "pill-sep"),
      (() => {
        const account = el("div", "pill-account");
        account.append(
          span("pill-account-plan", "Pro"),
          span("pill-account-meter", "412 / 1,000 credits"),
        );
        return account;
      })(),
    ],
  },
  {
    cls: "pill-context-content",
    name: "the context-usage card",
    bodyRow: ".pill-detail",
    rows: () => {
      const row = el("div", "pill-ctx-row");
      row.append(span("pill-ctx-label", "Tokens"), span("pill-detail", "48,120 / 200,000"));
      const metering = el("div", "pill-metering");
      const mrow = el("div", "pill-metering-row");
      mrow.append(
        span("pill-metering-label", "cache_read_input_tokens"),
        span("pill-metering-value", "1,204,882"),
      );
      metering.append(mrow);
      return [row, metering];
    },
  },
  {
    cls: "pill-model-list",
    name: "the model card",
    bodyRow: ".pill-model-item",
    // The card gives its padding to the scroller and does not scroll itself, so
    // the effort tiers stay pinned below a list that does. Both are stated at
    // `.pill-model-list` (15-input.css).
    overrides: {
      paddingTop: { value: "0px", because: "the effort section bleeds to both edges" },
      paddingLeft: { value: "0px", because: "the effort section bleeds to both edges" },
    },
    rows: () => {
      const scroll = el("div", "pill-model-scroll");
      const item = el("button", "pill-model-item");
      item.append(span("", "claude-opus-5"), span("pill-model-meta", "5x"));
      scroll.append(item);
      const effort = el("div", "effort-row");
      effort.append(span("effort-label", "Effort"), el("button", "effort-btn", "max"));
      return [scroll, effort];
    },
  },
  {
    cls: "pill-role-list",
    name: "the mode card",
    bodyRow: ".pill-role-item",
    rows: () => {
      const hint = el("div", "pill-role-hint", "Pick the mode this chat runs in");
      const item = el("button", "pill-role-item");
      item.append(
        span("pill-role-name", "semantic_reviewer"),
        span("pill-role-scope", "workspace"),
        span("pill-role-shadow", "shadows"),
      );
      return [hint, item];
    },
  },
  {
    cls: "chat-options-card",
    name: "the chat-actions card",
    bodyRow: ".chat-opt-name",
    // The only card whose rows WRAP: every row carries a hint sentence, so its
    // floor is a measure for prose rather than a minimum for a one-line strip.
    overrides: {
      minInlineSize: { value: "288px", because: "18rem is a measure for wrapped hint prose" },
    },
    rows: () => {
      const btn = el("button", "chat-opt-btn");
      const text = el("div", "chat-opt-text");
      text.append(
        span("chat-opt-name", "Start a tangent"),
        span("chat-opt-hint", "Fork this chat's context into a new one."),
      );
      btn.append(el("span", "chat-opt-icon"), text);
      return [btn];
    },
  },
  {
    cls: "",
    name: "the task-list card",
    bodyRow: ".task-text",
    rows: () => {
      const items = el("div", "task-list-items");
      const item = el("div", "task-item");
      item.append(span("task-icon", "\u2610"), span("task-text", "Read the steering docs"));
      items.append(item);
      return [items];
    },
  },
];

const host = document.createElement("div");
// A real anchor: the card is `position: absolute` against its slot, so it needs
// one or the offsets resolve against the page and the width measurement moves.
host.style.cssText = "position:fixed;top:0;left:0;inline-size:900px;block-size:600px;";
document.body.appendChild(host);

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
  host.remove();
  document.documentElement.removeAttribute("data-pointer");
});

afterEach(() => {
  host.replaceChildren();
});

/** Mount one card the way `static/index.html` does: a `.pill-slot` holding the
 *  trigger and, as its SIBLING, the card. `is-open` is the reveal class, and
 *  without it the card sits at `opacity: 0; transform: scale(0.4)` — which does
 *  not move a computed length, but does make a failure unreadable in a
 *  screenshot. */
function mount(popup: Popup): HTMLElement {
  const slot = el("span", "pill-slot");
  slot.append(el("button", "pill pill-expandable"));
  const card = el("span", `pill-expand-content is-open ${popup.cls}`.trim());
  card.append(...popup.rows());
  slot.append(card);
  host.append(slot);
  return card;
}

function resolved(popup: Popup, prop: Prop): string {
  return popup.overrides?.[prop]?.value ?? EXPECTED[prop];
}

function read(card: HTMLElement, prop: Prop, bodyRow: string): string {
  if (prop === "bodyFontSize") {
    const row = card.querySelector<HTMLElement>(bodyRow);
    expect(row, `the fixture must render ${bodyRow}`).not.toBeNull();
    return getComputedStyle(row as HTMLElement).fontSize;
  }
  return getComputedStyle(card)[prop];
}

describe("every popup answers one geometry", () => {
  it.each(POPUPS.map((p) => [p.name, p] as const))("%s", (_name, popup) => {
    document.documentElement.dataset["pointer"] = "fine";
    const card = mount(popup);

    for (const prop of Object.keys(EXPECTED) as Prop[]) {
      const want = resolved(popup, prop);
      const because = popup.overrides?.[prop]?.because;
      expect(
        read(card, prop, popup.bodyRow),
        because === undefined
          ? `${popup.name} must take the shared ${prop}; a card that needs its own keeps it ` +
              `with a reason at the override, and lists it in this file's table`
          : `${popup.name} departs on ${prop} because ${because}`,
      ).toBe(want);
    }
  });

  it("keeps the shared answer in ONE place, so a card cannot drift silently", () => {
    // The four properties are declared on `.pill-expand-content` and nowhere
    // else, which is what makes the parity above a property of the cascade rather
    // than of six rules that currently agree. Measured by mounting a card with NO
    // modifier class at all: it must already answer every shared value.
    document.documentElement.dataset["pointer"] = "fine";
    const bare = mount({ cls: "", name: "a bare card", bodyRow: "", rows: () => [] });
    const cs = getComputedStyle(bare);
    expect({
      paddingTop: cs.paddingTop,
      paddingLeft: cs.paddingLeft,
      minInlineSize: cs.minInlineSize,
      maxBlockSize: cs.maxBlockSize,
      fontSize: cs.fontSize,
    }).toEqual({
      paddingTop: EXPECTED.paddingTop,
      paddingLeft: EXPECTED.paddingLeft,
      minInlineSize: EXPECTED.minInlineSize,
      maxBlockSize: EXPECTED.maxBlockSize,
      fontSize: EXPECTED.bodyFontSize,
    });
  });

  it("caps every card's height WITH a way to reach what the cap hides", () => {
    // A cap without a scroll is a clip, and the content it clips is the reason
    // the card was opened. The model card is the documented exception: the CARD
    // does not scroll, its inner `.pill-model-scroll` does.
    document.documentElement.dataset["pointer"] = "fine";
    for (const popup of POPUPS) {
      const card = mount(popup);
      const scrolls =
        getComputedStyle(card).overflowY === "auto" ||
        card.querySelector(".pill-model-scroll") !== null;
      expect(scrolls, `${popup.name} caps its height, so it must be scrollable`).toBe(true);
      host.replaceChildren();
    }
  });

  it("leaves the floor a FLOOR, so a card wider than it still grows", () => {
    // What makes ONE floor safe for six cards: it is a minimum, not a width, so a
    // card whose content needs more is not cramped by sharing it. Measured rather
    // than reasoned, because the cards are `position: absolute` column flex
    // containers and a stretched row does not always push its container.
    document.documentElement.dataset["pointer"] = "fine";
    const card = mount({
      cls: "pill-role-list",
      name: "a mode card holding a long name",
      bodyRow: ".pill-role-item",
      rows: () => {
        const item = el("button", "pill-role-item");
        item.append(
          span("pill-role-name", "an-agent-whose-name-runs-past-the-floor-on-its-own"),
          span("pill-role-scope", "workspace"),
        );
        return [item];
      },
    });
    expect(card.getBoundingClientRect().width).toBeGreaterThan(224);
  });
});

describe("no popup text is below the mobile floor on a coarse pointer", () => {
  // 12px is the floor the app already keeps under a finger: `--fs-popup-meta`
  // resolves to `--fs-sm` there, and the effort tiers sit on it deliberately.
  const FLOOR_PX = 12;

  it.each(POPUPS.map((p) => [p.name, p] as const))("%s", (_name, popup) => {
    document.documentElement.dataset["pointer"] = "coarse";
    const card = mount(popup);

    const tooSmall: string[] = [];
    for (const node of [card, ...Array.from(card.querySelectorAll<HTMLElement>("*"))]) {
      // Only a node that renders text of its own: a wrapper's size says nothing
      // about what a reader sees, and an empty node has nothing to read.
      const own = Array.from(node.childNodes).some(
        (c) => c.nodeType === Node.TEXT_NODE && (c.textContent ?? "").trim() !== "",
      );
      if (!own) {
        continue;
      }
      const px = parseFloat(getComputedStyle(node).fontSize);
      if (px < FLOOR_PX) {
        tooSmall.push(`${node.className || node.tagName.toLowerCase()} at ${px}px`);
      }
    }

    expect(
      tooSmall,
      `every rendered string inside ${popup.name} is at least ${FLOOR_PX}px on a finger. ` +
        `A caption reads --fs-popup-meta; nothing inside a card names an --fs-* rung itself.`,
    ).toEqual([]);
  });

  it("lifts BOTH rungs on the pointer tier, not on a width query", () => {
    // The lift used to live in `@media (width <= 48rem)`, so an iPad — coarse
    // pointer, desktop width — got 44px controls beside 11px captions. This
    // measures the tier at the test viewport's own width, which is 1280.
    expect(window.innerWidth, "the lift must be readable at a desktop width").toBeGreaterThan(768);

    const card = mount(POPUPS[3] as Popup);
    const meta = card.querySelector<HTMLElement>(".pill-role-scope");
    expect(meta, "the fixture must render a caption").not.toBeNull();

    document.documentElement.dataset["pointer"] = "fine";
    const fine = {
      body: getComputedStyle(card).fontSize,
      meta: getComputedStyle(meta as HTMLElement).fontSize,
    };
    document.documentElement.dataset["pointer"] = "coarse";
    const coarse = {
      body: getComputedStyle(card).fontSize,
      meta: getComputedStyle(meta as HTMLElement).fontSize,
    };

    expect(fine).toEqual({ body: "13px", meta: "11px" });
    expect(coarse).toEqual({ body: "14px", meta: "12px" });
  });

  it("keeps a caption UNDER its row, so it cannot outsize what it labels", () => {
    // The two rungs collapsing onto one value would make `.effort-label` the same
    // size as the five tier buttons it labels, and a card's captions the same size
    // as its rows — which is the hierarchy the size split carries.
    for (const tier of ["fine", "coarse"] as const) {
      document.documentElement.dataset["pointer"] = tier;
      const card = mount(POPUPS[2] as Popup);
      const row = card.querySelector<HTMLElement>(".pill-model-item");
      const caption = card.querySelector<HTMLElement>(".pill-model-meta");
      const body = parseFloat(getComputedStyle(row as HTMLElement).fontSize);
      const meta = parseFloat(getComputedStyle(caption as HTMLElement).fontSize);
      expect(meta, `a caption must stay under its row on the ${tier} tier`).toBeLessThan(body);
      host.replaceChildren();
    }
  });
});
