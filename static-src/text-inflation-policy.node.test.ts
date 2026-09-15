// The root's text-inflation pin, guarded at the SOURCE because no engine
// available to this suite can observe the behaviour it defends.
//
// iOS Safari's default `-webkit-text-size-adjust: auto` scales each block by
// that block's own width, so a surface whose blocks differ in width renders one
// font size per block. The app's diff rows are exactly that shape — a
// `white-space: pre` line inside a horizontal scroller — and a phone rendered a
// tool card's inline diff at a different size per line. Full measurement:
// `vibekit-ui.md` "Text inflation".
//
// Chromium's own autosizing is Android-only and gated off by this app's
// `width=device-width` viewport, so a rendered assertion reads identically with
// the declaration present and absent. That is what makes the regression SILENT
// here and the source the only honest oracle.
//
// The likely regression is not a deletion but a `stylelint --fix`, which would
// rewrite the prefixed declaration away and leave the standard one — Safari
// implements only the prefixed form, so the fix would be gone with the suite
// green. `property-no-vendor-prefix` is fixable, and the synced config exempts
// this property by name (`web.md`), so nothing raises it today; the guard is
// against that exemption being narrowed or the rule being run with `--fix`
// under a config that lacks it.

import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const reset = readFileSync(join(here, "css", "02-reset.css"), "utf8");

/** The `html { … }` block of the reset layer, without its comments. */
function rootRule(): string {
  const stripped = reset.replace(/\/\*[\s\S]*?\*\//g, "");
  const m = /(^|\})\s*html\s*\{([^}]*)\}/m.exec(stripped);
  return m?.[2] ?? "";
}

describe("text inflation", () => {
  it("pins the PREFIXED property at the root, which is the only spelling Safari reads", () => {
    expect(rootRule()).toMatch(/-webkit-text-size-adjust:\s*100%/);
  });

  it("pins the standard property too, for the engines that implement it", () => {
    expect(rootRule()).toMatch(/(^|[\s;]) *text-size-adjust:\s*100%/);
  });

  it("declares it in exactly one place, so no surface can opt itself back in", () => {
    const declarations =
      reset.replace(/\/\*[\s\S]*?\*\//g, "").match(/text-size-adjust\s*:/g) ?? [];
    expect(declarations).toHaveLength(2);
  });

  it("never uses `none`, which also blocks the reader's own zoom-to-scale text", () => {
    expect(reset.replace(/\/\*[\s\S]*?\*\//g, "")).not.toMatch(/text-size-adjust:\s*none/);
  });
});
