// Table-driven tests for smd-renderer.ts TOKEN_TAG_MAP coverage via
// add_token_dom verifying all token types produce correct elements.

import { describe, it, expect } from "vitest";
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
