// ---------------------------------------------------------------------------
// DOM renderer with per-chunk animation hook.
//
// Each text fragment emitted by the parser is wrapped in a
// `<span data-vk-stream>` so CSS can fade it in as it mounts. The
// parser never rewrites existing DOM (append-only), so spans fade in
// once and stay put — no flicker from re-parsing partial tokens.
//
// On `finalize(animated = false)` the spans unwrap (turn-end, history
// replay) so long messages don't carry thousands of wrapper elements
// forever. The renderer is reusable across streams: reset via `nodes`/`index`.
// ---------------------------------------------------------------------------

import {
  DOCUMENT, BLOCKQUOTE, PARAGRAPH, LINE_BREAK, RULE,
  HEADING_1, HEADING_2, HEADING_3, HEADING_4, HEADING_5, HEADING_6,
  ITALIC_AST, ITALIC_UND, STRONG_AST, STRONG_UND, STRIKE,
  CODE_INLINE, CODE_BLOCK, CODE_FENCE, RAW_URL, LINK, IMAGE,
  LIST_UNORDERED, LIST_ORDERED, LIST_ITEM, CHECKBOX,
  TABLE, TABLE_ROW, TABLE_CELL, EQUATION_BLOCK, EQUATION_INLINE,
  attr_to_html_attr,
} from "./smd-parser-types.js";
import type { Token, Attr, Renderer } from "./smd-parser-types.js";
import { isSafeUrl } from "./files-shared.js";

export type { Renderer } from "./smd-parser-types.js";

export interface DomRendererData {
  nodes: (Element | null)[];
  index: number;
  animate: boolean;
  streamSpans: HTMLSpanElement[];
}

function makeEl(tag: string): HTMLElement {
  return document.createElement(tag);
}

const TOKEN_TAG_MAP: Readonly<Record<number, string>> = {
  [BLOCKQUOTE]: "blockquote",
  [PARAGRAPH]: "p",
  [LINE_BREAK]: "br",
  [RULE]: "hr",
  [HEADING_1]: "h1",
  [HEADING_2]: "h2",
  [HEADING_3]: "h3",
  [HEADING_4]: "h4",
  [HEADING_5]: "h5",
  [HEADING_6]: "h6",
  [ITALIC_AST]: "em",
  [ITALIC_UND]: "em",
  [STRONG_AST]: "strong",
  [STRONG_UND]: "strong",
  [STRIKE]: "s",
  [CODE_INLINE]: "code",
  [IMAGE]: "img",
  [LIST_UNORDERED]: "ul",
  [LIST_ORDERED]: "ol",
  [LIST_ITEM]: "li",
  [TABLE]: "table",
  [EQUATION_BLOCK]: "equation-block",
  [EQUATION_INLINE]: "equation-inline",
};

function add_token_dom(data: DomRendererData, type: Token): void {
  const parent = data.nodes[data.index] as Element;

  if (type === DOCUMENT) return;

  // Special cases with non-trivial logic
  switch (type) {
    case CHECKBOX: {
      const cb = makeEl("input") as HTMLInputElement;
      cb.type = "checkbox";
      cb.disabled = true;
      data.nodes[++data.index] = parent.appendChild(cb);
      return;
    }
    case CODE_BLOCK:
    case CODE_FENCE: {
      // <pre class="code"> ... <code>. The LANG attr (when set by the
      // parser) lands on the <code> via set_attr_dom as class="language-X"
      // so the existing code-blocks.ts decorator picks it up. The pre
      // gets a plain "code" class for CSS targeting.
      const pre = parent.appendChild(makeEl("pre"));
      pre.className = "code";
      const slot = makeEl("code");
      data.nodes[++data.index] = pre.appendChild(slot);
      return;
    }
    case LINK:
    case RAW_URL: {
      // Match render.ts link semantics: external-safe opener with
      // target="_blank" rel="noopener". LINK's href arrives via
      // set_attr_dom with HREF. RAW_URL's href is the element text
      // itself — set_attr_dom sets HREF there too for the link variant.
      const a = makeEl("a") as HTMLAnchorElement;
      a.target = "_blank";
      a.rel = "noopener";
      data.nodes[++data.index] = parent.appendChild(a);
      return;
    }
    case TABLE_ROW: {
      let tableParent = parent;
      switch (parent.children.length) {
        case 0:
          tableParent = parent.appendChild(makeEl("thead"));
          break;
        case 1:
          tableParent = parent.appendChild(makeEl("tbody"));
          break;
        default:
          tableParent = parent.children[1] as Element;
      }
      const slot = makeEl("tr");
      data.nodes[++data.index] = tableParent.appendChild(slot);
      return;
    }
    case TABLE_CELL: {
      const isHeader = parent.parentElement?.tagName === "THEAD";
      const slot = makeEl(isHeader ? "th" : "td");
      data.nodes[++data.index] = parent.appendChild(slot);
      return;
    }
  }

  // Simple token-to-tag mapping
  const tag = TOKEN_TAG_MAP[type];
  if (tag === undefined) return;
  data.nodes[++data.index] = parent.appendChild(makeEl(tag));
}

function end_token_dom(data: DomRendererData): void {
  data.index -= 1;
}

function add_text_dom(data: DomRendererData, text: string): void {
  const parent = data.nodes[data.index];
  if (parent === null || parent === undefined) return;

  // Inside code/pre/annotation blocks, don't wrap — syntax highlighters
  // and monospace formatting depend on seeing plain text.
  const tag = (parent as Element).tagName;
  if (tag === "CODE" || tag === "PRE" || tag === "SCRIPT" || tag === "STYLE") {
    parent.appendChild(document.createTextNode(text));
    return;
  }

  // IMG is void — text inside an image node is the alt text per
  // markdown syntax `![alt](url)`. Append to the alt attribute rather
  // than creating (ignored) child text nodes.
  if (tag === "IMG") {
    const img = parent as HTMLImageElement;
    img.alt = (img.alt ?? "") + text;
    return;
  }

  if (!data.animate) {
    parent.appendChild(document.createTextNode(text));
    return;
  }

  // Wrap every chunk of text in an animated span. The CSS animation
  // fires once on mount (via animation-fill-mode: both). Newly-arrived
  // chunks fade in over 200ms; already-mounted spans are never
  // touched, so no flicker on subsequent flushes.
  const span = document.createElement("span");
  span.setAttribute("data-vk-stream", "");
  span.appendChild(document.createTextNode(text));
  parent.appendChild(span);
  data.streamSpans.push(span);
}

function set_attr_dom(data: DomRendererData, attr: Attr, value: string): void {
  const node = data.nodes[data.index];
  if (node === null || node === undefined) return;
  const attrName = attr_to_html_attr(attr);
  if (attrName === "") return;
  // Links/images: treat URLs defensively — strip javascript:/data: to
  // match the safety posture of the rest of vibekit's markdown
  // rendering. Same logic as render.ts's extractURL.
  if ((attrName === "href" || attrName === "src") && !isSafeUrl(value)) {
    (node as Element).setAttribute(attrName, "#");
    return;
  }
  // Code-fence language: the parser emits LANG → "class" via attr_to_html_attr.
  // highlight.js and decorateCodeBlocks expect `class="language-X"`, so prefix
  // the raw language name here (parser emits "rust", we set "language-rust").
  if (attrName === "class" && (node as Element).tagName === "CODE") {
    (node as Element).setAttribute("class", "language-" + value);
    return;
  }
  (node as Element).setAttribute(attrName, value);
}

/** Factory for a DOM renderer rooted at `el`. Set `animate: false`
 *  for historical messages (chat switch, history replay) so the
 *  animation doesn't fire on content the user already saw. */
export function domRenderer(el: HTMLElement, animate = true): Renderer<DomRendererData> {
  return {
    add_token: add_token_dom,
    end_token: end_token_dom,
    add_text: add_text_dom,
    set_attr: set_attr_dom,
    data: {
      nodes: [el],
      index: 0,
      animate,
      streamSpans: [],
    },
  };
}

/** After the stream ends, unwrap the animation spans so the finalised
 *  message doesn't carry thousands of wrapper elements indefinitely.
 *  Text nodes are merged back into their parents via normalize(). */
export function unwrapStreamSpans(root: HTMLElement, spans?: HTMLSpanElement[]): void {
  const list = spans ?? root.querySelectorAll<HTMLSpanElement>("span[data-vk-stream]");
  for (const span of list) {
    const parent = span.parentNode;
    if (parent === null) continue;
    while (span.firstChild !== null) {
      parent.insertBefore(span.firstChild, span);
    }
    parent.removeChild(span);
  }
  root.normalize();
}
