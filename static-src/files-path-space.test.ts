// ---------------------------------------------------------------------------
// THE FILE SURFACE'S PATH-SPACE CONTRACT.
//
// One space serves the whole /api/file* surface and everything reading it:
// CONTAINER-ABSOLUTE, rooted at "/". Five sites already state it —
// `internal/filebrowse/search.go`'s FileMatch.Path ("the same namespace every
// other /api/file* route speaks"), `workspace.ts`'s module doc, the absolute
// index in `git-status-store.ts`, `navigate.ts`'s openChange, and
// `editor-types.ts`'s routeForPath — and for a year the file browser was the one
// layer that composed something else, so no key it produced could ever match.
//
// These cases pin the JOIN rather than any one side of it: the browser composes
// a row's path with `joinPath`, and the git-status store indexes the same file
// under `${repoAbs}/${relPath}`. They are two modules that never call each other,
// so nothing but a test can hold them to one spelling — which is why the failure
// mode was silent (no error, no empty state, just a letter that never appeared
// on 159 dirty files) and why the number to assert is the composed path itself,
// not a rendered badge.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach } from "vitest";

import { joinPath, parentPath, normalizeDirPath } from "./files-shared.js";
import { FileBrowserState } from "./files-state.js";
import { parseRoute, buildPath } from "./router.js";
import { statusForPath, statusUnder, _setReposForTest } from "./git-status-store.js";
import { absPath, setWorkspaceRoot, _resetForTest as resetWorkspace } from "./workspace.js";
import type { GitRepoStatus, GitFileEntry } from "./git-types.js";

function repo(name: string, files: { path: string; status: string }[]): GitRepoStatus {
  return {
    repo: name,
    is_repo: true,
    branch: "main",
    remote: "origin",
    ahead: 0,
    behind: 0,
    has_dirty: files.length > 0,
    stashes: 0,
    files: files.map((f): GitFileEntry => ({
      path: f.path,
      status: f.status,
      staged: false,
      display: f.path,
    })),
  };
}

/** Walk DOWN from the root listing the way the browser does, through the REAL
 *  state object: a fresh `FileBrowserState` starts on the mounts listing and a
 *  name click is `navigate(joinPath(currentPath, entry.name))`. Starting from
 *  production's own starting path is the whole point — a walk seeded with a
 *  hardcoded "/" passes today, because `joinPath` composes correctly from "/"
 *  and it was the root PATH, not the join, that was in the wrong space. */
function walk(...names: string[]): string {
  const state = new FileBrowserState();
  for (const name of names) {
    state.navigate(joinPath(state.currentPath, name));
  }
  return state.currentPath;
}

beforeEach(() => {
  resetWorkspace();
  _setReposForTest([]);
});

describe("the browser's composed row path is a key statusForPath can match", () => {
  beforeEach(() => {
    setWorkspaceRoot("/workspace");
    _setReposForTest([repo("vibekit", [{ path: "static-src/files.ts", status: "M" }])]);
  });

  it("starts a fresh listing on the filesystem root, in the space it composes into", () => {
    // The root listing is synthetic — `/api/files` answers it for "/" and calls
    // it "not a real directory in the allow-list model" — but its PATH is still
    // the one every child is composed from, so it belongs to the same space.
    expect(new FileBrowserState().currentPath).toBe("/");
  });

  it("returns to that same root on reset", () => {
    const state = new FileBrowserState();
    state.navigate("/workspace/vibekit");
    state.reset();
    expect(state.currentPath).toBe("/");
  });

  it("composes the container-absolute path, walking mount then repo then dir", () => {
    expect(walk("workspace", "vibekit", "static-src", "files.ts")).toBe(
      "/workspace/vibekit/static-src/files.ts",
    );
  });

  it("finds the file's letter under the path the browser composed", () => {
    expect(statusForPath(walk("workspace", "vibekit", "static-src", "files.ts"))).toBe("M");
  });

  it("finds a directory's rollup under the path the browser composed", () => {
    expect(statusUnder(walk("workspace", "vibekit", "static-src"))).toBe("M");
    expect(statusUnder(walk("workspace", "vibekit"))).toBe("M");
  });

  it("hands openChange's absPath a path it passes through untouched", () => {
    // openChange normalises every caller through absPath, because three of them
    // carry the agent's workspace-RELATIVE path. A browser path that is already
    // absolute is returned unchanged; one that is not gets the root joined onto
    // it a second time, which is the /workspace/workspace/... defect.
    const row = walk("workspace", "vibekit", "static-src", "files.ts");
    expect(absPath(row)).toBe(row);
  });

  it("reaches the parent listing's own path by the same spelling", () => {
    const dir = walk("workspace", "vibekit", "static-src");
    expect(parentPath(dir)).toBe("/workspace/vibekit");
    expect(parentPath("/workspace")).toBe("/");
    expect(parentPath("/")).toBe("/");
  });
});

describe("the /files route and the browser agree on the root", () => {
  // The router spells the root as a literal rather than importing FB_ROOT — a
  // feature module is the wrong dependency for a route table — so this is the
  // join that holds the two to one answer.
  it("parses /files to the path a fresh listing starts on", () => {
    expect(parseRoute("/files", "").kind).toBe("files");
    expect((parseRoute("/files", "") as { path: string }).path).toBe(
      new FileBrowserState().currentPath,
    );
  });

  it("builds /files back from that path, with no empty segment", () => {
    expect(buildPath({ kind: "files", path: new FileBrowserState().currentPath })).toBe("/files");
  });

  it("round-trips a directory below the root", () => {
    const dir = walk("workspace", "vibekit");
    const url = buildPath({ kind: "files", path: dir });
    expect((parseRoute(url, "") as { path: string }).path).toBe(dir);
  });
});

describe("a repo AT the workspace root", () => {
  // `repo: "."` means the workspace root IS the repository, so the store keys on
  // the root itself rather than joining a directory name onto it. The browser
  // reaches those files one level shallower.
  beforeEach(() => {
    setWorkspaceRoot("/workspace");
    _setReposForTest([repo(".", [{ path: "notes.md", status: "?" }])]);
  });

  it("matches the letter for a file directly under the mount", () => {
    expect(statusForPath(walk("workspace", "notes.md"))).toBe("?");
  });

  it("rolls the change up to the mount row", () => {
    expect(statusUnder(walk("workspace"))).toBe("?");
  });
});

describe("a mount that is not the workspace", () => {
  // The listing spans an allow-list of granted mounts, so `/config` is a row like
  // any other. It is in no repository, so it carries no letter — and it must not
  // be mangled into a workspace-relative form on the way to a lookup.
  beforeEach(() => {
    setWorkspaceRoot("/workspace");
    _setReposForTest([repo("vibekit", [{ path: "static-src/files.ts", status: "M" }])]);
  });

  it("composes the mount's own absolute path", () => {
    expect(walk("config", "mcp.json")).toBe("/config/mcp.json");
  });

  it("reports no letter for it", () => {
    expect(statusForPath(walk("config", "mcp.json"))).toBe("");
  });
});

describe("normalizeDirPath is the one door into the space", () => {
  // A path arriving from outside the module — the persisted `fb_path`, a /files
  // deep link, the editable path input — may be spelled any of these ways. One
  // normaliser at the entry is what keeps `currentPath` absolute by construction
  // rather than by every caller remembering.
  const cases: [string, string][] = [
    ["", "/"],
    ["/", "/"],
    [".", "/"],
    ["workspace/vibekit", "/workspace/vibekit"],
    ["/workspace/vibekit", "/workspace/vibekit"],
    ["//workspace//", "/workspace"],
    ["  /workspace/vibekit  ", "/workspace/vibekit"],
    ["workspace/vibekit/", "/workspace/vibekit"],
  ];

  for (const [input, expected] of cases) {
    it(`normalises ${JSON.stringify(input)} to ${expected}`, () => {
      expect(normalizeDirPath(input)).toBe(expected);
    });
  }

  it("keeps a normalised path a joinable base", () => {
    expect(joinPath(normalizeDirPath("workspace/vibekit"), "static-src")).toBe(
      "/workspace/vibekit/static-src",
    );
    expect(joinPath(normalizeDirPath(""), "workspace")).toBe("/workspace");
  });
});
