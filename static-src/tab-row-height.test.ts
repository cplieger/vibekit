// A CHAT TAB AND THE NEW CHAT BUTTON ARE THE SAME BOX, on a phone.
//
// The defect this pins: `.tab` is `min-height: var(--btn-h)` with
// `padding-block: var(--sp-2)` and a 1px border reserved on every row, so a child
// that paints its own `--btn-h` box STACKS on that chrome. 50-mobile.css used to
// give `.tab-close` `width`/`height: var(--btn-h)` for its touch target, which
// took every row to 62px against `#new-chat`'s 44px — reported as the chat tabs
// appearing larger than the New chat button. The × now grows its TARGET through an
// absolutely positioned `::after` instead, which is the app's documented escape for
// a control that must stay visually small (`vibekit-ui.md` "Hit targets").
//
// So there are two claims and they pull against each other, which is why both are
// here: the ROW must not grow, and the × must still be reachable with a finger. A
// test for either one alone passes for the shape that broke the other.
//
// Real layout, in an IFRAME: the page viewport is pinned at 1280x720
// (vitest.config.ts) and every rule involved sits behind `width <= 48rem`, so the
// narrow side needs a viewport of its own for the query to evaluate against.
// `data-pointer="coarse"` is set explicitly rather than left to 01-tokens.css's
// no-JS fallback, so the 44px tier is a stated premise rather than a side effect of
// the same width.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

const PHONE_W = 390;
const PHONE_H = 844;

let style: HTMLStyleElement;
let frame: HTMLIFrameElement;
let doc: Document;

/** The sidebar's two rows as the app builds them: `.sidebar-actions` wrapping
 *  `#new-chat` (10-shell-app.css / index.html), then `#tab-list` holding one
 *  top-level chat tab in `createTabEl`'s own child order. */
function mountSidebar(): { btn: HTMLElement; tab: HTMLElement; close: HTMLElement } {
  const sidebar = doc.createElement("nav");
  sidebar.id = "sidebar";
  // The drawer, OPEN. Below 48rem the sidebar is `transform: translateX(-100%)`
  // until `.open` lands (50-mobile.css), which puts every row off-screen — the
  // rects still measure (shifted equally, so a comparison between two of them
  // survives) while `elementFromPoint` answers null for all of them.
  sidebar.classList.add("open");

  const actions = doc.createElement("div");
  actions.className = "sidebar-actions";
  const btn = doc.createElement("button");
  btn.type = "button";
  btn.id = "new-chat";
  btn.className = "sidebar-action-btn";
  const btnIcon = doc.createElementNS("http://www.w3.org/2000/svg", "svg");
  btnIcon.setAttribute("class", "ic-ui");
  btn.append(btnIcon, doc.createTextNode("New chat"));
  actions.appendChild(btn);

  const list = doc.createElement("div");
  list.id = "tab-list";
  list.setAttribute("role", "tablist");

  const tab = doc.createElement("div");
  tab.className = "tab";
  tab.setAttribute("role", "tab");
  tab.dataset["kind"] = "chat";
  const dot = doc.createElement("span");
  dot.className = "tab-status-dot";
  dot.dataset["status"] = "idle";
  dot.setAttribute("aria-hidden", "true");
  const runDot = doc.createElement("span");
  runDot.className = "tab-run-dot";
  runDot.setAttribute("aria-hidden", "true");
  const name = doc.createElement("span");
  name.className = "tab-name";
  name.textContent = "Rebuild vibekit timeline rail";
  const sr = doc.createElement("span");
  sr.className = "tab-status-sr sr-only";
  const runSr = doc.createElement("span");
  runSr.className = "tab-run-sr sr-only";
  const pin = doc.createElement("span");
  pin.className = "tab-pin";
  const close = doc.createElement("span");
  close.className = "tab-close";
  close.setAttribute("aria-hidden", "true");
  const closeIcon = doc.createElementNS("http://www.w3.org/2000/svg", "svg");
  closeIcon.setAttribute("class", "ic-ui");
  close.appendChild(closeIcon);
  tab.append(dot, runDot, name, sr, runSr, pin, close);
  list.appendChild(tab);

  sidebar.append(actions, list);
  doc.body.replaceChildren(sidebar);
  return { btn, tab, close };
}

beforeAll(() => {
  style = mountAppCSS();
  frame = document.createElement("iframe");
  frame.width = String(PHONE_W);
  frame.height = String(PHONE_H);
  document.body.appendChild(frame);
  const inner = frame.contentDocument;
  if (inner === null) {
    throw new Error("iframe has no contentDocument");
  }
  doc = inner;
  doc.documentElement.dataset["pointer"] = "coarse";
  const sheet = doc.createElement("style");
  sheet.textContent = style.textContent;
  doc.head.appendChild(sheet);
});

afterAll(() => {
  frame.remove();
  style.remove();
});

describe("a chat tab against the New chat button", () => {
  it("is on the phone tier the rules are written for", () => {
    // The premise, stated rather than assumed: every rule under test sits behind
    // `width <= 48rem`, and the 44px control tier behind `[data-pointer="coarse"]`.
    expect(doc.defaultView?.innerWidth).toBeLessThanOrEqual(768);
    expect(getComputedStyle(doc.documentElement).getPropertyValue("--btn-h").trim()).toBe(
      "2.75rem",
    );
  });

  it("measures the same HEIGHT", () => {
    const { btn, tab } = mountSidebar();
    const bh = btn.getBoundingClientRect().height;
    const th = tab.getBoundingClientRect().height;
    expect(bh).toBeCloseTo(44, 0);
    expect(th, `a tab is ${th}px against the New chat button's ${bh}px`).toBeCloseTo(bh, 0);
  });

  it("measures the same WIDTH, on the same left and right edges", () => {
    // `#tab-list`'s inline padding and `.sidebar-actions`' margin are the SAME value
    // (`--sp-2`), which is what makes these two boxes one box rather than a button
    // lined up inside a wrapper that overhangs it.
    const { btn, tab } = mountSidebar();
    const b = btn.getBoundingClientRect();
    const t = tab.getBoundingClientRect();
    expect(t.width, `a tab is ${t.width}px wide against the button's ${b.width}px`).toBeCloseTo(
      b.width,
      0,
    );
    expect(t.left).toBeCloseTo(b.left, 0);
    expect(t.right).toBeCloseTo(b.right, 0);
  });

  it("puts the VISIBLE BORDER on the button, so it lines up with a tab's too", () => {
    // THE DEFECT THIS PINS, and the one the width case above could not see: the border
    // a reader sees used to belong to `.sidebar-actions`, a wrapper 6px wider than the
    // button inside it, so the two boxes agreed on their edges while the bordered box
    // overhung every tab by 3px a side. Reported as the New chat button being wider
    // than the tabs under it. So the assertion is that the WRAPPER paints nothing and
    // the BUTTON carries the border — a bordered wrapper of any width fails here.
    const { btn } = mountSidebar();
    const wrap = btn.parentElement as HTMLElement;
    const w = getComputedStyle(wrap);
    expect(wrap.className, "the wrapper is `.sidebar-actions`").toContain("sidebar-actions");
    expect(parseFloat(w.borderTopWidth), "the wrapper carries no border").toBe(0);
    expect(parseFloat(w.paddingLeft), "and no inset for the button to sit inside").toBe(0);
    expect(
      parseFloat(getComputedStyle(btn).borderTopWidth),
      "the button carries the border",
    ).toBeGreaterThan(0);
    // Which makes the visible box the same box, so the wrapper's rect is the button's.
    const wr = wrap.getBoundingClientRect();
    const br = btn.getBoundingClientRect();
    expect(wr.left).toBeCloseTo(br.left, 0);
    expect(wr.right).toBeCloseTo(br.right, 0);
  });
});

describe("the × keeps a finger-sized target inside that row", () => {
  it("paints a box the row can hold", () => {
    // The painted box is the 24px span 10-shell-app.css declares; growing it is
    // what pushed the row to 62px.
    const { close } = mountSidebar();
    const r = close.getBoundingClientRect();
    expect(r.width).toBeCloseTo(24, 0);
    expect(r.height).toBeCloseTo(24, 0);
  });

  it("answers a hit at the row's own top and bottom edges", () => {
    // The expander, measured the only way one can be: a real hit test past the
    // painted box. A style read cannot see it, and an `overflow` on the × or on
    // the row would clip it away while reading identically in the cascade.
    const { tab, close } = mountSidebar();
    const t = tab.getBoundingClientRect();
    const c = close.getBoundingClientRect();
    const x = c.left + c.width / 2;
    for (const [name, y] of [
      ["top edge", t.top + 1],
      ["bottom edge", t.bottom - 1],
    ] as const) {
      const hit = doc.elementFromPoint(x, y);
      expect(hit, `${name} of the row, above the × 's painted box`).toBe(close);
    }
  });

  it("reaches --hit-floor on both axes", () => {
    const { close } = mountSidebar();
    const c = close.getBoundingClientRect();
    const floor = parseFloat(
      getComputedStyle(doc.documentElement).getPropertyValue("--hit-floor"),
    ) as number;
    // --hit-floor is in rem at the 16px root size the app never changes.
    const px = floor * 16;
    const cx = c.left + c.width / 2;
    const cy = c.top + c.height / 2;
    // Half a pixel inside each end of the target, so the assertion is about the
    // expander's SIZE rather than about rounding at its boundary.
    for (const [name, x, y] of [
      ["inline start", cx - px / 2 + 1, cy],
      ["inline end", cx + px / 2 - 1, cy],
      ["block start", cx, cy - px / 2 + 1],
      ["block end", cx, cy + px / 2 - 1],
    ] as const) {
      expect(doc.elementFromPoint(x, y), `${name} of a ${px}px target`).toBe(close);
    }
  });
});
