// AN ACTION INSIDE A DENSE LIST ROW TAKES THE DENSE TIER, NOT `--btn-h`.
//
// `.btn-small` reads `min-height: var(--btn-h)` (2.25rem / 36px) from the shared
// button base in 14-tools.css, and `.list-row`'s own floor is `--ctl-h-sm`
// (1.5rem / 24px) — so a `.btn-small` inside a row DRIVES that row's height and
// reads as its subject rather than as its action. Reported against the Add-tool
// modal's Install button, which is `.btn-small.list-row-enable` inside
// `.list-row.tool-hit` (tools.ts `renderSearchHit`), as the button having "its
// desktop touch mode height, not desktop mouse size" — the pointer tier is
// correct and the token was wrong, which is the same call `vibekit-ui.md` "One
// control height per row" already made for the PRs tab's Merge/Close.
//
// TWO claims, and they pull against each other, which is why both are here: a
// `.btn-small` INSIDE a row shrinks to the dense tier, and a `.btn-small` that
// stands ALONE (a modal footer's Cancel / Open pull request) keeps `--btn-h`.
// A test for the first alone passes for a rule that shrank every `.btn-small` in
// the app, which is what the scoping exists to prevent.
//
// Real layout, because the claim is geometric: the rendered heights, and which
// element the row's own height comes from. `data-pointer="fine"` is stated
// rather than inherited, so the mouse tier is a premise instead of a side
// effect of the 1280px test viewport.
import { afterAll, beforeAll, describe, expect, it } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

/** `--ctl-h-dense` and `--btn-h` on the fine tier (01-tokens.css), in px. Stated
 *  here so a token retune fails this suite rather than silently redefining what it
 *  asserts. */
const DENSE_PX = 32;
const BTN_PX = 36;
/** `--hit-floor` and `--ctl-h` on the COARSE tier, which share a value — 44px is
 *  both the touch target floor and the number the reporter read off the row. */
const COARSE_PX = 44;

let style: HTMLStyleElement;
let pointerWas: string | null;
const hosts: HTMLElement[] = [];

function track<T extends HTMLElement>(el: T): T {
  document.body.append(el);
  hosts.push(el);
  return el;
}

/** A dense list row carrying one action, as `tools.ts` `renderSearchHit` builds
 *  the Add modal's result: the text column, then the Install button. */
function rowWithAction(): { row: HTMLElement; btn: HTMLElement } {
  const row = document.createElement("div");
  row.className = "list-row tool-hit";
  const text = document.createElement("div");
  text.className = "tool-hit-text";
  const name = document.createElement("span");
  name.className = "list-row-name";
  name.textContent = "ripgrep";
  text.append(name);
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = "btn-small list-row-enable";
  btn.textContent = "Install";
  row.append(text, btn);
  track(row);
  return { row, btn };
}

/** The Add modal's own close button (index.html `#tool-modal-close`), which is the
 *  height every other control on that surface already agrees on. The comparison
 *  the token number alone cannot make: this is what "one control height per row"
 *  is measured against. */
function modalIconButton(): HTMLElement {
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = "icon-btn";
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("class", "ic-ui");
  svg.setAttribute("viewBox", "0 0 24 24");
  btn.append(svg);
  return track(btn);
}

/** A `.btn-small` that stands alone in a modal's action bar (index.html's PR
 *  dialogs). The negative control: nothing scopes it to a row, so it keeps the
 *  full-size tier. */
function standaloneButton(): HTMLElement {
  const bar = document.createElement("div");
  bar.className = "modal-actions";
  const btn = document.createElement("button");
  btn.type = "button";
  btn.className = "btn-small";
  btn.textContent = "Cancel";
  bar.append(btn);
  track(bar);
  return btn;
}

const h = (el: HTMLElement): number => el.getBoundingClientRect().height;

beforeAll(() => {
  style = mountAppCSS();
  pointerWas = document.documentElement.getAttribute("data-pointer");
  document.documentElement.setAttribute("data-pointer", "fine");
});

afterAll(() => {
  style.remove();
  if (pointerWas === null) {
    document.documentElement.removeAttribute("data-pointer");
  } else {
    document.documentElement.setAttribute("data-pointer", pointerWas);
  }
  for (const el of hosts.splice(0)) {
    el.remove();
  }
});

describe("a .btn-small inside a .list-row", () => {
  it("takes the dense tier, agreeing with the modal's own icon button", () => {
    const { btn } = rowWithAction();
    expect(h(btn), "an action inside a dense row is --ctl-h-dense").toBeCloseTo(DENSE_PX, 0);
    expect(h(btn), "and so it agrees with every other control on that surface").toBeCloseTo(
      h(modalIconButton()),
      0,
    );
  });

  it("keeps its row off the coarse tier", () => {
    // The reported symptom, in the reporter's own terms: a 36px button in a row
    // whose floor is 24px rendered a 44px row, which is `--ctl-h` on the COARSE
    // tier — a touch-mode number on a fine-pointer desktop.
    const { row } = rowWithAction();
    expect(h(row), "a fine-pointer row must not measure the touch tier").toBeLessThan(COARSE_PX);
  });

  it("leaves a standing .btn-small at the full-size tier", () => {
    // The control for the whole change. Without it the first case passes just as
    // well for a rule that shrank every .btn-small in the app.
    expect(h(standaloneButton()), "a button that stands alone keeps --btn-h").toBeCloseTo(
      BTN_PX,
      0,
    );
  });

  it("still clears the touch floor on a coarse pointer", () => {
    // The other side of the same rule, and the reason it reads `max()`: the
    // universal hit-target floor scores zero, so this rule OUTRANKS it, and a bare
    // `--ctl-h-dense` (2.5rem coarse) would have put a 40px button beside the 44px
    // `.list-row-btn` in its own row. Measured: 44px before this change and 44px
    // after, so the fix is fine-tier only.
    document.documentElement.setAttribute("data-pointer", "coarse");
    try {
      const { btn } = rowWithAction();
      expect(h(btn), "a finger's action keeps the 44px target").toBeCloseTo(COARSE_PX, 0);
    } finally {
      document.documentElement.setAttribute("data-pointer", "fine");
    }
  });
});
