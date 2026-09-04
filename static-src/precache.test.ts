// The shell precache's two decisions, and one of them is a CROSS-LANGUAGE
// contract with no codegen behind it: cmd/bundle decides which names the build
// emits and `isShellPath` decides which the worker will answer for, so the Go
// writer's own literals are read here rather than restated.
//
// These live in precache.ts rather than in sw.ts precisely so they can be driven:
// sw.ts is coverage-excluded because it runs in ServiceWorkerGlobalScope, and
// everything in it that was worth pinning was pure.
import { describe, it, expect } from "vitest";
import { isShellPath, parseManifest } from "./precache.js";
import bundleGo from "../cmd/bundle/main.go?raw";

describe("isShellPath", () => {
  it("admits a name built from the Go side's own chunk template", () => {
    // The gate's whole claim is that an admitted name pins its own bytes, so the
    // template that MAKES those names is the literal to read: a build reconfigured
    // to `chunks/[name]` would leave this case red rather than quietly handing the
    // cache a name a release can replace.
    const tmpl = /ChunkNames:\s+"([^"]+)"/.exec(bundleGo);
    expect(tmpl, "ChunkNames not found in cmd/bundle/main.go").not.toBeNull();
    const name = String(tmpl?.[1]).replace("[name]", "api-client").replace("[hash]", "4K73XYBF");
    expect(isShellPath(`/${name}.js`), `${name}.js is not admitted`).toBe(true);
    // And the prefix the manifest writer builds each entry with is that same
    // directory, so the two sides cannot drift apart silently.
    const prefix = /assets = append\(assets, "([^"]+)"\+e\.Name\(\)\)/.exec(bundleGo);
    expect(prefix, "the chunk prefix not found in cmd/bundle/main.go").not.toBeNull();
    expect(name.startsWith(String(prefix?.[1]))).toBe(true);
  });

  it("refuses a chunk name that pins nothing", () => {
    // The degrade-safely half: a chunk whose name carries no content hash can have
    // its bytes replaced under it, exactly like the two stable names below, so the
    // cache must leave it to the network's revalidation.
    expect(isShellPath("/chunks/editor.js")).toBe(false);
    expect(isShellPath("/chunks/editor-4k73xybf.js"), "lowercase is not the hash").toBe(false);
    expect(isShellPath("/chunks/editor-4K73XYB.js"), "seven characters is not the hash").toBe(
      false,
    );
    expect(isShellPath("/chunks/editor-4K73XYBF.js.map"), "a sourcemap is not for running").toBe(
      false,
    );
  });

  it("refuses the two stable names, and the build stops offering them", () => {
    // The server marks these `no-cache` because a release replaces their bytes
    // under the same name, so a cache-first answer here pairs a fresh index.html
    // with the previous build's bundle — whose lazy imports name chunk hashes the
    // new server no longer serves. Both halves have to agree, so the Go writer's
    // asset list is read for their absence rather than trusted.
    expect(isShellPath("/app.js")).toBe(false);
    expect(isShellPath("/style.css")).toBe(false);
    const body = /func precacheAssets\(\)[\s\S]*?\n}\n/.exec(bundleGo);
    expect(body, "precacheAssets not found in cmd/bundle/main.go").not.toBeNull();
    for (const name of ["app.js", "style.css"]) {
      expect(body?.[0], `precacheAssets still lists ${name}`).not.toContain(`"${name}"`);
    }
  });

  it("refuses the API surface and the SSE stream", () => {
    // THE POINT OF THE GATE. A handler that decided by asking the cache had to
    // take over every same-origin GET to reach the answer, which put the worker
    // on the critical path for every API read and for the stream's whole lifetime.
    for (const p of ["/api/chats", "/api/settings", "/api/version", "/api/events"]) {
      expect(isShellPath(p), `${p} must not be answered from the shell cache`).toBe(false);
    }
  });

  it("refuses the shell itself, the manifest, and a chunk-shaped non-script", () => {
    // index.html stays no-store, which is what makes a deploy unmaskable; the
    // manifest is fetched no-store by the sync; sw.js is never cached at all.
    expect(isShellPath("/")).toBe(false);
    expect(isShellPath("/index.html")).toBe(false);
    expect(isShellPath("/precache.json")).toBe(false);
    expect(isShellPath("/sw.js")).toBe(false);
    expect(isShellPath("/chunks/notes.md")).toBe(false);
    // Not a prefix match on a sibling directory name.
    expect(isShellPath("/chunkstore/x.js")).toBe(false);
  });
});

describe("parseManifest", () => {
  it("accepts the document the build writes, and roots each asset", () => {
    const m = parseManifest({
      stamp: "0a1b2c3d4e5f6071",
      assets: ["chunks/editor-AAAA1111.js", "chunks/history-BBBB2222.js"],
    });
    expect(m).toEqual({
      stamp: "0a1b2c3d4e5f6071",
      assets: ["/chunks/editor-AAAA1111.js", "/chunks/history-BBBB2222.js"],
    });
  });

  it("accepts an empty asset list", () => {
    // A build that emitted nothing is a valid document: the sync then holds a
    // stamp and caches nothing, rather than treating the read as a failure.
    expect(parseManifest({ stamp: "s", assets: [] })).toEqual({ stamp: "s", assets: [] });
  });

  const rejected: readonly { why: string; doc: unknown }[] = [
    { why: "not an object", doc: "app.js" },
    { why: "null", doc: null },
    { why: "no stamp", doc: { assets: ["app.js"] } },
    { why: "an empty stamp", doc: { stamp: "", assets: ["app.js"] } },
    { why: "a non-string stamp", doc: { stamp: 7, assets: ["app.js"] } },
    { why: "no assets", doc: { stamp: "s" } },
    { why: "assets that are not a list", doc: { stamp: "s", assets: "app.js" } },
    { why: "a non-string asset", doc: { stamp: "s", assets: ["app.js", 7] } },
    { why: "an empty asset name", doc: { stamp: "s", assets: [""] } },
    { why: "an absolute asset name", doc: { stamp: "s", assets: ["/app.js"] } },
    { why: "an asset that climbs", doc: { stamp: "s", assets: ["../secrets.json"] } },
    { why: "an asset that climbs mid-path", doc: { stamp: "s", assets: ["chunks/../../x.js"] } },
  ];

  for (const { why, doc } of rejected) {
    it(`rejects a document with ${why}`, () => {
      // A rejected document must leave the cache as it is. Returning a partial
      // manifest would make the sync prune everything the document omitted.
      expect(parseManifest(doc)).toBeNull();
    });
  }
});
