// @vitest-environment happy-dom
import { describe, it, expect } from "vitest";
import * as fc from "fast-check";
import { parseRoute, buildPath, type Route, type SettingsTab, type DocsTab } from "./router";

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
      name: "/files → workspace root",
      pathname: "/files",
      hash: "",
      expected: { kind: "files", path: "." },
    },
    {
      name: "/files/ → workspace root",
      pathname: "/files/",
      hash: "",
      expected: { kind: "files", path: "." },
    },
    {
      name: "/files/src/main.go",
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
    // files with path "." (root)
    fc.constant<Route>({ kind: "files", path: "." }),
    // files with non-trivial path (segments without slashes or empty parts)
    fc
      .array(
        fc
          .string({ minLength: 1, maxLength: 15 })
          .filter((s) => !s.includes("/") && !s.includes("#") && s !== "." && s !== ""),
        { minLength: 1, maxLength: 4 },
      )
      .map((segs): Route => ({ kind: "files", path: segs.join("/") })),
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
    default:
      return route;
  }
}

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
