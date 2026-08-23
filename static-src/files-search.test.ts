// Find in files: the request it builds, the honesty of its note, and the
// second-press escape hatch that is the a11y justification for overriding Ctrl-F
// at all.
import { describe, it, expect, vi, beforeAll, afterAll, beforeEach, afterEach } from "vitest";
import type { FileSearchResult } from "./files-search.js";

const apiGet = vi.fn<(url: string, signal?: AbortSignal) => Promise<FileSearchResult | null>>();
const openAtLine = vi.fn();
const activateBrowser = vi.fn();

vi.mock("./api-client.js", () => ({
  apiGet: (url: string, signal?: AbortSignal) => apiGet(url, signal),
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
}));
vi.mock("./navigate.js", () => ({
  openAtLine: (path: string, line?: number) => openAtLine(path, line),
}));
vi.mock("./icons.js", () => ({ fileIcon: () => "<svg></svg>" }));
vi.mock("./icon-el.js", () => ({ iconEl: () => document.createElement("span") }));

const mod = await import("./files-search.js");
const bus = await import("./bus.js");
const {
  searchURL,
  searchNote,
  hitLabel,
  hitKey,
  initFilesSearch,
  openFilesSearch,
  closeFilesSearch,
  resetFilesSearch,
  handleFindInFilesHotkey,
  _isFilesSearchOpen,
  _filesSearchBar,
  _filesSearchResults,
} = mod;

function result(over: Partial<FileSearchResult> = {}): FileSearchResult {
  return { matches: [], scanned: 0, truncated: false, ...over };
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
    <button type="button" id="fb-search-btn" aria-pressed="false"></button>`;
  initFilesSearch({ getSearchPath: () => searchPath, activateBrowser });
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
  searchPath = "workspace/src";
  resetFilesSearch();
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

describe("searchNote", () => {
  it("states the file count when the scan finished", () => {
    expect(searchNote(result({ scanned: 12 }))).toBe("No matches in 12 files.");
  });

  it("says the scan stopped, so an empty result cannot imply the text is nowhere", () => {
    const note = searchNote(result({ scanned: 5000, truncated: true }));
    expect(note).toBe(
      "No matches in 5000 files. The scan stopped at its limit, so more were not read.",
    );
  });

  it("says it on a NON-empty result too, because the match list is short either way", () => {
    const note = searchNote(
      result({
        matches: [{ path: "/workspace/a.go", excerpt: "x", line: 1 }],
        truncated: true,
        scanned: 1,
      }),
    );
    expect(note).toBe("1 match in 1 file. The scan stopped at its limit, so more were not read.");
  });

  it("counts matches and files in the plural when there is more than one", () => {
    const matches = [
      { path: "/workspace/a.go", excerpt: "x", line: 1 },
      { path: "/workspace/b.go", excerpt: "y", line: 2 },
    ];
    expect(searchNote(result({ matches, scanned: 9 }))).toBe("2 matches in 9 files.");
  });
});

describe("hitLabel", () => {
  it("shows a path relative to the folder searched", () => {
    expect(hitLabel("workspace/src", "/workspace/src/a/b.go")).toBe("a/b.go");
  });

  it("falls back to the absolute path for a root search, which spans mounts", () => {
    expect(hitLabel(".", "/config/notes/x.md")).toBe("/config/notes/x.md");
    expect(hitLabel("", "/config/notes/x.md")).toBe("/config/notes/x.md");
  });

  it("keeps the absolute form for a path outside the folder searched", () => {
    expect(hitLabel("workspace/src", "/config/x.md")).toBe("/config/x.md");
  });
});

describe("hitKey", () => {
  it("separates two hits that differ only by line", () => {
    const a = hitKey({ path: "/w/a.go", excerpt: "", line: 1 });
    const b = hitKey({ path: "/w/a.go", excerpt: "", line: 2 });
    expect(a).not.toBe(b);
  });

  it("separates hits whose paths differ only where a colon falls", () => {
    // A colon is a legal filename character, which is why the composite goes
    // through keyenc instead of a template literal.
    const a = hitKey({ path: "/w/a:1", excerpt: "", line: 2 });
    const b = hitKey({ path: "/w/a", excerpt: "", line: 12 });
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
          { path: "/workspace/src/a.go", excerpt: "func Foo()", line: 12 },
          { path: "/workspace/src/b.go", excerpt: "Foo()", line: 4 },
        ],
      }),
    );
    openFilesSearch();
    input().value = "Foo";
    input().dispatchEvent(new Event("input"));
    await settle();

    const rows = _filesSearchResults().querySelectorAll(".fb-search-hit");
    expect(rows).toHaveLength(2);
    expect(document.getElementById("fb-search-note")?.textContent).toBe("2 matches in 3 files.");
    (rows[0] as HTMLElement).click();
    expect(openAtLine).toHaveBeenCalledWith("/workspace/src/a.go", 12);
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
                  matches: [{ path: "/workspace/src/stale.go", excerpt: "s", line: 1 }],
                }),
              );
            };
          })
        : Promise.resolve(
            result({
              scanned: 2,
              matches: [{ path: "/workspace/src/fresh.go", excerpt: "f", line: 3 }],
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
      result({ scanned: 1, matches: [{ path: "/workspace/src/a.go", excerpt: "x", line: 1 }] }),
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

  it("says so when the fetch fails rather than showing an empty result", async () => {
    apiGet.mockResolvedValue(null);
    openFilesSearch();
    input().value = "Foo";
    input().dispatchEvent(new Event("input"));
    await settle();
    expect(document.getElementById("fb-search-note")?.textContent).toBe(
      "Search failed. Check your connection.",
    );
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

  it("does NOT close when the switch is ARRIVING at the browser", () => {
    // openFilesSearch activates the files tab before it opens the bar, and the tab
    // store announces that switch from a batched effect — so a subscriber keyed on
    // "any change" would fire after the open landed and shut the bar the user just
    // asked for. Keying on the destination kind is what makes the order irrelevant.
    openFilesSearch();
    expect(_isFilesSearchOpen()).toBe(true);
    bus.emitBus(bus.BUS_TAB_CHANGED, { to: "__files__", kind: "files" });
    expect(_isFilesSearchOpen()).toBe(true);
  });
});
