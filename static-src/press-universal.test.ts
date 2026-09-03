import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";

// ---------------------------------------------------------------------------
// The press is ONE rule for the whole app, and this file exists because the
// obvious mechanism for it does not work.
//
// It replaced a hand-kept `:where()` list of 13 class names — the third such
// allow-list on this branch, after the hit floor's 25 and the focus ring's, and
// like both of those a control missing from the list simply had no press. 14
// interactive families were missing.
// ---------------------------------------------------------------------------

const host = document.createElement("div");
host.style.cssText = "position:fixed;top:-9999px;left:0;";
document.body.appendChild(host);

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
  host.remove();
});

afterEach(() => {
  host.replaceChildren();
});

const PRESS_SELECTOR = ':where(button, summary, [role="button"]):active';

/** The press cannot be read by forcing `:active` from script — nothing in the DOM
 *  API sets it — so these assert against the RULE, and the live cross-shape
 *  verification is recorded in the commit that introduced it.
 *
 *  `ruleContaining` cannot find this rule: it splits a selector list on commas to
 *  match one member, and a `:where()` prelude has commas INSIDE it, so the
 *  selector shatters into three fragments that match nothing. Hence a local
 *  reader that takes the whole prelude literally. */
function pressRule(): { selector: string; body: string } {
  const css = loadCSS("03-base.css").replace(/\/\*[\s\S]*?\*\//g, " ");
  const at = css.indexOf(PRESS_SELECTOR);
  expect(at, `${PRESS_SELECTOR} must exist exactly once`).toBeGreaterThan(-1);
  expect(css.indexOf(PRESS_SELECTOR, at + 1), "declared more than once").toBe(-1);
  const open = css.indexOf("{", at);
  const close = css.indexOf("}", open);
  return { selector: PRESS_SELECTOR, body: css.slice(open + 1, close) };
}

describe("the press is an inset shadow, not a background layer", () => {
  it("floods the box with --c-press", () => {
    const press = pressRule();
    expect(press.body).toMatch(/box-shadow:\s*inset 0 0 0 100vmax var\(--c-press\)/u);
  });

  it("does not use background-image, which a shorthand would reset", () => {
    // `background:` is a SHORTHAND that resets `background-image` to none, 395
    // rules in this tree use it against 23 using `background-color`, and a class
    // rule beats this rule's zero specificity. Measured live by forcing :active
    // over eight shapes: the layer form reached `summary` and `[role="button"]`
    // and NOT one single button class. 01-tokens.css still recommends the layer
    // for a site that opts in, which is a different claim.
    const press = pressRule();
    expect(press.body).not.toContain("background-image");
  });

  it("keeps the selector at zero specificity, so a tuned press still wins", () => {
    const press = pressRule();
    expect(press.selector).toContain(":where(");
  });

  it("carries no scale transform anywhere in the rule", () => {
    // A scale cannot be the universal press: 4% of a 30px icon-only pill is
    // ~1.2px of travel and does not read, while 4% of a full-width menu row reads
    // as the card wobbling. A rule whose correctness depends on the width of what
    // it hits is not a rule.
    const press = pressRule();
    expect(press.body).not.toContain("scale(");
  });
});

describe("the two element kinds that must be excluded, are", () => {
  // Both carry a real background-image of their own — the checkbox's glyph
  // (02-reset.css) and the select's chevron (18-pages.css) — and both have a
  // native press. A bare `a[href]` is out for a different reason: an inline link
  // in prose is not a control, and a wash across a word is not a press.
  it.each(["input", "select", "a"])("does not name %s in the press selector", (tag) => {
    const press = pressRule();
    expect(press.selector).not.toContain(tag + ",");
    expect(press.selector).not.toContain(tag + ")");
  });
});

describe("a selected surface opts out rather than double-stepping", () => {
  it("neutralises the wash where the selected press token applies", () => {
    // A selected surface presses one rung into ITS OWN fill
    // (`--c-selected-bg-press`). Adding the app-wide wash would put a second step
    // on a different ramp, so a pressed selected row would leave the selected
    // vocabulary. 70-selection.css owns that state, its hover and its press.
    const sel = loadCSS("70-selection.css");
    const press = ruleContaining(sel, ".tab.active:active", "top");
    expect(press.body).toMatch(/background:\s*var\(--c-selected-bg-press\)/u);
    expect(press.body).toMatch(/box-shadow:\s*none/u);
  });
});

describe("every button family resolves the press token", () => {
  it.each([
    "icon-btn",
    "btn",
    "btn-small",
    "chat-opt-btn",
    "list-row-btn",
    "git-action-btn",
    "perm-mode",
    "history-modal-row",
  ])("%s can be flooded, because --c-press resolves on it", (cls) => {
    const b = document.createElement("button");
    b.className = cls;
    host.appendChild(b);
    const v = getComputedStyle(b).getPropertyValue("--c-press").trim();
    expect(v, "--c-press must resolve on every press target").not.toBe("");
  });
});
