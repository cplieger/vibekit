// The turn-actions overflow menu as a GROUPED <details>, and the desktop path the
// group must not disturb.
//
// WHY THE GROUP. One `<details class="turn-actions-more">` is built per turn card
// and there is no sibling coordination anywhere: the only close-on-action is
// self-scoped (`btn.closest(".turn-actions-more")`). So on the phone layout — where
// the summary becomes the `…` trigger and the group becomes an absolutely
// positioned menu — opening turn 5's menu and then turn 9's would leave BOTH open,
// two floating cards over one transcript. A shared `name` makes that exclusive with
// no JS at all, which is the platform's own grouped-disclosure feature.
//
// WHY THE DESKTOP HALF IS HERE. `name` is an attribute on the same element that
// carries `display: contents` plus `::details-content { content-visibility:
// visible }`, and that pseudo-element rule is the ONLY thing keeping all five
// buttons painted and hittable at desktop widths while `open` is absent — its own
// comment in 61-mcp-tools.css records that they shipped unpainted without it. An
// attribute that changed the UA's closed-disclosure handling would take the whole
// desktop action row with it, silently, because the buttons would still be in the
// DOM and every existing test would still find them. So the group is pinned
// together with the thing it could break.
//
// WHY DISMISSAL IS HERE TOO. The group makes the menus exclusive of each other and
// nothing more: a native disclosure does not close because the reader looked away,
// so `messages-turn-actions.ts` adds one document-level `pointerdown` and one
// `keydown`. The case that matters is the one that looks like a defensive check and
// is not — `pointerdown` fires BEFORE `click`, so the listener has to exempt the
// details' own subtree or an action clicked from the collapsed menu loses the
// `open` the handler reads, and with it the "Copied" toast that is the only
// confirmation a menu click has. That is asserted through the `silent` argument
// the action was dispatched with, with the closed-menu case as its control.

import { describe, it, expect, vi, beforeAll, afterAll, afterEach } from "vitest";
import { page } from "vitest/browser";

import type { Message } from "./types.js";
import type { Turn } from "./turns.js";
import { loadCSS, mountAppCSS } from "./__test-helpers__/css-rules.js";

vi.mock("./store.js", () => ({
  getActive: () => ({ id: "c1", name: "chat", messages: [] }),
  getActiveId: () => "c1",
  get: vi.fn(() => undefined),
  getSessions: vi.fn(() => []),
  tabStatusFor: vi.fn(() => ""),
}));
vi.mock("./actions/messages.js", () => ({
  copyClipboard: { dispatch: vi.fn(() => Promise.resolve()) },
}));
vi.mock("./chat-export.js", () => ({ downloadChatExport: vi.fn() }));

const { mountTurnFooterActions, initTurnActionCallbacks } =
  await import("./messages-turn-actions.js");
const { copyClipboard } = await import("./actions/messages.js");

initTurnActionCallbacks({
  svgTemplate: () => () => document.createElement("span"),
});

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
});

afterEach(() => {
  document.body.replaceChildren();
});

/** One turn card with a mounted footer, appended to the page. */
function mountCard(id: string): HTMLDetailsElement {
  const card = document.createElement("div");
  card.className = "turn";
  const footer = document.createElement("div");
  footer.className = "turn-footer";
  card.appendChild(footer);
  document.body.appendChild(card);
  const msg: Message = { id, role: "assistant", ts: 1, content: "reply" };
  const turn: Turn = {
    id,
    n: 1,
    trigger: undefined,
    body: [msg],
    ts: 1,
    outcome: "completed",
    rewindTo: undefined,
  };
  mountTurnFooterActions(footer, card, turn);
  const menu = footer.querySelector<HTMLDetailsElement>("details.turn-actions-more");
  expect(menu, "the footer mounts an overflow details").not.toBeNull();
  if (menu === null) {
    throw new Error("no overflow menu");
  }
  return menu;
}

describe("the overflow menu is an exclusive group", () => {
  it("declares the shared group name on every turn's menu", () => {
    const first = mountCard("m1");
    const second = mountCard("m2");
    // One name, so the group has more than one member — which is what makes the
    // attribute do anything at all.
    expect(first.name).toBe("turn-actions-overflow");
    expect(second.name).toBe(first.name);
  });

  it("closes the other turn's menu when one opens", () => {
    const first = mountCard("m1");
    const second = mountCard("m2");

    first.open = true;
    expect([first.open, second.open]).toEqual([true, false]);

    // The whole point: opening the second must not leave the first standing.
    second.open = true;
    expect([first.open, second.open], "at most one menu is open").toEqual([false, true]);
  });

  it("still closes on its own action, which is the self-scoped path", () => {
    const menu = mountCard("m1");
    menu.open = true;
    const btn = menu.querySelector<HTMLButtonElement>(".turn-actions-group .turn-action-btn");
    expect(btn).not.toBeNull();
    btn?.click();
    expect(menu.open).toBe(false);
  });
});

describe("the phone layout, at a real viewport size", () => {
  // A media query answers about the VIEWPORT, so the only honest test resizes one.
  // The block restores the entry size in `afterAll`, read off the frame rather than
  // copied from `vitest.config.ts`.
  let entry: { readonly width: number; readonly height: number } | null = null;

  beforeAll(() => {
    entry = { width: window.innerWidth, height: window.innerHeight };
  });

  afterAll(async () => {
    if (entry !== null) {
      await page.viewport(entry.width, entry.height);
    }
  });

  async function phone(): Promise<void> {
    await page.viewport(375, 800);
    expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
      375, 800,
    ]);
  }

  it("collapses the actions behind the … trigger and reveals them on open", async () => {
    await phone();
    const menu = mountCard("m1");
    const summary = menu.querySelector<HTMLElement>("summary.turn-action-more");
    const group = menu.querySelector<HTMLElement>(".turn-actions-group");
    expect(summary).not.toBeNull();
    expect(group).not.toBeNull();

    // The trigger exists here and only here; on desktop it is `display: none`.
    expect(getComputedStyle(summary as HTMLElement).display).not.toBe("none");
    expect(getComputedStyle(group as HTMLElement).display, "closed hides the group").toBe("none");

    menu.open = true;
    expect(getComputedStyle(group as HTMLElement).display, "open reveals it").toBe("flex");
    expect((group as HTMLElement).getBoundingClientRect().height).toBeGreaterThan(0);
  });

  it("never shows two menus at once, even opened by the trigger", async () => {
    await phone();
    const first = mountCard("m1");
    const second = mountCard("m2");
    const trigger = (m: HTMLDetailsElement): HTMLElement => {
      const s = m.querySelector<HTMLElement>("summary.turn-action-more");
      if (s === null) {
        throw new Error("no trigger");
      }
      return s;
    };

    trigger(first).click();
    expect(first.open).toBe(true);
    trigger(second).click();

    expect([first.open, second.open]).toEqual([false, true]);
    // And the CONSEQUENCE that matters on a phone: only one menu card is painted.
    const painted = [...document.querySelectorAll<HTMLElement>(".turn-actions-group")].filter(
      (g) => getComputedStyle(g).display !== "none",
    );
    expect(painted, "one floating menu on screen, not two").toHaveLength(1);
  });
});

describe("the desktop path the group must not disturb", () => {
  it("keeps every action painted and hittable while `open` is absent", () => {
    // The browser project's own 1280px viewport, which is above the 40rem media
    // rule, so this is the inline layout.
    const menu = mountCard("m1");
    expect(menu.open, "the inline layout is the CLOSED disclosure").toBe(false);

    // `display: contents` removes the details' own box, not the box the UA hides a
    // closed disclosure through, so this declaration is what cancels the skip.
    expect(getComputedStyle(menu, "::details-content").contentVisibility).toBe("visible");
    // The trigger is not part of the desktop row.
    const summary = menu.querySelector<HTMLElement>("summary.turn-action-more");
    expect(getComputedStyle(summary as HTMLElement).display).toBe("none");

    const btns = [...menu.querySelectorAll<HTMLElement>(".turn-actions-group .turn-action-btn")];
    expect(btns).toHaveLength(5);
    for (const btn of btns) {
      const box = btn.getBoundingClientRect();
      expect(box.width, "a laid-out box, not a skipped subtree").toBeGreaterThan(0);
      expect(box.height).toBeGreaterThan(0);
      // Hittable: the element at its own centre is the button or something inside
      // it. A `content-visibility: hidden` subtree fails this even while its boxes
      // still measure, which is why the geometry check alone is not the assertion.
      const hit = document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 2);
      expect(hit === null ? null : btn.contains(hit) || hit === btn).toBe(true);
    }
  });
});

/** The menu's first action button, which is Copy as text. */
function firstAction(menu: HTMLDetailsElement): HTMLButtonElement {
  const btn = menu.querySelector<HTMLButtonElement>(".turn-actions-group .turn-action-btn");
  expect(btn, "the group holds action buttons").not.toBeNull();
  if (btn === null) {
    throw new Error("no action button");
  }
  return btn;
}

/** A real pointer press on `node`, which is what puts the dismissal listener ahead
 *  of the button's own click the way a finger or a mouse does. */
function press(node: EventTarget): void {
  node.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true }));
}

/** The `silent` flag the click's `copyClipboard.dispatch` carried.
 *
 *  This is the observable the open-before-handler read moves, and the only one it
 *  moves: `silent: false` asks the action for its "Copied" toast, which a click
 *  made from an OPEN menu needs and a click on a visible button does not. */
function dispatchedSilent(): boolean | undefined {
  const { calls } = vi.mocked(copyClipboard.dispatch).mock;
  expect(calls, "the copy action was dispatched exactly once").toHaveLength(1);
  return calls[0]?.[1]?.silent;
}

describe("dismissal, which the native disclosure does not supply", () => {
  it("closes an open menu on a pointerdown outside it", () => {
    const menu = mountCard("m1");
    menu.open = true;
    press(document.body);
    expect(menu.open).toBe(false);
  });

  it("leaves it open on a pointerdown INSIDE it", () => {
    const menu = mountCard("m1");
    menu.open = true;
    press(firstAction(menu));
    expect(menu.open).toBe(true);
  });

  it("closes an open menu on Escape", () => {
    const menu = mountCard("m1");
    menu.open = true;
    document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    expect(menu.open).toBe(false);
  });

  it("lets Escape keep propagating, so the app's own handling still sees it", () => {
    mountCard("m1");
    let reached = false;
    const spy = (): void => {
      reached = true;
    };
    // On `window`, which is past `document` in the bubble order, so it is reached
    // only if the dismissal did not stop propagation.
    window.addEventListener("keydown", spy);
    document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    window.removeEventListener("keydown", spy);
    expect(reached).toBe(true);
  });

  it("keeps an action's handler reading open===true when the click came from the menu", () => {
    const menu = mountCard("m1");
    menu.open = true;
    const btn = firstAction(menu);
    press(btn);
    btn.click();
    expect(dispatchedSilent(), "the menu click asked for the toast").toBe(false);
  });

  it("still closes the menu after that action's handler has run", () => {
    const menu = mountCard("m1");
    menu.open = true;
    const btn = firstAction(menu);
    press(btn);
    btn.click();
    expect(menu.open).toBe(false);
  });

  it("keeps the toast suppressed for a click made with the menu closed", () => {
    // The control that makes the case above mean something: `silent` is a
    // function of the menu's state, not a constant.
    const menu = mountCard("m1");
    const btn = firstAction(menu);
    press(btn);
    btn.click();
    expect(dispatchedSilent()).toBe(true);
  });
});

/** The block that follows `marker`, brace-matched. */
function blockAfter(css: string, marker: string): string {
  const at = css.indexOf(marker);
  expect(at, `not found in the stylesheet: ${marker}`).toBeGreaterThan(-1);
  const open = css.indexOf("{", at + marker.length);
  let depth = 0;
  for (let i = open; i < css.length; i++) {
    if (css[i] === "{") {
      depth++;
    } else if (css[i] === "}") {
      depth--;
      if (depth === 0) {
        return css.slice(open + 1, i);
      }
    }
  }
  throw new Error(`unbalanced braces after ${marker}`);
}

/** A block's declarations, with comments dropped and whitespace flattened — so the
 *  comparison below is about the rules and not about how deep one arm is nested. */
function declarations(block: string): string {
  return block
    .replace(/\/\*[\s\S]*?\*\//gu, " ")
    .replace(/\s+/gu, " ")
    .trim();
}

describe("the two collapse arms are one rule", () => {
  it("states each threshold exactly once", () => {
    // Or "the two arms" is a claim about whichever pair a reader's grep found
    // first, and a third arm could sit anywhere in the file unnoticed.
    const css = loadCSS("61-mcp-tools.css");
    expect(css.match(/@media \(width <= 40rem\)/gu), "the every-tier arm").toHaveLength(1);
    expect(css.match(/@media \(width <= 56rem\)/gu), "the coarse arm").toHaveLength(1);
  });

  it("carries identical declarations in both", () => {
    // The remedy for a duplication no token can remove: a media query cannot read
    // a custom property, so the QUERY is stated twice and this is what stops the
    // two bodies drifting. Same shape turn-rewind-css.test.ts uses to pin two
    // numbers that must stay equal.
    const css = loadCSS("61-mcp-tools.css");
    const fine = declarations(blockAfter(css, "@media (width <= 40rem)"));
    const coarse = declarations(
      blockAfter(
        blockAfter(css, "@media (width <= 56rem)"),
        ':where(:root:not([data-pointer="fine"]))',
      ),
    );
    expect(fine, "the arm being compared is the collapsed-actions one").toContain(
      ".turn-actions-more",
    );
    expect(coarse).toBe(fine);
  });
});

describe("the coarse tier collapses at a width a fine pointer does not", () => {
  // 896px IS 56rem, and `<=` includes it, so this is the widest row a coarse
  // pointer collapses. It is also above the 40rem arm and above 01-tokens.css's
  // 48rem no-JS fallback, so the tier attribute is the only thing that can collapse
  // it — which is what makes the fine case a control rather than a second reading of
  // one rule.
  //
  // MEASURED, and it corrects the width this suite was asked for: at 1024px NEITHER
  // tier collapses, because 1024 is past 56rem. A pair of cases at that width would
  // have passed for the wrong reason on the fine side and been unsatisfiable on the
  // coarse one, so the third case below pins 1024 as INLINE on a coarse pointer
  // instead — the coarse arm is a threshold, not "a finger always gets the menu".
  //
  // The tier IS an attribute, so this is a faithful test of the RULE and cannot
  // prove a real tablet takes that path. Same limit run-card-metrics.ts accepts and
  // states.
  //
  // Last in the file, and it restores the size it found: `page.viewport` has no
  // getter, so a hand-copied pair would silently leave every later file measuring
  // at the wrong size.
  let entry: { readonly width: number; readonly height: number } | null = null;

  beforeAll(() => {
    entry = { width: window.innerWidth, height: window.innerHeight };
  });

  afterAll(async () => {
    document.documentElement.removeAttribute("data-pointer");
    if (entry !== null) {
      await page.viewport(entry.width, entry.height);
    }
  });

  async function at(width: number, tier: "fine" | "coarse"): Promise<void> {
    await page.viewport(width, 900);
    expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
      width,
      900,
    ]);
    document.documentElement.dataset["pointer"] = tier;
  }

  it("hides the closed group and paints the … trigger on a coarse pointer", async () => {
    await at(896, "coarse");
    const menu = mountCard("m1");
    const summary = menu.querySelector<HTMLElement>("summary.turn-action-more");
    const group = menu.querySelector<HTMLElement>(".turn-actions-group");
    expect(summary).not.toBeNull();
    expect(group).not.toBeNull();
    expect(getComputedStyle(group as HTMLElement).display, "closed hides the group").toBe("none");
    expect(getComputedStyle(summary as HTMLElement).display, "the trigger is painted").not.toBe(
      "none",
    );
  });

  it("keeps the actions inline at the same width on a fine pointer", async () => {
    await at(896, "fine");
    const menu = mountCard("m1");
    const summary = menu.querySelector<HTMLElement>("summary.turn-action-more");
    const group = menu.querySelector<HTMLElement>(".turn-actions-group");
    expect(getComputedStyle(group as HTMLElement).display, "the row is inline").toBe("inline-flex");
    expect(getComputedStyle(summary as HTMLElement).display, "no trigger to press").toBe("none");
  });

  it("leaves a coarse pointer inline once the row is wider than the arm", async () => {
    await at(1024, "coarse");
    const menu = mountCard("m1");
    const group = menu.querySelector<HTMLElement>(".turn-actions-group");
    expect(getComputedStyle(group as HTMLElement).display, "56rem is a bound, not a tier").toBe(
      "inline-flex",
    );
  });
});
