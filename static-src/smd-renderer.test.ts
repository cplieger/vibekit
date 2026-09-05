// Table-driven tests for smd-renderer.ts TOKEN_TAG_MAP coverage via
// add_token_dom verifying all token types produce correct elements.

import { describe, it, expect, beforeAll, afterEach } from "vitest";
import { domRenderer } from "./smd-renderer.js";
import {
  PARAGRAPH,
  HEADING_1,
  HEADING_2,
  HEADING_3,
  HEADING_4,
  HEADING_5,
  HEADING_6,
  BLOCKQUOTE,
  ITALIC_AST,
  ITALIC_UND,
  STRONG_AST,
  STRONG_UND,
  STRIKE,
  CODE_INLINE,
  LIST_UNORDERED,
  LIST_ORDERED,
  LIST_ITEM,
  TABLE,
  CHECKBOX,
  CODE_FENCE,
  LINK,
  IMAGE,
  EQUATION_BLOCK,
  EQUATION_INLINE,
  UNCLOSED,
} from "./smd-parser-types.js";
import type { Token } from "./smd-parser-types.js";

describe("smd-renderer TOKEN_TAG_MAP coverage", () => {
  const cases: { token: Token; expectedTag: string; label: string }[] = [
    { token: PARAGRAPH, expectedTag: "P", label: "PARAGRAPH → p" },
    { token: HEADING_1, expectedTag: "H1", label: "HEADING_1 → h1" },
    { token: HEADING_2, expectedTag: "H2", label: "HEADING_2 → h2" },
    { token: HEADING_3, expectedTag: "H3", label: "HEADING_3 → h3" },
    { token: HEADING_4, expectedTag: "H4", label: "HEADING_4 → h4" },
    { token: HEADING_5, expectedTag: "H5", label: "HEADING_5 → h5" },
    { token: HEADING_6, expectedTag: "H6", label: "HEADING_6 → h6" },
    { token: BLOCKQUOTE, expectedTag: "BLOCKQUOTE", label: "BLOCKQUOTE → blockquote" },
    { token: ITALIC_AST, expectedTag: "EM", label: "ITALIC_AST → em" },
    { token: ITALIC_UND, expectedTag: "EM", label: "ITALIC_UND → em" },
    { token: STRONG_AST, expectedTag: "STRONG", label: "STRONG_AST → strong" },
    { token: STRONG_UND, expectedTag: "STRONG", label: "STRONG_UND → strong" },
    { token: STRIKE, expectedTag: "S", label: "STRIKE → s" },
    { token: CODE_INLINE, expectedTag: "CODE", label: "CODE_INLINE → code" },
    { token: LIST_UNORDERED, expectedTag: "UL", label: "LIST_UNORDERED → ul" },
    { token: LIST_ORDERED, expectedTag: "OL", label: "LIST_ORDERED → ol" },
    { token: LIST_ITEM, expectedTag: "LI", label: "LIST_ITEM → li" },
    { token: TABLE, expectedTag: "TABLE", label: "TABLE → table" },
    { token: CHECKBOX, expectedTag: "INPUT", label: "CHECKBOX → input" },
    { token: CODE_FENCE, expectedTag: "PRE", label: "CODE_FENCE → pre" },
    { token: LINK, expectedTag: "A", label: "LINK → a" },
    { token: IMAGE, expectedTag: "IMG", label: "IMAGE → img" },
    // The equation tokens open a plain HTML host that holds the raw LaTeX until
    // the expression closes; the namespaced <math> subtree replaces its children
    // at end_token. See the equation cases below and mathml.test.ts.
    {
      token: EQUATION_BLOCK,
      expectedTag: "SPAN",
      label: "EQUATION_BLOCK → span host",
    },
    {
      token: EQUATION_INLINE,
      expectedTag: "SPAN",
      label: "EQUATION_INLINE → span host",
    },
  ];

  it.each(cases)("$label", ({ token, expectedTag }) => {
    const container = document.createElement("div");
    const renderer = domRenderer(container, { animateText: false });
    renderer.add_token(renderer.data, token);
    const child = container.firstElementChild;
    expect(child).not.toBeNull();
    expect(child!.tagName).toBe(expectedTag);
  });
});

// ---------------------------------------------------------------------------
// Equations: the host, the namespace, and the raw degradation.
//
// The namespace assertion is the load-bearing one. `document.createElement`
// would produce an element whose tagName is also "math", so a test that only
// checked the tag name would pass on the exact defect this code exists to avoid.
// ---------------------------------------------------------------------------

const MATHML_NS = "http://www.w3.org/1998/Math/MathML";

/** Feed one equation through the renderer the way the parser does: open the
 *  token, hand over the LaTeX as text, close it. */
function renderEquation(token: Token, latex: string): HTMLElement {
  const container = document.createElement("div");
  const r = domRenderer(container, { animateText: false });
  r.add_token(r.data, token);
  r.add_text(r.data, latex);
  r.end_token(r.data);
  return container;
}

describe("smd-renderer equations", () => {
  it("holds the raw LaTeX while the expression is still open", () => {
    const container = document.createElement("div");
    const r = domRenderer(container, { animateText: false });
    r.add_token(r.data, EQUATION_INLINE);
    r.add_text(r.data, "x^2");
    const host = container.firstElementChild;
    expect(host?.getAttribute("data-math")).toBe("inline");
    expect(host?.hasAttribute("data-math-raw")).toBe(true);
    expect(host?.textContent).toBe("x^2");
    expect(host?.querySelector("math")).toBeNull();
  });

  it("swaps in a MathML subtree in the MathML namespace on close", () => {
    const container = renderEquation(EQUATION_INLINE, "x^2");
    const host = container.firstElementChild;
    expect(host?.hasAttribute("data-math-raw")).toBe(false);
    const math = host?.firstElementChild;
    expect(math?.tagName.toLowerCase()).toBe("math");
    expect(math?.namespaceURI).toBe(MATHML_NS);
    // Every descendant, not just the root: one createElement anywhere in the
    // converter would leave a subtree that does not render as mathematics.
    for (const node of math?.querySelectorAll("*") ?? []) {
      expect(node.namespaceURI).toBe(MATHML_NS);
    }
  });

  it("marks a block equation display=block", () => {
    const container = renderEquation(EQUATION_BLOCK, "\\frac{a}{b}");
    const host = container.firstElementChild;
    expect(host?.getAttribute("data-math")).toBe("block");
    expect(host?.firstElementChild?.getAttribute("display")).toBe("block");
  });

  it("keeps the raw string when the converter does not understand it", () => {
    const src = "\\begin{pmatrix} a & b \\end{pmatrix}";
    const container = renderEquation(EQUATION_INLINE, src);
    const host = container.firstElementChild;
    expect(host?.hasAttribute("data-math-raw")).toBe(true);
    expect(host?.querySelector("math")).toBeNull();
    expect(host?.textContent).toBe(src);
  });

  it("keeps the raw string for an expression split across chunks and closed", () => {
    const container = document.createElement("div");
    const r = domRenderer(container, { animateText: true });
    r.add_token(r.data, EQUATION_INLINE);
    // Split mid-command: the parser slices at a fixed byte budget, so any
    // delimiter or command can arrive in pieces.
    r.add_text(r.data, "\\al");
    r.add_text(r.data, "pha + \\beta");
    r.end_token(r.data);
    const host = container.firstElementChild;
    // animateText must NOT wrap an equation host's text: the chunk spans are
    // thrown away on close, and textContent has to be the exact source.
    expect(host?.querySelector("[data-vk-chunk-enter]")).toBeNull();
    const math = host?.firstElementChild;
    expect(math?.namespaceURI).toBe(MATHML_NS);
    expect(math?.textContent).toBe("\u03b1+\u03b2");
  });
});

describe("the streaming caret attribute", () => {
  // The CSS caret (13-messages.css) renders off `data-vk-caret`, the element
  // text last landed in — inline after the last word, not on its own line
  // below the paragraph, which is what a container ::after produced (user
  // report: "the cursor seems to not lead the text output but to be on the
  // next line").

  it("marks the element receiving text while streaming", () => {
    const container = document.createElement("div");
    const r = domRenderer(container, { animateText: true });
    r.add_token(r.data, PARAGRAPH);
    r.add_text(r.data, "hello");
    const p = container.querySelector("p");
    expect(p?.hasAttribute("data-vk-caret")).toBe(true);
  });

  it("moves with the insertion point, keeping exactly one caret", () => {
    const container = document.createElement("div");
    const r = domRenderer(container, { animateText: true });
    r.add_token(r.data, PARAGRAPH);
    r.add_text(r.data, "first");
    r.end_token(r.data);
    r.add_token(r.data, PARAGRAPH);
    r.add_text(r.data, "second");
    const marked = container.querySelectorAll("[data-vk-caret]");
    expect(marked).toHaveLength(1);
    expect(marked[0]?.textContent).toBe("second");
  });

  it("follows into an inline element mid-paragraph", () => {
    // Mid-inline is why the attribute cannot be a :last-child qualifier: the
    // insertion element during a bold run is the STRONG, not the paragraph.
    const container = document.createElement("div");
    const r = domRenderer(container, { animateText: true });
    r.add_token(r.data, PARAGRAPH);
    r.add_text(r.data, "plain ");
    r.add_token(r.data, STRONG_AST);
    r.add_text(r.data, "bold");
    const marked = container.querySelectorAll("[data-vk-caret]");
    expect(marked).toHaveLength(1);
    expect(marked[0]?.tagName).toBe("STRONG");
  });

  it("writes no caret on the replay path", () => {
    // Historical content paints flat: animateText=false gates the attribute
    // writes, so a reloaded transcript never pays them.
    const container = document.createElement("div");
    const r = domRenderer(container, { animateText: false });
    r.add_token(r.data, PARAGRAPH);
    r.add_text(r.data, "history");
    expect(container.querySelector("[data-vk-caret]")).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// Unwrapping an inline element whose token never closed.
//
// The parser is append-only and cannot un-open a token; the renderer still holds
// the element at close time, so it replaces it with its own delimiter followed
// by its children. That is what CommonMark renders for a delimiter run with no
// closer, and it is the only route to it that does not stop the stream.
// ---------------------------------------------------------------------------

/** Open `token`, add `text`, then close it unresolved with `delim`. */
function renderUnresolved(
  token: Token,
  text: string,
  delim: string,
  options: { animateText?: boolean } = {},
): HTMLElement {
  const container = document.createElement("div");
  const r = domRenderer(container, { animateText: options.animateText ?? false });
  r.add_token(r.data, PARAGRAPH);
  r.add_token(r.data, token);
  r.add_text(r.data, text);
  r.set_attr(r.data, UNCLOSED, delim);
  r.end_token(r.data);
  return container;
}

describe("smd-renderer unresolved inline tokens", () => {
  const cases: { label: string; token: Token; delim: string; tag: string }[] = [
    { label: "STRONG_AST", token: STRONG_AST, delim: "**", tag: "strong" },
    { label: "ITALIC_UND", token: ITALIC_UND, delim: "_", tag: "em" },
    { label: "STRIKE", token: STRIKE, delim: "~~", tag: "s" },
    { label: "CODE_INLINE", token: CODE_INLINE, delim: "`", tag: "code" },
    { label: "LINK", token: LINK, delim: "[", tag: "a" },
  ];

  it.each(cases)("replaces an unresolved $label with its delimiter", ({ token, delim, tag }) => {
    const container = renderUnresolved(token, "b", delim);
    expect(container.querySelector(tag)).toBeNull();
    expect(container.textContent).toBe(delim + "b");
  });

  it("replaces an unresolved IMAGE with its delimiter and alt text", () => {
    const container = document.createElement("div");
    const r = domRenderer(container, { animateText: false });
    r.add_token(r.data, PARAGRAPH);
    r.add_token(r.data, IMAGE);
    r.add_text(r.data, "img reference");
    r.set_attr(r.data, UNCLOSED, "![");
    r.end_token(r.data);
    expect(container.querySelector("img")).toBeNull();
    expect(container.textContent).toBe("![img reference");
  });

  it("does not convert an unresolved equation host to MathML", () => {
    const container = renderUnresolved(EQUATION_INLINE, "x^2", "$");
    expect(container.querySelector("math")).toBeNull();
    expect(container.querySelector("[data-math]")).toBeNull();
    expect(container.textContent).toBe("$x^2");
  });

  it("leaves a token that closed normally alone", () => {
    const container = document.createElement("div");
    const r = domRenderer(container, { animateText: false });
    r.add_token(r.data, PARAGRAPH);
    r.add_token(r.data, STRONG_AST);
    r.add_text(r.data, "b");
    r.end_token(r.data);
    expect(container.querySelector("strong")?.textContent).toBe("b");
  });

  it("unwraps nested unresolved tokens innermost first, keeping text order", () => {
    const container = document.createElement("div");
    const r = domRenderer(container, { animateText: false });
    r.add_token(r.data, PARAGRAPH);
    r.add_token(r.data, STRONG_AST);
    r.add_text(r.data, "b");
    r.add_token(r.data, ITALIC_AST);
    r.add_text(r.data, "c");
    r.set_attr(r.data, UNCLOSED, "*");
    r.end_token(r.data);
    r.set_attr(r.data, UNCLOSED, "**");
    r.end_token(r.data);
    expect(container.querySelector("strong")).toBeNull();
    expect(container.querySelector("em")).toBeNull();
    expect(container.textContent).toBe("**b*c");
  });

  it("moves the caret off the element it removes", () => {
    const container = document.createElement("div");
    const r = domRenderer(container, { animateText: true });
    r.add_token(r.data, PARAGRAPH);
    r.add_token(r.data, STRONG_AST);
    r.add_text(r.data, "b");
    expect(container.querySelector("strong")?.hasAttribute("data-vk-caret")).toBe(true);
    r.set_attr(r.data, UNCLOSED, "**");
    r.end_token(r.data);
    const marked = container.querySelectorAll("[data-vk-caret]");
    expect(marked).toHaveLength(1);
    expect(marked[0]?.tagName).toBe("P");
  });

  it("does not fire onBlockComplete for an unwrapped inline token", () => {
    const blocks: HTMLElement[] = [];
    const container = document.createElement("div");
    const r = domRenderer(container, {
      animateText: false,
      onBlockComplete: (b) => {
        blocks.push(b);
      },
    });
    r.add_token(r.data, PARAGRAPH);
    r.add_token(r.data, STRONG_AST);
    r.add_text(r.data, "b");
    r.set_attr(r.data, UNCLOSED, "**");
    r.end_token(r.data);
    expect(blocks).toHaveLength(0);
    r.end_token(r.data);
    expect(blocks.map((b) => b.tagName)).toEqual(["P"]);
  });
});

// ---------------------------------------------------------------------------
// The load-bearing R6 measurement, in a real browser rather than argued: the
// per-chunk fade animates each `<span data-vk-chunk-enter>` ONCE on mount, so an
// unwrap that recreated those spans would re-run the fade over settled text.
// ---------------------------------------------------------------------------

describe("unwrapping does not re-mount the per-chunk fade spans", () => {
  const ANIM = "chunk-enter-probe";
  const hosts: HTMLElement[] = [];

  beforeAll(() => {
    const style = document.createElement("style");
    style.textContent = `
      @keyframes ${ANIM} { from { opacity: 0 } to { opacity: 1 } }
      .${ANIM}-scope [data-vk-chunk-enter] { animation: ${ANIM} 5s linear backwards }
      .${ANIM}-scope [data-vk-chunk-settled] { animation: none }
    `;
    document.head.append(style);
  });

  afterEach(() => {
    for (const h of hosts.splice(0)) {
      h.remove();
    }
  });

  function nextFrame(): Promise<void> {
    return new Promise((resolve) => {
      requestAnimationFrame(() => {
        requestAnimationFrame(() => {
          resolve();
        });
      });
    });
  }

  it("re-parents the same span objects and does not restart their animation", async () => {
    const container = document.createElement("div");
    container.className = `${ANIM}-scope`;
    document.body.append(container);
    hosts.push(container);

    let starts = 0;
    container.addEventListener("animationstart", () => {
      starts += 1;
    });

    const r = domRenderer(container, { animateText: true });
    r.add_token(r.data, PARAGRAPH);
    r.add_token(r.data, STRONG_AST);
    r.add_text(r.data, "one ");
    r.add_text(r.data, "two ");
    r.add_text(r.data, "three");
    await nextFrame();
    const before = [...container.querySelectorAll("[data-vk-chunk-enter]")];
    expect(before).toHaveLength(3);
    expect(starts).toBe(3);

    r.set_attr(r.data, UNCLOSED, "**");
    r.end_token(r.data);
    await nextFrame();

    const after = [...container.querySelectorAll("[data-vk-chunk-enter]")];
    // Identity, not equality: `replaceWith(...el.childNodes)` MOVES the spans.
    expect(after).toEqual(before);
    // Re-inserting a node DOES restart its animation in Chromium (measured: 6
    // starts without the settled marker), so the unwrap marks the spans it moves
    // and the CSS rule skips them.
    expect(after.every((s) => s.hasAttribute("data-vk-chunk-settled"))).toBe(true);
    expect(starts).toBe(3);
    expect(container.textContent).toBe("**one two three");
  });
});
