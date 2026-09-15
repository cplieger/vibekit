// The route VOCABULARY's own cases live in route-path.test.ts, beside the module
// that owns them. What is left here is the DOM-bound half: the suppression window,
// pushRoute's fragment collapse, the location claim and the navigation origin.

import { describe, it, expect, afterEach, vi } from "vitest";
import {
  claimLocation,
  navigationOrigin,
  pushRoute,
  releaseLocation,
  replaceRoute,
  suppressPush,
} from "./router";

// ---------------------------------------------------------------------------
// suppressPush: the re-entrant window every boot-time restore writes through
// ---------------------------------------------------------------------------

/** Spy on both history writers, so a case reads whether the router ISSUED a write
 *  without moving the test runner's own iframe URL. `restoreMocks` puts them back. */
function spyHistory() {
  return {
    pushState: vi.spyOn(History.prototype, "pushState").mockImplementation(() => undefined),
    replaceState: vi.spyOn(History.prototype, "replaceState").mockImplementation(() => undefined),
  };
}

// The depth is module state, so every case below CLOSES the window it opened. A
// shared drain in an `afterEach` cannot: without the clamp under test it would
// itself drive the depth negative, and one clamp regression would fail every case
// in the block instead of the one that names it.
describe("suppressPush", () => {
  it("lets both writers through while nothing is suppressing", () => {
    const spy = spyHistory();

    pushRoute({ kind: "git", tab: "changes" });
    replaceRoute({ kind: "settings", tab: "tools" });

    expect(spy.pushState).toHaveBeenCalledWith(null, "", "/git");
    expect(spy.replaceState).toHaveBeenCalledWith(null, "", "/settings/tools");
  });

  it("drops a push made inside the window", () => {
    const spy = spyHistory();

    suppressPush(true);
    pushRoute({ kind: "git", tab: "changes" });
    suppressPush(false);

    expect(spy.pushState).not.toHaveBeenCalled();
  });

  it("drops a replace made inside the window", () => {
    const spy = spyHistory();

    suppressPush(true);
    replaceRoute({ kind: "settings", tab: "tools" });
    suppressPush(false);

    expect(spy.replaceState).not.toHaveBeenCalled();
  });

  it("stays suppressed until the LAST caller closes its window", () => {
    // A COUNT rather than a flag, because the boot's regions run concurrently: with a
    // boolean, whichever region closed first un-suppressed the other's window and its
    // restore pushed a URL.
    const spy = spyHistory();

    suppressPush(true);
    suppressPush(true);
    suppressPush(false);
    pushRoute({ kind: "git", tab: "changes" });
    expect(spy.pushState).not.toHaveBeenCalled();

    suppressPush(false);
    pushRoute({ kind: "git", tab: "changes" });
    expect(spy.pushState).toHaveBeenCalledTimes(1);
  });

  it("cannot be driven below zero by an unbalanced close", () => {
    // The clamp. Without it this close takes the depth to -1, and the window opened
    // next sits at 0 — so the app is left permanently un-suppressible by one stray
    // `false`.
    const spy = spyHistory();

    suppressPush(false);
    suppressPush(true);
    pushRoute({ kind: "git", tab: "changes" });
    suppressPush(false);

    expect(spy.pushState).not.toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// pushRoute's fragment collapse.
//
// A fragment names a position inside the page its route already names, and the
// app consumes it on arrival: a cold `/run/x#node=y` focuses that step and then
// activates the tab, whose own route carries no fragment. Pushing there would
// leave the reader an entry that renders identically to the one they opened, so
// the first Back press looks inert. Asserted through spies rather than
// `history.length`, which counts entries this runner's iframe made before the
// test and cannot be reset.
// ---------------------------------------------------------------------------

describe("pushRoute", () => {
  const originalHref = location.pathname + location.search + location.hash;

  afterEach(() => {
    vi.restoreAllMocks();
    history.replaceState(null, "", originalHref);
  });

  it("replaces rather than pushes when the only change is dropping the fragment", () => {
    expect.assertions(3);
    history.replaceState(null, "", "/run/wf_1#node=wf_1/build-loop/iter-0/implement");
    const push = vi.spyOn(history, "pushState");
    const replace = vi.spyOn(history, "replaceState");

    pushRoute({ kind: "run", id: "wf_1" });

    expect(push).not.toHaveBeenCalled();
    expect(replace).toHaveBeenCalledTimes(1);
    expect(replace.mock.calls[0]?.[2]).toBe("/run/wf_1");
  });

  it("pushes when a fragment is ADDED, because that is a real move to a position", () => {
    expect.assertions(2);
    history.replaceState(null, "", "/run/wf_1");
    const push = vi.spyOn(history, "pushState");

    pushRoute({ kind: "run", id: "wf_1", node: "wf_1/plan" });

    expect(push).toHaveBeenCalledTimes(1);
    expect(push.mock.calls[0]?.[2]).toBe("/run/wf_1#node=wf_1%2Fplan");
  });

  it("pushes when the PATH changes, fragment or no fragment", () => {
    expect.assertions(2);
    history.replaceState(null, "", "/run/wf_1#node=wf_1/plan");
    const push = vi.spyOn(history, "pushState");

    pushRoute({ kind: "run", id: "wf_2" });

    expect(push).toHaveBeenCalledTimes(1);
    expect(push.mock.calls[0]?.[2]).toBe("/run/wf_2");
  });

  it("does nothing when the target is already the current location", () => {
    expect.assertions(2);
    history.replaceState(null, "", "/run/wf_1");
    const push = vi.spyOn(history, "pushState");
    const replace = vi.spyOn(history, "replaceState");

    pushRoute({ kind: "run", id: "wf_1" });

    expect(push).not.toHaveBeenCalled();
    expect(replace).not.toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// The location CLAIM.
//
// A deep link whose opener is reached through a dynamic import is not open when
// `applyRoute` returns, and the tab projection writes the URL from the ACTIVE ROW
// on every mutation — so the first unrelated emit used to push the restored tab's
// route over the location the reader opened, and their first Back press landed on
// a chat they never navigated to.
//
// Not a second suppression window: a window silences every push, while the claim
// silences only a push to a DIFFERENT location, which is what leaves the claimed
// location's own activation able to land.
// ---------------------------------------------------------------------------

describe("claimLocation", () => {
  const originalHref = location.pathname + location.search + location.hash;

  afterEach(() => {
    releaseLocation();
    vi.restoreAllMocks();
    history.replaceState(null, "", originalHref);
  });

  it("drops a push to a location other than the claimed one", () => {
    expect.assertions(1);
    history.replaceState(null, "", "/run/wf_1");
    claimLocation("/run/wf_1");
    const push = vi.spyOn(history, "pushState");

    // The shape of the defect: the projection's view effect writing the restored
    // tab's route while the run view's chunk is still loading.
    pushRoute({ kind: "chat", id: "c-restored" });

    expect(push).not.toHaveBeenCalled();
  });

  it("lets the claimed location's OWN activation land", () => {
    expect.assertions(2);
    history.replaceState(null, "", "/chat/c-other");
    claimLocation("/run/wf_1");
    const push = vi.spyOn(history, "pushState");

    pushRoute({ kind: "run", id: "wf_1" });

    expect(push).toHaveBeenCalledTimes(1);
    expect(push.mock.calls[0]?.[2]).toBe("/run/wf_1");
  });

  it("admits a push that only moves the position INSIDE the claimed location", () => {
    // A fragment is a position inside the page the claimed route already names, so
    // moving it is a real move rather than a competing location. This is also why
    // the claim is compared as a pathname: the document may have loaded at
    // `/run/wf_1#node=…` and the tab's own route carries no fragment at all.
    expect.assertions(2);
    history.replaceState(null, "", "/run/wf_1");
    claimLocation("/run/wf_1#node=wf_1%2Fplan");
    const push = vi.spyOn(history, "pushState");

    pushRoute({ kind: "run", id: "wf_1", node: "wf_1/build" });

    expect(push).toHaveBeenCalledTimes(1);
    expect(push.mock.calls[0]?.[2]).toBe("/run/wf_1#node=wf_1%2Fbuild");
  });

  it("leaves replaceRoute alone, because a replace leaves no history entry", () => {
    // The whole defect is a spurious ENTRY, and `applyInitialRoute`'s own
    // canonicalization is a replace — guarding it would leave the address bar
    // naming nothing on a `/` boot.
    expect.assertions(2);
    history.replaceState(null, "", "/run/wf_1");
    claimLocation("/run/wf_1");
    const replace = vi.spyOn(history, "replaceState");

    replaceRoute({ kind: "chat", id: "c-restored" });

    expect(replace).toHaveBeenCalledTimes(1);
    expect(replace.mock.calls[0]?.[2]).toBe("/chat/c-restored");
  });

  it("stops guarding once released", () => {
    expect.assertions(2);
    history.replaceState(null, "", "/run/wf_1");
    claimLocation("/run/wf_1");
    releaseLocation();
    const push = vi.spyOn(history, "pushState");

    pushRoute({ kind: "chat", id: "c-restored" });

    expect(push).toHaveBeenCalledTimes(1);
    expect(push.mock.calls[0]?.[2]).toBe("/chat/c-restored");
  });
});

// ---------------------------------------------------------------------------
// navigationOrigin: whether the DOCUMENT was restored rather than navigated to.
//
// The signal for the one copy of "which tab this device was last on" that had no
// guard. A restored load's URL means "activate this if it is open"; a deliberate
// navigation means "open this". Measured in Chromium 1234: a reload answers
// `reload`, a cross-document back or forward answers `back_forward`, a fresh load
// answers `navigate`, and a same-document `pushState` mints no entry at all.
// ---------------------------------------------------------------------------

describe("navigationOrigin", () => {
  /** The navigation entry the engine reports, or none at all. */
  function reports(type: string | null): void {
    vi.spyOn(performance, "getEntriesByType").mockReturnValue(
      type === null ? [] : [{ type } as unknown as PerformanceEntry],
    );
  }

  it("reads a reload as a restore", () => {
    expect.assertions(1);
    reports("reload");

    expect(navigationOrigin()).toBe("restore");
  });

  it("reads a history traversal as a restore", () => {
    expect.assertions(1);
    reports("back_forward");

    expect(navigationOrigin()).toBe("restore");
  });

  it("reads a deliberate navigation as a deep link", () => {
    expect.assertions(1);
    reports("navigate");

    expect(navigationOrigin()).toBe("deeplink");
  });

  it("fails toward a deep link when the engine reports no entry", () => {
    // The direction that loses no capability: an engine reporting nothing keeps
    // genuine deep links working, where the other default would stop them opening.
    expect.assertions(1);
    reports(null);

    expect(navigationOrigin()).toBe("deeplink");
  });

  it("fails toward a deep link on a type it does not recognise", () => {
    // `prerender` is the live member of this set: the page was navigated to, just
    // early. Anything a later engine adds lands here too.
    expect.assertions(1);
    reports("prerender");

    expect(navigationOrigin()).toBe("deeplink");
  });
});
