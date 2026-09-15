import { buildPath, parseRoute } from "./route-path.js";
import type { Route } from "./route-path.js";

// --- Where a route came from ---

/** Where a route came from, because the three answer "this names nothing that is
 *  open" differently.
 *
 *  A `deeplink` MAY open what it names. A `history` entry and a `restore` may only
 *  ACTIVATE something already open: both name a location this browser was at rather
 *  than one that still exists, so applying either as a deep link RE-CREATES the tab —
 *  server-side, and broadcast to every other device. */
export type RouteOrigin = "deeplink" | "history" | "restore";

/** Whether the document was RESTORED rather than navigated to.
 *
 *  Fails toward `deeplink` on an absent or unrecognised entry, deliberately: that
 *  keeps a genuine deep link working on any engine that reports nothing, which is the
 *  direction that loses no capability. Measured in Chromium 1234 — a reload answers
 *  `reload`, a cross-document back or forward answers `back_forward`, and a
 *  same-document `pushState` mints no entry, so the answer describes the DOCUMENT's
 *  load. What an iOS WebContent eviction reports cannot be measured here and is
 *  carried as unverified. */
export function navigationOrigin(): RouteOrigin {
  const [entry] = performance.getEntriesByType("navigation") as PerformanceNavigationTiming[];
  return entry?.type === "reload" || entry?.type === "back_forward" ? "restore" : "deeplink";
}

// --- Push or replace the URL without triggering popstate ---

/** How many callers are currently suppressing pushes.
 *
 *  A COUNT, not a flag, because the boot's regions run concurrently now: the
 *  settings restore and the tab restore each open a window, and with a boolean
 *  whichever closed first un-suppressed the other's — so a restore's own
 *  activation pushed a URL. Clamped at zero so an unbalanced `false` cannot leave
 *  the app permanently suppressed. */
let suppressDepth = 0;

export function suppressPush(v: boolean): void {
  suppressDepth = v ? suppressDepth + 1 : Math.max(0, suppressDepth - 1);
}

/** The path the router is mid-way through applying, or "" when none. A CLAIM rather
 *  than a second suppression window: a window silences every push, a claim silences
 *  only a push to a DIFFERENT location, so the claimed location's own activation still
 *  lands. It defends a deep link whose opener is reached through a dynamic `import()`:
 *  until that resolves the active row is still whatever the boot restored, and the tab
 *  projection writes the URL from the active row on EVERY mutation. */
let claimedPath = "";

/** Claim a location for the duration of applying it. `releaseLocation` in a `finally`;
 *  a second claim replaces the first, which is what a mid-boot re-entry means. The claim
 *  is a PATHNAME, so a push that only moves the fragment is a real move inside the
 *  claimed location and stays admissible. */
export function claimLocation(path: string): void {
  claimedPath = pathnameOf(path);
}

export function releaseLocation(): void {
  claimedPath = "";
}

function pathnameOf(path: string): string {
  const hash = path.indexOf("#");
  return hash === -1 ? path : path.slice(0, hash);
}

export function pushRoute(route: Route): void {
  if (suppressDepth > 0) {
    return;
  }
  const target = buildPath(route);
  const current = location.pathname + location.hash;
  if (target === current) {
    return;
  }
  // Guards `pushRoute` ONLY: a replace cannot leave a history entry, which is the whole
  // defect, and `applyInitialRoute`'s own canonicalisation is a replace.
  if (claimedPath !== "" && pathnameOf(target) !== claimedPath) {
    return;
  }
  // A push that only DROPS the current fragment REPLACES instead. A fragment here
  // is a position inside the page its own route already names (`#node=` on a run,
  // `#L<line>` on a file) and it is consumed on arrival: a cold load of
  // `/run/x#node=y` focuses that step and then activates the tab, whose route
  // carries no fragment, so a push would stack `/run/x` on top of the URL the
  // reader opened and their first Back press would land on a location that renders
  // identically — an entry that looks inert. The inverse still pushes: going from
  // `/run/x` to `/run/x#node=y` is a real move to a position, and Back out of it
  // means something.
  if (target === location.pathname && location.hash !== "") {
    history.replaceState(null, "", target);
    return;
  }
  history.pushState(null, "", target);
}

export function replaceRoute(route: Route): void {
  if (suppressDepth > 0) {
    return;
  }
  const target = buildPath(route);
  const current = location.pathname + location.hash;
  if (target !== current) {
    history.replaceState(null, "", target);
  }
}

// --- Listen for back/forward navigation ---

let popstateHandler: ((route: Route) => void) | undefined;

export function onPopState(handler: (route: Route) => void): void {
  popstateHandler = handler;
}

// Cache the last parsed route to skip redundant URL parsing on rapid popstate.
let cachedKey = "";
let cachedRoute: Route | undefined;

window.addEventListener("popstate", () => {
  if (popstateHandler !== undefined) {
    const key = location.pathname + location.hash;
    if (key === cachedKey && cachedRoute !== undefined) {
      popstateHandler(cachedRoute);
      return;
    }
    const route = parseRoute(location.pathname, location.hash);
    cachedKey = key;
    cachedRoute = route;
    popstateHandler(route);
  }
});
