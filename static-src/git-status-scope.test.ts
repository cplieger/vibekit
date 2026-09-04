// What a scoped git-status read puts on the wire, and what it dedups against.
//
// `?paths=` is what makes a trigger per edit affordable: an unscoped scan is ~110
// git subprocesses over 54 worktrees, and the repositories that own a turn's
// changed files are normally one. The server resolves path→repository (`ownerOf`),
// so the client's whole share of the contract is the query it builds and the key it
// collapses reads under — and both have a failure mode that is invisible on screen,
// which is why they are pinned here rather than left to the store's index tests.
//
// The action layer is mocked at the DEFINITION, so the real `request` and `dedupe`
// builders run and this reads their output. git-status-triggers.test.ts counts reads
// with a mock that discards them; this one keeps them.
import { describe, it, expect, vi } from "vitest";

const { captured } = vi.hoisted(() => ({
  captured: { paths: [] as string[], keys: [] as string[] },
}));

interface ScopeArgs {
  paths?: readonly string[];
}
interface ApiCfg {
  request: (a: ScopeArgs) => { method: string; path: string };
}
interface DefCfg {
  dedupe: (a: ScopeArgs) => string;
  run: (a: ScopeArgs) => Promise<{ repos: never[] }>;
}

vi.mock("./actions/index.js", () => ({
  apiAction: (cfg: ApiCfg) => ({
    dispatch: (args: ScopeArgs) => {
      captured.paths.push(cfg.request(args).path);
      return Promise.resolve({ repos: [] });
    },
  }),
  defineAction: (cfg: DefCfg) => ({
    dispatch: (args: ScopeArgs) => {
      captured.keys.push(cfg.dedupe(args));
      return cfg.run(args);
    },
  }),
  pollAction: () => undefined,
}));
vi.mock("./bus.js", () => ({ onSSE: () => undefined }));

const store = await import("./git-status-store.js");

/** The URL the last read requested. */
async function read(paths?: readonly string[]): Promise<string> {
  captured.paths.length = 0;
  await store.refreshGitStatus(paths);
  return captured.paths[captured.paths.length - 1] ?? "";
}

/** The dedupe key the last read collapsed under. */
async function key(paths?: readonly string[]): Promise<string> {
  captured.keys.length = 0;
  await store.refreshGitStatus(paths);
  return captured.keys[captured.keys.length - 1] ?? "";
}

describe("the URL a git-status read builds", () => {
  it("asks for the whole tree when the caller can name nothing", async () => {
    expect(await read()).toBe("/api/git/status-all");
    expect(await read([])).toBe("/api/git/status-all");
  });

  it("names the paths it was given, comma-joined and encoded once", async () => {
    expect(await read(["subflux/main.go", "vibekit/app.ts"])).toBe(
      "/api/git/status-all?paths=subflux%2Fmain.go%2Cvibekit%2Fapp.ts",
    );
  });

  // A path with a comma in it would otherwise split into two paths the server
  // resolves to nothing, and the repository that really changed would go unscanned
  // while the read reported success.
  it("encodes a path that contains the separator", async () => {
    const url = await read(["repo/a,b.txt"]);
    expect(url).toBe("/api/git/status-all?paths=repo%2Fa%2Cb.txt");
    expect(decodeURIComponent(url.slice(url.indexOf("=") + 1))).toBe("repo/a,b.txt");
  });

  // A completed tool call routinely reports the same file in `locations` and again
  // in `diffs[].path`, so duplicates are the normal input rather than an edge case.
  it("drops duplicates and empty entries rather than sending them", async () => {
    expect(await read(["a/x.go", "a/x.go", "", "b/y.go"])).toBe(
      "/api/git/status-all?paths=a%2Fx.go%2Cb%2Fy.go",
    );
  });

  // The server caps the count too and its cap is the one that binds; this stops a
  // turn that touched hundreds of files building a URL most of which is discarded.
  it("caps how many paths it names", async () => {
    const many = Array.from({ length: 200 }, (_, i) => `r${String(i)}/f.go`);
    const url = await read(many);
    const sent = decodeURIComponent(url.slice(url.indexOf("=") + 1)).split(",");
    expect(sent).toHaveLength(64);
    expect(sent[0]).toBe("r0/f.go");
  });

  // A scope of nothing but empties is not a scope. Sending `?paths=` would ask the
  // server for a scoped read whose resolved repository set is empty, which it
  // answers from the snapshot without scanning at all — so the tree would never be
  // re-read for a caller that meant "something changed, I don't know where".
  it("falls back to the whole tree when every path was empty", async () => {
    expect(await read(["", ""])).toBe("/api/git/status-all");
  });
});

describe("the key a git-status read dedups under", () => {
  // THE REGRESSION THIS EXISTS FOR. The key used to be blanket-true, which was
  // right while every read was unscoped and is wrong now: two reads naming
  // different repositories would collapse into one, the second repository would
  // never be scanned, and its badge and file decorations would stay stale — the
  // exact defect scoping was added to fix, reintroduced on the client side.
  it("gives two different scopes two different keys", async () => {
    const a = await key(["subflux/main.go"]);
    const b = await key(["vibekit/app.ts"]);
    expect(a).not.toBe(b);
  });

  it("gives one scope one key, so a burst of identical reads is one read", async () => {
    expect(await key(["a/x.go"])).toBe(await key(["a/x.go"]));
    // Order and duplicates do not make a new scope: the same files reported by
    // `locations` and by `diffs` in either order are the same repositories.
    expect(await key(["a/x.go", "b/y.go"])).toBe(await key(["a/x.go", "b/y.go", "a/x.go"]));
  });

  it("gives the unscoped read its own key, apart from every scoped one", async () => {
    const whole = await key();
    expect(whole).toBe(await key([]));
    expect(whole).not.toBe(await key(["a/x.go"]));
  });
});
