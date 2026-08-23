//
// The one inert SVG render path. The load-bearing assertion is NOT that a diagram
// appears — it is that it appears through an ELEMENT the code created and assigned
// a `src` to, never through markup. Routing SVG through an `innerHTML` path would
// destroy the property the transcript renderer earns its keep with: no renderer
// output ever passes through an HTML parser.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import fc from "fast-check";
import { renderSvgBlock } from "./svg-block.js";
import { createMarkdownStream, renderMarkdownInto } from "./markdown.js";

const SVG = '<svg viewBox="0 0 10 10"><rect width="10" height="10"/></svg>';

function fence(lang: string, source: string): HTMLElement {
  const pre = document.createElement("pre");
  pre.className = "code";
  const code = document.createElement("code");
  code.className = `language-${lang}`;
  code.textContent = source;
  pre.appendChild(code);
  const host = document.createElement("div");
  host.appendChild(pre);
  return pre;
}

describe("renderSvgBlock", () => {
  it("replaces an svg fence with a figure holding an image", () => {
    const pre = fence("svg", SVG);
    const host = pre.parentElement;
    const figure = renderSvgBlock(pre);
    expect(figure?.tagName).toBe("FIGURE");
    expect(host?.querySelector("pre")).toBeNull();
    expect(host?.querySelector("figure > img")).not.toBeNull();
  });

  it("encodes the source as a data: URL on the image's src", () => {
    const pre = fence("svg", SVG);
    const img = renderSvgBlock(pre)?.querySelector("img");
    expect(img?.getAttribute("src")).toBe(`data:image/svg+xml,${encodeURIComponent(SVG)}`);
  });

  // THE invariant. The source text reaches the document only as a percent-encoded
  // URL, so no `<svg>`, `<script>` or `<rect>` node is ever parsed into the page.
  it("parses no markup: the SVG becomes no element of its own", () => {
    const pre = fence("svg", '<svg onload="alert(1)"><script>alert(2)</script></svg>');
    const figure = renderSvgBlock(pre);
    expect(figure).not.toBeNull();
    expect(figure?.querySelector("svg")).toBeNull();
    expect(figure?.querySelector("script")).toBeNull();
    expect(figure?.querySelectorAll("*")).toHaveLength(1); // the <img>, and only it
    expect(figure?.querySelector("img")?.getAttribute("onload")).toBeNull();
  });

  // A `data:` URL, not a blob URL: CSP is `img-src 'self' data:` and `blob:` is
  // absent, so createObjectURL would be blocked outright.
  it("uses a data: URL rather than a blob: URL", () => {
    const pre = fence("svg", SVG);
    const src = renderSvgBlock(pre)?.querySelector("img")?.getAttribute("src") ?? "";
    expect(src.startsWith("data:image/svg+xml,")).toBe(true);
    expect(src.startsWith("blob:")).toBe(false);
  });

  it("carries alt text so the figure is not an empty announcement", () => {
    const pre = fence("svg", SVG);
    expect(renderSvgBlock(pre)?.querySelector("img")?.getAttribute("alt")).not.toBe("");
  });

  it("leaves every other fence alone", () => {
    for (const lang of ["go", "ts", "bash", "", "xml", "html", "mermaid"]) {
      const pre = fence(lang, SVG);
      expect(renderSvgBlock(pre), lang).toBeNull();
      expect(pre.parentElement?.querySelector("pre"), lang).toBe(pre);
    }
  });

  // Mermaid is deferred by decision, not answered: source and render are
  // separable, and mermaid source is not SVG.
  it("does not claim a mermaid fence", () => {
    const pre = fence("mermaid", "graph TD; A-->B;");
    expect(renderSvgBlock(pre)).toBeNull();
  });

  // A diagram that cannot be built is better read as its source.
  it("leaves an empty fence as source", () => {
    const pre = fence("svg", "   \n  ");
    expect(renderSvgBlock(pre)).toBeNull();
    expect(pre.parentElement?.querySelector("pre")).toBe(pre);
  });

  it("degrades to a named message when the image fails to load", () => {
    const pre = fence("svg", SVG);
    const figure = renderSvgBlock(pre);
    const img = figure?.querySelector("img");
    img?.dispatchEvent(new Event("error"));
    expect(figure?.querySelector("img")).toBeNull();
    expect(figure?.querySelector(".img-missing")?.textContent).toContain("could not be rendered");
  });
});

describe("through the markdown renderer", () => {
  function render(md: string): HTMLElement {
    const host = document.createElement("div");
    renderMarkdownInto(host, md);
    return host;
  }

  it("renders a closed svg fence as a diagram", () => {
    const host = render("```svg\n" + SVG + "\n```\n");
    expect(host.querySelector("figure > img")).not.toBeNull();
    expect(host.querySelector("pre")).toBeNull();
  });

  // A converted diagram is no longer a code block, so it must not collect the
  // language label, Copy button or highlighting for source nobody can see.
  it("collects no code-block chrome", () => {
    const host = render("```svg\n" + SVG + "\n```\n");
    expect(host.querySelector(".code-lang")).toBeNull();
    expect(host.textContent).not.toContain("<rect");
  });

  it("still renders an ordinary fence as a code block", () => {
    const host = render("```go\npackage main\n```\n");
    expect(host.querySelector("pre")).not.toBeNull();
    expect(host.querySelector("figure")).toBeNull();
  });

  // Inline `<svg>` in prose is TEXT and stays text — that is the property this
  // work protects rather than changes. There is no HTML token in the parser.
  it("leaves inline svg markup in prose as literal text", () => {
    const host = render("here is <svg><script>alert(1)</script></svg> inline\n");
    expect(host.querySelector("svg")).toBeNull();
    expect(host.querySelector("script")).toBeNull();
    expect(host.querySelector("img")).toBeNull();
    expect(host.textContent).toContain("<svg>");
  });

  // The src must be assigned outside smd-renderer's set_attr funnel, which gates
  // `src` through isSafeUrl — and isSafeUrl rejects `data:`, so a diagram routed
  // through it would be rewritten to "#".
  it("does not lose the src to the renderer's url gate", () => {
    const host = render("```svg\n" + SVG + "\n```\n");
    expect(host.querySelector("img")?.getAttribute("src")).not.toBe("#");
  });

  it("is safe against an unterminated fence: source, not a broken diagram", () => {
    const host = render("```svg\n" + SVG);
    expect(host.querySelector("figure")).toBeNull();
    expect(host.querySelector("pre")).not.toBeNull();
  });
});

// The invariant that actually holds, kept as a test so a future edit trips it
// rather than a reviewer having to notice.
//
// It is scoped to the SVG PATH, not to the renderer as a whole. `code-blocks.ts`
// `finalizeBlock` assigns `codeEl.innerHTML = highlightByLang(...)` for every
// completed ordinary code block, so "no innerHTML anywhere in the markdown
// renderers" was false — and a test named after a false claim proves the wrong
// thing. What is true, and what matters, is that SVG source never reaches a parser
// sink: conversion runs ahead of code decoration, so an svg fence never gets there.
describe("svg source never reaches an HTML parser sink", () => {
  it("renders a diagram without assigning innerHTML or calling insertAdjacentHTML", async () => {
    const proto = Element.prototype as unknown as Record<string, unknown>;
    const insert = vi.spyOn(Element.prototype, "insertAdjacentHTML");
    const descriptor = Object.getOwnPropertyDescriptor(proto, "innerHTML");
    const setter = vi.fn();
    Object.defineProperty(proto, "innerHTML", {
      configurable: true,
      get: () => "",
      set: setter,
    });
    try {
      const host = document.createElement("div");
      renderMarkdownInto(host, "```svg\n" + SVG + "\n```\n");
    } finally {
      if (descriptor !== undefined) {
        Object.defineProperty(proto, "innerHTML", descriptor);
      }
      insert.mockRestore();
    }
    expect(setter).not.toHaveBeenCalled();
    expect(insert).not.toHaveBeenCalled();
    await Promise.resolve();
  });

  // The counterexample that makes the narrower claim the honest one. An ordinary
  // fence DOES use the sink, deliberately (it highlights already-escaped text), so a
  // test asserting the whole pipeline is sink-free would be asserting a falsehood.
  it("does use the sink for an ordinary code block, which is why the claim is scoped", () => {
    const host = document.createElement("div");
    renderMarkdownInto(host, "```go\npackage main\n```\n");
    const code = host.querySelector("pre code");
    expect(code).not.toBeNull();
    // Highlighting produced element children out of what arrived as one text node.
    expect(code?.querySelectorAll("*").length).toBeGreaterThan(0);
  });
});

// The live path, which no test reached before: replay never performs the
// provisional pass, so the wrapper bug was invisible to every test above.
describe("a fence that streams in over several deltas", () => {
  /** Write `parts` as separate deltas, forcing the 200ms flush between each so the
   *  provisional decoration pass actually runs — that pass is the whole subject. */
  function stream(...parts: string[]): HTMLElement {
    const host = document.createElement("div");
    const r = createMarkdownStream(host);
    for (const part of parts) {
      r.writeDelta(part);
      vi.advanceTimersByTime(250);
    }
    r.end();
    return host;
  }

  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  // The finding. An open fence collects `.code-wrap`, a language label and Copy from
  // decorateStreamingCodeTail; replacing only the nested `<pre>` left all of it
  // standing around the figure, so live streaming and replay produced different DOM
  // for the same completed message.
  it("leaves no code-block chrome around the converted diagram", () => {
    const host = stream("```svg\n" + SVG.slice(0, 20), SVG.slice(20) + "\n```\n");

    expect(host.querySelectorAll("figure")).toHaveLength(1);
    expect(host.querySelector("figure > img")).not.toBeNull();
    expect(host.querySelector(".code-wrap")).toBeNull();
    expect(host.querySelector(".code-lang")).toBeNull();
    expect(host.querySelector(".code-head")).toBeNull();
    expect(host.querySelector(".code-act-btn")).toBeNull();
    expect(host.querySelector("pre")).toBeNull();
  });

  // The provisional pass must genuinely have run, or the test above would pass for
  // the wrong reason on a change that simply stopped decorating open fences.
  it("really does decorate the fence while it is still open", () => {
    const host = document.createElement("div");
    const r = createMarkdownStream(host);
    r.writeDelta("```svg\n" + SVG.slice(0, 20));
    vi.advanceTimersByTime(250);
    expect(host.querySelector(".code-wrap")).not.toBeNull();
    expect(host.querySelector("figure")).toBeNull();
    r.end();
  });

  // An ordinary fence keeps every piece of that chrome; only a diagram sheds it.
  it("keeps the chrome on a streamed ordinary fence", () => {
    const host = stream("```go\npackage ", "main\n```\n");
    expect(host.querySelector(".code-wrap")).not.toBeNull();
    expect(host.querySelector(".code-lang")?.textContent).toBe("go");
    expect(host.querySelector("figure")).toBeNull();
  });
});

// The invariant under arbitrary source, not just the hand-picked hostile cases
// above. A diagram path that accepts model-authored content is the shape a
// sanitizer would normally guard, and the whole point of routing through `<img>`
// is that there is no sanitizer to get wrong — so the property is that NO node
// from the source ever reaches the document, whatever the source says.
describe("property: arbitrary svg source never becomes a node", () => {
  it("emits exactly one <img> and no element the source named", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(
        fc.stringMatching(/^[^`]*$/).filter((s) => s.trim() !== ""),
        (source) => {
          const host = document.createElement("div");
          renderMarkdownInto(host, "```svg\n" + source + "\n```\n");
          // Either it converted (one figure holding one img) or it declined and
          // left source behind. Both are safe; what must never happen is a node
          // parsed out of the source.
          const figure = host.querySelector("figure");
          if (figure === null) {
            return host.querySelector("script") === null && host.querySelector("svg") === null;
          }
          const inside = [...figure.querySelectorAll("*")];
          return (
            inside.length === 1 &&
            inside[0]?.tagName === "IMG" &&
            host.querySelector("script") === null &&
            host.querySelector("svg") === null &&
            host.querySelector("iframe") === null &&
            host.querySelector("[onload]") === null &&
            host.querySelector("[onerror]") === null
          );
        },
      ),
      { numRuns: 300 },
    );
    expect(result.failed).toBe(false);
  });

  // The source survives round-trip through the URL, so a rendered diagram is the
  // diagram the model wrote and not a filtered version of it.
  it("carries the source verbatim in the data: URL", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(
        fc.stringMatching(/^[^`\n]*$/).filter((s) => s.trim() !== ""),
        (source) => {
          const host = document.createElement("div");
          renderMarkdownInto(host, "```svg\n" + source + "\n```\n");
          const src = host.querySelector("img")?.getAttribute("src");
          if (src === undefined || src === null) {
            return true; // declined; covered by the property above
          }
          const decoded = decodeURIComponent(src.slice("data:image/svg+xml,".length));
          return decoded.trimEnd() === source.trimEnd();
        },
      ),
      { numRuns: 300 },
    );
    expect(result.failed).toBe(false);
  });
});
