// The composer's staged attachment row. The module had no test file at all
// before the pill body became clickable, and the click is exactly the behaviour
// that needed one: the row is bound once behind a latch, so a pill built for the
// wrong chat or wired to the wrong path is invisible until a user hits it.
import { describe, it, expect, beforeEach, vi } from "vitest";

const { row } = vi.hoisted(() => ({ row: document.createElement("ul") }));
vi.mock("./dom.js", () => ({ $: { attachmentRow: row } }));

import { addAttachment, takeAttachments } from "./attachments.js";
import { initAttachmentPillCallbacks } from "./attachment-pill.js";

const opened: string[] = [];

beforeEach(() => {
  // The live collection is module state and the row is bound to it once, so the
  // reset is a take rather than a re-import.
  takeAttachments();
  opened.length = 0;
  initAttachmentPillCallbacks({
    open: (p) => {
      opened.push(p);
    },
  });
});

function pills(): HTMLElement[] {
  return [...row.querySelectorAll<HTMLElement>(".attachment-pill")];
}

function bodies(): HTMLButtonElement[] {
  return [...row.querySelectorAll<HTMLButtonElement>(".attachment-open")];
}

describe("the staged attachment row", () => {
  it("renders one pill per attached file, in the order they were added", () => {
    addAttachment("src/a.ts");
    addAttachment("docs/b.md");
    expect(pills().map((p) => p.getAttribute("title"))).toEqual(["src/a.ts", "docs/b.md"]);
    expect(pills().map((p) => p.querySelector(".attachment-name")?.textContent)).toEqual([
      "a.ts",
      "b.md",
    ]);
  });

  it("hides the row when nothing is attached and shows it once something is", () => {
    expect(row.classList.contains("hidden")).toBe(true);
    addAttachment("src/a.ts");
    expect(row.classList.contains("hidden")).toBe(false);
    takeAttachments();
    expect(row.classList.contains("hidden")).toBe(true);
  });

  // The tab id is `editor:<path>` and openTab dedupes on that id alone, so N
  // DISTINCT paths are N tabs. This is the client half of that claim: each pill
  // asks for its own path and no other. (tabs.test.ts pins the id rule itself.)
  it("opens each attachment under its own path, so N attachments reach N tabs", () => {
    addAttachment("src/a.ts");
    addAttachment("docs/b.md");
    addAttachment("out/shot.png");
    const targets = bodies();
    expect(targets).toHaveLength(3);
    for (const b of targets) {
      b.click();
    }
    expect(opened).toEqual(["src/a.ts", "docs/b.md", "out/shot.png"]);
    expect(new Set(opened).size).toBe(3);
  });

  // Two pills for one path would open one tab and re-activate it, which is the
  // right behaviour — but the row should not offer the same file twice either.
  it("deduplicates by path", () => {
    addAttachment("src/a.ts");
    addAttachment("src/a.ts");
    expect(pills()).toHaveLength(1);
  });

  it("removes the file when its × is clicked, and opens nothing", () => {
    addAttachment("src/a.ts");
    addAttachment("docs/b.md");
    row.querySelector<HTMLButtonElement>(".attachment-pill .attachment-close")?.click();
    expect(pills().map((p) => p.getAttribute("title"))).toEqual(["docs/b.md"]);
    expect(takeAttachments().map((a) => a.path)).toEqual(["docs/b.md"]);
    expect(opened).toEqual([]);
  });

  // The staged pill is removable; the sent one is not. Both come from the same
  // builder, so the composer's half of that split is worth pinning here.
  it("gives every staged pill a remove control", () => {
    addAttachment("src/a.ts");
    expect(pills()[0]?.querySelector(".attachment-close")).not.toBeNull();
  });
});
