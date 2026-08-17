// ---------------------------------------------------------------------------
// Where the two lazily-loaded pages hand Ctrl-F their own entry point.
//
// A LEAF with no imports, and that is the whole reason it is a separate module
// rather than two exports on find-dispatch.ts. The dispatcher statically imports
// find-in-chat.ts, which pulls in scroll.ts's self-initialising singleton; so
// registering through the dispatcher would have made `/docs` and `/history`
// import the chat transcript's machinery to gain a search box. It did, and
// docs.test.ts caught it immediately ("Missing element: #messages" at import
// time) — a page that cannot be imported without the chat view's DOM is a page
// that is no longer lazily loadable.
//
// Why a registry at all, rather than an import in either direction:
//
//   - A DYNAMIC import cannot serve the hotkey. `preventDefault` must be called
//     during the event, and by the time a promise callback runs the browser has
//     already opened its own find.
//   - A STATIC import from the dispatcher would put docs.ts and history.ts on
//     the boot path (history.ts pulls chat.ts in behind it), which is the exact
//     cost their laziness buys.
//
// The invariant that makes a null read safe: a page cannot BE the active tab
// without its module having been imported to render it, so a null here means
// that page is not active and the dispatcher's branch for it was unreachable.
// ---------------------------------------------------------------------------

/** A page's answer to Ctrl-F. `false` means it declined — nothing to focus — so
 *  the caller leaves the chord to the browser's native find. */
export type FindFocuser = () => boolean;

const focusers = new Map<string, FindFocuser>();

/** Register a page's find entry point, keyed by its tab kind. Idempotent:
 *  re-registering replaces, so a page may call it on every mount. */
export function registerFind(kind: string, focus: FindFocuser): void {
  focusers.set(kind, focus);
}

/** The registered focuser for a tab kind, or undefined. */
export function findFocuser(kind: string): FindFocuser | undefined {
  return focusers.get(kind);
}

/** @internal Test seam: drop every registration. */
export function _resetFindRegistry(): void {
  focusers.clear();
}
