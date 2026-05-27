// @vitest-environment happy-dom
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
  const cases: Array<{ token: Token; expectedTag: string; label: string }> = [
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
    {
      token: EQUATION_BLOCK,
      expectedTag: "EQUATION-BLOCK",
      label: "EQUATION_BLOCK → equation-block",
    },
    {
      token: EQUATION_INLINE,
      expectedTag: "EQUATION-INLINE",
      label: "EQUATION_INLINE → equation-inline",
    },
  ];

  it.each(cases)("$label", ({ token, expectedTag }) => {
    const container = document.createElement("div");
    const renderer = domRenderer(container, false);
    renderer.add_token(renderer.data, token);
    const child = container.firstElementChild;
    expect(child).not.toBeNull();
    expect(child!.tagName).toBe(expectedTag);
  });
});
