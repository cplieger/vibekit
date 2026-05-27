// @vitest-environment happy-dom
// Unit tests for FileBrowserState navigation logic (pure state machine).
import { describe, it, expect, vi } from "vitest";

// Mock the dom module to avoid element lookups.
vi.mock("./dom.js", () => ({
  $: new Proxy({}, { get: () => document.createElement("div") }),
  maybeViewTransition: (fn: () => void) => fn(),
  el: () => document.createElement("div"),
}));
vi.mock("./bus.js", () => ({ onBus: vi.fn(), BUS_KEYS_ESCAPE: "escape" }));
vi.mock("./tabs.js", () => ({ toggleFilesView: vi.fn() }));
vi.mock("./editor-openers.js", () => ({ openFile: vi.fn() }));
vi.mock("./modals.js", () => ({ closeModal: vi.fn() }));
vi.mock("./confirm.js", () => ({ confirm: vi.fn().mockResolvedValue(true) }));
vi.mock("./ui-state.js", () => ({ save: vi.fn() }));
vi.mock("./upload.js", () => ({ uploadFiles: vi.fn() }));
vi.mock("./icons.js", () => ({ fileIcon: vi.fn(() => ""), FILE_ICONS: {} }));
vi.mock("./router.js", () => ({ pushRoute: vi.fn() }));
vi.mock("./chat.js", () => ({ attachPathToActiveChat: vi.fn() }));
vi.mock("./files-browser-drop.js", () => ({ initBrowserDragDrop: vi.fn() }));
vi.mock("./files-picker.js", () => ({ setOnUploadComplete: vi.fn() }));
vi.mock("./api-client.js", () => ({ apiPost: vi.fn(), apiGet: vi.fn() }));
vi.mock("./scroll.js", () => ({
  scroll: vi.fn(),
  trimOldMessages: vi.fn(),
  setUserScrolledUp: vi.fn(),
}));
vi.mock("./transport.js", () => ({ send: vi.fn() }));
vi.mock("./store.js", () => ({
  getActiveId: vi.fn(() => ""),
}));

import { FileBrowserState } from "./files.js";

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
          expect(s.history).toEqual([".", "src"]);
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
          expect(s.history).toEqual([".", "a", "c"]);
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
        steps: (_s: FileBrowserState) => {},
        check: (s: FileBrowserState) => {
          expect(s.goBack()).toBe(false);
          expect(s.currentPath).toBe(".");
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
          expect(s.currentPath).toBe(".");
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

  describe("reset", () => {
    it("restores initial state", () => {
      const s = new FileBrowserState();
      s.navigate("deep/path");
      s.selected.add("file");
      s.entries = [{ name: "x", isDir: false, size: 0, modTime: 0, mode: "" }];
      s.reset();
      expect(s.currentPath).toBe(".");
      expect(s.history).toEqual(["."]);
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
