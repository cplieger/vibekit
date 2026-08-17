// @vitest-environment happy-dom
//
// The image read surface, and the SVG rule that rides on it.
//
// Two things are being pinned. First, that an image never reaches the JSON read
// route: `GET /api/file` refuses a binary with a 415 (a NUL byte in the first
// 8 KiB) and caps the read at 2 MB, so the text path could only ever paint that
// error — the bytes come from `GET /api/file/download` instead. Second, that a
// `.svg` is DISPLAYED and never offered as something to open: the download route
// answers `Content-Type: image/svg+xml`, which is script-capable when navigated
// to as a document, while an SVG referenced as an image is inert by
// specification.
import { describe, it, expect, beforeEach, vi } from "vitest";

const { surfaces, apiGet } = vi.hoisted(() => ({
  surfaces: {
    editorHighlight: document.createElement("pre"),
    editorCode: document.createElement("code"),
    editorContent: document.createElement("textarea"),
    editorMarkdown: document.createElement("div"),
    editorImage: document.createElement("div"),
    editorDiffPane: document.createElement("div"),
    editorGutter: document.createElement("pre"),
    editorFilename: document.createElement("span"),
    editorError: document.createElement("div"),
    editorEditBtn: document.createElement("button"),
    editorSaveBtn: document.createElement("button"),
    editorCancelBtn: document.createElement("button"),
    editorDiffBtn: document.createElement("button"),
  },
  apiGet: vi.fn(),
}));

vi.mock("./dom.js", () => ({ $: surfaces }));
vi.mock("./highlight.js", () => ({ highlight: (s: string) => s }));
vi.mock("./store.js", () => ({ getActiveId: () => "" }));
// The read route. Nothing in this file may reach it — the assertion is that it
// stays uncalled for an image.
vi.mock("./api-client.js", () => ({ apiGet }));
vi.mock("./actions/editor.js", () => ({
  fetchAgentLines: { cancel: () => undefined, dispatch: () => Promise.resolve(null) },
  loadDiff: { dispatch: () => ({ outcome: Promise.resolve({ status: "cancelled" }) }) },
}));
vi.mock("./editor-scroll.js", () => ({
  scrollToEditorLine: () => undefined,
  flashEditorLine: () => undefined,
}));
vi.mock("./tabs.js", () => ({
  openEditorView: (_p: string, onShow: () => void) => {
    onShow();
  },
  // No tab is active before the open, so onShow fires and `open` skips its
  // already-active fallback — one activation, which is what a real first open is.
  getActiveTabId: () => "",
  editorTabID: (path: string) => `editor:${path}`,
  setTabDirty: () => undefined,
}));
vi.mock("./router.js", () => ({ pushRoute: () => undefined }));
vi.mock("./ui-state.js", () => ({ save: () => undefined }));
vi.mock("./actions/index.js", () => ({ registerCleanup: () => undefined }));

import { openFile } from "./editor-openers.js";
import { fileStates } from "./editor-types.js";
import { isViewableImage } from "./file-extensions.js";

function img(): HTMLImageElement | null {
  return surfaces.editorImage.querySelector<HTMLImageElement>("img");
}

function hidden(el: HTMLElement): boolean {
  return el.classList.contains("hidden");
}

beforeEach(() => {
  apiGet.mockReset();
  apiGet.mockResolvedValue({ content: "" });
  fileStates.clear();
  for (const el of Object.values(surfaces)) {
    el.className = "";
    el.replaceChildren();
  }
});

describe("isViewableImage", () => {
  it("covers every raster the browser paints, case-insensitively", () => {
    for (const p of [
      "out/shot.png",
      "a.JPG",
      "a.jpeg",
      "a.gif",
      "a.webp",
      "a.avif",
      "a.ico",
      "a.bmp",
    ]) {
      expect(isViewableImage(p), p).toBe(true);
    }
  });

  it("includes .svg, which is what routes it to the image surface", () => {
    expect(isViewableImage("docs/arch.svg")).toBe(true);
  });

  it("leaves text and unknown files to the text path", () => {
    for (const p of ["main.go", "README.md", "notes", "a.png.bak", "dir.png/file.go"]) {
      expect(isViewableImage(p), p).toBe(false);
    }
  });

  // A dotfile's leading dot names the file rather than typing it.
  it("does not read a dotfile's name as an extension", () => {
    expect(isViewableImage(".png")).toBe(false);
    expect(isViewableImage("a/.svg")).toBe(false);
  });
});

describe("opening an image", () => {
  it("never calls the JSON read route", () => {
    openFile("out/shot.png");
    expect(apiGet).not.toHaveBeenCalled();
  });

  // The control: the same call path DOES reach the read route for a text file,
  // so "not called" above is about the image branch rather than about the
  // harness. It is a COUNT now: `open()` used to activate twice on a first open
  // (once through the tab's onShow, once unconditionally afterwards), and the
  // second load aborted the first, which is the wasted round trip this asserts
  // is gone.
  it("still calls the JSON read route for a text file, exactly once", () => {
    openFile("main.go");
    expect(apiGet).toHaveBeenCalledTimes(1);
    expect(apiGet.mock.calls[0]?.[0]).toBe("/api/file?path=main.go");
  });

  it("paints an <img> pointed at the byte-serving route", () => {
    openFile("out/shot.png");
    expect(img()?.getAttribute("src")).toBe("/api/file/download?path=out%2Fshot.png");
  });

  it("shows the image surface and hides every text surface", () => {
    openFile("out/shot.png");
    expect(hidden(surfaces.editorImage)).toBe(false);
    expect(hidden(surfaces.editorHighlight)).toBe(true);
    expect(hidden(surfaces.editorContent)).toBe(true);
    expect(hidden(surfaces.editorMarkdown)).toBe(true);
    expect(hidden(surfaces.editorDiffPane)).toBe(true);
  });

  // Source line numbers beside a picture number nothing on screen — the same
  // argument the rendered-markdown surface makes.
  it("hides the gutter", () => {
    openFile("out/shot.png");
    expect(hidden(surfaces.editorGutter)).toBe(true);
  });

  // There is no buffer to edit and a two-pane text diff over a PNG compares
  // nothing, so the controls are gone rather than disabled-but-present.
  it("hides every text-editing control", () => {
    openFile("out/shot.png");
    expect(hidden(surfaces.editorEditBtn)).toBe(true);
    expect(hidden(surfaces.editorSaveBtn)).toBe(true);
    expect(hidden(surfaces.editorCancelBtn)).toBe(true);
    expect(hidden(surfaces.editorDiffBtn)).toBe(true);
  });

  it("names the file in the alt text so a failed load is legible", () => {
    openFile("out/shot.png");
    expect(img()?.alt).toBe("out/shot.png");
  });

  it("repaints from scratch, leaving no trace of the previous image", () => {
    openFile("out/a.png");
    openFile("out/b.png");
    expect(surfaces.editorImage.querySelectorAll("img")).toHaveLength(1);
    expect(img()?.getAttribute("src")).toBe("/api/file/download?path=out%2Fb.png");
  });

  it("marks the state loaded so a re-activation does not try to fetch", () => {
    openFile("out/shot.png");
    expect(fileStates.get("out/shot.png")?.loaded).toBe(true);
    expect(fileStates.get("out/shot.png")?.mode.value.kind).toBe("image");
  });
});

// D21b. The trap is easy to add later by accident, so it is asserted rather than
// only commented: `<img>` is inert, a same-origin navigation to the same `.svg`
// is not, and neither is an `<iframe>` pointing at it.
describe("an SVG is an image to display, never a page to open", () => {
  it("renders it as an image", () => {
    openFile("docs/arch.svg");
    expect(hidden(surfaces.editorImage)).toBe(false);
    expect(img()).not.toBeNull();
    expect(img()?.getAttribute("src")).toBe("/api/file/download?path=docs%2Farch.svg");
  });

  it("reaches the surface without the JSON route", () => {
    openFile("docs/arch.svg");
    expect(apiGet).not.toHaveBeenCalled();
  });

  it("offers no raw link, frame or embed for it", () => {
    openFile("docs/arch.svg");
    const host = surfaces.editorImage;
    expect(host.querySelector("a")).toBeNull();
    expect(host.querySelector("iframe")).toBeNull();
    expect(host.querySelector("object")).toBeNull();
    expect(host.querySelector("embed")).toBeNull();
    // Nothing anywhere on the surface carries a navigable href.
    expect(host.querySelector("[href]")).toBeNull();
  });

  // Belt and braces on the same rule: the only child of the surface is the image.
  it("puts nothing but the image on the surface", () => {
    openFile("docs/arch.svg");
    expect([...surfaces.editorImage.children].map((c) => c.tagName)).toEqual(["IMG"]);
  });
});
