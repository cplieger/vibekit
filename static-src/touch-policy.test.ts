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

describe("pinch-zoom stays disabled", () => {
  it("keeps `touch-action: pan-x pan-y` on the body", () => {
    // USER RULING, stated twice: pinch to zoom is off on purpose. This is an app
    // shell with its own scroll containers, a docked composer and a terminal, and
    // a pinch that scales the whole layout leaves every one of them mispositioned
    // with no way back except a reload.
    //
    // It is pinned because it is exactly what a well-meaning accessibility sweep
    // deletes: WCAG 1.4.4 wants text resizable to 200%, and this LOOKS like the
    // rule that prevents it. It is not — the app honours the OS text size and its
    // own font tokens are rem-based, so text scales without the layout gesture.
    // Scoped to the `reset` layer, which is where 02-reset.css puts its element
    // defaults — a top-level lookup finds nothing.
    const reset = loadCSS("02-reset.css");
    const body = ruleContaining(reset, "body", "reset");
    expect(body.body).toMatch(/touch-action:\s*pan-x pan-y/u);
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
    expect(bar.body, "the visual bar must stay thin").toMatch(/height:\s*0\.1875rem/u);
  });

  it("expands the target asymmetrically, away from the header's buttons", () => {
    // Downward is the header's own 44px buttons, and every pixel the handle takes
    // there resizes the panel when the reader meant to press a button. Upward is
    // the transcript's dead space. Derived from --hit-floor so the target follows
    // the pointer tier with no second declaration.
    const shell = loadCSS("21-shell-panel.css");
    const expander = ruleContaining(shell, ".shell-resize::before", "top");
    expect(expander.body).toContain("var(--hit-floor)");
    expect(expander.body, "only a hair may reach into the header").toMatch(/0\s+-0\.25rem/u);
  });
});

describe("row actions a finger cannot hover are shown on a coarse pointer", () => {
  // Both were `opacity: 0` revealed by `:hover` or by keyboard focus, and neither
  // is reachable by touch: there is no hover, and focusing the control means
  // tapping something invisible until it is tapped. So on a tablet these controls
  // did not exist — including Discard, which is destructive.
  it.each([
    ["19-files.css", ".fb-add-to-chat"],
    ["22-git-multirepo.css", ".git-file-actions"],
  ])("reveals %s's %s", (file, selector) => {
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
