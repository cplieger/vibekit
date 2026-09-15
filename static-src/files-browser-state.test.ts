// Unit tests for FileBrowserState navigation logic (pure state machine).
import { describe, it, expect, vi, beforeEach } from "vitest";

// Mock the dom module to avoid element lookups.
vi.mock("./dom.js", () => ({
  $: new Proxy({}, { get: () => document.createElement("div") }),
  el: () => document.createElement("div"),
}));
vi.mock("./bus.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  onSSE: undefined,
  onBus: vi.fn(),
  BUS_KEYS_ESCAPE: "escape",
}));
vi.mock("./tabs.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  setGitTab: undefined,
  openGitView: undefined,
  toggleFilesView: vi.fn(() => Promise.resolve()),
  openFilesView: vi.fn(() => Promise.resolve()),
  getActiveTabKind: vi.fn(() => "files"),
  setFilesRoute: vi.fn(),
  renameTab: vi.fn(),
  filesTabIdFor: vi.fn(() => ""),
  openTab: vi.fn(() => Promise.resolve("opened")),
}));
vi.mock("./editor-openers.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  openFileGitDiff: undefined,
  openFileDiff: undefined,
  openFile: vi.fn(),
  openFileInBackground: vi.fn(),
}));
vi.mock("./modals.js", () => ({ closeModal: vi.fn() }));
vi.mock("./confirm.js", () => ({ confirm: vi.fn().mockResolvedValue(true) }));
vi.mock("./upload.js", () => ({ uploadFiles: vi.fn() }));
// The two save-indicator glyphs are present because the browser writes its
// path through persist.ts now, which reaches save-indicator.ts: Browser Mode
// links for real, so a name in the graph has to exist on the mock.
vi.mock("./icons.js", () => ({
  fileIcon: vi.fn(() => ""),
  FILE_ICONS: {},
  ICON_SAVE_OK: "",
  ICON_SAVE_FAIL: "",
}));
vi.mock("./chat.js", () => ({ attachPathsToActiveChat: vi.fn() }));
vi.mock("./files-browser-drop.js", () => ({ initBrowserDragDrop: vi.fn() }));
// files-search.ts is the browser's other satellite, stubbed for the same reason
// as the drop module: this file tests FileBrowserState, and the search bar's own
// behaviour is files-search.test.ts's.
// closeFilesSearch is inert here but the NAME has to exist: Browser Mode links
// ESM for real, so every name anything in this file's import graph reaches must
// be on the mock — files.ts imports it for the search bar's folder door.
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
  skeletonTiming: () => armSkeleton(),
}));
vi.mock("./scroll.js", () => ({
  scroll: vi.fn(),
  setUserScrolledUp: vi.fn(),
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  apiGetTyped: vi.fn(),
}));
vi.mock("./transport.js", () => ({ send: vi.fn() }));
vi.mock("./store.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  activeSession: undefined,
  getActiveId: vi.fn(() => ""),
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  newOpID: vi.fn(() => "op-test"),
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  get: vi.fn(() => undefined),
  getActive: vi.fn(() => undefined),
  getSessions: vi.fn(() => []),
  tabStatusFor: vi.fn(() => ""),
}));

import { FileBrowserState, pointFilesTab, releaseFilesTab, showFilesTab } from "./files.js";
import { FB_ROOT } from "./files-shared.js";
import { apiGet } from "./api-client.js";

/** The rows placeholder's ARM. The mock never runs the paint closure, and this
 *  suite's `$` hands out a fresh element per access, so the arm is the observable
 *  here rather than the painted DOM. */
const armSkeleton = vi.fn(() => ({
  commit: (render: () => void) => {
    render();
  },
  cancel: vi.fn(),
}));

describe("FileBrowserState", () => {
  describe("navigate", () => {
    const cases = [
      {
        name: "navigate from root pushes to history",
        steps: (s: FileBrowserState) => {
          s.navigate("src");
        },
        check: (s: FileBrowserState) => {
          expect(s.currentPath).toBe("src");
          expect(s.history).toEqual(["/", "src"]);
          expect(s.historyIdx).toBe(1);
        },
      },
      {
        name: "navigate clears selection",
        steps: (s: FileBrowserState) => {
          s.selected.add("file.txt");
          s.lastClickedName = "file.txt";
          s.navigate("lib");
        },
        check: (s: FileBrowserState) => {
          expect(s.selected.size).toBe(0);
          expect(s.lastClickedName).toBe("");
        },
      },
      {
        name: "navigate truncates forward history",
        steps: (s: FileBrowserState) => {
          s.navigate("a");
          s.navigate("b");
          s.goBack();
          s.navigate("c");
        },
        check: (s: FileBrowserState) => {
          expect(s.history).toEqual(["/", "a", "c"]);
          expect(s.historyIdx).toBe(2);
          expect(s.currentPath).toBe("c");
        },
      },
    ];

    for (const { name, steps, check } of cases) {
      it(name, () => {
        const s = new FileBrowserState();
        steps(s);
        check(s);
      });
    }
  });

  describe("goBack", () => {
    const cases = [
      {
        name: "returns false at start of history",
        steps: (_s: FileBrowserState) => {
          /* noop */
        },
        check: (s: FileBrowserState) => {
          expect(s.goBack()).toBe(false);
          expect(s.currentPath).toBe("/");
          expect(s.historyIdx).toBe(0);
        },
      },
      {
        name: "moves back one step",
        steps: (s: FileBrowserState) => {
          s.navigate("src");
        },
        check: (s: FileBrowserState) => {
          expect(s.goBack()).toBe(true);
          expect(s.currentPath).toBe("/");
          expect(s.historyIdx).toBe(0);
        },
      },
      {
        name: "clears selection on goBack",
        steps: (s: FileBrowserState) => {
          s.navigate("src");
          s.selected.add("x");
        },
        check: (s: FileBrowserState) => {
          s.goBack();
          expect(s.selected.size).toBe(0);
        },
      },
    ];

    for (const { name, steps, check } of cases) {
      it(name, () => {
        const s = new FileBrowserState();
        steps(s);
        check(s);
      });
    }
  });

  describe("goForward", () => {
    const cases = [
      {
        name: "returns false at end of history",
        steps: (s: FileBrowserState) => {
          s.navigate("a");
        },
        check: (s: FileBrowserState) => {
          expect(s.goForward()).toBe(false);
          expect(s.currentPath).toBe("a");
        },
      },
      {
        name: "moves forward after goBack",
        steps: (s: FileBrowserState) => {
          s.navigate("a");
          s.navigate("b");
          s.goBack();
          s.goBack();
        },
        check: (s: FileBrowserState) => {
          expect(s.goForward()).toBe(true);
          expect(s.currentPath).toBe("a");
          expect(s.historyIdx).toBe(1);
        },
      },
      {
        name: "clears selection on goForward",
        steps: (s: FileBrowserState) => {
          s.navigate("a");
          s.goBack();
          s.selected.add("y");
        },
        check: (s: FileBrowserState) => {
          s.goForward();
          expect(s.selected.size).toBe(0);
        },
      },
    ];

    for (const { name, steps, check } of cases) {
      it(name, () => {
        const s = new FileBrowserState();
        steps(s);
        check(s);
      });
    }
  });

  // A browser is opened AT a folder now, so the constructor is what makes a freshly
  // bound tab's trail one entry long. Both toolbar nav buttons read exactly the two
  // fields asserted here (files.ts `updateNavButtons`), so a state pointed at its
  // folder by `navigate` instead would render Back ENABLED and walk to a mounts
  // listing that tab was never at.
  describe("constructed at a directory", () => {
    it("starts with a one-entry trail, so both nav buttons are disabled", () => {
      const s = new FileBrowserState("/workspace/x");
      expect(s.currentPath).toBe("/workspace/x");
      expect(s.history).toEqual(["/workspace/x"]);
      expect(s.historyIdx).toBe(0);
      // What the two buttons compute.
      expect(s.historyIdx <= 0).toBe(true);
      expect(s.historyIdx >= s.history.length - 1).toBe(true);
    });

    it("defaults to the mounts listing", () => {
      expect(new FileBrowserState().currentPath).toBe("/");
    });

    it("gives each browser its own trail", () => {
      const a = new FileBrowserState("/a");
      const b = new FileBrowserState("/b");
      a.navigate("/a/deep");
      expect(b.history).toEqual(["/b"]);
      expect(b.currentPath).toBe("/b");
    });
  });

  // The document trail and a tab's own trail are the SAME trail while the browser is
  // active, so a route-driven move onto an adjacent entry has to STEP rather than
  // push: without it repeated Back presses grow `history` without bound and the
  // toolbar Back button stops meaning what the reader's trail says.
  describe("pointTo", () => {
    it("steps BACK onto the previous entry rather than pushing", () => {
      const s = new FileBrowserState("/a");
      s.navigate("/b");
      s.pointTo("/a");
      expect(s.currentPath).toBe("/a");
      expect(s.history).toEqual(["/a", "/b"]);
      expect(s.historyIdx).toBe(0);
    });

    // A THREE-entry trail, because a two-entry one cannot tell a forward step from a
    // push: both leave ["/a", "/b"]. The surviving "/c" is the discriminator — a push
    // truncates at historyIdx + 1 and takes it.
    it("steps FORWARD onto the next entry, keeping the trail beyond it", () => {
      const s = new FileBrowserState("/a");
      s.navigate("/b");
      s.navigate("/c");
      s.goBack();
      s.goBack();
      s.pointTo("/b");
      expect(s.currentPath).toBe("/b");
      expect(s.history).toEqual(["/a", "/b", "/c"]);
      expect(s.historyIdx).toBe(1);
    });

    it("pushes an unrelated folder, because that is a genuine arrival", () => {
      const s = new FileBrowserState("/a");
      s.navigate("/b");
      s.pointTo("/z");
      expect(s.currentPath).toBe("/z");
      expect(s.history).toEqual(["/a", "/b", "/z"]);
      expect(s.historyIdx).toBe(2);
    });

    it("is a no-op for the folder already showing", () => {
      const s = new FileBrowserState("/a");
      s.selectEntry("keep.txt");
      s.pointTo("/a");
      expect(s.history).toEqual(["/a"]);
      expect(s.historyIdx).toBe(0);
      // navigate() clears the selection, so a no-op that pushed would be visible here.
      expect(s.selected.has("keep.txt")).toBe(true);
    });
  });

  describe("reset", () => {
    // Back to the MOUNTS listing rather than to the tab's origin: the one caller is
    // the auto-heal, and a heal back to an unreachable origin would loop.
    it("lands on the mounts listing, not on the folder the browser opened at", () => {
      const s = new FileBrowserState("/workspace/x");
      s.reset();
      expect(s.currentPath).toBe("/");
      expect(s.history).toEqual(["/"]);
      expect(s.historyIdx).toBe(0);
    });

    it("restores initial state", () => {
      const s = new FileBrowserState();
      s.navigate("deep/path");
      s.selected.add("file");
      s.entries = [{ name: "x", isDir: false, size: 0, modTime: 0, mode: "" }];
      s.reset();
      expect(s.currentPath).toBe("/");
      expect(s.history).toEqual(["/"]);
      expect(s.historyIdx).toBe(0);
      expect(s.selected.size).toBe(0);
      expect(s.lastClickedName).toBe("");
      expect(s.entries).toEqual([]);
    });
  });

  describe("selection", () => {
    it("selectEntry adds to set and tracks last clicked", () => {
      const s = new FileBrowserState();
      s.selectEntry("a.txt");
      s.selectEntry("b.txt");
      expect(s.selected.has("a.txt")).toBe(true);
      expect(s.selected.has("b.txt")).toBe(true);
      expect(s.lastClickedName).toBe("b.txt");
    });

    it("deselectEntry removes from set", () => {
      const s = new FileBrowserState();
      s.selectEntry("a.txt");
      s.deselectEntry("a.txt");
      expect(s.selected.has("a.txt")).toBe(false);
      expect(s.lastClickedName).toBe("a.txt");
    });

    it("deselectAll clears set", () => {
      const s = new FileBrowserState();
      s.selectEntry("a");
      s.selectEntry("b");
      s.deselectAll();
      expect(s.selected.size).toBe(0);
    });
  });
});

describe("pointFilesTab normalises what it is handed", () => {
  // Its two callers both pass a path from OUTSIDE this module — a document history
  // entry and a `/files/<path>` deep link — so neither can be trusted to be in the
  // browser's space. The listing request is the observable: whatever the caller
  // spelled, the fetch is container-absolute.
  beforeEach(() => {
    releaseFilesTab(FB_ROOT);
    showFilesTab(FB_ROOT);
    vi.mocked(apiGet).mockClear();
  });

  const cases: [string, string][] = [
    ["a rootless path an older build persisted", "workspace/vibekit"],
    ["the same path spelled absolutely", "/workspace/vibekit"],
    ["a trailing slash", "/workspace/vibekit/"],
  ];

  for (const [name, saved] of cases) {
    it(`fetches the absolute listing for ${name}`, () => {
      pointFilesTab(FB_ROOT, saved);
      expect(vi.mocked(apiGet).mock.calls[0]?.[0]).toBe("/api/files?path=%2Fworkspace%2Fvibekit");
    });
  }

  it("leaves the browser where it is when handed nothing", () => {
    pointFilesTab(FB_ROOT, "");
    showFilesTab(FB_ROOT);
    expect(vi.mocked(apiGet).mock.calls[0]?.[0]).toBe("/api/files?path=%2F");
  });
});

describe("the rows placeholder's arm", () => {
  beforeEach(() => {
    armSkeleton.mockClear();
    vi.mocked(apiGet).mockReset();
    vi.mocked(apiGet).mockResolvedValue({ files: [], writable: true });
    releaseFilesTab(FB_ROOT);
  });

  // `showFilesTab` stands in for the deleted loader: bindFilesTab early-returns once
  // the ref is already bound, so a repeat call is a pure load and the counts below
  // mean what they did.
  it("arms one for a directory this client has never read", () => {
    showFilesTab(FB_ROOT);
    expect(armSkeleton).toHaveBeenCalledTimes(1);
  });

  it("arms nothing for an EMPTY directory the route has already answered", async () => {
    showFilesTab(FB_ROOT);
    expect(armSkeleton).toHaveBeenCalledTimes(1);
    // A macrotask, so the answer has provably landed rather than merely not having
    // been scheduled yet.
    await new Promise((r) => setTimeout(r, 0));

    showFilesTab(FB_ROOT);
    expect(armSkeleton).toHaveBeenCalledTimes(1);
  });

  it("a navigation returns the state to no-record, so the next directory can arm one", () => {
    const s = new FileBrowserState();
    s.answered = true;
    s.navigate("src");
    expect(s.answered).toBe(false);
  });

  it("a reset returns it too", () => {
    const s = new FileBrowserState();
    s.answered = true;
    s.reset();
    expect(s.answered).toBe(false);
  });
});
