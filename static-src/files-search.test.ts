// Find in files: the request it builds, the honesty of its note, and the
// second-press escape hatch that is the a11y justification for overriding Ctrl-F
// at all.
import { describe, it, expect, vi, beforeAll, afterAll, beforeEach, afterEach } from "vitest";
import type { FileMatch, FileSearchResult } from "./wire/types.gen.js";

/** The wire, as the test controls it: what the server would have answered. A
 *  fixture typed as the reply so a case cannot build one the decoder refuses by
 *  accident; the one case that wants a refusal forges it deliberately. */
const apiGet = vi.fn<(url: string, signal?: AbortSignal) => Promise<unknown>>();
const openAtLine = vi.fn();
const activateBrowser = vi.fn();
const openFolder = vi.fn();
const shortcutsSheet = vi.fn();

vi.mock("./api-client.js", () => ({
  apiGet: (url: string, signal?: AbortSignal) => apiGet(url, signal),
  apiGetOrError: vi.fn(() => Promise.resolve({ ok: false, status: 0, data: null, error: "" })),
  CancellableSlot: class {
    private ctrl: AbortController | null = null;
    start(): AbortSignal {
      this.ctrl?.abort();
      this.ctrl = new AbortController();
      return this.ctrl.signal;
    }
    abort(): void {
      this.ctrl?.abort();
      this.ctrl = null;
    }
  },
  // The production shape, over the wire fixture above: a fixture passes through
  // the REAL generated decoder, so a reply the bundle cannot read collapses to
  // null here exactly as it does in production.
  apiGetTyped: async (
    url: string,
    decoder: (v: unknown) => unknown,
    signal?: AbortSignal,
  ): Promise<unknown> => {
    const raw = await apiGet(url, signal);
    if (raw === null) {
      return null;
    }
    try {
      return decoder(raw);
    } catch {
      return null;
    }
  },
}));
vi.mock("./navigate.js", () => ({
  openAtLine: (path: string, line?: number) => openAtLine(path, line),
}));
// ICON_CLOSE_UI is inert: search-shell.ts imports it, so ESM linking needs the name.
vi.mock("./icons.js", () => ({ fileIcon: () => "<svg></svg>", ICON_CLOSE_UI: "<svg></svg>" }));
vi.mock("./icon-el.js", () => ({ iconEl: () => document.createElement("span") }));
// The whole tab store, inert. Complete rather than the two names this module reads,
// because Browser Mode links ESM for real and tabs.ts drags a dozen modules behind
// it — a partial factory here fails the whole file's COLLECTION rather than one case.
vi.mock("./tabs.js", async () => ({
  ...(await import("./__test-helpers__/tabs-mock.js")).tabsMock(),
  getActiveTabId: () => activeTabID,
  getActiveTabKind: () => activeTabKind,
}));
// keys.ts's only non-bus dependency, so its real listener can be installed below.
vi.mock("./modals.js", () => ({ closeTopModal: () => false, openModal: vi.fn() }));

/** The tab the bar opens over, and the kind the type-ahead gate reads. */
let activeTabID = "files-a";
let activeTabKind: string | null = "files";

const mod = await import("./files-search.js");
const { initKeyboardShortcuts } = await import("./keys.js");
const bus = await import("./bus.js");
const {
  searchURL,
  hitLabel,
  hitKey,
  initFilesSearch,
  openFilesSearch,
  closeFilesSearch,
  resetFilesSearch,
  handleFindInFilesHotkey,
  handleFilesTypeAhead,
  _isFilesSearchOpen,
  _filesSearchBar,
  _filesSearchResults,
} = mod;

/** A reply whose `matched` defaults to the row count: nothing cut unless a case
 *  says so. */
function result(over: Partial<FileSearchResult> = {}): FileSearchResult {
  const matches = over.matches ?? [];
  return { matches, scanned: 0, matched: matches.length, truncated: false, ...over };
}

function input(): HTMLInputElement {
  const el = document.getElementById("fb-search-input");
  if (!(el instanceof HTMLInputElement)) {
    throw new Error("search input not built");
  }
  return el;
}

function ctrlF(): KeyboardEvent {
  return new KeyboardEvent("keydown", { key: "f", ctrlKey: true, cancelable: true });
}

/** Let the debounce fire and the awaited fetch settle. */
async function settle(): Promise<void> {
  await vi.advanceTimersByTimeAsync(150);
  await Promise.resolve();
}

let searchPath = "workspace/src";

// The fixture DOM and the wiring are built ONCE, because the module attaches its
// lazily-built bar to this DOM and keeps a reference to it — exactly as it does
// against the real files view, which also outlives every open and close. Tearing
// the body down per test would leave that reference pointing at a detached node,
// which is a property of the fixture rather than of the module.
beforeAll(() => {
  document.body.innerHTML = `
    <div class="fb-list-wrap">
      <div class="fb-list" id="fb-list"></div>
    </div>
    <button type="button" id="find-btn" aria-pressed="false"></button>`;
  initFilesSearch({ getSearchPath: () => searchPath, activateBrowser, openFolder });
  // keys.ts's REAL document listener, installed once (it guards against a second
  // install), so the `?` case below measures the actual interaction rather than a
  // stand-in for it. It acts only on Escape, a bare `?` and Ctrl chords.
  initKeyboardShortcuts({
    newChat: vi.fn(),
    toggleShell: vi.fn(),
    toggleFiles: vi.fn(),
    toggleGit: vi.fn(),
    toggleSettings: vi.fn(),
    sendMessage: vi.fn(),
    showShortcuts: shortcutsSheet,
  });
  document.addEventListener("keydown", handleFilesTypeAhead);
});

afterAll(() => {
  document.body.innerHTML = "";
});

beforeEach(() => {
  vi.useFakeTimers();
  apiGet.mockReset();
  apiGet.mockResolvedValue(result());
  openAtLine.mockReset();
  activateBrowser.mockReset();
  openFolder.mockReset();
  searchPath = "workspace/src";
  activeTabID = "files-a";
  activeTabKind = "files";
  resetFilesSearch();
  // This fixture loads no stylesheet, so `.hidden` paints nothing and a closed bar
  // keeps whatever focus the previous case left in its field. The real one is
  // display:none, which the engine blurs — so the blur restores the state a closed
  // bar is actually in, rather than papering over a production behaviour.
  if (document.activeElement instanceof HTMLElement) {
    document.activeElement.blur();
  }
});

afterEach(() => {
  resetFilesSearch();
  vi.useRealTimers();
});

describe("searchURL", () => {
  it("asks the files search endpoint with the encoded root and query", () => {
    expect(searchURL("workspace/a b", "func Foo")).toBe(
      "/api/files/search?path=workspace%2Fa+b&q=func+Foo",
    );
  });

  it("omits case unless asked, so an unset toggle keeps the server default", () => {
    expect(searchURL("workspace", "x")).not.toContain("case=");
    expect(searchURL("workspace", "x", { caseSensitive: true })).toContain("case=1");
  });

  it("omits an empty glob field rather than sending a pattern nothing typed", () => {
    const url = searchURL("workspace", "x", { include: "", exclude: "node_modules" });
    expect(url).not.toContain("include=");
    expect(url).toContain("exclude=node_modules");
  });
});

describe("hitLabel", () => {
  it("shows a path relative to the folder searched", () => {
    expect(hitLabel("/workspace/src", "/workspace/src/a/b.go")).toBe("a/b.go");
  });

  it("falls back to the absolute path for a root search, which spans mounts", () => {
    expect(hitLabel("/", "/config/notes/x.md")).toBe("/config/notes/x.md");
    expect(hitLabel("", "/config/notes/x.md")).toBe("/config/notes/x.md");
  });

  it("keeps the absolute form for a path outside the folder searched", () => {
    expect(hitLabel("/workspace/src", "/config/x.md")).toBe("/config/x.md");
  });
});

describe("hitKey", () => {
  it("separates two hits that differ only by line", () => {
    const a = hitKey({ path: "/w/a.go", excerpt: "", kind: "content", line: 1 });
    const b = hitKey({ path: "/w/a.go", excerpt: "", kind: "content", line: 2 });
    expect(a).not.toBe(b);
  });

  it("separates hits whose paths differ only where a colon falls", () => {
    // A colon is a legal filename character, which is why the composite goes
    // through keyenc instead of a template literal.
    const a = hitKey({ path: "/w/a:1", excerpt: "", kind: "content", line: 2 });
    const b = hitKey({ path: "/w/a", excerpt: "", kind: "content", line: 12 });
    expect(a).not.toBe(b);
  });
});

describe("the search bar", () => {
  it("hides the directory listing while it is showing results", () => {
    openFilesSearch();
    expect(document.getElementById("fb-list")?.classList.contains("hidden")).toBe(true);
    expect(_filesSearchResults().classList.contains("hidden")).toBe(false);
    closeFilesSearch();
    expect(document.getElementById("fb-list")?.classList.contains("hidden")).toBe(false);
  });

  it("brings the browser into view, so Ctrl-F from an editor tab has somewhere to land", () => {
    openFilesSearch();
    expect(activateBrowser).toHaveBeenCalled();
  });

  it("renders one row per hit and opens the editor at the line", async () => {
    apiGet.mockResolvedValue(
      result({
        scanned: 3,
        matches: [
          { path: "/workspace/src/a.go", excerpt: "func Foo()", kind: "content", line: 12 },
          { path: "/workspace/src/b.go", excerpt: "Foo()", kind: "content", line: 4 },
        ],
      }),
    );
    openFilesSearch();
    input().value = "Foo";
    input().dispatchEvent(new Event("input"));
    await settle();

    const rows = _filesSearchResults().querySelectorAll(".fb-search-hit");
    expect(rows).toHaveLength(2);
    expect(document.getElementById("fb-search-note")?.textContent).toBe(
      "2 matches; 3 files scanned",
    );
    (rows[0] as HTMLElement).click();
    expect(openAtLine).toHaveBeenCalledWith("/workspace/src/a.go", 12);
  });

  // --- The note is the reply's tally through the shared grammar -----------
  //
  // The words are copy.ts's and pinned there; what these cases pin is which FACTS
  // this surface hands it, because a scan that stopped or a list that was cut
  // has to be stated over an answer the reader would otherwise read as whole.

  async function noteFor(res: FileSearchResult): Promise<string | null | undefined> {
    apiGet.mockResolvedValue(res);
    openFilesSearch();
    input().value = "needle";
    input().dispatchEvent(new Event("input"));
    await settle();
    return document.getElementById("fb-search-note")?.textContent;
  }

  it("states the cut when the server counted more lines than it sent rows", async () => {
    const matches: FileMatch[] = Array.from({ length: 20 }, (_, i) => ({
      path: "/workspace/src/many.txt",
      excerpt: "needle",
      kind: "content",
      line: i + 1,
    }));
    expect(await noteFor(result({ matches, matched: 25, scanned: 1 }))).toBe(
      "20 of 25 matches shown; 1 file scanned",
    );
  });

  it("says a stopped scan did not read everything, beside the rows it did find", async () => {
    const matches: FileMatch[] = [
      { path: "/workspace/src/a.go", excerpt: "needle", kind: "content", line: 1 },
    ];
    expect(await noteFor(result({ matches, scanned: 5000, truncated: true }))).toBe(
      "1 match; 5,000 files scanned, not everything was read",
    );
  });

  it("says a stopped scan did not read everything when it found NOTHING, so an empty answer cannot imply the text is nowhere", async () => {
    expect(await noteFor(result({ scanned: 5000, truncated: true }))).toBe(
      "No matches in 5,000 files; not everything was searched",
    );
  });

  it("says plainly that nothing matched when the scan finished", async () => {
    expect(await noteFor(result({ scanned: 12 }))).toBe("No matches");
  });

  it("coalesces a burst of keystrokes into one request for the query in the box", async () => {
    openFilesSearch();
    input().value = "on";
    input().dispatchEvent(new Event("input"));
    input().value = "one";
    input().dispatchEvent(new Event("input"));
    await settle();
    expect(apiGet).toHaveBeenCalledTimes(1);
    expect(apiGet.mock.calls[0]?.[0]).toContain("q=one");
  });

  it("aborts the previous request when a new query starts", async () => {
    const signals: (AbortSignal | undefined)[] = [];
    apiGet.mockImplementation((_url, signal) => {
      signals.push(signal);
      return Promise.resolve(result());
    });
    openFilesSearch();
    input().value = "one";
    input().dispatchEvent(new Event("input"));
    await settle();
    input().value = "two";
    input().dispatchEvent(new Event("input"));
    await settle();

    expect(signals).toHaveLength(2);
    expect(signals[0]?.aborted).toBe(true);
    expect(signals[1]?.aborted).toBe(false);
  });

  it("drops a stale reply that lands after a newer query, rather than repainting with it", async () => {
    let releaseFirst: (() => void) | undefined;
    apiGet.mockImplementation((url) =>
      url.includes("q=one")
        ? new Promise((resolve) => {
            releaseFirst = () => {
              resolve(
                result({
                  scanned: 1,
                  matches: [
                    { path: "/workspace/src/stale.go", excerpt: "s", kind: "content", line: 1 },
                  ],
                }),
              );
            };
          })
        : Promise.resolve(
            result({
              scanned: 2,
              matches: [
                { path: "/workspace/src/fresh.go", excerpt: "f", kind: "content", line: 3 },
              ],
            }),
          ),
    );
    openFilesSearch();
    input().value = "one";
    input().dispatchEvent(new Event("input"));
    await settle();
    input().value = "two";
    input().dispatchEvent(new Event("input"));
    await settle();
    // The first query's answer arrives last. It must not land.
    releaseFirst?.();
    await settle();

    const rows = _filesSearchResults().querySelectorAll(".fb-search-hit");
    expect(rows).toHaveLength(1);
    expect((rows[0] as HTMLElement).dataset["path"]).toBe("/workspace/src/fresh.go");
  });

  it("clears the results without asking the server when the query empties", async () => {
    apiGet.mockResolvedValue(
      result({
        scanned: 1,
        matches: [{ path: "/workspace/src/a.go", excerpt: "x", kind: "content", line: 1 }],
      }),
    );
    openFilesSearch();
    input().value = "x";
    input().dispatchEvent(new Event("input"));
    await settle();
    expect(_filesSearchResults().querySelectorAll(".fb-search-hit")).toHaveLength(1);

    apiGet.mockClear();
    input().value = "";
    input().dispatchEvent(new Event("input"));
    await settle();
    expect(apiGet).not.toHaveBeenCalled();
    expect(_filesSearchResults().querySelectorAll(".fb-search-hit")).toHaveLength(0);
    expect(document.getElementById("fb-search-note")?.textContent).toBe("");
  });

  it("sends case=1 once the Aa toggle is latched", async () => {
    openFilesSearch();
    input().value = "Foo";
    const aa = _filesSearchBar()?.querySelector<HTMLButtonElement>(".fb-search-case");
    expect(aa?.getAttribute("aria-pressed")).toBe("false");
    aa?.click();
    await settle();
    expect(aa?.getAttribute("aria-pressed")).toBe("true");
    expect(apiGet.mock.calls.at(-1)?.[0]).toContain("case=1");
  });

  it("passes the glob fields through", async () => {
    openFilesSearch();
    input().value = "Foo";
    const include = document.getElementById("fb-search-include") as HTMLInputElement;
    include.value = "*.go";
    include.dispatchEvent(new Event("input"));
    await settle();
    expect(apiGet.mock.calls.at(-1)?.[0]).toContain("include=*.go");
  });

  // --- Name hits -------------------------------------------------------
  //
  // KIND decides the row's SHAPE and where it goes, and the kind is a registered
  // wire enum: the generated decoder refuses a value this bundle has no arm for,
  // so the untrusted-wire question is answered at the boundary rather than in the
  // row.

  async function search(matches: FileMatch[]): Promise<HTMLElement[]> {
    apiGet.mockResolvedValue(result({ scanned: 1, matches }));
    openFilesSearch();
    input().value = "book";
    input().dispatchEvent(new Event("input"));
    await settle();
    return [..._filesSearchResults().querySelectorAll<HTMLElement>(".fb-search-hit")];
  }

  it("renders a name hit with no :line and no excerpt, because a name has neither", async () => {
    const [row] = await search([
      { path: "/workspace/src/cover-book.png", excerpt: "", kind: "name", line: 0 },
    ]);
    expect(row?.querySelector(".fb-search-lineno")).toBeNull();
    expect(row?.querySelector(".fb-search-excerpt")).toBeNull();
    // The label is hitLabel's, unchanged by the kind: this fixture's search path
    // is rootless, so nothing strips and the absolute form is the honest answer.
    expect(row?.querySelector(".fb-name")?.textContent).toBe("/workspace/src/cover-book.png");
  });

  it("opens a file name hit in the editor with NO line, so it lands at the top", async () => {
    const [row] = await search([
      { path: "/workspace/src/cover-book.png", excerpt: "", kind: "name", line: 0 },
    ]);
    row?.click();
    expect(openAtLine).toHaveBeenCalledWith("/workspace/src/cover-book.png", undefined);
  });

  it("navigates the browser to a dir hit and closes the bar, in that order", async () => {
    const [row] = await search([
      { path: "/workspace/src/notebook-dir", excerpt: "", kind: "dir", line: 0 },
    ]);
    row?.click();
    expect(openFolder).toHaveBeenCalledWith("/workspace/src/notebook-dir");
    // A folder is not a file: reaching the editor here would open a directory.
    expect(openAtLine).not.toHaveBeenCalled();
  });

  it("refuses a reply naming a kind this bundle does not know, rather than rendering a row nothing can open", async () => {
    // A kind added server-side that this bundle has never heard of. It is a
    // registered enum, so the decoder fails the whole reply and the surface
    // reports it as a search that could not be run — a row for it could reach
    // neither the folder door nor the editor honestly.
    apiGet.mockResolvedValue({
      matches: [{ path: "/workspace/src/book.bin", excerpt: "", kind: "sigil", line: 0 }],
      scanned: 1,
      matched: 1,
      truncated: false,
    });
    openFilesSearch();
    input().value = "book";
    input().dispatchEvent(new Event("input"));
    await settle();
    expect(_filesSearchResults().querySelectorAll(".fb-search-hit")).toHaveLength(0);
    expect(document.getElementById("fb-search-note")?.textContent).toBe("Could not search");
    expect(openFolder).not.toHaveBeenCalled();
    expect(openAtLine).not.toHaveBeenCalled();
  });

  it("keeps the :line row for a content hit, so the two shapes stay distinguishable", async () => {
    const [name, content] = await search([
      { path: "/workspace/src/book.md", excerpt: "", kind: "name", line: 0 },
      { path: "/workspace/src/book.md", excerpt: "a book here", kind: "content", line: 7 },
    ]);
    // matchLines starts at line 1, so a name hit's line 0 cannot collide with a
    // content hit for the same path: two rows, keyed distinctly, no dedupe.
    expect(name).not.toBe(content);
    expect(name?.querySelector(".fb-search-lineno")).toBeNull();
    expect(content?.querySelector(".fb-search-lineno")?.textContent).toBe(":7");
    expect(content?.querySelector(".fb-search-excerpt")?.textContent).toBe("a book here");
  });

  it("says so when the fetch fails rather than showing an empty result", async () => {
    apiGet.mockResolvedValue(null);
    openFilesSearch();
    input().value = "Foo";
    input().dispatchEvent(new Event("input"));
    await settle();
    expect(document.getElementById("fb-search-note")?.textContent).toBe("Could not search");
  });
});

describe("the Ctrl-F hotkey", () => {
  it("opens the bar and pre-empts the browser's native find", () => {
    const e = ctrlF();
    handleFindInFilesHotkey(e);
    expect(_isFilesSearchOpen()).toBe(true);
    expect(e.defaultPrevented).toBe(true);
  });

  it("falls through on a SECOND press while our field has focus, so native find stays reachable", () => {
    handleFindInFilesHotkey(ctrlF());
    input().focus();
    const second = ctrlF();
    handleFindInFilesHotkey(second);
    expect(second.defaultPrevented).toBe(false);
  });

  it("ignores every other chord, including the Ctrl+Shift+F that toggles the view", () => {
    for (const init of [
      { key: "f" },
      { key: "f", ctrlKey: true, shiftKey: true },
      { key: "f", ctrlKey: true, altKey: true },
      { key: "g", ctrlKey: true },
    ]) {
      const e = new KeyboardEvent("keydown", { ...init, cancelable: true });
      handleFindInFilesHotkey(e);
      expect(e.defaultPrevented).toBe(false);
    }
    expect(_isFilesSearchOpen()).toBe(false);
  });
});

describe("a tab switch", () => {
  // This bar was the one search surface that survived a tab switch — the
  // transcript's and the editor's have closed on it for as long as they have
  // existed. So the browser kept a stale hit list and a stale query where its
  // directory listing belongs, and the next visit to the file browser opened in
  // search mode. Reported as a chat's search being inherited by the files tab,
  // because that is the gesture that exposes it.
  function leaveBrowser(): void {
    bus.emitBus(bus.BUS_TAB_CHANGED, { to: "c-1", kind: "chat" });
  }

  it("closes the bar and restores the listing when you LEAVE the browser", async () => {
    apiGet.mockResolvedValue(result({ scanned: 3 }));
    openFilesSearch();
    input().value = "Foo";
    input().dispatchEvent(new Event("input"));
    await settle();
    expect(_isFilesSearchOpen()).toBe(true);

    leaveBrowser();
    expect(_isFilesSearchOpen()).toBe(false);
    expect(document.getElementById("fb-list")?.classList.contains("hidden")).toBe(false);
    expect(_filesSearchResults().children).toHaveLength(0);
  });

  it("forgets the query AND the globs, so nothing narrows a later search invisibly", async () => {
    openFilesSearch();
    input().value = "Foo";
    const include = document.getElementById("fb-search-include") as HTMLInputElement;
    const exclude = document.getElementById("fb-search-exclude") as HTMLInputElement;
    include.value = "*.go";
    exclude.value = "node_modules";
    await settle();

    leaveBrowser();
    expect(input().value).toBe("");
    expect(include.value).toBe("");
    expect(exclude.value, "a stale exclude silently narrows a search nobody scoped").toBe("");
  });

  it("does NOT close when the switch is ARRIVING at the tab that owns the bar", () => {
    // openFilesSearch activates the files tab before it opens the bar, and the tab
    // store announces that switch from a batched effect — so a subscriber keyed on
    // "any change" would fire after the open landed and shut the bar the user just
    // asked for. Keying on the owning tab's IDENTITY is what makes the order
    // irrelevant: the arriving emit names the very tab the bar was recorded against.
    openFilesSearch();
    expect(_isFilesSearchOpen()).toBe(true);
    bus.emitBus(bus.BUS_TAB_CHANGED, { to: activeTabID, kind: "files" });
    expect(_isFilesSearchOpen()).toBe(true);
  });

  it("DOES close on a switch to another FILES tab, which the kind test could not see", () => {
    // Two browsers are one subject apiece now, so `kind: "files"` no longer means
    // "arriving where the bar already is": tab B would inherit A's query and hit
    // list while its own directory sat hidden behind them.
    openFilesSearch();
    expect(_isFilesSearchOpen()).toBe(true);
    bus.emitBus(bus.BUS_TAB_CHANGED, { to: "files-b", kind: "files" });
    expect(_isFilesSearchOpen()).toBe(false);
  });

  it("resets nothing for an owner nobody recorded, so an empty strip is left alone", () => {
    // getActiveTabId() answers "" on an empty strip. A bar opened in that state has
    // no owner to compare against, and tearing it down on the first activation that
    // follows would be a behaviour this change invented.
    activeTabID = "";
    openFilesSearch();
    expect(_isFilesSearchOpen()).toBe(true);
    bus.emitBus(bus.BUS_TAB_CHANGED, { to: "files-b", kind: "files" });
    expect(_isFilesSearchOpen()).toBe(true);
  });

  it("cannot be reset by a later switch once the bar is CLOSED", () => {
    openFilesSearch();
    closeFilesSearch();
    input().value = "Foo";
    bus.emitBus(bus.BUS_TAB_CHANGED, { to: "files-b", kind: "files" });
    // resetFilesSearch would have cleared the field; the owner is "" so nothing ran.
    expect(input().value).toBe("Foo");
  });
});

// Item 5: the bar's third door, and the only one that is neither a click nor a
// chord. What a synthetic KeyboardEvent CANNOT do is insert text — no engine types
// for a dispatched event — so "the character lands in the field" is measured as the
// two conditions the platform needs for it: the field holds focus during this
// keydown, and the default was not prevented. Appending the character by hand is
// exactly what would lose a dead key or an IME composition.
describe("type-to-search", () => {
  /** Drive the handler directly, as app.ts's document listener does. */
  function typeAhead(key: string, init: KeyboardEventInit = {}): KeyboardEvent {
    const e = new KeyboardEvent("keydown", { key, cancelable: true, ...init });
    handleFilesTypeAhead(e);
    return e;
  }

  /** Through the real document, so keys.ts's listener runs first. */
  function pressOnDocument(key: string): void {
    document.body.dispatchEvent(
      new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true }),
    );
  }

  it("opens the bar on a printable key and leaves the character to land in the field", () => {
    const e = typeAhead("b");
    expect(_isFilesSearchOpen()).toBe(true);
    expect(document.activeElement).toBe(input());
    expect(e.defaultPrevented, "preventDefault is what would drop the character").toBe(false);
  });

  it("does not open on Space, which activates a focused control", () => {
    typeAhead(" ");
    expect(_isFilesSearchOpen()).toBe(false);
  });

  it("does not open on a chord or a composition", () => {
    for (const init of [
      { ctrlKey: true },
      { metaKey: true },
      { altKey: true },
      { isComposing: true },
    ]) {
      typeAhead("b", init);
      expect(_isFilesSearchOpen()).toBe(false);
    }
  });

  it("does not open on a key that is not a printable character", () => {
    for (const key of ["Enter", "Escape", "Tab", "ArrowDown", "F2"]) {
      typeAhead(key);
      expect(_isFilesSearchOpen()).toBe(false);
    }
  });

  it("does not open when focus sits in a text-entry surface", () => {
    for (const tag of ["input", "textarea", "select"] as const) {
      const el = document.createElement(tag);
      document.body.append(el);
      el.focus();
      typeAhead("b");
      expect(_isFilesSearchOpen(), `focus in <${tag}>`).toBe(false);
      el.remove();
    }
  });

  it("does not open when focus sits in a contenteditable rename box", () => {
    const box = document.createElement("div");
    box.contentEditable = "true";
    document.body.append(box);
    box.focus();
    typeAhead("b");
    expect(_isFilesSearchOpen()).toBe(false);
    box.remove();
  });

  it("does not open from inside an open dialog or the terminal surface", () => {
    for (const html of [
      `<dialog open><button id="probe"></button></dialog>`,
      `<div class="wt-root"><button id="probe"></button></div>`,
    ]) {
      const host = document.createElement("div");
      host.innerHTML = html;
      document.body.append(host);
      host.querySelector<HTMLButtonElement>("#probe")?.focus();
      typeAhead("b");
      expect(_isFilesSearchOpen(), html).toBe(false);
      host.remove();
    }
  });

  it("does not open on a tab that is not the file browser", () => {
    activeTabKind = "chat";
    typeAhead("b");
    expect(_isFilesSearchOpen()).toBe(false);
  });

  it("does not re-open a bar that is already open", () => {
    // The observable is the SELECTION, not the text: `openFilesSearch` ends in
    // shell.focus(), whose select() would leave the whole query highlighted, so the
    // next character the reader typed would REPLACE what they had typed so far
    // instead of extending it. Nothing clears the field, so asserting on `value`
    // passes with the guard deleted.
    openFilesSearch();
    input().value = "Foo";
    input().setSelectionRange(3, 3);
    // Focus legitimately leaves the field while the bar is open — a hit row is a tab
    // stop with its own Enter/Space handling — so the focus guard above does not
    // cover this case and the open test is the one doing the work.
    input().blur();
    typeAhead("b");
    expect([input().selectionStart, input().selectionEnd]).toEqual([3, 3]);
  });

  it("does not open when no browser is BOUND, which would search the mounts root", () => {
    // FEAT-004 answers "" for the search root between a files tab's activation and
    // the lazy refresh()'s bind. This door is what makes that window reachable on a
    // bare keystroke, so the guard is the same test the query itself makes.
    searchPath = "";
    typeAhead("b");
    expect(_isFilesSearchOpen()).toBe(false);
  });

  it("never sees a bare ?, because keys.ts stops the event for the shortcuts sheet", () => {
    // keys.ts calls stopImmediatePropagation so the `?` is not ALSO typed into the
    // composer while the sheet opens; that stops every later document listener on
    // the same node, and this handler is one of them. Correct, and undocumented
    // until now: `?` cannot open the file search.
    shortcutsSheet.mockClear();
    pressOnDocument("?");
    expect(shortcutsSheet).toHaveBeenCalledTimes(1);
    expect(_isFilesSearchOpen()).toBe(false);
  });

  it("lets every other bare printable key through to it", () => {
    shortcutsSheet.mockClear();
    pressOnDocument("b");
    expect(shortcutsSheet).not.toHaveBeenCalled();
    expect(_isFilesSearchOpen()).toBe(true);
  });
});
