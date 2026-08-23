// The vertical centring of a text label inside a flex row, pinned as a SOURCE
// fact because the test page loads neither the app stylesheet nor its webfont.
//
// The defect: `align-items: center` centres a flex item's BOX, and a text item's
// box is not symmetric about the letterforms in it. With the baseline `ascent`
// from the box top, the box centre lands at `baseline + (ascent - descent)/2`
// while the glyph band's optical centre is at `baseline + band/2`, so the label
// renders low by ((ascent - descent)/2 - band/2)/unitsPerEm em. Measured off real
// font files: +0.120em at Noto Sans, +0.083em at Liberation Sans, +0.073em at
// DejaVu Sans, +0.164em at Segoe UI.
//
// The reason this test exists rather than a `line-height` declaration: the
// formula has NO line-height term. CSS2.1 10.8.1 distributes leading half above
// and half below, so the box centre is `baseline + (ascent - descent)/2` for
// every value including `normal`, and `line-height: 1` moves the glyphs by
// exactly zero while shrinking the box enough that the descenders overflow it.
// On a label that clips for its ellipsis, that overflow is cut. So the two things
// worth guarding are the two that are easy to get wrong again: that no numeric
// line-height reappears here claiming to be the fix, and that every trimmed label
// which clips has symmetric room for its descenders.
//
// Node environment: this reads the shipped stylesheets as text.

import { describe, it, expect } from "vitest";
import { loadCSS } from "./__test-helpers__/css-rules.js";

/** The labels the trim applies to, and where each one's rule lives. Both pill
 *  labels and the sidebar address are the same defect in two files. */
const TRIMMED: { name: string; sheet: string; clips: boolean }[] = [
  // Digits and a percent sign only, so it can never have a descender.
  { name: "context-label", sheet: "15-input.css", clips: false },
  { name: "ctx-model-pill", sheet: "15-input.css", clips: true },
  { name: "pill-role-label", sheet: "15-input.css", clips: true },
  { name: "sidebar-email", sheet: "10-shell-app.css", clips: true },
];

const SHEETS = ["15-input.css", "10-shell-app.css"];

/** Strip comments so prose ABOUT a declaration is never mistaken for one. The
 *  comments here quote `line-height: 1` to explain why it is wrong. */
function source(sheet: string): string {
  return loadCSS(sheet).replace(/\/\*[\s\S]*?\*\//g, " ");
}

/**
 * Every rule body whose SELECTOR mentions `name`, in either the id form
 * (`#name`), the attribute form (`[id="name"]`) or the class form (`.name`).
 *
 * A substring scan over selectors rather than `ruleContaining`, because these
 * labels are addressed three different ways across two files and one of them
 * (`#ctx-model-pill`) is reached through a NESTED `& [id="ctx-model-pill"]`
 * block inside `.pill-model`. Asserting on one spelling would let the others
 * drift.
 */
function declarationsFor(sheet: string, name: string): string {
  const css = source(sheet);
  // Boundary-anchored: a bare `.pill` must not match `.pill-expand-content`,
  // whose `line-height: 1.4` is correct for a multi-line card and has nothing to
  // do with centring a single-line label.
  const wanted = [
    new RegExp(`#${name}(?![\\w-])`),
    new RegExp(`\\[id="${name}"\\]`),
    new RegExp(`\\.${name}(?![\\w-])`),
  ];
  const out: string[] = [];
  let depth = 0;
  let selStart = 0;
  const stack: { sel: string; bodyStart: number }[] = [];
  for (let i = 0; i < css.length; i++) {
    if (css[i] === "{") {
      stack.push({ sel: css.slice(selStart, i).trim(), bodyStart: i + 1 });
      depth++;
    } else if (css[i] === "}") {
      depth--;
      const frame = stack.pop();
      if (frame !== undefined && wanted.some((w) => w.test(frame.sel))) {
        // Own declarations only: drop nested blocks so a child's rules are not
        // read as the parent's. `[^{};]*` for the nested SELECTOR rather than
        // `[^{}]*`, because the latter reaches back across every `;` before it
        // and takes the parent's own declarations with the block — which silently
        // emptied this scanner for any rule that has nested states, and `.pill`
        // has two.
        out.push(css.slice(frame.bodyStart, i).replace(/[^{};]*\{[^{}]*\}/g, " "));
      }
      selStart = i + 1;
    } else if (depth === 0 && css[i] === ";") {
      selStart = i + 1;
    }
  }
  return out.join(";");
}

describe("label centring", () => {
  it("trims every label's box to its cap band", () => {
    for (const label of TRIMMED) {
      expect(
        declarationsFor(label.sheet, label.name),
        `${label.sheet} must trim ${label.name} to 'trim-both cap alphabetic': that makes the box edges ` +
          `the cap band, so centring the box centres the letterforms and the offset is zero by ` +
          `construction rather than by a value authored per font`,
      ).toMatch(/text-box:\s*trim-both\s+cap\s+alphabetic/);
    }
  });

  it("gives every trimmed label that clips symmetric descender room", () => {
    // The trim moves the block-END edge up to the baseline, so a 'g' or a 'p'
    // leaves the content box. `overflow: hidden` (which these labels carry for
    // `text-overflow: ellipsis`) then cuts it. Symmetric padding is the answer
    // that does not undo the centring: `align-items: center` centres the MARGIN
    // box, so equal padding leaves the cap band exactly where the trim put it.
    const missing: string[] = [];
    for (const label of TRIMMED) {
      const decls = declarationsFor(label.sheet, label.name);
      const clips = /overflow:\s*hidden/.test(decls);
      expect(
        clips,
        `${label.name} is recorded as ${label.clips ? "clipping" : "not clipping"} but its rules say otherwise; ` +
          `update the table above with the reason`,
      ).toBe(label.clips);
      if (!clips) {
        continue;
      }
      // Symmetric by construction: `padding-block` with ONE value, or the
      // logical pair set equal. A block-end-only pad would shift the cap band.
      if (!/padding-block:\s*[\d.]+em\s*;/.test(decls)) {
        missing.push(
          `${label.sheet}: ${label.name} is trimmed and clips, with no symmetric padding-block`,
        );
      }
    }
    expect(
      missing,
      "a cap/alphabetic trim under `overflow: hidden` clips descenders. That is the regression " +
        "`line-height: 1` would have shipped for no visible gain, and it is just as available here.",
    ).toEqual([]);
  });

  it("declares no numeric line-height on any of them", () => {
    // `line-height` cannot move a glyph band relative to its box centre — the
    // leading is symmetric — so one added here would be a change that looks like
    // the fix, measures as nothing, and costs the descenders on the labels that
    // clip. `.pill-role-icon`'s `line-height: 0` is a DIFFERENT mechanism and
    // stays: its content is a replaced element sitting on the baseline, so the
    // strut is what oversizes it, and collapsing the strut is the fix for that.
    const offenders: string[] = [];
    for (const sheet of SHEETS) {
      for (const label of [...TRIMMED.map((t) => t.name), "pill"]) {
        const decls = declarationsFor(sheet, label);
        const m = /line-height:\s*([^;]+)/.exec(decls);
        const value = m?.[1]?.trim() ?? "";
        if (value !== "" && value !== "0") {
          offenders.push(`${sheet}: ${label} declares line-height: ${value}`);
        }
      }
    }
    expect(
      offenders,
      "the vertical offset is ((ascent - descent)/2 - band/2)em and has no line-height term in it. " +
        "Adding one moves the letterforms by zero and shrinks the box out from under the descenders.",
    ).toEqual([]);
  });
});
