// ---------------------------------------------------------------------------
// The title bar's heading: one writer for every view's title and subtitle.
//
// Before this there were nine answers to one question. Five views carried a
// `.page-title` inside their own scrolling content — three hardcoded in markup,
// two written by their own tab module — four carried none at all, and the bar held
// a separate `#toolbar-title` that only ever appeared below 48rem and was
// deliberately EMPTY for a chat, so the one view people spend their time in had
// no title on a phone. No view had a subtitle. There was no registry;
// `TabViewSpec.name` came closest and fed only that mobile span and the sidebar's
// rows.
//
// # The subtitle carries what the title does not
//
// It is never decoration and never a restatement. For a view whose section is
// already named by a segmented bar (settings, docs, git) it is the FALLBACK name,
// suppressed in CSS while that bar shows its own labels and revealed when the bar
// drops them — see 12-chat.css, which reads `.tab-bar-icons` directly, so this
// module does not need to know the bar exists. For every other view it is a fact
// the title cannot hold: a path, a file's directory, a run's name.
//
// # Two writers, and why the subtitle is remembered rather than passed
//
// A view switch and a sub-tab switch are different events on different clocks:
// `showView` knows the title (the tab's name) and nothing about sub-tabs, while
// `settings-tabs.ts` / `docs.ts` / `git-tabs.ts` know their section and nothing
// about which view is on screen. Whichever fires last would clobber the other's
// half if both wrote the whole heading, and the order is not fixed — a deep link
// corrects the sub-tab after the view is shown, an ordinary tab click does not.
//
// So the subtitle is stored PER VIEW KIND here and the two writers each own their
// own half. `tabs.ts` cannot ask the three modules for their labels directly:
// those modules already reach back into the tab store, so the import would close
// a cycle. Keeping the value here is what avoids that.
// ---------------------------------------------------------------------------

import { byId } from "./dom.js";

/** Per-view-kind subtitles, written by the view that owns the section and read on
 *  every view switch. Keyed by `TabKind` as a plain string so this module stays a
 *  leaf: importing the generated union would pull the wire types in for no
 *  checking benefit, since the keys only ever come from a caller that has one. */
const subtitles = new Map<string, string>();

/** The kind whose title is currently painted. `setPageSubtitle` compares against
 *  it rather than painting unconditionally: a sub-tab setter can run for a view
 *  that is not on screen (a deep link resolving, a store correction), and writing
 *  then would put one view's section name under another view's title. Recording
 *  it is enough, because every view switch repaints from the map. */
let shownKind = "";

/** The class a heading wears when the title cannot render in the room the bar's
 *  actions leave it: the app's screen-reader-only utility (40-a11y.css), so the
 *  document keeps its `<h1>` and only the pixels go. `display: none` would take the
 *  page's one heading out of the accessibility tree on exactly the devices where
 *  this bar is the only thing naming the view. */
const CLIPPED = "sr-only";

/** Clip the heading when its title would render truncated.
 *
 *  MEASURED, never keyed to a width, for `tab-bar-fit.ts`'s reason: the answer
 *  depends on the TEXT (a chat's name against "Git") and on how many actions the
 *  bar is showing — `#find-btn` collapses on views with nothing to search, and the
 *  hamburger only exists below 48rem — so no container width knows it. Measured at
 *  390px on the coarse tier: eight 44px targets plus the bar's 24px of padding fill
 *  the row exactly, leaving the heading 0px, while at 768px it gets 378px against a
 *  title wanting 230px.
 *
 *  The read is taken with the heading SHOWN — the class comes off, the measurement
 *  happens, the class goes back — so the decision never depends on its own outcome.
 *  Without that, a clipped heading measures 1px, reports no overflow, and unclips
 *  itself on the next pass forever. */
function fitHeading(): void {
  // Null-tolerant, unlike the WRITE below, and the asymmetry is the point: a title
  // with no element to land in is a bug and `byId` should say so, while a FIT is a
  // refinement over a laid-out bar and having no bar is not an error. Hard-failing
  // here made `setPageTitle` throw for any caller that owns a subtitle span without
  // a toolbar around it.
  const heading = document.getElementById("titlebar-heading");
  const title = document.getElementById("titlebar-title");
  if (heading === null || title === null) {
    return;
  }
  heading.classList.remove(CLIPPED);
  // A hidden view, and an empty title (`.titlebar-title:empty` is `display: none`),
  // both report 0/0 — no overflow, so shown. The observer fires again when the bar
  // gains real geometry, so a bar measured before layout self-corrects.
  const truncated = title.scrollWidth > title.clientWidth;
  heading.classList.toggle(CLIPPED, truncated);
}

/** Watch the heading and keep the title's presence fitted to the room the actions
 *  leave. Call once at init; the bar is a static singleton, so there is nothing to
 *  release.
 *
 *  The observer watches the HEADING rather than the bar because the bar's width is
 *  not the only input — a collapsing find button changes the room without changing
 *  the bar. That costs one redundant pass per flip (clipping the heading resizes it,
 *  which re-notifies), and the second pass re-measures the same shown layout,
 *  reaches the same verdict and mutates nothing, so it settles there. */
export function initPageTitleFit(): void {
  const ro = new ResizeObserver(() => {
    // Deferred a frame: mutating class state inside the RO delivery cycle is what
    // produces the browser's benign "loop completed with undelivered
    // notifications" console error.
    requestAnimationFrame(fitHeading);
  });
  ro.observe(byId<HTMLElement>("titlebar-heading"));
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

/** Record this view's section name, and show it when that view is the one on
 *  screen.
 *
 *  The title is left exactly as it is, which is what lets a sub-tab switch repaint
 *  half the heading without knowing what the other half says. */
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
