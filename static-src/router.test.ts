// @vitest-environment happy-dom
import { describe, it, expect } from "vitest";
import * as fc from "fast-check";
import { parseRoute, buildPath, type Route, type SettingsTab } from "./router";

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
    { name: "/git", pathname: "/git", hash: "", expected: { kind: "git" } },
    { name: "/history", pathname: "/history", hash: "", expected: { kind: "history" } },
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
      name: "/settings/git",
      pathname: "/settings/git",
      hash: "",
      expected: { kind: "settings", tab: "git" },
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
    { name: "trailing slashes stripped", pathname: "/git///", hash: "", expected: { kind: "git" } },
  ];

  it.each(cases)("$name", ({ pathname, hash, expected }) => {
    expect(parseRoute(pathname, hash)).toEqual(expected);
  });
});

// ---------------------------------------------------------------------------
// Proposal tarch-b14-p1: Property-based round-trip test (parseRoute ∘ buildPath)
// ---------------------------------------------------------------------------

describe("parseRoute/buildPath round-trip (property-based)", () => {
  const settingsTabs: SettingsTab[] = ["general", "tools", "permissions", "instructions", "git"];

  // Arbitrary for a canonical Route (one that round-trips cleanly).
  const arbRoute: fc.Arbitrary<Route> = fc.oneof(
    // chat with non-empty id (empty id maps to "/" which is the default)
    fc
      .string({ minLength: 1, maxLength: 30 })
      .filter((s) => !s.includes("/") && !s.includes("#"))
      .map((id): Route => ({ kind: "chat", id })),
    // git
    fc.constant<Route>({ kind: "git" }),
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
