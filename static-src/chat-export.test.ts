// ---------------------------------------------------------------------------
// chat-export tests: the Export affordance triggers a same-origin anchor
// download with the right endpoint URL, format, and filename, and never
// leaves the transient anchor attached to the document.
// ---------------------------------------------------------------------------

import { afterEach, describe, expect, it, vi } from "vitest";
import { downloadChatExport } from "./chat-export.js";

/** Runs fn with element.click() stubbed (click lives on HTMLElement.prototype),
 *  returning the element that was clicked (or null if none). The stub prevents
 *  the browser from attempting a real navigation. */
function captureDownloadAnchor(fn: () => void): HTMLElement | null {
  let clicked: HTMLElement | null = null;
  // Capture the appended anchor from the DOM during the click (it is appended
  // before .click() and removed right after), avoiding a `this` alias.
  const spy = vi.spyOn(HTMLElement.prototype, "click").mockImplementation(() => {
    clicked = document.body.querySelector<HTMLElement>("a[download]");
  });
  fn();
  spy.mockRestore();
  return clicked;
}

describe("downloadChatExport", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    document.body.replaceChildren();
  });

  it("defaults to a Markdown export anchor", () => {
    const a = captureDownloadAnchor(() => {
      downloadChatExport("c1", "My Chat");
    });
    expect(a).not.toBeNull();
    expect(a?.getAttribute("href")).toBe("/api/chats/c1/export?format=md");
    expect(a?.getAttribute("download")).toBe("My Chat-c1.md");
    expect(a?.getAttribute("rel")).toBe("noopener");
  });

  it("builds a JSON export anchor when asked", () => {
    const a = captureDownloadAnchor(() => {
      downloadChatExport("c1", "My Chat", "json");
    });
    expect(a?.getAttribute("href")).toBe("/api/chats/c1/export?format=json");
    expect(a?.getAttribute("download")).toBe("My Chat-c1.json");
  });

  it("falls back to the chat id when the name is empty", () => {
    const a = captureDownloadAnchor(() => {
      downloadChatExport("c1", "", "md");
    });
    expect(a?.getAttribute("download")).toBe("c1.md");
  });

  it("sanitises unsafe characters in the download filename", () => {
    const a = captureDownloadAnchor(() => {
      downloadChatExport("c1", 'bad/name:"x"', "md");
    });
    expect(a?.getAttribute("download")).toBe("bad_name__x_-c1.md");
  });

  it("removes the transient anchor after clicking", () => {
    captureDownloadAnchor(() => {
      downloadChatExport("c1", "My Chat", "md");
    });
    expect(document.querySelectorAll("a[download]").length).toBe(0);
  });

  it("is a no-op for an empty chat id", () => {
    const a = captureDownloadAnchor(() => {
      downloadChatExport("", "My Chat", "md");
    });
    expect(a).toBeNull();
    expect(document.querySelectorAll("a[download]").length).toBe(0);
  });
});
