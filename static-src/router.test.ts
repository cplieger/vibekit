import { describe, it, expect, afterEach, vi } from "vitest";
import * as fc from "fast-check";
import {
  parseRoute,
  buildPath,
  claimLocation,
  navigationOrigin,
  pushRoute,
  releaseLocation,
  replaceRoute,
  suppressPush,
  type Route,
  type SettingsTab,
  type DocsTab,
} from "./router";

// ---------------------------------------------------------------------------
// Proposal tarch-b14-p2: Table-driven test for parseRoute
// ---------------------------------------------------------------------------

describe("parseRoute (table-driven)", () => {
  const cases: { name: string; pathname: string; hash: string; expected: Route }[] = [
    { name: "root → default chat", pathname: "/", hash: "", expected: { kind: "chat", id: "" } },
    {
      name: "empty string → default chat",
      pathname: "",
      hash: "",
      expected: { kind: "chat", id: "" },
    },
    { name: "/chat/abc", pathname: "/chat/abc", hash: "", expected: { kind: "chat", id: "abc" } },
    {
      name: "/chat/with%20space",
      pathname: "/chat/with%20space",
      hash: "",
      expected: { kind: "chat", id: "with space" },
    },
    {
      name: "/chat/ (missing id) → default",
      pathname: "/chat/",
      hash: "",
      expected: { kind: "chat", id: "" },
    },
    { name: "/git", pathname: "/git", hash: "", expected: { kind: "git", tab: "changes" } },
    {
      name: "/git/prs",
      pathname: "/git/prs",
      hash: "",
      expected: { kind: "git", tab: "prs" },
    },
    {
      name: "/git/sources",
      pathname: "/git/sources",
      hash: "",
      expected: { kind: "git", tab: "sources" },
    },
    {
      name: "/git/changes (explicit) → changes",
      pathname: "/git/changes",
      hash: "",
      expected: { kind: "git", tab: "changes" },
    },
    {
      name: "/git/unknown → changes",
      pathname: "/git/bogus",
      hash: "",
      expected: { kind: "git", tab: "changes" },
    },
    { name: "/history", pathname: "/history", hash: "", expected: { kind: "history" } },
    {
      // The spec board is deleted outright — no shim, no redirect. A saved
      // /specs bookmark is just an unknown path now.
      name: "/specs (retired route) → default chat",
      pathname: "/specs",
      hash: "",
      expected: { kind: "chat", id: "" },
    },
    {
      name: "/files → the mounts listing",
      pathname: "/files",
      hash: "",
      expected: { kind: "files", path: "/" },
    },
    {
      name: "/files/ → the mounts listing",
      pathname: "/files/",
      hash: "",
      expected: { kind: "files", path: "/" },
    },
    {
      // Verbatim, root-slash and all: the router parses, and the file browser
      // NORMALISES at its own entry (`restoreFileBrowser` → `normalizeDirPath`),
      // which is what lets a bookmark written by an older build still resolve.
      // One normaliser, at the module that owns the space.
      name: "/files/src/main.go (a rootless legacy link, normalised by the browser)",
      pathname: "/files/src/main.go",
      hash: "",
      expected: { kind: "files", path: "src/main.go" },
    },
    {
      name: "/files/dir%20name/f.ts",
      pathname: "/files/dir%20name/f.ts",
      hash: "",
      expected: { kind: "files", path: "dir name/f.ts" },
    },
    {
      name: "/file/readme.md (no hash)",
      pathname: "/file/readme.md",
      hash: "",
      expected: { kind: "file", path: "readme.md" },
    },
    {
      name: "/file/src/app.ts#L42",
      pathname: "/file/src/app.ts",
      hash: "#L42",
      expected: { kind: "file", path: "src/app.ts", line: 42 },
    },
    {
      name: "/file/x.ts#L0 (invalid line)",
      pathname: "/file/x.ts",
      hash: "#L0",
      expected: { kind: "file", path: "x.ts" },
    },
    {
      name: "/file/x.ts#Lfoo (non-numeric)",
      pathname: "/file/x.ts",
      hash: "#Lfoo",
      expected: { kind: "file", path: "x.ts" },
    },
    {
      name: "/file/ (missing path) → default",
      pathname: "/file/",
      hash: "",
      expected: { kind: "chat", id: "" },
    },
    {
      // The baseline: a run URL with no fragment carries no node, which is what
      // every caller that means "the run" produces.
      name: "/run/wf_1 (no hash)",
      pathname: "/run/wf_1",
      hash: "",
      expected: { kind: "run", id: "wf_1" },
    },
    {
      // A node path contains `/`, which is why it is a fragment rather than a
      // path segment — the tab's identity stays `(run, workflowId)`.
      name: "/run/wf_1#node=wf_1%2Fiter-0%2Fwork",
      pathname: "/run/wf_1",
      hash: "#node=wf_1%2Fiter-0%2Fwork",
      expected: { kind: "run", id: "wf_1", node: "wf_1/iter-0/work" },
    },
    {
      name: "/run/wf_1#node= (empty value) → no node",
      pathname: "/run/wf_1",
      hash: "#node=",
      expected: { kind: "run", id: "wf_1" },
    },
    {
      name: "/run/wf_1#L12 (the editor's fragment) → no node",
      pathname: "/run/wf_1",
      hash: "#L12",
      expected: { kind: "run", id: "wf_1" },
    },
    {
      // safeDecode is the decoder, so a bare `%` survives as itself instead of
      // throwing — a hash arrives straight off location and off popstate.
      name: "/run/wf_1#node=%zz (malformed percent) → raw value",
      pathname: "/run/wf_1",
      hash: "#node=%zz",
      expected: { kind: "run", id: "wf_1", node: "%zz" },
    },
    {
      name: "/settings → general",
      pathname: "/settings",
      hash: "",
      expected: { kind: "settings", tab: "general" },
    },
    {
      name: "/settings/ → general",
      pathname: "/settings/",
      hash: "",
      expected: { kind: "settings", tab: "general" },
    },
    {
      name: "/settings/tools",
      pathname: "/settings/tools",
      hash: "",
      expected: { kind: "settings", tab: "tools" },
    },
    {
      name: "/settings/permissions",
      pathname: "/settings/permissions",
      hash: "",
      expected: { kind: "settings", tab: "permissions" },
    },
    {
      name: "/settings/instructions",
      pathname: "/settings/instructions",
      hash: "",
      expected: { kind: "settings", tab: "instructions" },
    },
    {
      // The "git" settings tab was retired (no panel/pill existed in the
      // DOM — deep-linking it landed on a blank Settings body); the segment
      // now canonicalizes to General like any unknown tab.
      name: "/settings/git (retired tab) → general",
      pathname: "/settings/git",
      hash: "",
      expected: { kind: "settings", tab: "general" },
    },
    {
      name: "/settings/unknown → general",
      pathname: "/settings/bogus",
      hash: "",
      expected: { kind: "settings", tab: "general" },
    },
    {
      name: "/unknown → default chat",
      pathname: "/unknown",
      hash: "",
      expected: { kind: "chat", id: "" },
    },
    {
      name: "trailing slashes stripped",
      pathname: "/git///",
      hash: "",
      expected: { kind: "git", tab: "changes" },
    },
  ];

  it.each(cases)("$name", ({ pathname, hash, expected }) => {
    expect(parseRoute(pathname, hash)).toEqual(expected);
  });
});

// ---------------------------------------------------------------------------
// Proposal tarch-b14-p1: Property-based round-trip test (parseRoute ∘ buildPath)
// ---------------------------------------------------------------------------

describe("parseRoute/buildPath round-trip (property-based)", () => {
  // "git" removed: the retired Git & forges settings tab no longer exists.
  const settingsTabs: SettingsTab[] = ["general", "tools", "permissions", "instructions"];

  // Exhaustive BY CONSTRUCTION: `satisfies Record<DocsTab, true>` makes a
  // missing tab a compile error, so a seventh sub-tab cannot be added to the
  // type without appearing here — and once it is here, the round-trip below
  // fails until parseDocsTab learns it.
  //
  // That chain is not hypothetical. `workflows` was added to DocsTab and to
  // buildPath but not to parseDocsTab, so the app wrote /docs/workflows and read
  // it straight back as /docs: a reload, a back button or a shared link landed
  // on Steering, and nothing failed because this family was absent from the
  // arbitrary below.
  const DOCS_TABS = {
    steering: true,
    skills: true,
    agents: true,
    specs: true,
    hooks: true,
    workflows: true,
  } satisfies Record<DocsTab, true>;
  const docsTabs = Object.keys(DOCS_TABS) as DocsTab[];

  // Arbitrary for a canonical Route (one that round-trips cleanly).
  const arbRoute: fc.Arbitrary<Route> = fc.oneof(
    // chat with non-empty id (empty id maps to "/" which is the default)
    fc
      .string({ minLength: 1, maxLength: 30 })
      .filter((s) => !s.includes("/") && !s.includes("#"))
      .map((id): Route => ({ kind: "chat", id })),
    // git (all three sub-tabs round-trip: changes→/git, prs→/git/prs, …)
    fc.constantFrom<Route>(
      { kind: "git", tab: "changes" },
      { kind: "git", tab: "prs" },
      { kind: "git", tab: "sources" },
    ),
    // history
    fc.constant<Route>({ kind: "history" }),
    // files at the root listing
    fc.constant<Route>({ kind: "files", path: "/" }),
    // files with non-trivial path (segments without slashes or empty parts).
    // Container-absolute, like every path the file surface speaks, so the URL
    // carries the leading slash as an empty first segment (`/files//a/b`) — the
    // shape `/file//a/b` already has for an absolute file.
    fc
      .array(
        fc
          .string({ minLength: 1, maxLength: 15 })
          .filter((s) => !s.includes("/") && !s.includes("#") && s !== "." && s !== ""),
        { minLength: 1, maxLength: 4 },
      )
      .map((segs): Route => ({ kind: "files", path: `/${segs.join("/")}` })),
    // file without line
    fc
      .array(
        fc
          .string({ minLength: 1, maxLength: 15 })
          .filter((s) => !s.includes("/") && !s.includes("#") && s !== ""),
        { minLength: 1, maxLength: 4 },
      )
      .map((segs): Route => ({ kind: "file", path: segs.join("/") })),
    // file with line
    fc
      .tuple(
        fc.array(
          fc
            .string({ minLength: 1, maxLength: 15 })
            .filter((s) => !s.includes("/") && !s.includes("#") && s !== ""),
          { minLength: 1, maxLength: 4 },
        ),
        fc.integer({ min: 1, max: 10000 }),
      )
      .map(([segs, line]): Route => ({ kind: "file", path: segs.join("/"), line })),
    // run without a node — the "the run" spelling, byte-identical to what it
    // has always been
    fc
      .string({ minLength: 1, maxLength: 30 })
      .filter((s) => !s.includes("/") && !s.includes("#"))
      .map((id): Route => ({ kind: "run", id })),
    // run WITH a node: a multi-segment path, which is the case a path segment
    // could not carry
    fc
      .tuple(
        fc
          .string({ minLength: 1, maxLength: 20 })
          .filter((s) => !s.includes("/") && !s.includes("#")),
        fc.array(
          fc
            .string({ minLength: 1, maxLength: 15 })
            .filter((s) => !s.includes("/") && !s.includes("#") && s !== ""),
          { minLength: 1, maxLength: 4 },
        ),
      )
      .map(([id, segs]): Route => ({ kind: "run", id, node: segs.join("/") })),
    // settings
    fc.constantFrom(...settingsTabs).map((tab): Route => ({ kind: "settings", tab })),
    // docs — every sub-tab. "steering" omits the segment (/docs), the rest
    // carry it, and all six must survive the trip.
    fc.constantFrom(...docsTabs).map((tab): Route => ({ kind: "docs", tab })),
  );

  it("buildPath(route) round-trips through parseRoute to the canonical form", () => {
    fc.assert(
      fc.property(arbRoute, (route) => {
        const path = buildPath(route);
        // Split path and hash for parseRoute
        const hashIdx = path.indexOf("#");
        const pathname = hashIdx >= 0 ? path.slice(0, hashIdx) : path;
        const hash = hashIdx >= 0 ? path.slice(hashIdx) : "";
        const parsed = parseRoute(pathname, hash);
        expect(parsed).toEqual(canonicalize(route));
      }),
      { numRuns: 500 },
    );
  });
});

/** Canonicalize a route to the form parseRoute would produce. */
function canonicalize(route: Route): Route {
  switch (route.kind) {
    case "settings":
      // /settings/general → tab "general" (already canonical)
      return route;
    case "file":
      // line <= 0 or undefined → no line property in parsed output
      if (route.line === undefined || route.line <= 0) {
        return { kind: "file", path: route.path };
      }
      return route;
    case "run":
      // An empty node is dropped on both sides: buildPath omits the fragment
      // and parseRoute reads `#node=` as no node. Mirrors the file/line case.
      if (route.node === undefined || route.node === "") {
        return { kind: "run", id: route.id };
      }
      return route;
    default:
      return route;
  }
}

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
// Adversarial percent-encoding property test (tarch-b15-c7-p5)
// ---------------------------------------------------------------------------
describe("parseRoute adversarial inputs (no-throw)", () => {
  it("never throws on arbitrary pathname strings", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.string({ minLength: 0, maxLength: 200 }), (pathname) => {
        const r = parseRoute(pathname, "");
        return r !== null && typeof r === "object" && "kind" in r;
      }),
      { numRuns: 500 },
    );
    expect(result.failed).toBe(false);
  });

  it("never throws on adversarial percent-encoded paths", () => {
    expect.assertions(1);
    const arbPath = fc.oneof(
      fc.string().map((s) => "/" + s),
      fc.string().map((s) => "/chat/" + encodeURIComponent(s)),
      fc
        .string()
        .map((s) => "/file/" + s.replace(/[^%]/g, (c) => "%" + c.charCodeAt(0).toString(16))),
      fc.constant("/%"),
      fc.constant("/%zz"),
      fc.constant("/%0"),
      fc.constant("/chat/%2"),
      fc.constant("/file/\x00bar"),
    );
    const result = fc.check(
      fc.property(arbPath, (pathname) => {
        const r = parseRoute(pathname, "");
        return r !== null && typeof r === "object" && "kind" in r;
      }),
      { numRuns: 300 },
    );
    expect(result.failed).toBe(false);
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
