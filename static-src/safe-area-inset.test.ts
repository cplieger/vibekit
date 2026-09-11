// HOW THE APP SPENDS `env(safe-area-inset-*)`, measured.
//
// Chromium reports every one of those as 0, so neither claim below is observable
// against the shipped stylesheet: the app looks correct here and wrong on a phone.
// The instrument is the assembled bundle with the iPhone values SUBSTITUTED (top 47,
// bottom 34), which is the only way to measure this class at all
// (`vibekit-ui.md`: "Chromium reports every `env(safe-area-inset-*)` as 0 … the
// final look needs a real device"). Each case is paired with the UNSUBSTITUTED
// sheet as its control, so a rule that stopped reading the inset entirely fails
// rather than passing for the wrong reason.
//
// Two rules, one subject:
//
//   THE INSET IS A CLEARANCE FOR THE NEAREST CONTROL. The composer's last pill sits
//   exactly on Apple's 34pt boundary, and the band is the CARD's own material
//   rather than empty page below it (Apple's bars fill it; ours used to leave 29px
//   of page there, reported as a safety area below the app looking way too large).
//
//   AND IT IS PAID ONCE. `.settings-header` used to add the TOP inset a second
//   time under a title bar that had already paid it, which is the gap reported
//   above the multi-select menu on the settings, docs and git pages.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

const INSET_TOP = 47;
const INSET_BOTTOM = 34;
/** A notched iPhone in LANDSCAPE, where the notch inset lands on an inline edge
 *  rather than on the top. This is the orientation the inline rules exist for, and
 *  the one where the `(width <= 48rem)` phone block stops matching (844px on an
 *  iPhone 15), so the base `.bottom-bar` rule governs alone. */
const INSET_SIDE = 59;

let style: HTMLStyleElement;
let frame: HTMLIFrameElement;
let doc: Document;
let sheet: HTMLStyleElement;
/** A second frame in landscape, for the two rules whose behaviour depends on
 *  whether `50-mobile.css`'s `(width <= 48rem)` arm matches. */
let wide: HTMLIFrameElement;
let wideDoc: Document;
let wideSheet: HTMLStyleElement;

/** The shipped bundle with the device's values substituted for its `env()` reads,
 *  or unchanged when `on` is false. `side` is the inline inset: it defaults to 0 so
 *  every existing case keeps neutralizing left/right exactly as it did. */
function substituted(on: boolean, side: number): string {
  const css = style.textContent ?? "";
  return on
    ? css
        .replace(/env\(\s*safe-area-inset-top\s*(?:,[^)]*)?\)/g, `${INSET_TOP}px`)
        .replace(/env\(\s*safe-area-inset-bottom\s*(?:,[^)]*)?\)/g, `${INSET_BOTTOM}px`)
        .replace(/env\(\s*safe-area-inset-(?:left|right)\s*(?:,[^)]*)?\)/g, `${side}px`)
    : css;
}

/** Swap the sheet in the phone frame for one with the iPhone values substituted,
 *  or (`false`) for the shipped one, where every inset resolves to 0. */
function withInsets(on: boolean, side = 0): void {
  sheet.textContent = substituted(on, side);
}

/** The same swap in the landscape frame. */
function withWideInsets(on: boolean, side = 0): void {
  wideSheet.textContent = substituted(on, side);
}

/** The composer as `static/index.html` authors it, down to the one pill the
 *  measurement is about. `#prompt-form` is `#chat-area`'s last flex child, so in
 *  production its block-end border edge IS the viewport's bottom — which is what
 *  makes a gap measured against this element a gap to the screen edge. */
function mountComposer(): { form: HTMLElement; box: HTMLElement; pill: HTMLElement } {
  const form = doc.createElement("form");
  form.id = "prompt-form";
  form.className = "bottom-bar";

  const box = doc.createElement("div");
  box.className = "prompt-box";
  const ta = doc.createElement("textarea");
  ta.id = "prompt-input";
  const pills = doc.createElement("div");
  pills.className = "prompt-pills";
  const slot = doc.createElement("span");
  slot.className = "pill-slot";
  const pill = doc.createElement("button");
  pill.type = "button";
  pill.className = "send-btn";
  const glyph = doc.createElementNS("http://www.w3.org/2000/svg", "svg");
  glyph.setAttribute("class", "ic-ui");
  pill.appendChild(glyph);
  slot.appendChild(pill);
  pills.appendChild(slot);
  box.append(ta, pills);
  form.appendChild(box);
  doc.body.replaceChildren(form);
  return { form, box, pill };
}

/** The settings shell as all three tabbed pages author it: a title bar above, then
 *  the scroller holding the sticky header that carries the tab bar. */
function mountTabbedPage(): { bar: HTMLElement; header: HTMLElement; titlebar: HTMLElement } {
  const area = doc.createElement("main");
  area.id = "chat-area";
  const titlebar = doc.createElement("div");
  titlebar.className = "chat-toolbar";
  const h1 = doc.createElement("h1");
  h1.className = "titlebar-heading";
  h1.textContent = "Settings";
  titlebar.appendChild(h1);

  const view = doc.createElement("div");
  view.id = "settings-view";
  view.dataset["tabView"] = "";
  const shell = doc.createElement("div");
  shell.className = "settings-shell";
  const header = doc.createElement("header");
  header.className = "settings-header";
  const bar = doc.createElement("nav");
  bar.id = "settings-tab-bar";
  bar.className = "settings-tab-bar";
  bar.setAttribute("role", "tablist");
  for (const label of ["General", "Tools", "Permissions"]) {
    const tab = doc.createElement("button");
    tab.type = "button";
    tab.className = "settings-tab";
    tab.setAttribute("role", "tab");
    tab.textContent = label;
    bar.appendChild(tab);
  }
  header.appendChild(bar);
  shell.appendChild(header);
  view.appendChild(shell);
  area.append(titlebar, view);
  doc.body.replaceChildren(area);
  return { bar, header, titlebar };
}

beforeAll(() => {
  style = mountAppCSS();
  frame = document.createElement("iframe");
  frame.width = "390";
  frame.height = "844";
  document.body.appendChild(frame);
  const inner = frame.contentDocument;
  if (inner === null) {
    throw new Error("iframe has no contentDocument");
  }
  doc = inner;
  doc.documentElement.dataset["pointer"] = "coarse";
  sheet = doc.createElement("style");
  doc.head.appendChild(sheet);

  wide = document.createElement("iframe");
  wide.width = "844";
  wide.height = "390";
  document.body.appendChild(wide);
  const innerWide = wide.contentDocument;
  if (innerWide === null) {
    throw new Error("landscape iframe has no contentDocument");
  }
  wideDoc = innerWide;
  wideDoc.documentElement.dataset["pointer"] = "coarse";
  wideSheet = wideDoc.createElement("style");
  wideDoc.head.appendChild(wideSheet);
});

afterAll(() => {
  frame.remove();
  wide.remove();
  style.remove();
});

describe("the composer's bottom clearance", () => {
  it("lands the last pill on Apple's 34pt boundary", () => {
    withInsets(true);
    const { form, pill } = mountComposer();
    const gap = form.getBoundingClientRect().bottom - pill.getBoundingClientRect().bottom;
    expect(gap, `the last control sits ${gap}px above the bar's block-end edge`).toBeCloseTo(
      INSET_BOTTOM,
      0,
    );
  });

  it("spends that band as the CARD's material, not as page below it", () => {
    // The reported half. Apple's own bottom bars fill the inset with the bar's
    // material and inset only their content; this used to charge the whole 34px to
    // the FORM's padding, leaving 29px of page under a floating card.
    withInsets(true);
    const { form, box } = mountComposer();
    const page = form.getBoundingClientRect().bottom - box.getBoundingClientRect().bottom;
    const house = parseFloat(getComputedStyle(doc.documentElement).getPropertyValue("--sp-3")) * 16;
    expect(house).toBeCloseTo(12, 0);
    expect(page, `${page}px of page below the card`).toBeCloseTo(house, 0);
  });

  it("leaves an inset-less device exactly as it was", () => {
    // The control, and it is what stops the two rules above passing for a
    // stylesheet that reads the inset nowhere: with `env()` at 0 the pill row keeps
    // its uniform `--pill-inset` and the bar keeps the house gap, so the whole
    // mechanism is invisible.
    withInsets(false);
    const { form, box, pill } = mountComposer();
    const row = pill.closest(".prompt-pills") as HTMLElement;
    const inset = parseFloat(getComputedStyle(row).paddingBlockEnd);
    // 4 -> 6 with amendment §B (2026-09-10): the row's inset now pays for its
    // controls' target expander, so it is `--composer-pill-pad` rather than
    // `--pill-inset`. Read off the row rather than restated, so a retune moves this
    // with it; the floor's own value is what the `max()` in that term resolves to on
    // this frame, which is coarse through the no-JS width fallback.
    // Compared against the row's own TOP inset, which reads the same term and no
    // `env()` at all: with the device reporting nothing, the two edges agree, which
    // is what "uniform" means here. A custom property's computed value is its token
    // stream (`max(…)`), so it cannot be read as a length.
    const top = parseFloat(getComputedStyle(row).paddingBlockStart);
    expect(top, "the row's own inset term, resolved").toBeCloseTo(6, 0);
    expect(inset).toBeCloseTo(top, 0);
    const page = form.getBoundingClientRect().bottom - box.getBoundingClientRect().bottom;
    expect(page).toBeCloseTo(12, 0);
  });
});

describe("the tabbed pages' sticky header", () => {
  it("adds no second top inset under a title bar that already paid it", () => {
    withInsets(true);
    const { bar, header, titlebar } = mountTabbedPage();
    // The premise: the title bar IS paying it, so a second payment here is a
    // double charge rather than the only one.
    expect(parseFloat(getComputedStyle(titlebar).paddingBlockStart)).toBeCloseTo(INSET_TOP, 0);
    const pad = parseFloat(getComputedStyle(header).paddingBlockStart);
    expect(pad, `the header reserves ${pad}px above its tab bar`).toBeLessThan(INSET_TOP);
    // And the visible consequence: the gap from the title bar's bottom to the tab
    // bar's top. It measured 68px with the second payment, 21px without it.
    const gap = bar.getBoundingClientRect().top - titlebar.getBoundingClientRect().bottom;
    expect(gap, `${gap}px between the title bar and the tab bar`).toBeLessThan(INSET_TOP);
  });

  it("reserves the same room whether the device has a top inset or not", () => {
    // The sharpest form of the rule: this header's own padding is not a function of
    // the inset at all any more, so the two sheets have to agree about it.
    withInsets(true);
    const on = getComputedStyle(mountTabbedPage().header).paddingBlockStart;
    withInsets(false);
    const off = getComputedStyle(mountTabbedPage().header).paddingBlockStart;
    expect(on).toBe(off);
  });
});

// ---------------------------------------------------------------------------
// THE INLINE EDGES (item 13). Same instrument, same pairing rule, one more axis:
// four surfaces reach a screen edge sideways, and in LANDSCAPE that is where a
// notch inset actually lands. Chromium reports 0 for every one of these, so
// "0, so unused" is not a finding — each case measures the SUBSTITUTED sheet and
// is paired with the unsubstituted one as its control, or a rule that stopped
// reading the inset would pass by resolving to the house value it also has to keep.
//
// The house value is what makes `max()` the right shape: the inset wins only where
// the device reports one, so a desktop's measure does not move.
// ---------------------------------------------------------------------------

/** `padding-inline` as the engine resolved it, from the element's own view. */
function inlinePadding(el: Element): { start: number; end: number } {
  const view = el.ownerDocument.defaultView;
  if (view === null) {
    throw new Error("element has no view");
  }
  const cs = view.getComputedStyle(el);
  return { start: parseFloat(cs.paddingInlineStart), end: parseFloat(cs.paddingInlineEnd) };
}

/** Mount one element into a frame and return it. */
function mountBare(d: Document, html: string, selector: string): Element {
  d.body.innerHTML = html;
  const el = d.body.querySelector(selector);
  if (el === null) {
    throw new Error(`fixture has no ${selector}`);
  }
  return el;
}

/** The four edge surfaces, with the house value each keeps where there is no
 *  inset. `#sidebar` is the one with NO house value — it declared no inline
 *  padding at all before this, which is why it takes the bare `env()` rather than
 *  the `max()` the other three use. */
const EDGE_SURFACES = {
  "#sidebar, a full-width drawer on a phone": {
    html: `<nav id="sidebar"></nav>`,
    selector: "[id='sidebar']",
    house: 0,
  },
  ".chat-toolbar, which holds the icon-button row": {
    html: `<div class="chat-toolbar"></div>`,
    selector: ".chat-toolbar",
    house: 12,
  },
  "#messages-wrap, keeping its scrollbar arithmetic": {
    html: `<div id="messages-wrap"></div>`,
    selector: "[id='messages-wrap']",
    house: 16,
  },
  ".bottom-bar under the phone rule": {
    html: `<div class="bottom-bar"></div>`,
    selector: ".bottom-bar",
    house: 16,
  },
} as const;

describe("the inline safe-area insets", () => {
  it.each(Object.entries(EDGE_SURFACES))("%s clears a side inset", (_name, surface) => {
    withInsets(true, INSET_SIDE);
    const el = mountBare(doc, surface.html, surface.selector);
    const pad = inlinePadding(el);
    expect(pad.start, `leading edge: ${pad.start}px against a ${INSET_SIDE}px inset`).toBeCloseTo(
      INSET_SIDE,
      0,
    );
    expect(pad.end, `trailing edge: ${pad.end}px against a ${INSET_SIDE}px inset`).toBeCloseTo(
      INSET_SIDE,
      0,
    );
  });

  it.each(Object.entries(EDGE_SURFACES))(
    "%s keeps its house value with no inset",
    (_name, surface) => {
      // The control. Without it every case above passes for a sheet that reads no
      // inset at all, since 59px is also a value a hand-written literal could carry.
      withInsets(false);
      const el = mountBare(doc, surface.html, surface.selector);
      const pad = inlinePadding(el);
      expect(pad.start).toBeCloseTo(surface.house, 0);
      expect(pad.end).toBeCloseTo(surface.house, 0);
    },
  );

  it("keeps #messages-wrap's scrollbar subtraction and its 0 clamp", () => {
    // The one surface whose trailing edge is not a plain `max(house, inset)`: it
    // gives the scrollbar's width back out of its own padding, clamped at 0px, so
    // the inset joins as a THIRD term rather than replacing that arithmetic. With
    // the gutter measured at 20px the house term goes NEGATIVE and the clamp is
    // what answers, and the inset still outranks both.
    withInsets(true, INSET_SIDE);
    const el = mountBare(
      doc,
      `<div id="messages-wrap"></div>`,
      "[id='messages-wrap']",
    ) as HTMLElement;
    el.style.setProperty("--scrollbar-w", "20px");
    expect(inlinePadding(el).end).toBeCloseTo(INSET_SIDE, 0);

    withInsets(false);
    const bare = mountBare(
      doc,
      `<div id="messages-wrap"></div>`,
      "[id='messages-wrap']",
    ) as HTMLElement;
    bare.style.setProperty("--scrollbar-w", "20px");
    expect(inlinePadding(bare).end, "clamped at 0, not -4px").toBeCloseTo(0, 0);
  });

  it("folds the inset into the BASE .bottom-bar rule, which is what governs landscape", () => {
    // The rule that matters most, and the reason the fold is written twice: at
    // 844px the `(width <= 48rem)` phone block stops matching, so this base rule is
    // the only one in force — and landscape is exactly the orientation where the
    // notch inset lands on an inline edge. The block-axis padding identifies WHICH
    // rule answered: --sp-2 here against the phone rule's --sp-3.
    withWideInsets(true, INSET_SIDE);
    const el = mountBare(wideDoc, `<div class="bottom-bar"></div>`, ".bottom-bar");
    const view = wideDoc.defaultView;
    if (view === null) {
      throw new Error("landscape frame has no view");
    }
    expect(
      parseFloat(view.getComputedStyle(el).paddingBlockStart),
      "the base rule is the one in force at this width",
    ).toBeCloseTo(8, 0);

    const pad = inlinePadding(el);
    expect(pad.start).toBeCloseTo(INSET_SIDE, 0);
    expect(pad.end).toBeCloseTo(INSET_SIDE, 0);
  });

  it("leaves the base .bottom-bar's house measure alone with no inset", () => {
    withWideInsets(false);
    const el = mountBare(wideDoc, `<div class="bottom-bar"></div>`, ".bottom-bar");
    const pad = inlinePadding(el);
    expect(pad.start).toBeCloseTo(16, 0);
    expect(pad.end).toBeCloseTo(16, 0);
  });

  it("is the PHONE rule that answers at phone width, so both rules need the fold", () => {
    // The pair to the case above, and together they are why the same fold is
    // written in two files: this rule replaces the whole `padding` shorthand, so a
    // side inset restored only in the base rule would be discarded here.
    withInsets(true, INSET_SIDE);
    const el = mountBare(doc, `<div class="bottom-bar"></div>`, ".bottom-bar");
    const view = doc.defaultView;
    if (view === null) {
      throw new Error("phone frame has no view");
    }
    expect(
      parseFloat(view.getComputedStyle(el).paddingBlockStart),
      "the phone rule is the one in force at this width",
    ).toBeCloseTo(12, 0);
    expect(inlinePadding(el).start).toBeCloseTo(INSET_SIDE, 0);
  });
});
