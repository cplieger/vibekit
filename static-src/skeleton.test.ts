import { KEY_ATTR } from "@cplieger/reactive";
import { describe, it, expect, vi, beforeEach } from "vitest";

import { editorDocSkeleton, fileRowsSkeleton, paintPlaceholder } from "./skeleton.js";

function host(): HTMLElement {
  const el = document.createElement("div");
  document.body.appendChild(el);
  return el;
}

function keyedRow(): HTMLElement {
  const row = document.createElement("div");
  row.setAttribute(KEY_ATTR, "row-1");
  return row;
}

function placeholder(): HTMLElement {
  const p = document.createElement("div");
  p.className = "shimmer";
  return p;
}

describe("paintPlaceholder", () => {
  beforeEach(() => {
    document.body.replaceChildren();
  });

  it("refuses a host that already holds a keyed child", () => {
    const h = host();
    h.appendChild(keyedRow());
    paintPlaceholder(h, placeholder);
    expect(h.querySelector(".shimmer")).toBeNull();
  });

  it("returns a no-op teardown on a refusal, so a caller can call it blindly", () => {
    const h = host();
    const row = keyedRow();
    h.appendChild(row);
    paintPlaceholder(h, placeholder)();
    expect(h.children).toHaveLength(1);
    expect(h.firstElementChild).toBe(row);
  });

  it("does NOT call build when it refuses", () => {
    const h = host();
    h.appendChild(keyedRow());
    const build = vi.fn(placeholder);
    paintPlaceholder(h, build);
    expect(build).not.toHaveBeenCalled();
  });

  it("admits an empty host and returns a teardown that removes what it mounted", () => {
    const h = host();
    const teardown = paintPlaceholder(h, placeholder);
    expect(h.querySelector(".shimmer")).not.toBeNull();
    teardown();
    expect(h.querySelector(".shimmer")).toBeNull();
  });

  it("honours a custom content selector", () => {
    const h = host();
    const row = document.createElement("div");
    row.setAttribute("data-key", "s-1");
    h.appendChild(row);
    paintPlaceholder(h, placeholder, { content: "[data-key]" });
    expect(h.querySelector(".shimmer")).toBeNull();
  });

  it("admits a host whose only child the custom selector does not name", () => {
    const h = host();
    h.appendChild(keyedRow());
    paintPlaceholder(h, placeholder, { content: "[data-key]" });
    expect(h.querySelector(".shimmer")).not.toBeNull();
  });

  it("treats a null host as a refusal", () => {
    const build = vi.fn(placeholder);
    expect(() => {
      paintPlaceholder(null, build)();
    }).not.toThrow();
    expect(build).not.toHaveBeenCalled();
  });

  it("CLEARS a host holding a non-content child by default", () => {
    const h = host();
    const err = document.createElement("div");
    err.className = "load-error";
    h.appendChild(err);
    paintPlaceholder(h, placeholder);
    expect(h.querySelector(".load-error")).toBeNull();
    expect(h.querySelector(".shimmer")).not.toBeNull();
  });

  it("leaves a non-content child standing under mount append", () => {
    const h = host();
    const err = document.createElement("div");
    err.className = "load-error";
    h.appendChild(err);
    paintPlaceholder(h, placeholder, { mount: "append" });
    expect(h.querySelector(".load-error")).not.toBeNull();
    expect(h.querySelector(".shimmer")).not.toBeNull();
  });

  it("refuses on ANY element child under a `*` selector", () => {
    // The editor pane cannot use it for that reason: a highlighted file leaves 157
    // `span.hl-*` children, so the door would refuse every placeholder the pane
    // could paint. That site clears the pane instead.
    const h = host();
    h.appendChild(document.createElement("span"));
    paintPlaceholder(h, placeholder, { content: "*" });
    expect(h.querySelector(".shimmer")).toBeNull();
  });
});

describe("editorDocSkeleton", () => {
  it("stands a title bar on the declaration line and one bar on each line below it", () => {
    const wrap = editorDocSkeleton();
    expect(wrap.getAttribute("aria-hidden")).toBe("true");
    expect(wrap.firstElementChild?.className).toContain("editor-skel-title");
    expect(wrap.querySelectorAll(".editor-skel-title")).toHaveLength(1);
    expect(wrap.querySelectorAll(".editor-skel-line")).toHaveLength(10);
  });

  it("leaves a blank line blank, with no bar to shimmer", () => {
    const wrap = editorDocSkeleton();
    const blank = [...wrap.querySelectorAll<HTMLElement>(".editor-skel-line")].filter(
      (line) => !line.classList.contains("skeleton"),
    );
    expect(blank).toHaveLength(2);
    for (const line of blank) {
      expect(line.style.width).toBe("");
    }
  });
});

describe("fileRowsSkeleton", () => {
  it("wears `.fb-row`, so a row's box is a real row's", () => {
    const wrap = fileRowsSkeleton();
    expect(wrap.getAttribute("aria-hidden")).toBe("true");
    const rows = [...wrap.children];
    expect(rows).toHaveLength(8);
    for (const row of rows) {
      expect(row.classList.contains("fb-row")).toBe(true);
      expect(row.classList.contains("fb-row-skel")).toBe(true);
    }
  });

  it("reserves the check column without a bar in it, and carries a size and a date bar", () => {
    const row = fileRowsSkeleton().firstElementChild;
    const check = row?.querySelector(".fb-skel-check");
    expect(check).not.toBeNull();
    expect(check?.classList.contains("skeleton")).toBe(false);
    expect(row?.querySelectorAll(".fb-skel-meta")).toHaveLength(2);
    expect(row?.querySelector(".fb-skel-name .skeleton")).not.toBeNull();
  });
});
