// @vitest-environment happy-dom
// The attachment pill: one component for the composer's staged row and a sent
// turn's header. The two assertions that matter are that the body OPENS and the
// `×` does not, in both directions — the pill is the only place in the app where
// two different verbs share one small target.
import { describe, it, expect, vi, beforeEach } from "vitest";
import {
  buildAttachmentPill,
  iconForAttachment,
  initAttachmentPillCallbacks,
} from "./attachment-pill.js";

const opened: string[] = [];

beforeEach(() => {
  opened.length = 0;
  initAttachmentPillCallbacks({
    open: (p) => {
      opened.push(p);
    },
  });
});

function body(pill: HTMLElement): HTMLButtonElement {
  const b = pill.querySelector<HTMLButtonElement>(".attachment-open");
  if (b === null) {
    throw new Error("no .attachment-open");
  }
  return b;
}

describe("buildAttachmentPill", () => {
  it("renders the badge, the name and the path as the tooltip", () => {
    const pill = buildAttachmentPill({ path: "src/notes.md", name: "notes.md" });
    expect(pill.tagName).toBe("LI");
    expect(pill.getAttribute("title")).toBe("src/notes.md");
    expect(pill.querySelector(".attachment-name")?.textContent).toBe("notes.md");
    expect(pill.querySelector(".attachment-icon")?.textContent).not.toBe("");
  });

  // A button cannot contain a button, so the body could never have wrapped the
  // `×`. Siblinghood is what makes the two verbs independent, and it is a
  // structural guarantee rather than a stopPropagation call a later edit can drop.
  it("puts the body and the × side by side, never nested", () => {
    const pill = buildAttachmentPill(
      { path: "a/b.txt", name: "b.txt" },
      { onRemove: () => undefined },
    );
    const open = body(pill);
    const close = pill.querySelector<HTMLButtonElement>(".attachment-close");
    expect(close).not.toBeNull();
    expect(open.parentElement).toBe(pill);
    expect(close?.parentElement).toBe(pill);
    expect(open.querySelector(".attachment-close")).toBeNull();
  });

  it("opens the file when the body is clicked", () => {
    const pill = buildAttachmentPill({ path: "docs/spec.md", name: "spec.md" });
    body(pill).click();
    expect(opened).toEqual(["docs/spec.md"]);
  });

  it("does not remove the attachment when the body is clicked", () => {
    const onRemove = vi.fn();
    const pill = buildAttachmentPill({ path: "docs/spec.md", name: "spec.md" }, { onRemove });
    body(pill).click();
    expect(opened).toEqual(["docs/spec.md"]);
    expect(onRemove).not.toHaveBeenCalled();
  });

  // The inverse, and the one a nested structure would have broken: removing a
  // file must not also open it.
  it("does not open the file when the × is clicked", () => {
    const onRemove = vi.fn();
    const pill = buildAttachmentPill({ path: "docs/spec.md", name: "spec.md" }, { onRemove });
    pill.querySelector<HTMLButtonElement>(".attachment-close")?.click();
    expect(onRemove).toHaveBeenCalledWith("docs/spec.md");
    expect(opened).toEqual([]);
  });

  // A sent attachment cannot be un-sent, so the header's pill has no `×` at all
  // rather than a hidden one — a display:none button is still a button in the
  // accessibility tree.
  it("builds no × when no remover is supplied", () => {
    const pill = buildAttachmentPill({ path: "a/b.txt", name: "b.txt" });
    expect(pill.querySelector(".attachment-close")).toBeNull();
  });

  it("labels both controls for a screen reader", () => {
    const pill = buildAttachmentPill(
      { path: "a/b.txt", name: "b.txt" },
      { onRemove: () => undefined },
    );
    expect(body(pill).getAttribute("aria-label")).toBe("Open b.txt");
    expect(pill.querySelector(".attachment-close")?.getAttribute("aria-label")).toBe(
      "Remove b.txt",
    );
  });

  // Unwired is the default so a pill built in a test renders; the click must be
  // inert rather than a crash.
  it("is inert when no opener has been injected", () => {
    initAttachmentPillCallbacks({ open: () => undefined });
    const pill = buildAttachmentPill({ path: "a/b.txt", name: "b.txt" });
    expect(() => {
      body(pill).click();
    }).not.toThrow();
  });
});

describe("iconForAttachment", () => {
  it("reads the badge off the shared extension registry", () => {
    expect(iconForAttachment("report.pdf")).toBe("📄");
    expect(iconForAttachment("shot.png")).toBe("🖼️");
  });

  it("falls back to a paperclip for an extension with no badge and for none at all", () => {
    expect(iconForAttachment("Makefile")).toBe("📎");
    expect(iconForAttachment("archive.xyz")).toBe("📎");
  });

  it("is case-insensitive about the extension", () => {
    expect(iconForAttachment("REPORT.PDF")).toBe("📄");
  });
});
