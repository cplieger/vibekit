import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";

// ---------------------------------------------------------------------------
// Touch policy: the small number of decisions about how this app behaves under
// a finger that are easy to "fix" back into a defect.
//
// Each case here exists because the correct value LOOKS wrong to a passing
// reader — a disabled zoom gesture reads as an accessibility oversight, a
// control opted out of the hit floor reads as a control nobody floored, and a
// permanently visible row action reads as clutter. Every one is deliberate, and
// the reason is on the assertion.
// ---------------------------------------------------------------------------

describe("pinch-zoom is enabled", () => {
  it("keeps `pinch-zoom` in the body's touch-action list", () => {
    // THE PRIOR RULING IS OVERTURNED, deliberately, and this case is its record.
    // It used to assert `pan-x pan-y` and read: "USER RULING, stated twice: pinch
    // to zoom is off on purpose. This is an app shell with its own scroll
    // containers, a docked composer and a terminal, and a pinch that scales the
    // whole layout leaves every one of them mispositioned with no way back except
    // a reload." It also said the rule was pinned because it is what a
    // well-meaning accessibility sweep deletes.
    //
    // What changed is that the sweep happened and was RATIFIED (2026-09-10): the
    // viewport meta lost `maximum-scale=1.0, user-scalable=no` for WCAG 1.4.4, and
    // that clause is inert while this list excludes the gesture — one suppressed
    // the pinch, the other forbade it. So the two travel together: without
    // `pinch-zoom` here the meta change buys nothing at all.
    //
    // The mispositioning cost the old ruling named is real and is accepted rather
    // than answered. `pinch-zoom` does NOT reintroduce the 300ms double-tap delay
    // that `touch-action: manipulation` on `:where(button)` exists to remove; the
    // two are independent values.
    //
    // Scoped to the `reset` layer, which is where 02-reset.css puts its element
    // defaults — a top-level lookup finds nothing.
    const reset = loadCSS("02-reset.css");
    const body = ruleContaining(reset, "body", "reset");
    expect(body.body).toMatch(/touch-action:\s*pan-x pan-y pinch-zoom/u);
  });
});

describe("a control that must stay visually small opts out of the box floor", () => {
  it("gives .shell-resize min-width and min-height of 0", () => {
    // It is a `role="separator"` with a tabindex, so it matches the universal hit
    // floor (61-mcp-tools.css) and its BOX was grown to --hit-floor. That is the
    // one shape the floor must never grow: the bar is absolutely positioned across
    // the panel's full width at `z-index: 1`, so a 44px box lay directly over the
    // whole header and took all four of its buttons' clicks. Measured before the
    // fix: box 44px on coarse, 24px on fine, against a visual bar of 3px.
    //
    // The floor's own comment names this control as one of two that grow their
    // TARGET instead, so the expander is the intended mechanism and this is its
    // missing half.
    const shell = loadCSS("21-shell-panel.css");
    const bar = ruleContaining(shell, ".shell-resize", "top");
    expect(bar.body).toMatch(/min-width:\s*0/u);
    expect(bar.body).toMatch(/min-height:\s*0/u);
    expect(bar.body, "the visual bar must stay thin").toMatch(/height:\s*var\(--shell-bar-h\)/u);

    const panel = ruleContaining(shell, ".shell-panel", "top");
    expect(panel.body, "and the hairline is 3px").toMatch(/--shell-bar-h:\s*0\.1875rem/u);
  });

  it("expands the target DOWNWARD, and nothing else pays for it", () => {
    // THE PREMISE OF THIS CASE HAS REVERSED TWICE, so both are on the record.
    //
    // (1) It first asserted `/0\s+-0\.25rem/` — an expander reaching UP and only
    // 4px down, on the reasoning that downward is the header's own buttons. Wrong,
    // because `.shell-panel` declares `overflow: hidden`, so every pixel reaching
    // upward was CLIPPED: hit-tested, 6px of real target against the 24/44 the
    // declaration claimed. (Up is also the wrong direction unclipped — the
    // panel's previous sibling is the composer, whose Send button ends 13px above
    // the seam.)
    //
    // (2) The reversal to DOWN then made `.shell-header` pay the reach in its top
    // padding AND its height, to keep its buttons out of the target. Measured cost
    // at the fine tier: a 56px header around a 32px content band, so 43% of the bar
    // was empty, and 89px around a 44px band on a coarse pointer. The buttons
    // out-stack the expander now (the case below), so the header pays nothing and
    // this case asserts the ABSENCE of that arithmetic.
    //
    // The source-level companion to the elementFromPoint cases at the end of this
    // file. Those measure the target and cannot say which selectors carry it; this
    // one cannot say the arithmetic works.
    const shell = loadCSS("21-shell-panel.css");

    const expander = ruleContaining(shell, ".shell-resize::before", "top");
    expect(expander.body, "reaching DOWN, by the floor minus the bar it paints").toMatch(
      /inset:\s*0 0 calc\(var\(--shell-bar-h\) - var\(--hit-floor\)\)/u,
    );

    const header = ruleContaining(shell, ".shell-header", "top");
    expect(header.body, "the header is one content row").toMatch(/height:\s*2rem/u);
    expect(header.body, "with no block padding to hold a target").toMatch(
      /padding-inline:\s*var\(--sp-3\)/u,
    );
    expect(
      header.body.includes("--hit-floor") || header.body.includes("--shell-bar-h"),
      "the header restates no part of the target's arithmetic",
    ).toBe(false);

    // The half that makes all of the above safe, declared on the control that has
    // to win: the bar carries `z-index: 1`, so a button under the expander needs a
    // higher one or the seam takes its clicks.
    const button = ruleContaining(shell, ".shell-header-btn", "top");
    expect(button.body, "the buttons out-stack the bar").toMatch(/z-index:\s*2/u);
    expect(button.body, "and a z-index needs a position to apply").toMatch(/position:\s*relative/u);
  });
});

describe("row actions a finger cannot hover are shown on a coarse pointer", () => {
  // It is `opacity: 0` revealed by `:hover` or by keyboard focus, and neither is
  // reachable by touch: there is no hover, and focusing the control means tapping
  // something invisible until it is tapped. So on a tablet these controls did not
  // exist — including Discard, which is destructive.
  //
  // The table had a second row, `19-files.css`'s `.fb-add-to-chat`. It went with
  // that block in 2026-09: no TypeScript ever emitted the class, so the reveal it
  // asserted was styling a control the app does not render, and the case could not
  // fail for any reason a reader would care about.
  it.each([["22-git-multirepo.css", ".git-file-actions"]])("reveals %s's %s", (file, selector) => {
    const css = loadCSS(file);
    const reveals = css
      .split("}")
      .filter((r) => r.includes(selector) && /opacity:\s*1/u.test(r))
      .map((r) => r.split("{")[0] ?? "");
    expect(
      reveals.some((sel) => sel.includes('data-pointer="coarse"')),
      `${selector} needs a reveal that is neither :hover nor :focus`,
    ).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// A native checkbox or radio PAINTS the box the hit-target floor grows, so the
// floor was inflating the mark rather than its target: a 16px control rendered
// 24x24 on a mouse and 44x44 under a finger. Apple's HIG figure and WCAG 2.5.5's
// are both minimum TARGETS, so the fix keeps the target and shrinks the box —
// which is why every case below measures BOTH numbers. Measuring only the
// painted size would pass just as well with the floor deleted.
// ---------------------------------------------------------------------------

/** The composer's `+` menu, as chat-options.ts builds it. Synthetic rather than
 *  the real card: the subject here is the stylesheet, and chat-options.test.ts
 *  owns the DOM this mirrors. */
const OPT_ROWS = [
  ["Attach a file", "Pick a workspace file or upload one (up to 10 MB)"],
  ["Set a goal", "The agent iterates toward it until it reports success"],
  ["Start a tangent", "Branch this conversation into a sub-chat that keeps its context"],
  ["Compact the context", "Summarize the history so far to free up context"],
] as const;

function optMenuHTML(): string {
  const actions = OPT_ROWS.map(
    ([name, hint]) => `<div class="chat-opt-entry"><button type="button" class="chat-opt-btn">
      <span class="chat-opt-icon"><svg width="14" height="14" viewBox="0 0 24 24"></svg></span>
      <span class="chat-opt-text"><span class="chat-opt-name">${name}</span
      ><span class="chat-opt-hint">${hint}</span></span>
    </button></div>`,
  ).join("");
  return `<span class="pill-slot" style="position:relative;display:block">
    <span class="pill-expand-content chat-options-card is-open">${actions}
      <label class="chat-opt-row"><input type="checkbox">
        <span class="chat-opt-text"><span class="chat-opt-name">Supervised mode</span
        ><span class="chat-opt-hint">Review this chat's file changes at the end of each turn</span></span>
      </label>
    </span></span>`;
}

const PERM_ROWS = {
  "a Settings > Permissions profile radio": `<div class="profile-list">
    <label class="perm-mode profile-row"><input type="radio"><span>Guarded</span>
      <p class="section-hint profile-desc">Confined reads, and it asks before anything else.</p></label>
  </div>`,
  "the Supervised-default checkbox": `<label class="perm-mode"><input type="checkbox">
    <span>Turn on Supervised mode for new conversations</span></label>`,
} as const;

const boxHost = document.createElement("div");
boxHost.style.cssText = "position:fixed;top:600px;left:40px;inline-size:420px;";

let boxStyle: HTMLStyleElement;

beforeAll(() => {
  boxStyle = mountAppCSS();
  document.body.appendChild(boxHost);
});

afterAll(() => {
  boxStyle.remove();
  boxHost.remove();
  document.documentElement.removeAttribute("data-pointer");
});

afterEach(() => {
  boxHost.replaceChildren();
});

/** `--hit-floor` in px at the tier currently set, read from the token rather than
 *  restated, so retiering moves every assertion below with it. */
function hitFloorPx(): number {
  const probe = document.createElement("div");
  probe.style.inlineSize = "var(--hit-floor)";
  boxHost.appendChild(probe);
  const px = probe.getBoundingClientRect().width;
  probe.remove();
  return px;
}

function mount(html: string): void {
  boxHost.innerHTML = html;
}

function tier(name: "fine" | "coarse"): void {
  document.documentElement.dataset["pointer"] = name;
}

/** One token's value in px at the tier currently set, read the way `hitFloorPx`
 *  reads the floor: through a real box, so the whole cascade decides it. */
function tokenPx(name: string): number {
  const probe = document.createElement("div");
  probe.style.inlineSize = `var(${name})`;
  boxHost.appendChild(probe);
  const px = probe.getBoundingClientRect().width;
  probe.remove();
  return px;
}

describe("the third tier state moves the hit floor and nothing else", () => {
  // The other half of item 10: `pointer-tier.test.ts` pins WHEN `data-touched` is
  // written, and this pins what it BUYS. A hybrid device driven by its mouse keeps
  // the dense layout — so every control height is the fine tier's — while a finger
  // still lands a 44px target, grown through the floor's zero-specificity `min-*`
  // rules rather than by any box growing.
  afterEach(() => {
    document.documentElement.removeAttribute("data-touched");
  });

  it("takes the coarse floor on a fine pointer that has been touched", () => {
    tier("fine");
    expect(hitFloorPx(), "the control, without the flag").toBe(24);

    document.documentElement.setAttribute("data-touched", "");
    expect(hitFloorPx()).toBe(44);
  });

  it("leaves every control-height token at the fine tier's value", () => {
    // "That token only" is the whole ruling, and it is what keeps a painted box or
    // a glyph from moving: --ctl-h and its two siblings decide heights, --icon-ui
    // decides glyph size, and none of them may follow a touch that has already
    // happened. Their coarse values are 2.75/2.5/2.25rem and 1.25rem.
    tier("fine");
    document.documentElement.setAttribute("data-touched", "");
    expect(tokenPx("--ctl-h")).toBe(36);
    expect(tokenPx("--ctl-h-dense")).toBe(32);
    expect(tokenPx("--ctl-h-sm")).toBe(24);
    expect(tokenPx("--icon-ui")).toBe(16);
  });
});

describe("a native box control paints its own size and grows only its target", () => {
  it.each(Object.entries(PERM_ROWS))("%s", (_name, html) => {
    mount(html);
    const input = boxHost.querySelector("input");
    if (input === null) {
      throw new Error("fixture has no input");
    }

    for (const t of ["fine", "coarse"] as const) {
      tier(t);
      const painted = input.getBoundingClientRect();
      // 1rem: the checkbox's own size (02-reset.css) and the radio's, declared
      // beside the floor because a UA-appearance radio has none of its own.
      expect(painted.width, `painted width on ${t}`).toBe(16);
      expect(painted.height, `painted height on ${t}`).toBe(16);

      // And the target the floor exists for is still exactly the tier's floor,
      // carried by the `::after` expander instead of by the box.
      const floor = hitFloorPx();
      const target = getComputedStyle(input, "::after");
      expect(Number.parseFloat(target.width), `target width on ${t}`).toBeCloseTo(floor, 1);
      expect(Number.parseFloat(target.height), `target height on ${t}`).toBeCloseTo(floor, 1);
    }
  });

  it("the + menu's supervised switch paints at 1rem on both tiers", () => {
    mount(optMenuHTML());
    const input = boxHost.querySelector(".chat-opt-row > input");
    if (input === null) {
      throw new Error("fixture has no switch");
    }
    for (const t of ["fine", "coarse"] as const) {
      tier(t);
      const painted = input.getBoundingClientRect();
      expect(painted.width, `painted width on ${t}`).toBe(16);
      expect(painted.height, `painted height on ${t}`).toBe(16);
    }
  });
});

describe("the + menu's switch takes its target from its row, not an expander", () => {
  it("gives the row itself at least --hit-floor on both tiers", () => {
    // This is the assertion that keeps the carve-out honest: the switch drops the
    // expander because its <label> is already a bigger target, so a later edit
    // that shrinks the row has to fail here rather than silently leaving a 16px
    // control with nothing around it.
    mount(optMenuHTML());
    const row = boxHost.querySelector(".chat-opt-row");
    if (row === null) {
      throw new Error("fixture has no switch row");
    }
    for (const t of ["fine", "coarse"] as const) {
      tier(t);
      const floor = hitFloorPx();
      const box = row.getBoundingClientRect();
      expect(box.height, `row height on ${t}`).toBeGreaterThanOrEqual(floor);
      expect(box.width, `row width on ${t}`).toBeGreaterThanOrEqual(floor);
    }
  });

  it("leaves the bottom edge of the row above to that row", () => {
    // Why the carve-out exists, and the reason this probes the switch's own
    // COLUMN rather than the row's centre: the checkbox sits against the row's
    // top edge, so an expander centred on it overhangs upward and only over the
    // leading column. Measured 4px into the Compact row on a coarse pointer,
    // which is that row's whole bottom chrome.
    mount(optMenuHTML());
    const input = boxHost.querySelector(".chat-opt-row > input");
    const above = boxHost.querySelector(".chat-opt-entry:last-of-type .chat-opt-btn");
    if (input === null || above === null) {
      throw new Error("fixture is missing a row");
    }
    for (const t of ["fine", "coarse"] as const) {
      tier(t);
      const col = input.getBoundingClientRect();
      const edge = above.getBoundingClientRect();
      const hit = document.elementFromPoint(col.left + col.width / 2, edge.bottom - 1);
      expect(above.contains(hit), `the row above owns its bottom edge on ${t}`).toBe(true);
    }
  });
});

// ---------------------------------------------------------------------------
// The file browser's git letter is the second member of the grow-the-TARGET
// family, beside `.shell-resize` above. It IS a control (it
// opens that file's diff — `files-decoration.test.ts` drives the click) so
// `role="button"` is correct and the app-wide floor matching it is correct; what
// the floor may not do is grow the MARK, which `19-files.css` declares as a fixed
// 1rem square so the meta column does not shift as letters appear and disappear
// across polls. Left unopted-out it rendered a 44px letter under a finger, AND
// overflowed `.fb-row`'s intrinsic-size reserve by 4-24px per row — the
// geometry half of that is `files-row-metrics.test.ts`'s, and its own red check
// records that all ten of its cases stay green with the expander deleted. So
// these cases are the only thing standing between the expander and a reader
// deleting it as unused CSS.
//
// RED-CHECKED both halves separately, which is what shows they are two facts:
// delete the `::after` and the target, neighbour and source cases fail while the
// PAINT case stays green; delete `min-width: 0; min-height: 0` instead and the
// paint and source cases fail while the target and neighbour cases stay green
// (the expander is sized off the mark's own box, so it measures --hit-floor
// either way).
// ---------------------------------------------------------------------------

/** One `.fb-row` carrying a dirty FILE's letter, as `files.ts` `entryRow` orders
 *  its children: name, then the badge, then the metadata. The checkbox and icon
 *  are omitted — nothing here measures them, and the row's own height is not the
 *  subject. `role="button"` is what makes the floor match at all. */
const LETTERED_ROW = `<div class="fb-row" role="listitem">
  <span class="fb-name fb-name-link">some-entry-with-a-long-name.ts</span
  ><span class="fb-git-letter git-st-m fb-git-clickable" role="button"
    aria-label="Git status: modified">M</span
  ><span class="fb-meta">1.2 KB   ·   2026-09-01   ·   -rw-r--r--</span>
</div>`;

/** Mount that row and return its letter.
 *
 *  The `content-visibility` override is required for the neighbour case and free
 *  for the other two, so it is applied once here. `.fb-row` declares
 *  `content-visibility: auto` for the long-listing paint bound, and a SKIPPED
 *  subtree is not hit-testable: measured in Chromium 151, every
 *  `document.elementFromPoint` across the whole row answered `.fb-row` itself,
 *  the letter's own column included, while `getBoundingClientRect` and
 *  `getComputedStyle(el, "::after")` on the same children returned real values —
 *  a rect query forces the skipped layout and hit testing does not. That is the
 *  same asymmetry `files-row-metrics.test.ts` records from the other side, and it
 *  is why relevance is STATED rather than awaited: relevance is decided by the
 *  renderer, so a case that waited for a frame would be load-sensitive, and a row
 *  a finger can reach is on screen by definition. */
function mountLetteredRow(): Element {
  mount(LETTERED_ROW);
  const row = boxHost.firstElementChild;
  if (!(row instanceof HTMLElement)) {
    throw new Error("fixture has no row");
  }
  row.style.contentVisibility = "visible";
  const badge = row.querySelector(".fb-git-clickable");
  if (badge === null) {
    throw new Error("fixture has no git letter");
  }
  return badge;
}

describe("the file browser's git letter grows its target, not its mark", () => {
  it("paints the letter at 1rem on both tiers", () => {
    // The assertion that fails if anyone deletes the `min-width: 0; min-height: 0`
    // pair: the floor then sets the letter's BOX to --hit-floor, so the mark is
    // 24px on a mouse and a 44px purple square under a finger.
    const badge = mountLetteredRow();
    for (const t of ["fine", "coarse"] as const) {
      tier(t);
      const painted = badge.getBoundingClientRect();
      expect(painted.width, `painted width on ${t}`).toBe(16);
      expect(painted.height, `painted height on ${t}`).toBe(16);
    }
  });

  it("keeps the target at exactly --hit-floor on both tiers", () => {
    // Read off the positioned pseudo's USED size, the way the PERM_ROWS case above
    // reads a checkbox's expander. Derived from the token rather than restated, so
    // retiering moves this with it — and it is what separates the expander from a
    // pair of per-tier literals, which would pass one of these two tiers.
    const badge = mountLetteredRow();
    for (const t of ["fine", "coarse"] as const) {
      tier(t);
      const floor = hitFloorPx();
      const target = getComputedStyle(badge, "::after");
      expect(Number.parseFloat(target.width), `target width on ${t}`).toBeCloseTo(floor, 1);
      expect(Number.parseFloat(target.height), `target height on ${t}`).toBeCloseTo(floor, 1);
    }
  });

  it("leaves .fb-name's own trailing edge to .fb-name", () => {
    // Why the expander is ASYMMETRIC, and the assertion that pins it. The letter
    // sits between the name (which opens the FILE) and the metadata (inert), so a
    // CENTRED expander would reach --hit-floor/2 - 50% - --sp-2 past the row's gap
    // into the name's box — 0px on fine and 6px on both coarse tiers, where a tap
    // on the end of a filename opens the diff instead of the file. Growing into
    // the metadata column instead costs nothing. Measured with the shipped
    // asymmetric insets: the name answers through its own last pixel on both
    // tiers, and the letter's target begins exactly at the name's right edge.
    const badge = mountLetteredRow();
    const name = boxHost.querySelector(".fb-name");
    if (name === null) {
      throw new Error("fixture has no name");
    }
    for (const t of ["fine", "coarse"] as const) {
      tier(t);
      const box = name.getBoundingClientRect();
      const mid = box.top + box.height / 2;
      expect(
        name.contains(document.elementFromPoint(box.right - 1, mid)),
        `the name owns its trailing edge on ${t}`,
      ).toBe(true);
      // The other half of the same fact: the target really is there, one pixel
      // further on. Without it the case passes with the expander deleted.
      expect(
        badge.contains(document.elementFromPoint(box.right + 1, mid)) ||
          document.elementFromPoint(box.right + 1, mid) === badge,
        `the letter's target starts at the name's edge on ${t}`,
      ).toBe(true);
    }
  });

  it("declares the opt-out and the expander on .fb-git-clickable itself", () => {
    // Source-level companions, matching the `.shell-resize` pair above: the three
    // measurements say the target is right, and cannot say WHICH selector carries
    // it, so a reader who moved either half elsewhere would leave them green.
    const files = loadCSS("19-files.css");
    const control = ruleContaining(files, ".fb-git-clickable", "top");
    expect(control.body).toMatch(/min-width:\s*0/u);
    expect(control.body).toMatch(/min-height:\s*0/u);

    // `ruleContaining` returns a rule's nested `&` blocks as part of its body and
    // only indexes TOP-LEVEL selectors, so the expander is sliced out of the body
    // rather than looked up. A plain slice rather than a brace walk because this
    // block nests nothing — the moment it does, it wants a reader in
    // `css-rules.ts` instead of a second parser here.
    const at = control.body.indexOf("&::after {");
    expect(at, "the target is an ::after expander on this control").toBeGreaterThan(-1);
    const expander = control.body.slice(at, control.body.indexOf("}", at));
    expect(expander, "the target is derived from the tier's floor").toContain("var(--hit-floor)");
    expect(expander, "the start side takes the row's own gap").toContain("var(--sp-2)");
  });
});

describe("every + menu row's label starts on one x", () => {
  it("puts an action row's name and the switch row's name in the same column", () => {
    // The switch row carried no border where its five neighbours carry a
    // transparent 1px one, and its leading column was the checkbox where theirs
    // is a 14px glyph — so its label started 9px right of theirs on a mouse and
    // 29px right under a finger, growing with the tier because the floor was
    // sizing the checkbox.
    mount(optMenuHTML());
    const names = [...boxHost.querySelectorAll(".chat-opt-name")];
    expect(names.length, "one name per row").toBe(OPT_ROWS.length + 1);

    for (const t of ["fine", "coarse"] as const) {
      tier(t);
      const lefts = names.map((n) => n.getBoundingClientRect().left);
      const [first] = lefts;
      for (const left of lefts) {
        expect(left, `every row's label shares one inline start on ${t}`).toBeCloseTo(
          first ?? 0,
          1,
        );
      }
    }
  });
});

// ---------------------------------------------------------------------------
// THE SHELL PANEL'S RESIZE BAR, HIT-TESTED. This is the case item 18 exists for,
// and its absence is how the defect survived: everything above about
// `.shell-resize` reads DECLARATIONS, and a declaration claiming a 24/44px target
// says nothing about whether the target is REACHABLE. The old expander reached
// UPWARD out of a panel that declares `overflow: hidden`, so the whole reach was
// clipped away and the real target was 3px of bar plus the 4px it also reached
// down — measured 6px against the 24/44 the source claimed, on a control whose
// entire job is to be grabbable.
//
// The bar is `position: absolute; top: 0` inside the panel, so the panel needs to
// be ON SCREEN for `elementFromPoint` to answer at all: this host sits at the top
// of the viewport rather than reusing `boxHost` (600px down, where a 16rem panel
// would run off the bottom).
// ---------------------------------------------------------------------------

/** The shell panel as `static/index.html` authors it, down to one header button.
 *  The `shell-closed` class it SHIPS with is deliberately absent — that state is
 *  `height: 0`, so the fixture would have no geometry to measure. */
const SHELL_PANEL = `<div class="shell-panel">
  <div class="shell-resize" role="separator" tabindex="0"></div>
  <div class="shell-header">
    <span class="shell-title"><svg class="ic-ui" viewBox="0 0 24 24"></svg><span>Shell</span></span>
    <button type="button" class="shell-header-btn" aria-label="Close shell">
      <svg class="ic-inline" viewBox="0 0 24 24"></svg>
    </button>
  </div>
  <div class="shell-terminal"></div>
</div>`;

const shellHost = document.createElement("div");
shellHost.style.cssText = "position:fixed;top:0;left:40px;inline-size:420px;";

describe("the shell resize bar's real target", () => {
  beforeAll(() => {
    document.body.appendChild(shellHost);
  });

  afterAll(() => {
    shellHost.remove();
  });

  function mountPanel(): { bar: Element; button: Element } {
    shellHost.innerHTML = SHELL_PANEL;
    const bar = shellHost.querySelector(".shell-resize");
    const button = shellHost.querySelector(".shell-header-btn");
    if (bar === null || button === null) {
      throw new Error("fixture is missing an element");
    }
    return { bar, button };
  }

  /** What owns the point, as a click would find it. */
  function ownerAt(x: number, y: number): Element | null {
    return document.elementFromPoint(x, y);
  }

  it.each([
    ["fine", 24],
    ["coarse", 44],
  ] as const)("is exactly --hit-floor tall on %s (%ipx)", (t, expected) => {
    tier(t);
    const { bar } = mountPanel();
    const floor = hitFloorPx();
    expect(floor, `--hit-floor on ${t}`).toBe(expected);

    const box = bar.getBoundingClientRect();
    expect(box.height, "the painted bar stays a 3px hairline").toBeCloseTo(3, 1);

    // Sampled at the bar's centre, which is over the TITLE: that is the header's
    // inert region and the only span where the target is unobstructed, since the
    // buttons deliberately out-stack it at the row's trailing end (case ii).
    const x = box.left + box.width / 2;
    // (i) the bar owns every row from its own top edge down to the floor.
    for (let dy = 0.5; dy < floor; dy += 1) {
      expect(ownerAt(x, box.top + dy), `the bar owns y+${dy} on ${t}`).toBe(bar);
    }
    // (iii) and not one row further — which is what makes this a MEASUREMENT of
    // the reach rather than a lower bound: the painted bar plus the floor minus the
    // bar, reaching down.
    expect(ownerAt(x, box.top + floor + 0.5), `the target ends at the floor on ${t}`).not.toBe(bar);
  });

  it.each(["fine", "coarse"] as const)("leaves the header button its own whole box on %s", (t) => {
    // (ii) THE OTHER HALF OF THE REACH, and the case the current mechanism rests
    // on: the bar lies over the header at `z-index: 1`, so every pixel of a button
    // inside the reach is a pixel that resizes the panel when the reader meant to
    // press Close. On a coarse pointer the expander covers this row WHOLE — 44px of
    // target over a 44px button — so nothing geometric separates them and the only
    // thing that does is `.shell-header-btn`'s own `z-index: 2`. Deleting it fails
    // here, and so does making `.shell-header` a STACKING CONTEXT — red-checked with
    // `isolation: isolate`, a `transform`, an `opacity` below 1, and `position` plus
    // a non-auto `z-index`. A bare `position: relative` does not create one and does
    // not fail, which is why the CSS comment names the context rather than the
    // property.
    //
    // It used to be separated by geometry instead: the header carried the whole
    // reach as top padding and grew its height to match, which is what made the bar
    // 56px tall on a mouse and 89px under a finger.
    tier(t);
    const { bar, button } = mountPanel();
    const box = button.getBoundingClientRect();
    expect(box.height, `the button has a box on ${t}`).toBeGreaterThan(0);

    for (const [name, y] of [
      ["top edge", box.top + 0.5],
      ["centre", box.top + box.height / 2],
      ["bottom edge", box.bottom - 0.5],
    ] as const) {
      const hit = ownerAt(box.left + box.width / 2, y);
      expect(hit, `the resize bar must not own the button's ${name} on ${t}`).not.toBe(bar);
      expect(hit !== null && button.contains(hit), `the button owns its ${name} on ${t}`).toBe(
        true,
      );
    }
  });

  it.each([
    ["fine", 32],
    ["coarse", 45],
  ] as const)("stays one content row tall on %s (%ipx)", (t, expected) => {
    // (iv) THE READER-VISIBLE HALF, and the case this bar was reported for: it was
    // 56px around a 32px content band on a mouse and 89px around 44px under a
    // finger, because the header was buying its buttons clearance from the resize
    // target. The numbers are hardcoded rather than derived, so re-deriving the
    // header's height from the floor — which is the shape that produced the defect —
    // fails here instead of passing by construction. 45 is 44 plus the 1px border
    // the header declares, which `box-sizing: border-box` charges to its height.
    tier(t);
    const { bar } = mountPanel();
    const head = bar.parentElement?.querySelector(".shell-header");
    expect(head, "fixture has a header").not.toBeNull();
    expect(head?.getBoundingClientRect().height, `header height on ${t}`).toBeCloseTo(expected, 1);
  });
});
