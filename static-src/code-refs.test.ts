// @vitest-environment happy-dom
// ---------------------------------------------------------------------------
// Tests for code-refs.ts — the licensed-code attribution footnote renderer.
// Pure DOM (no store/bus): syncCodeReferences(wrap, m) builds/updates/removes
// the footnote off m.code_references, gating links on http/https safety.
// ---------------------------------------------------------------------------

import { describe, it, expect } from "vitest";
import { syncCodeReferences } from "./code-refs.js";
import type { Message, CodeReference } from "./types.js";

function assistantMsg(refs?: CodeReference[]): Message {
  return {
    id: "m1",
    role: "assistant",
    ts: 0,
    content: "code",
    ...(refs !== undefined && { code_references: refs }),
  };
}

describe("syncCodeReferences", () => {
  it("renders a footnote with a count summary + license + safe source link", () => {
    const wrap = document.createElement("div");
    syncCodeReferences(
      wrap,
      assistantMsg([
        {
          license_name: "MIT",
          repository: "github.com/foo/bar",
          url: "https://github.com/foo/bar",
        },
      ]),
    );
    expect(wrap.querySelector(".code-refs")).not.toBeNull();
    expect(wrap.querySelector(".code-refs-count")?.textContent).toBe("1 code reference");
    expect(wrap.querySelector(".code-refs-license")?.textContent).toBe("MIT");
    const link = wrap.querySelector<HTMLAnchorElement>(".code-refs-link");
    expect(link?.getAttribute("href")).toBe("https://github.com/foo/bar");
    expect(link?.getAttribute("rel")).toBe("noopener noreferrer");
    expect(link?.getAttribute("target")).toBe("_blank");
    expect(link?.textContent).toContain("github.com/foo/bar");
  });

  it("renders plain source text (no link) for a non-http(s) URL", () => {
    const wrap = document.createElement("div");
    syncCodeReferences(
      wrap,
      assistantMsg([
        { license_name: "GPL-2.0", repository: "somerepo", url: "javascript:alert(1)" },
      ]),
    );
    expect(wrap.querySelector(".code-refs-link")).toBeNull();
    expect(wrap.querySelector(".code-refs-source")?.textContent).toBe("somerepo");
  });

  it("uses the URL host as the label when repository is absent", () => {
    const wrap = document.createElement("div");
    syncCodeReferences(
      wrap,
      assistantMsg([{ license_name: "MIT", url: "https://example.com/path/to/file" }]),
    );
    expect(wrap.querySelector(".code-refs-link")?.textContent).toContain("example.com");
  });

  it("falls back to 'source' when there is no repository and no safe URL", () => {
    const wrap = document.createElement("div");
    syncCodeReferences(wrap, assistantMsg([{ license_name: "MIT", url: "ftp://x/y" }]));
    expect(wrap.querySelector(".code-refs-link")).toBeNull();
    expect(wrap.querySelector(".code-refs-source")?.textContent).toBe("source");
  });

  it("pluralizes the count summary", () => {
    const wrap = document.createElement("div");
    syncCodeReferences(
      wrap,
      assistantMsg([
        { license_name: "MIT", repository: "a" },
        { license_name: "Apache-2.0", repository: "b" },
      ]),
    );
    expect(wrap.querySelector(".code-refs-count")?.textContent).toBe("2 code references");
    expect(wrap.querySelectorAll(".code-refs-item").length).toBe(2);
  });

  it("is idempotent: an unchanged count does not rebuild the node", () => {
    const wrap = document.createElement("div");
    const m = assistantMsg([{ license_name: "MIT", repository: "a" }]);
    syncCodeReferences(wrap, m);
    const first = wrap.querySelector(".code-refs");
    syncCodeReferences(wrap, m);
    expect(wrap.querySelector(".code-refs")).toBe(first);
  });

  it("rebuilds when the reference count grows and preserves the open state", () => {
    const wrap = document.createElement("div");
    syncCodeReferences(wrap, assistantMsg([{ license_name: "MIT", repository: "a" }]));
    const details = wrap.querySelector<HTMLDetailsElement>(".code-refs");
    if (details !== null) {
      details.open = true;
    }
    syncCodeReferences(
      wrap,
      assistantMsg([
        { license_name: "MIT", repository: "a" },
        { license_name: "BSD-3-Clause", repository: "b" },
      ]),
    );
    expect(wrap.querySelectorAll(".code-refs-item").length).toBe(2);
    expect(wrap.querySelector<HTMLDetailsElement>(".code-refs")?.open).toBe(true);
  });

  it("removes the footnote when references become empty", () => {
    const wrap = document.createElement("div");
    syncCodeReferences(wrap, assistantMsg([{ license_name: "MIT", repository: "a" }]));
    syncCodeReferences(wrap, assistantMsg(undefined));
    expect(wrap.querySelector(".code-refs")).toBeNull();
  });

  it("treats reference text as inert text (no HTML injection)", () => {
    const wrap = document.createElement("div");
    syncCodeReferences(
      wrap,
      assistantMsg([{ license_name: "<img src=x onerror=alert(1)>", repository: "r" }]),
    );
    expect(wrap.querySelector("img")).toBeNull();
    expect(wrap.querySelector(".code-refs-license")?.textContent).toBe(
      "<img src=x onerror=alert(1)>",
    );
  });
});
