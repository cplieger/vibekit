// ---------------------------------------------------------------------------
// The title bar's heading: one writer for every view's title and subtitle. The
// subtitle is REMEMBERED per view kind here rather than passed, because a view switch
// and a sub-tab switch are separate events with no fixed order.
// ---------------------------------------------------------------------------

import { byId } from "./dom.js";

/** Per-view-kind subtitles, read on every view switch. Keyed by `TabKind` as a plain
 *  string so this module stays a leaf; every caller already holds one. */
const subtitles = new Map<string, string>();

/** The kind whose title is currently painted, so a sub-tab setter running for a view
 *  that is NOT on screen records and writes nothing; every view switch repaints. */
let shownKind = "";

/** The class a heading wears when its title will not fit: the screen-reader-only
 *  utility (40-a11y.css), so only the pixels go. `display: none` would take the page's
 *  one heading out of the accessibility tree on the devices where this bar is the only
 *  thing naming the view. */
const CLIPPED = "sr-only";

/** Whether the bar's ACTIONS are on more than one row, read off the rendered rows
 *  rather than computed from widths.
 *
 *  The buttons are direct children of the bar and every one of them is at least
 *  `--btn-h` wide (12-chat.css), so on a phone eight of them plus their gaps need
 *  more than the bar's content box and the bar wraps. A `display: none` button (the
 *  hamburger above 48rem) reports an empty rect and is skipped, or it would count as
 *  a row of its own at y=0; a COLLAPSED one (`#find-btn` on a view with nothing to
 *  search) keeps its height and its row, which is correct — it is still in flow. */
function actionsWrapped(heading: HTMLElement): boolean {
  const bar = heading.parentElement;
  if (bar === null) {
    return false;
  }
  const rows = new Set<number>();
  for (const btn of bar.querySelectorAll<HTMLElement>(":scope > .icon-btn")) {
    const r = btn.getBoundingClientRect();
    if (r.width === 0 && r.height === 0) {
      continue;
    }
    rows.add(Math.round(r.top));
  }
  return rows.size > 1;
}

/** Clip the heading when it does not fit: either its own title would render truncated,
 *  or the ACTIONS have wrapped. MEASURED, never keyed to a width, for
 *  `tab-bar-fit.ts`'s reason. The read is taken with the heading SHOWN, or a clipped
 *  heading measures 1px, reports no overflow and unclips itself forever.
 *
 *  THE SECOND TEST IS WHAT MAKES THE OUTCOME STABLE, and its absence was a bistable
 *  band at 387-391px. The heading is `flex: 1 1 0`, so it contributes no BASIS to the
 *  line, but it is still a flex item and the bar is still charged its GAP — 2px the
 *  eight 44px buttons and their seven gaps cannot spare, since those come to exactly
 *  the content box a 390px viewport gives. So the bar wrapped, the wrap handed the
 *  heading a whole row of its own, the title then measured as fitting, and it rendered
 *  BECAUSE the bar had broken: a title visible only in the state where the actions
 *  had to spill onto a second row. Reading the actions' own rows is what breaks that
 *  circularity — the decision is taken from the SHOWN layout and never depends on its
 *  own outcome, so 390 and 391 clip once and then hold one row, and below 390 the two
 *  rows are deliberate (eight 44px targets do not fit 296px of content box at 320,
 *  and shrinking a target is not on the table). */
function fitHeading(): void {
  // Null-tolerant, unlike the WRITE below: a title with no element to land in is a bug
  // and `byId` should say so, while a fit over a bar that is not there is not an error.
  const heading = document.getElementById("titlebar-heading");
  const title = document.getElementById("titlebar-title");
  if (heading === null || title === null) {
    return;
  }
  heading.classList.remove(CLIPPED);
  // A hidden view and an empty title both report 0/0, so no overflow and shown; the
  // observer fires again once the bar has real geometry.
  const truncated = title.scrollWidth > title.clientWidth;
  heading.classList.toggle(CLIPPED, truncated || actionsWrapped(heading));
}

/** Watch the heading AND the bar, and keep the title's presence fitted to the room the
 *  actions leave. Call once at init; both are static singletons, so nothing to release.
 *
 *  BOTH, because each sees a change the other cannot. The heading is what a collapsing
 *  find button moves without moving the bar. And the bar is the only one of the two
 *  that a VIEWPORT change reaches once the heading is clipped: `.sr-only` is a 1x1
 *  absolutely positioned box at every width, so an observer watching only the heading
 *  goes silent exactly while the clip is in force — measured on the served page, a
 *  monotonic 320 -> 768 resize sweep kept the title hidden at every width, where a
 *  fresh load at 768 showed it. That is a rotation on a phone: portrait clips the
 *  title, landscape has room for it and never re-asked.
 *
 *  Clipping resizes both, so one redundant pass per flip re-measures the same shown
 *  layout, agrees, and settles. */
export function initPageTitleFit(): void {
  const ro = new ResizeObserver(() => {
    // Deferred a frame: a class write inside the RO cycle logs a benign loop error.
    requestAnimationFrame(fitHeading);
  });
  const heading = byId<HTMLElement>("titlebar-heading");
  ro.observe(heading);
  const bar = heading.parentElement;
  if (bar !== null) {
    ro.observe(bar);
  }
  fitHeading();
}

function paint(title: string, subtitle: string): void {
  const titleEl = byId<HTMLElement>("titlebar-title");
  const subtitleEl = byId<HTMLElement>("titlebar-subtitle");
  // Guarded because this runs from the view effect, which re-runs on every
  // projection mutation: a rename, a dot change and a background tab's close all
  // repaint the same two strings, and `fitHeading` forces layout to read
  // `scrollWidth`. A width change is the observer's job, not this one's.
  const moved = titleEl.textContent !== title || subtitleEl.textContent !== subtitle;
  titleEl.textContent = title;
  subtitleEl.textContent = subtitle;
  if (moved) {
    fitHeading();
  }
}

/** Show `title`, with whatever subtitle `kind` last recorded.
 *
 *  Called on every view switch. `textContent`, never markup: a title can be a
 *  chat name, a filename or a branch, all arbitrary text from outside this app. */
export function setPageTitle(title: string, kind = ""): void {
  shownKind = kind;
  paint(title, subtitles.get(kind) ?? "");
}

/** Record this view's section name, and show it when that view is the one on screen.
 *  The title is left exactly as it is, which is what lets a sub-tab switch repaint half
 *  the heading without knowing what the other half says.
 *
 *  For a view whose section a segmented bar already names, this is the FALLBACK name:
 *  12-chat.css suppresses it while that bar shows its own labels and reveals it when the
 *  bar drops them, reading `.tab-bar-icons` directly. */
export function setPageSubtitle(kind: string, subtitle: string): void {
  subtitles.set(kind, subtitle);
  if (kind !== shownKind) {
    return;
  }
  const el = byId<HTMLElement>("titlebar-subtitle");
  if (el.textContent === subtitle) {
    return;
  }
  el.textContent = subtitle;
  fitHeading();
}

/** Clear the heading. Used when no tab is open, where a stale title would name a
 *  view that is no longer on screen. */
export function clearPageTitle(): void {
  shownKind = "";
  paint("", "");
}
