// ---------------------------------------------------------------------------
// Where the lazily-loaded pages hand Ctrl-F their own entry point.
//
// A LEAF with no imports, and that is the whole reason it is a separate module
// rather than three exports on find-dispatch.ts. The dispatcher statically
// imports find-in-chat.ts, which pulls in scroll.ts's self-initialising
// singleton; so registering through the dispatcher would have made `/docs`,
// `/history` and the git view import the chat transcript's machinery to gain a
// search box. It did, and docs.test.ts caught it immediately ("Missing element:
// #messages" at import time) — a page that cannot be imported without the chat
// view's DOM is a page that is no longer lazily loadable.
//
// Why a registry at all, rather than an import in either direction:
//
//   - A DYNAMIC import cannot serve the hotkey. `preventDefault` must be called
//     during the event, and by the time a promise callback runs the browser has
//     already opened its own find.
//   - A STATIC import from the dispatcher would put docs.ts, history.ts and the
//     git tabs on the boot path (history.ts pulls chat.ts in behind it), which is
//     the exact cost their laziness buys.
//
// The invariant that makes a null read safe: a page cannot BE the active tab
// without its module having been imported to render it, so a null here means
// that page is not active and the dispatcher's branch for it was unreachable.
//
// WHY THE MAP IS FRONTED BY A SIGNAL. Every page registers from inside its own
// lazily-imported module, and `tabs.activateTab` announces the switch BEFORE it
// calls `onShow` — so the toolbar paints its magnifier from this registry one
// module-fetch before the page arrives to fill it. A plain Map cannot invalidate
// the effect that read it, so the button stayed in whatever state the previous
// tab left it: absent on the first `/docs` open of a page session (and for the
// whole session on a boot-restored docs tab), and still present but inert after
// a git Changes → Sources switch. The version counter is what makes a
// registration re-derive the answer, and reading it inside `pageFind` is what
// subscribes every caller without any of them knowing about it.
// ---------------------------------------------------------------------------

import { signal } from "@cplieger/reactive";

/** What a find affordance MEANS on a page, which decides its glyph and its
 *  wording wherever it is offered.
 *
 *  A `search` reaches past what is on screen; a `filter` narrows rows already
 *  loaded. One vocabulary, so the toolbar button, the box it opens and the
 *  placeholder inside it cannot tell a reader three different stories. */
export type FindKind = "search" | "filter";

/** A page's answer to the find chord and to the toolbar's magnifier.
 *
 *  Three functions rather than one, because the chord and the button mean
 *  different things and both need the third to be honest about it: the chord
 *  OPENS (a second press is the escape hatch to the browser's own find, which is
 *  the a11y justification for overriding it at all), the button TOGGLES (a
 *  button that only ever opens is not a toggle), and `focused` is what separates
 *  a first press from a second. */
export interface PageFind {
  /** Open, or refocus an already-open box. `false` means the page declined —
   *  nothing to focus — so the caller leaves the chord to native find. */
  open: () => boolean;
  toggle: () => void;
  /** Whether this page's find is open AND holding the caret. */
  focused: () => boolean;
  /** Search or filter. A function rather than a constant because a sub-tabbed
   *  page can be one on one tab and the other on the next; the git view is the
   *  first (its two panels filter, its Sources tab has no box at all). */
  kind: () => FindKind;
  /** Whether the toolbar's magnifier has a destination here. Optional, because
   *  most pages always do; the git view is the one that does not, since its
   *  Sources tab lists forge accounts rather than a filterable inventory.
   *
   *  Separate from `open` returning false on purpose: `open` is asked DURING a
   *  keypress, and this is asked while painting the toolbar. A button that stays
   *  visible where nothing can answer it is the dead door find-dispatch.ts was
   *  written to remove. */
  available?: () => boolean;
}

const registry = new Map<string, PageFind>();

/** Bumped on every registration, read by `pageFind`. See the header note. */
const version = signal(0);

/** Register a page's find entry point, keyed by its tab kind. Idempotent:
 *  re-registering replaces, so a page may call it on every mount. */
export function registerFind(kind: string, find: PageFind): void {
  const had = registry.get(kind);
  registry.set(kind, find);
  if (had !== find) {
    version.value = version.value + 1;
  }
}

/** The registered find for a tab kind, or undefined.
 *
 *  Reads the version signal, so a caller inside an effect re-runs when a page
 *  registers. Outside an effect that read is free. */
export function pageFind(kind: string): PageFind | undefined {
  void version.value;
  return registry.get(kind);
}

/** @internal Test seam: drop every registration. */
export function _resetFindRegistry(): void {
  registry.clear();
  version.value = version.value + 1;
}
