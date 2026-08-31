// ---------------------------------------------------------------------------
// A card header IS its disclosure's hit target.
//
// Measured on a live transcript (110 chevrons, 9 distinct header shapes): four
// of the collapsible cards in the chat already took a click anywhere on their
// header — the tool group, the delegated-work card, the run card and every
// native `<summary>` — while two demanded the chevron itself. The tool card's
// was a 24x24 button at the FAR END of a 775px row, and the turn card's was
// 16x16, under the 24px desktop floor `vibekit-ui.md` states. 54 tool headers
// and 3 turn headers on that one page reported `cursor: auto`. So the same
// gesture worked on some cards and not on others, which is the whole defect.
//
// This is an ACTIVATION SURFACE, not a second control. The header forwards its
// click to the button that already owns the disclosure, so `aria-expanded`, the
// keyboard path, the focus ring and every CSS rule keyed off that attribute
// stay exactly where they were, and the disclosure primitive still sees one
// trigger.
//
// Promoting the header to `role="button"` — the shape the delegated-work card
// and the run card use — is NOT available to either of these two, and that is
// the reason this module exists rather than a fourth copy of that shape. Both
// headers contain real `<button>`s: the tool card's filename link, the turn
// card's Copy. A button inside a `role="button"` is axe's `nested-interactive`
// (serious), and `aria-hidden` plus `tabindex="-1"` does not clear it, because
// a `tabindex="-1"` element is still focusable by click and by script. That
// finding is written up twice in this codebase already
// (`fundamentals/subagent-block.ts`, and `tabs.ts` for the tab row's close
// affordance), and the CSS-side twin is in `22-git-multirepo.css`.
//
// Two gestures the header must NOT swallow, both real rather than theoretical:
//
//   - A click on a nested control. Skipped by ELEMENT KIND, not by a class
//     list, so a control added to one of these headers later is covered without
//     anyone remembering this file. That is also what makes the forwarded click
//     terminate: the synthetic click lands on the `<button>` the header
//     forwarded to, so the header's own listener sees a control and stops.
//   - A click that ends a text SELECTION. A reader dragging a prompt out of a
//     turn header and having the turn fold shut under the cursor is worse than
//     a small chevron, and it is what keeps the turn header's prompt text
//     selectable while the whole band stays the fold's target.
// ---------------------------------------------------------------------------

/** Anything inside a header row that owns its own click.
 *
 *  Matched by kind rather than by name. `summary` and `label` are here because
 *  both activate something else when clicked; `[contenteditable]` because a
 *  click there places a caret. */
const OWNS_ITS_CLICK =
  "a[href],area[href],button,input,select,textarea,summary,label," +
  "[role=button],[role=link],[role=checkbox],[role=menuitem],[contenteditable]";

/** Make every part of `row` activate `control`, except a nested control and a
 *  click that ends a selection inside the row.
 *
 *  `control` keeps the disclosure: it carries `aria-expanded`, it is what Tab
 *  reaches, and it is what the stylesheet keys the chevron's rotation off. */
export function wireRowToggle(row: HTMLElement, control: HTMLElement): void {
  row.addEventListener("click", (e: Event) => {
    const target = e.target;
    if (!(target instanceof Element)) {
      return;
    }
    const owner = target.closest(OWNS_ITS_CLICK);
    // Bounded to the row: `closest` walks past it, and the row itself may be a
    // control in some future caller.
    if (owner !== null && owner !== row && row.contains(owner)) {
      return;
    }
    if (hasSelectionIn(row)) {
      return;
    }
    control.click();
  });
}

/** Whether a live text selection sits inside `row`.
 *
 *  Checked at CLICK time, which is when the answer is available: a drag inside
 *  the row leaves the selection standing through `mouseup`, and a selection
 *  made anywhere else was already collapsed by this click's own `mousedown`. */
function hasSelectionIn(row: HTMLElement): boolean {
  const sel = row.ownerDocument.defaultView?.getSelection() ?? null;
  if (sel === null || sel.isCollapsed || sel.rangeCount === 0) {
    return false;
  }
  const anchor = sel.getRangeAt(0).commonAncestorContainer;
  return row.contains(anchor) || anchor.contains(row);
}
