// ---------------------------------------------------------------------------
// The file browser is MULTI-INSTANCE over one shared view element, and middle-click
// is the door that asks for a second one.
//
// Two properties, and they are the two halves of item 7. N browsers each hold their
// own directory, selection and nav trail across a switch — the state is per TAB and
// the DOM is shared, so a switch re-points rather than rebuilding. And a middle click
// opens in the BACKGROUND: the reader stays where they are and the queue builds up
// behind them.
//
// The real files.ts DOM, driven through the same doors the factory drives
// (`showFilesTab` / `bindFilesTab` / `releaseFilesTab`), with `tabs.js` spied so a
// dispatch's SHAPE is the observable. The collapse of two identical dispatches into
// one mutation is the framework's and is pinned in actions/tabs-actions.test.ts; what
// is pinned here is that the two clicks name the same subject, which is the key that
// collapse reads.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach } from "vitest";

// Every mock's state lives in one hoisted block: a `vi.mock` factory is lifted above
// module-level `const`s, and this file value-imports from two of the modules it mocks,
// so a plain const would be read before initialisation.
const h = vi.hoisted(() => {
  // A memoizing registry, so a row built into `$.fbList` is still there to click.
  const els = new Map<string, HTMLElement>();
  function stub(key: string): HTMLElement {
    const existing = els.get(key);
    if (existing !== undefined) {
      return existing;
    }
    const made =
      key === "fbPath" || key === "fbSearchInput"
        ? document.createElement("input")
        : key === "fbBack" || key === "fbFwd" || key === "fbUp"
          ? document.createElement("button")
          : document.createElement("div");
    els.set(key, made);
    return made;
  }
  return {
    els,
    stub,
    openTab: vi.fn((_subject: { kind: string; ref: string; activate?: boolean }) =>
      Promise.resolve("opened"),
    ),
    setFilesRoute: vi.fn(),
    renameTab: vi.fn(),
    openFile: vi.fn(),
    openFileInBackground: vi.fn(),
  };
});

vi.mock("./dom.js", () => ({
  $: new Proxy({} as Record<string, HTMLElement>, { get: (_t, k: string) => h.stub(k) }),
  el: () => document.createElement("div"),
  byId: (id: string) => h.stub(id),
}));
vi.mock("./bus.js", () => ({
  onSSE: undefined,
  onBus: vi.fn(),
  BUS_KEYS_ESCAPE: "escape",
}));

vi.mock("./tabs.js", () => ({
  setGitTab: undefined,
  openGitView: undefined,
  toggleFilesView: vi.fn(() => Promise.resolve()),
  openFilesView: vi.fn(() => Promise.resolve()),
  getActiveTabKind: vi.fn(() => "files"),
  setFilesRoute: h.setFilesRoute,
  renameTab: h.renameTab,
  filesTabIdFor: vi.fn(() => "t-files"),
  openTab: h.openTab,
}));

vi.mock("./editor-openers.js", () => ({
  openFileGitDiff: undefined,
  openFileDiff: undefined,
  openFile: h.openFile,
  openFileInBackground: h.openFileInBackground,
}));
vi.mock("./modals.js", () => ({ closeModal: vi.fn() }));
vi.mock("./confirm.js", () => ({ confirm: vi.fn().mockResolvedValue(true) }));
vi.mock("./upload.js", () => ({ uploadFiles: vi.fn() }));
vi.mock("./icons.js", () => ({
  fileIcon: vi.fn(() => ""),
  FILE_ICONS: {},
  ICON_SAVE_OK: "",
  ICON_SAVE_FAIL: "",
}));
vi.mock("./chat.js", () => ({ attachPathsToActiveChat: vi.fn() }));
vi.mock("./files-browser-drop.js", () => ({ initBrowserDragDrop: vi.fn() }));
vi.mock("./files-search.js", () => ({
  initFilesSearch: vi.fn(),
  resetFilesSearch: vi.fn(),
  closeFilesSearch: vi.fn(),
}));
vi.mock("./files-picker.js", () => ({ setOnUploadComplete: vi.fn() }));
vi.mock("./api-client.js", () => ({
  apiPost: vi.fn(),
  apiGet: vi.fn(),
  apiGetOrError: vi.fn(() => Promise.resolve({ ok: false, status: 0, data: null, error: "" })),
}));
vi.mock("@cplieger/ui-primitives/skeleton", () => ({
  skeletonTiming: () => ({ cancel: vi.fn(), commit: (r: () => void) => r() }),
}));
vi.mock("./scroll.js", () => ({
  scroll: vi.fn(),
  setUserScrolledUp: vi.fn(),
  apiGetTyped: vi.fn(),
}));
vi.mock("./transport.js", () => ({ send: vi.fn() }));
vi.mock("./store.js", () => ({
  activeSession: undefined,
  getActiveId: vi.fn(() => ""),
  newOpID: vi.fn(() => "op-test"),
  get: vi.fn(() => undefined),
  getActive: vi.fn(() => undefined),
  getSessions: vi.fn(() => []),
  tabStatusFor: vi.fn(() => ""),
}));

import { $ } from "./dom.js";
import { apiGet } from "./api-client.js";
import { FB_CHECK, FB_NAME } from "./files-shared.js";
import { bindFilesTab, releaseFilesTab, showFilesTab } from "./files.js";

/** The listing every fetch answers: one folder, one file. */
function listing(): { files: { name: string; isDir: boolean }[]; writable: boolean } {
  return {
    files: [
      { name: "sub", isDir: true },
      { name: "note.txt", isDir: false },
    ],
    writable: true,
  };
}

/** A load, awaited to the point the rows exist. */
async function show(ref: string): Promise<void> {
  showFilesTab(ref);
  await new Promise((r) => setTimeout(r, 0));
}

/** A present-or-throw narrowing, so a missing node names itself instead of
 *  spending a `!` the lint config does not want in a test. */
function must<T>(v: T | null | undefined): T {
  if (v === null || v === undefined) {
    throw new Error("expected the node to exist");
  }
  return v;
}

/** Throws rather than asserting: a helper that spends the test's own declared
 *  assertion budget makes every `expect.assertions` count a lie. */
function rowFor(name: string): HTMLElement {
  const row = [...$.fbList.children].find((c) => (c as HTMLElement).dataset["name"] === name) as
    HTMLElement | undefined;
  if (row === undefined) {
    throw new Error(`no row for ${name}`);
  }
  return row;
}

/** A middle click, as the platform delivers one: mousedown, then auxclick. Returns
 *  the mousedown so a caller can read its `defaultPrevented`. */
function middleClick(target: HTMLElement): MouseEvent {
  const down = new MouseEvent("mousedown", { button: 1, bubbles: true, cancelable: true });
  target.dispatchEvent(down);
  target.dispatchEvent(new MouseEvent("auxclick", { button: 1, bubbles: true, cancelable: true }));
  return down;
}

beforeEach(() => {
  releaseFilesTab("/a");
  releaseFilesTab("/b");
  h.els.clear();
  vi.clearAllMocks();
  vi.mocked(apiGet).mockImplementation(() => Promise.resolve(listing()));
});

describe("two browsers over one view element", () => {
  it("gives each tab its own directory, and a switch re-points rather than merging", async () => {
    expect.assertions(2);
    await show("/a");
    // A navigation in /a's browser: the folder row's own click handler.
    rowFor("sub").querySelector(`.${FB_NAME}`)?.dispatchEvent(new MouseEvent("click"));
    await new Promise((r) => setTimeout(r, 0));
    expect(($.fbPath as HTMLInputElement).value).toBe("/a/sub");

    // Switching to the second browser must show ITS folder, not the first's.
    await show("/b");
    expect(($.fbPath as HTMLInputElement).value).toBe("/b");
  });

  it("keeps each tab's selection across a switch", async () => {
    expect.assertions(2);
    await show("/a");
    const check = rowFor("note.txt").querySelector<HTMLInputElement>(`.${FB_CHECK}`);
    must(check).checked = true;
    check?.dispatchEvent(new Event("change"));

    await show("/b");
    expect(
      rowFor("note.txt").querySelector<HTMLInputElement>(`.${FB_CHECK}`)?.checked,
      "the second browser starts with nothing selected",
    ).toBe(false);

    // Back to the first, whose selection is its own state's and survived.
    await show("/a");
    expect(rowFor("note.txt").querySelector<HTMLInputElement>(`.${FB_CHECK}`)?.checked).toBe(true);
  });

  it("keeps each tab's nav trail, so Back never walks into another browser's", async () => {
    expect.assertions(2);
    await show("/a");
    rowFor("sub").querySelector(`.${FB_NAME}`)?.dispatchEvent(new MouseEvent("click"));
    await new Promise((r) => setTimeout(r, 0));

    // /b is freshly opened, so both of its buttons are disabled by construction.
    await show("/b");
    expect(($.fbBack as HTMLButtonElement).disabled).toBe(true);

    // /a has somewhere to go back to, and that is still true after the round trip.
    await show("/a");
    expect(($.fbBack as HTMLButtonElement).disabled).toBe(false);
  });
});

describe("middle-click opens in the background", () => {
  it("opens a folder row in an unfocused browser at that folder", async () => {
    expect.assertions(2);
    await show("/a");
    middleClick(rowFor("sub"));
    expect(h.openTab).toHaveBeenCalledWith({ kind: "files", ref: "/a/sub", activate: false });
    // NOTHING steals focus, structurally: `activate: false` never reaches
    // activateTab, so no view swap and no pushRoute runs for the new row.
    expect(h.setFilesRoute).not.toHaveBeenCalled();
  });

  it("leaves the on-screen listing exactly where it was", async () => {
    expect.assertions(2);
    await show("/a");
    middleClick(rowFor("sub"));
    await new Promise((r) => setTimeout(r, 0));
    expect(($.fbPath as HTMLInputElement).value).toBe("/a");
    expect(rowFor("note.txt")).toBeDefined();
  });

  it("opens a file row in an unfocused editor tab", async () => {
    expect.assertions(3);
    await show("/a");
    middleClick(rowFor("note.txt"));
    expect(h.openFileInBackground).toHaveBeenCalledWith("/a/note.txt");
    // The normal row-open path must NOT also fire. A middle click produces no
    // `click` in any current engine, so the two are disjoint by construction.
    expect(h.openFile).not.toHaveBeenCalled();
    expect(h.setFilesRoute).not.toHaveBeenCalled();
  });

  it("opens the .. row's parent in the background too", async () => {
    expect.assertions(1);
    await show("/a/deep");
    const parent = [...$.fbList.children].find(
      (c) => (c as HTMLElement).dataset["name"] === undefined,
    ) as HTMLElement;
    middleClick(parent);
    expect(h.openTab).toHaveBeenCalledWith({ kind: "files", ref: "/a", activate: false });
  });

  it("cancels the mousedown default, which is what suppresses autoscroll and paste", async () => {
    expect.assertions(1);
    await show("/a");
    // The absence of autoscroll is not observable in any headless engine; the
    // cancelled default is, and it is the mechanism. `.fb-list-wrap` IS a scroller,
    // which is why preventDefault on auxclick alone would not be enough.
    expect(middleClick(rowFor("sub")).defaultPrevented).toBe(true);
  });

  it("does nothing at all for the LEFT button", async () => {
    expect.assertions(1);
    await show("/a");
    rowFor("sub").dispatchEvent(
      new MouseEvent("auxclick", { button: 0, bubbles: true, cancelable: true }),
    );
    expect(h.openTab).not.toHaveBeenCalled();
  });

  it("is excluded on the checkbox", async () => {
    expect.assertions(2);
    await show("/a");
    const check = rowFor("sub").querySelector<HTMLElement>(`.${FB_CHECK}`);
    middleClick(must(check));
    expect(h.openTab).not.toHaveBeenCalled();
    expect(h.openFileInBackground).not.toHaveBeenCalled();
  });

  it("is excluded on the git badge", async () => {
    expect.assertions(2);
    await show("/a");
    // The badge is only built for a path with a status, so this asserts the GATE
    // rather than the badge's presence: a synthetic child carrying the class is
    // exactly what the `closest` test looks for.
    const row = rowFor("sub");
    const badge = document.createElement("span");
    badge.className = "fb-git-letter";
    row.appendChild(badge);
    middleClick(badge);
    expect(h.openTab).not.toHaveBeenCalled();
    expect(h.openFileInBackground).not.toHaveBeenCalled();
  });

  it("names ONE subject for two clicks on one row, which is what the dedupe reads", async () => {
    expect.assertions(2);
    await show("/a");
    const row = rowFor("sub");
    middleClick(row);
    middleClick(row);
    // Two dispatches, one subject. The collapse itself is the action framework's
    // `dedupe` on (kind, ref) and is pinned in actions/tabs-actions.test.ts; what
    // this browser owes is a key those two dispatches agree on.
    expect(h.openTab).toHaveBeenCalledTimes(2);
    expect(new Set(h.openTab.mock.calls.map((c) => JSON.stringify(c[0]))).size).toBe(1);
  });

  it("survives a bind with no load, so a queued row is clickable before its fetch", async () => {
    expect.assertions(1);
    await show("/a");
    bindFilesTab("/a");
    middleClick(rowFor("sub"));
    expect(h.openTab).toHaveBeenCalledWith({ kind: "files", ref: "/a/sub", activate: false });
  });
});
