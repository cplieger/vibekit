import { describe, it, expect } from "vitest";

import { loadCSS, ruleContaining } from "./__test-helpers__/css-rules.js";

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
