// ---------------------------------------------------------------------------
// The touch/mouse toggle: its three visibility conditions, the click, and the
// a11y triple.
//
// The button is mounted from `static/index.html` rather than a copy of its markup,
// because the authored glyph classes and the initial `.hidden` ARE the contract
// this module reads — a hand-written fixture would keep passing after the page
// stopped agreeing with it.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeAll, beforeEach, afterEach, afterAll, vi } from "vitest";
// The viewport control, for the one gate that can only be measured by resizing.
// `vitest/browser` is the Vitest 5 spelling; `@vitest/browser/context` is a stub
// that throws.
import { page } from "vitest/browser";
import indexHtml from "../static/index.html?raw";

import { initPointerModeToggle, revealPointerModeToggle } from "./pointer-mode.js";
import { initPointerTier, currentTier } from "./pointer-tier.js";
import { markCoarseSeen, pointerModeChoice, setPointerModeChoice } from "./device-view.js";
import { allRules, loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";

/** The real button, lifted out of the page by id. */
function markupFor(anchor: string): string {
  const at = indexHtml.indexOf(anchor);
  expect(at, `static/index.html has no ${anchor}`).toBeGreaterThan(-1);
  const open = indexHtml.lastIndexOf("<button", at);
  const end = indexHtml.indexOf("</button>", at);
  expect(end, `${anchor} is not inside a button`).toBeGreaterThan(at);
  return indexHtml.slice(open, end + "</button>".length);
}

function mountButton(): HTMLButtonElement {
  document.body.innerHTML = markupFor('id="pointer-mode-btn"');
  const btn = document.getElementById("pointer-mode-btn");
  expect(btn).not.toBeNull();
  return btn as HTMLButtonElement;
}

/** The whole `<div …>…</div>` opening with `openTag`, matched by counting nested
 *  div tags. Nothing formats `static/index.html` — prettier runs from
 *  `static-src/` and the page is its sibling — so its whitespace is not a
 *  contract and a blank-line delimiter would break on a reindent. */
function divAt(openTag: string): string {
  const start = indexHtml.indexOf(openTag);
  expect(start, `static/index.html has no ${openTag}`).toBeGreaterThan(-1);
  const tags = /<div\b|<\/div\s*>/g;
  tags.lastIndex = start;
  let depth = 0;
  for (let m = tags.exec(indexHtml); m !== null; m = tags.exec(indexHtml)) {
    depth += m[0].startsWith("</") ? -1 : 1;
    if (depth === 0) {
      return indexHtml.slice(start, m.index + m[0].length);
    }
  }
  throw new Error(`unbalanced divs after ${openTag}`);
}

function hiddenGlyphs(btn: HTMLElement): string[] {
  return [...btn.querySelectorAll("svg.hidden")].map((el) =>
    el.classList.contains("pointer-icon-fine") ? "fine" : "coarse",
  );
}

beforeEach(() => {
  localStorage.clear();
  document.documentElement.removeAttribute("data-pointer");
});

afterEach(() => {
  localStorage.clear();
  document.documentElement.removeAttribute("data-pointer");
  document.body.innerHTML = "";
});

describe("the toggle's visibility", () => {
  it("stays hidden on a device that has never reported a coarse pointer", () => {
    const btn = mountButton();
    initPointerTier();
    initPointerModeToggle();

    expect(btn.classList.contains("hidden")).toBe(true);
  });

  it("is shown once the sticky flag is set", () => {
    markCoarseSeen();
    const btn = mountButton();
    initPointerTier();
    initPointerModeToggle();

    expect(btn.classList.contains("hidden")).toBe(false);
  });

  it("is revealed by the callback the tier's observer fires, once per device", () => {
    const btn = mountButton();
    initPointerTier({ onCoarseSeen: revealPointerModeToggle });
    initPointerModeToggle();
    expect(btn.classList.contains("hidden")).toBe(true);

    btn.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, pointerType: "touch" }));
    expect(btn.classList.contains("hidden")).toBe(false);

    // Idempotent, so the composition root needs no guard of its own.
    revealPointerModeToggle();
    expect(btn.classList.contains("hidden")).toBe(false);
  });
});

describe("the toggle's click", () => {
  it("flips the mode, persists the choice, and moves data-pointer", () => {
    markCoarseSeen();
    const btn = mountButton();
    initPointerTier();
    initPointerModeToggle();
    expect(currentTier()).toBe("fine");

    btn.click();
    expect(currentTier()).toBe("coarse");
    expect(pointerModeChoice()).toBe("coarse");

    btn.click();
    expect(currentTier()).toBe("fine");
    expect(pointerModeChoice()).toBe("fine");
  });

  it("swaps which glyph carries .hidden", async () => {
    markCoarseSeen();
    const btn = mountButton();
    initPointerTier();
    initPointerModeToggle();
    // The FIRST paint has nothing on screen to slide out, so it settles
    // synchronously — which is also what puts the right glyph up on a cold load.
    expect(hiddenGlyphs(btn)).toEqual(["coarse"]);

    btn.click();
    // The swap waits for the outgoing glyph's slide, so it is the one part of the
    // click that is not synchronous. No stylesheet is mounted here, so nothing
    // transitions and the settle arrives on the module's own safety timeout —
    // which is the same path reduced motion takes in production.
    await vi.waitFor(() => {
      expect(hiddenGlyphs(btn)).toEqual(["fine"]);
    });
  });

  it("converges on the second click when two land inside one slide", async () => {
    // The first click's settle is deferred behind the outgoing glyph's slide, so it
    // is still pending when the second click paints. Two things have to hold: the
    // superseded settle must change nothing when it finally arrives, and the glyph
    // it left parked below its resting position must be put back — the settle
    // clears `.icon-setting` on the glyph it HIDES, never on the one it reveals.
    markCoarseSeen();
    const btn = mountButton();
    initPointerTier();
    initPointerModeToggle();

    btn.click();
    btn.click();
    expect(currentTier()).toBe("fine");
    expect(hiddenGlyphs(btn)).toEqual(["coarse"]);
    expect([...btn.querySelectorAll("svg.icon-setting")], "a glyph left mid-slide").toEqual([]);

    // The first click's 350ms safety timeout still has to arrive and do nothing.
    await new Promise((resolve) => setTimeout(resolve, 400));
    expect(hiddenGlyphs(btn)).toEqual(["coarse"]);
    expect(btn.getAttribute("aria-pressed")).toBe("false");
  });

  it("flips aria-pressed and the tooltip while the accessible name stays put", () => {
    markCoarseSeen();
    const btn = mountButton();
    initPointerTier();
    initPointerModeToggle();

    expect(btn.getAttribute("aria-pressed")).toBe("false");
    expect(btn.getAttribute("aria-label")).toBe("Touch mode");
    expect(btn.getAttribute("data-tooltip")).toBe("Mouse mode. Switch to touch mode");

    btn.click();
    expect(btn.getAttribute("aria-pressed")).toBe("true");
    expect(btn.getAttribute("aria-label")).toBe("Touch mode");
    expect(btn.getAttribute("data-tooltip")).toBe("Touch mode. Switch to mouse mode");
  });

  it("reports the tier a stored choice put the document in", () => {
    // The button paints from what is IN FORCE, not from a default: a device that
    // chose the enlarged tier must not come back offering to enlarge it again.
    setPointerModeChoice("coarse");
    markCoarseSeen();
    const btn = mountButton();
    initPointerTier();
    initPointerModeToggle();

    expect(btn.getAttribute("aria-pressed")).toBe("true");
    expect(btn.getAttribute("aria-label")).toBe("Touch mode");
    expect(hiddenGlyphs(btn)).toEqual(["fine"]);
  });

  it("keeps one accessible name in both states while the tooltip carries the state", () => {
    // `aria-pressed` is the state channel, so the name may not also change with the
    // state: announced, a flipping action name reads as "Switch to mouse mode,
    // pressed", attaching a state to a phrase about the next press. The tooltip has
    // no such channel beside it, so it says both. Deliberately literal-free — the
    // case above pins the exact copy, and this one has to survive a wording change
    // or the rule stops being guarded the moment somebody rewrites the strings.
    markCoarseSeen();
    const btn = mountButton();
    initPointerTier();
    initPointerModeToggle();

    const before = {
      label: btn.getAttribute("aria-label") ?? "",
      tooltip: btn.getAttribute("data-tooltip") ?? "",
    };
    btn.click();
    const after = {
      label: btn.getAttribute("aria-label") ?? "",
      tooltip: btn.getAttribute("data-tooltip") ?? "",
    };

    expect(after.label, "the accessible name is stable across a toggle").toBe(before.label);
    expect(before.label, "the name carries no action").not.toMatch(/\bSwitch\b/);
    expect(before.tooltip, "the tooltip names the state, then the action").toMatch(
      /^.+ mode\. Switch to .+ mode$/,
    );
    expect(after.tooltip, "the tooltip names the state, then the action").toMatch(
      /^.+ mode\. Switch to .+ mode$/,
    );
    expect(after.tooltip, "the tooltip's state half moves with the tier").not.toBe(before.tooltip);
  });
});

/** The one rule in the mobile stylesheet that hides the toggle. Spelled in full
 *  because `ruleContaining` keys on an exact selector-list MEMBER, and stating it
 *  here is what lets the scope checks ("inside the query that names each arm,
 *  exactly once") do their work. */
const MOBILE_HIDE = '[id="pointer-mode-btn"]';

describe("the phone-shaped arm of the visibility rule", () => {
  it("is display:none inside ONE query naming both the narrow and the short arm", () => {
    // The viewport condition is CSS's, in the one file that owns the definition of
    // mobile — so it tracks a rotation and a window resize for free and cannot
    // disagree with `.mobile-only`. `ruleContaining` demands exactly one match per
    // scope, so asking it under each arm and then comparing the two bodies is what
    // says the arms sit in one prelude rather than in two rules that can drift.
    const narrow = ruleContaining(loadCSS("50-mobile.css"), MOBILE_HIDE, "48rem");
    const short = ruleContaining(loadCSS("50-mobile.css"), MOBILE_HIDE, "30rem");
    expect(narrow.body).toMatch(/display:\s*none/);
    expect(short.body, "one rule, two arms").toBe(narrow.body);
  });

  it("hides the toggle on a phone-shaped viewport whatever the tier is", () => {
    // The phone layout is touch-only in either orientation, so a control that
    // revokes the enlarged tier has nothing to offer there. Asserted over EVERY
    // rule that reaches the id rather than over this one, so a rule that gates the
    // hide on the tier again fails here instead of quietly reintroducing the
    // width-gated escape hatch a `fine` pin used to survive on.
    const reaching = allRules(loadCSS("50-mobile.css")).filter((r) =>
      r.selector.includes('[id="pointer-mode-btn"]'),
    );
    expect(
      reaching.map((r) => r.selector),
      "mobile rules reaching the toggle",
    ).toEqual([MOBILE_HIDE]);
  });
});

describe("the toolbar row at phone width", () => {
  // Nullable and cleaned up conditionally, so a failure BEFORE the fixture is
  // built reports itself rather than being replaced by a hook error on undefined.
  let styleEl: HTMLStyleElement | null = null;
  let app: HTMLElement | null = null;

  afterAll(() => {
    styleEl?.remove();
    app?.remove();
  });

  it("keeps every toolbar button on one row at 390px with coarse controls", () => {
    // Settings moved into this bar, so the row carries one more 44px touch target
    // than it did. `.chat-toolbar` wraps below 30rem of chat area rather than
    // overflowing, and this is the measurement that says whether it has to.
    //
    // The viewport here is the browser project's 1280px, so the `width <= 48rem`
    // media query does not apply and `#menu-toggle` keeps its `display: none` — a
    // real phone shows the hamburger too, and that is the one button this case
    // cannot account for. MEASURED: these 7 render 44px each, so 7x44 + 6x2 + 24 of
    // padding is 344 and the row still holds at 360px. Adding the hamburger makes
    // it 8x44 + 7x2 + 24 = 390, which fits a 390px phone EXACTLY and wraps below
    // it. That is a cost to report, not one to fix by shrinking a touch target:
    // `.chat-toolbar` sets `flex-wrap: wrap` under this container query, so the
    // 8th button drops to a second row rather than overflowing.
    styleEl = mountAppCSS();
    app = document.createElement("div");
    app.id = "app";
    app.innerHTML = `<main id="chat-area">${divAt('<div class="chat-toolbar">')}</main>`;
    document.body.appendChild(app);

    const area = app.querySelector<HTMLElement>("#chat-area");
    expect(area).not.toBeNull();
    area?.style.setProperty("inline-size", "390px");
    document.documentElement.setAttribute("data-pointer", "coarse");

    const buttons = [...app.querySelectorAll<HTMLElement>(".chat-toolbar > button")].filter(
      (b) => b.offsetParent !== null,
    );
    expect(buttons.length, "the persistent toolbar buttons").toBe(7);
    const tops = [...new Set(buttons.map((b) => b.offsetTop))];
    const widths = buttons.map((b) => `${b.id}:${String(b.offsetWidth)}`).join(" ");
    expect(tops, `one row expected; button widths were ${widths}`).toHaveLength(1);
  });
});

describe("the phone-shaped gate, measured at real viewport sizes", () => {
  // A media query answers about the VIEWPORT, so the only honest test of this gate
  // resizes one — no amount of DOM setup can stand in for it. The block sits LAST
  // in the file and restores the size in `afterAll`, because the toolbar case above
  // reads the browser project's own width. `page.viewport` has no getter, so the
  // size is READ off the frame on entry rather than copied from `vitest.config.ts`:
  // a hand-copied pair would silently leave every later file measuring at the old
  // size if that config moved.
  let entry: { readonly width: number; readonly height: number } | null = null;
  let styleEl: HTMLStyleElement | null = null;

  beforeAll(() => {
    entry = { width: window.innerWidth, height: window.innerHeight };
    styleEl = mountAppCSS();
  });

  afterAll(async () => {
    styleEl?.remove();
    if (entry !== null) {
      await page.viewport(entry.width, entry.height);
    }
  });

  /** The button's computed `display` at one viewport size. `.hidden` is cleared
   *  first: it is the JS gate's channel and carries `display: none !important`, so
   *  leaving the authored class on would answer "none" for every case and the CSS
   *  gate — the subject here — would go unmeasured. The resize is asserted, or a
   *  `page.viewport` that stopped moving the frame would make every case below
   *  report about the project's own size while still naming a phone. */
  async function displayAt(width: number, height: number): Promise<string> {
    await page.viewport(width, height);
    expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
      width,
      height,
    ]);
    const btn = mountButton();
    btn.classList.remove("hidden");
    return getComputedStyle(btn).display;
  }

  it("hides the toggle on a narrow, tall viewport — a phone in portrait", async () => {
    expect(await displayAt(360, 800)).toBe("none");
  });

  it("hides the toggle on a wide, SHORT viewport — the same phone rotated", async () => {
    // The shape that used to slip the gate: past 48rem wide, so the narrow arm
    // stops matching, on a device where every hit target should stay at 44px. One
    // tap here pinned `fine`, and portrait then had no control to undo it with.
    expect(await displayAt(900, 400)).toBe("none");
  });

  it("offers the toggle on a viewport that is neither narrow nor short", async () => {
    // A tablet in landscape clears both arms, which is what measuring the SHORT
    // edge buys over measuring the width: 1024x768 is wide and tall, 900x400 is
    // wide and short, and only the first is a device with a pointer to choose.
    expect(await displayAt(1024, 768)).not.toBe("none");
  });
});
