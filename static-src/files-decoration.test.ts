// ---------------------------------------------------------------------------
// Tests for the file browser's change DECORATION — the git letter on a row. Not
// the browser's navigation or CRUD.
//
// Each case pins a decision the decoration rests on:
//   - a repaint is in place, so a 15s poll cannot blow away the selection or the
//     scroll position of a listing the user is working in
//   - a directory row carries the worst status BENEATH it, or a change three
//     levels down is invisible until you walk into it
//   - only a file's letter is clickable; a directory rollup has no single diff
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";

// Leaves that reach for DOM or state this module does not own.
vi.mock("./scroll.js", () => ({
  setUserScrolledUp: vi.fn(),
  scrollToBottom: vi.fn(),
  initScroll: vi.fn(),
}));
vi.mock("./editor-openers.js", () => ({
  openFile: vi.fn(),
  openFileDiff: undefined,
  openFileGitDiff: vi.fn(),
  // files.ts imports this for the middle-click background open. Browser Mode links
  // the module for real, so a name absent from the factory fails COLLECTION rather
  // than a test.
  openFileInBackground: vi.fn(),
}));
// chat.ts transitively mounts the transcript view at import time (#messages).
vi.mock("./chat.js", () => ({ attachPathsToActiveChat: vi.fn() }));

import { _repaintRowsForTest } from "./files.js";
import { FB_ROOT, joinPath } from "./files-shared.js";
import { openFileGitDiff } from "./editor-openers.js";
import { _setReposForTest } from "./git-status-store.js";
import { setWorkspaceRoot, _resetForTest as resetWorkspace } from "./workspace.js";
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

/** One row, shaped exactly as entryRow builds it: the decoration reads only
 *  `data-path` / `data-is-dir` and inserts before `.fb-meta`.
 *
 *  The path is COMPOSED the way entryRow composes it — `joinPath` walked down
 *  from the browser's own root listing — rather than written as a literal. That
 *  is not ceremony: these cases were green for a year while every row the shipped
 *  browser produced carried a ROOTLESS path (`w/r/a/b.go`) that no key in the
 *  git-status index could match, because the fixture supplied a space the
 *  composition did not. Composing it here means a regression in the space fails
 *  these DOM cases too, not only the contract test. */
function row(segments: string[], isDir = false): HTMLElement {
  let path = FB_ROOT;
  for (const seg of segments) {
    path = joinPath(path, seg);
  }
  const r = document.createElement("div");
  r.className = "fb-row";
  r.dataset["path"] = path;
  r.dataset["isDir"] = String(isDir);
  r.dataset["name"] = path.slice(path.lastIndexOf("/") + 1);
  const meta = document.createElement("span");
  meta.className = "fb-meta";
  r.appendChild(meta);
  return r;
}

function list(): HTMLElement {
  return document.getElementById("fb-list") as HTMLElement;
}

function letters(): string[] {
  return [...list().querySelectorAll(".fb-git-letter")].map((n) => n.textContent ?? "");
}

beforeEach(() => {
  document.body.replaceChildren();
  const l = document.createElement("div");
  l.id = "fb-list";
  document.body.appendChild(l);
  resetWorkspace();
  // /api/git/status-all names each repo by a bare directory under the workspace,
  // so the absolute keys only exist once the handshake has stated the root.
  setWorkspaceRoot("/w");
  _setReposForTest([]);
  vi.mocked(openFileGitDiff).mockClear();
});

describe("git letter decoration", () => {
  it("puts the file's own letter on its row, before the meta column", () => {
    _setReposForTest([repo("r", [{ path: "a/b.go", status: "M" }])]);
    list().append(row(["w", "r", "a", "b.go"]));
    _repaintRowsForTest();
    expect(letters()).toEqual(["M"]);
    expect(list().firstElementChild?.children[0]?.className).toContain("fb-git-letter");
  });

  it("reuses the app's git-st-* colour vocabulary rather than a browser-local one", () => {
    _setReposForTest([repo("r", [{ path: "a.go", status: "M" }])]);
    list().append(row(["w", "r", "a.go"]));
    _repaintRowsForTest();
    expect(list().querySelector(".fb-git-letter")?.className).toContain("git-st-m");
  });

  it("gives a directory the WORST letter beneath it", () => {
    _setReposForTest([
      repo("r", [
        { path: "a/untracked.go", status: "?" },
        { path: "a/conflict.go", status: "U" },
      ]),
    ]);
    list().append(row(["w", "r", "a"], true));
    _repaintRowsForTest();
    expect(letters()).toEqual(["U"]);
  });

  it("leaves a clean row undecorated", () => {
    _setReposForTest([repo("r", [{ path: "a.go", status: "M" }])]);
    list().append(row(["w", "r", "clean.go"]));
    _repaintRowsForTest();
    expect(letters()).toEqual([]);
  });

  it("opens the file's diff when its letter is clicked, without selecting the row", () => {
    _setReposForTest([repo("r", [{ path: "a.go", status: "M" }])]);
    const r = row(["w", "r", "a.go"]);
    let rowClicks = 0;
    r.addEventListener("click", () => {
      rowClicks++;
    });
    list().append(r);
    _repaintRowsForTest();
    const badge = r.querySelector<HTMLElement>(".fb-git-letter");
    expect(badge?.classList.contains("fb-git-clickable")).toBe(true);
    badge?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(vi.mocked(openFileGitDiff)).toHaveBeenCalledWith("/w/r/a.go", "HEAD");
    // stopPropagation: clicking the badge must not also toggle the row.
    expect(rowClicks).toBe(0);
  });

  it("does not make a directory's rollup letter clickable — it has no one diff", () => {
    _setReposForTest([repo("r", [{ path: "a/b.go", status: "M" }])]);
    list().append(row(["w", "r", "a"], true));
    _repaintRowsForTest();
    const badge = list().querySelector<HTMLElement>(".fb-git-letter");
    expect(badge?.classList.contains("fb-git-clickable")).toBe(false);
    badge?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(vi.mocked(openFileGitDiff)).not.toHaveBeenCalled();
  });

  it("repaints in place: the same row elements survive, so selection is untouched", () => {
    _setReposForTest([repo("r", [{ path: "a.go", status: "M" }])]);
    const r = row(["w", "r", "a.go"]);
    r.classList.add("fb-row-selected");
    list().append(r);
    _repaintRowsForTest();
    expect(list().firstElementChild).toBe(r);
    expect(r.classList.contains("fb-row-selected")).toBe(true);
  });

  it("replaces the letter on the next poll rather than stacking a second one", () => {
    _setReposForTest([repo("r", [{ path: "a.go", status: "M" }])]);
    list().append(row(["w", "r", "a.go"]));
    _repaintRowsForTest();
    _setReposForTest([repo("r", [{ path: "a.go", status: "D" }])]);
    _repaintRowsForTest();
    expect(letters()).toEqual(["D"]);
  });

  it("drops the letter when the tree goes clean", () => {
    _setReposForTest([repo("r", [{ path: "a.go", status: "M" }])]);
    list().append(row(["w", "r", "a.go"]));
    _repaintRowsForTest();
    _setReposForTest([repo("r", [])]);
    _repaintRowsForTest();
    expect(letters()).toEqual([]);
  });
});
