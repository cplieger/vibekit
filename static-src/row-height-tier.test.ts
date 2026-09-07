import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

// ---------------------------------------------------------------------------
// A repeated list row takes its height from the TIER, not from its font.
//
// Ten row families declared no height at all, so each resolved to its line box
// plus padding — measured 23px to 36px, and identical on a coarse pointer and a
// fine one, while `.tab` beside them correctly went 36px to 44px. A finger got
// the same 23px git-changes row as a mouse.
//
// `--ctl-h-sm` is 24px fine / 36px coarse, which is why it is the right rung
// here: as a FLOOR it moves desktop by at most 1px (only the git row was under
// 24) while giving every list row a real target on touch.
// ---------------------------------------------------------------------------

/** Repeated rows in a list. Each is floored. */
const LIST_ROWS = [
  ["list-row", ["list-row-title", "list-row-name"]],
  ["docs-row", ["docs-row-name"]],
  ["popup-item", []],
  ["mcp-row", ["mcp-row-name"]],
  ["git-file-row", ["git-file-path"]],
  ["history-table-row", ["list-row-title"]],
  ["pill-model-item", []],
  ["pill-role-item", []],
  ["forge-account-repo-row", []],
  ["turn-file-row", []],
] as const;

/** Form WRAPPERS, deliberately NOT floored: each holds a control that carries
 *  its own tokenised height, so a floor here would be a second answer to one
 *  question. `.section-option` wraps a checkbox whose own hit floor is already
 *  44px on touch; `.sched-row` wraps a select at the dense tier. Both are listed
 *  so the distinction is testable rather than remembered. */
const FORM_WRAPPERS = ["section-option", "sched-row"] as const;

const host = document.createElement("div");
host.style.cssText = "position:fixed;top:-9999px;left:0;inline-size:420px;";
document.body.appendChild(host);

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
  host.remove();
  document.documentElement.removeAttribute("data-pointer");
});

afterEach(() => {
  host.replaceChildren();
});

function mountRow(cls: string, kids: readonly string[]): HTMLElement {
  const row = document.createElement("div");
  row.className = cls;
  let mount: HTMLElement = row;
  for (const k of kids) {
    const c = document.createElement("span");
    c.className = k;
    mount.appendChild(c);
    mount = c;
  }
  mount.textContent = "A representative row label";
  host.appendChild(row);
  return row;
}

describe("every repeated list row answers the pointer tier", () => {
  it.each(LIST_ROWS.map(([cls, kids]) => [cls, kids] as const))(
    ".%s grows on a coarse pointer",
    (cls, kids) => {
      const row = mountRow(cls, kids);

      document.documentElement.dataset["pointer"] = "fine";
      const fine = row.getBoundingClientRect().height;
      document.documentElement.dataset["pointer"] = "coarse";
      const coarse = row.getBoundingClientRect().height;

      // 36px is --ctl-h-sm on coarse. The floor is what makes this true; without
      // it every one of these measured its font's line box on both tiers.
      expect(coarse, `${cls} must reach the coarse rung`).toBeGreaterThanOrEqual(36);
      expect(coarse, `${cls} must actually RESPOND to the tier`).toBeGreaterThan(fine - 0.5);
      // And the fine tier keeps its own compact height: the rung is 24px, so a row
      // that already renders taller is untouched.
      expect(
        fine,
        `${cls} must clear WCAG 2.5.8's 24px on a fine pointer too`,
      ).toBeGreaterThanOrEqual(24);
    },
  );

  it("reads the height from a token, never a literal", () => {
    // `.turn-file-row` spelled `min-height: 1.5rem`. Both are 24px on a fine
    // pointer, so nothing moved there — but a literal cannot flip, so that row
    // stayed 24px under a finger while every control around it grew.
    const row = mountRow("turn-file-row", []);
    document.documentElement.dataset["pointer"] = "coarse";
    expect(row.getBoundingClientRect().height).toBeGreaterThanOrEqual(36);
  });
});

describe("a form wrapper is not floored, because its control already is", () => {
  it.each(FORM_WRAPPERS)(".%s declares no row height of its own", (cls) => {
    const row = mountRow(cls, []);
    const declared = getComputedStyle(row).minBlockSize;
    expect(
      declared === "0px" || declared === "auto",
      `${cls} wraps a control that carries its own tokenised height; a floor here ` +
        `would be a second answer to one question`,
    ).toBe(true);
  });
});
