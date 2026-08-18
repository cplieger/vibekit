// ---------------------------------------------------------------------------
// THE disclosure chevron. One glyph, one class, one rotation, for every
// "this expands" affordance in the app.
//
// Before this module there were eight of them in three techniques: an SVG icon
// whose direction was swapped in JS (the tool card), two chevrons drawn from a
// pair of rotated borders (the turn fold, the turn ledger), and five text
// triangles — `▸`/`▾` as CSS `content` on a `::before` (the tool group, the
// subagent card, the reasoning summary) and as a text child from TS (the git
// repo sections, the settings forge repos). They disagreed on more than
// technique: on the resting direction, on whether a glyph appeared at all when
// collapsed, and on the rotation (90deg for some, ±45deg for the border pair).
//
// The SVG icon wins for two reasons that are not taste. It is the app's icon
// system, so it carries the same 24-unit viewBox, 2px stroke and round caps as
// every icon beside it. And it renders identically everywhere, where a text
// triangle is a FONT glyph: its metrics, weight and vertical position come from
// whatever font resolved, which is what put the subagent card's `▾` off centre.
//
// The convention is `<details>`'s own: closed points along the inline axis,
// open points DOWN at the body hanging below it. The CSS owns direction; no
// caller swaps a glyph, and a second icon for the open state is not needed.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { iconEl } from "./icon-el.js";
import { ICON_CHEVRON_DOWN } from "./icons.js";

/** A disclosure chevron, closed. Rotation is the stylesheet's
 *  (`.disclosure-chevron` in `css/10-shell-app.css`): each site flips it from
 *  whatever element carries its own open state — `[open]`, `aria-expanded`, a
 *  `.collapsed` class, a data attribute.
 *
 *  Always `aria-hidden`. Every one of these sits inside a control that already
 *  carries the expanded state (`aria-expanded`, or a native `<summary>`), so the
 *  glyph is decoration and announcing it would name a second control. */
export function chevronEl(): HTMLElement {
  return el(
    "span",
    { className: "disclosure-chevron", "aria-hidden": "true" },
    iconEl(ICON_CHEVRON_DOWN),
  );
}
