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

function paint(title: string, subtitle: string): void {
  byId<HTMLElement>("titlebar-title").textContent = title;
  byId<HTMLElement>("titlebar-subtitle").textContent = subtitle;
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
  if (kind === shownKind) {
    byId<HTMLElement>("titlebar-subtitle").textContent = subtitle;
  }
}

/** Clear the heading. Used when no tab is open, where a stale title would name a
 *  view that is no longer on screen. */
export function clearPageTitle(): void {
  shownKind = "";
  paint("", "");
}
