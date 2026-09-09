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

import { describe, it, expect, vi, beforeAll, afterAll, afterEach } from "vitest";
import { page } from "vitest/browser";

import type { Message } from "./types.js";
import type { Turn } from "./turns.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

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
