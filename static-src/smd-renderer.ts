// ---------------------------------------------------------------------------
// DOM renderer for smd-parser.
//
// The renderer supports two streaming concerns:
//
//   1. Per-block animation. When a top-level block closes (paragraph,
//      pre/code, heading, list, blockquote, table), the renderer fires
//      `onBlockComplete(element)`. Callers tag the element with
//      `data-vk-block-enter` so a single CSS rule fades it in. This
//      complements the per-chunk fade (add_text_dom wraps each streamed
//      text fragment in a `<span data-vk-chunk-enter>`): the block-level
//      fade covers structural elements (pre/table/blockquote) that
//      shouldn't be split into per-fragment spans.
//
//   2. Inline decoration. Same hook lets callers run code-block
//      syntax highlighting + path linkification per block as it
//      arrives, so users see colored code and clickable paths during
//      streaming, not only after `parser_end`.
//
// The renderer doesn't own animation policy or decoration — it
// exposes the structural events and lets callers decide.
// ---------------------------------------------------------------------------

import {
  DOCUMENT,
  BLOCKQUOTE,
  PARAGRAPH,
  LINE_BREAK,
  RULE,
  HEADING_1,
  HEADING_2,
  HEADING_3,
  HEADING_4,
  HEADING_5,
  HEADING_6,
  ITALIC_AST,
  ITALIC_UND,
  STRONG_AST,
  STRONG_UND,
  STRIKE,
  CODE_INLINE,
  CODE_BLOCK,
  CODE_FENCE,
  RAW_URL,
  LINK,
  IMAGE,
  LIST_UNORDERED,
  LIST_ORDERED,
  LIST_ITEM,
  CHECKBOX,
  TABLE,
  TABLE_ROW,
  TABLE_CELL,
  EQUATION_BLOCK,
  EQUATION_INLINE,
  attr_to_html_attr,
} from "./smd-parser-types.js";
import type { Token, Attr, Renderer } from "./smd-parser-types.js";
import { isSafeUrl, rewriteWorkspaceImageSrc } from "./utils-url.js";
import { mediaElementFor } from "./media-block.js";
import { latexToMathML } from "./mathml.js";
import { el } from "@cplieger/reactive";

export type { Renderer } from "./smd-parser-types.js";

export interface DomRendererData {
  nodes: (Element | null)[];
  index: number;
  onBlockComplete: ((block: HTMLElement) => void) | undefined;
  /** Wrap each text emission in a `<span data-vk-chunk-enter>` so the
   *  per-chunk fade-in CSS animation fires as text streams in. Off
   *  for replay paths so historical chats don't animate. Skipped
   *  inside `<code>` / `<pre>` because spans there break syntax
   *  highlighters that expect raw text children. */
  animateText: boolean;
  /** The current `data-vk-caret` holder — the element the stream last wrote
   *  text into, where the CSS caret renders. See moveCaret. */
  caretEl?: Element | null;
}

function makeEl(tag: string): HTMLElement {
  return el(tag);
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
};

/** Attribute marking an equation host, and the state it is in.
 *
 *  `data-math` is the kind (`inline` / `block`); `data-math-raw` says the host
 *  is still showing LaTeX rather than mathematics. Set at creation — a
 *  mid-stream equation genuinely IS raw text at that moment — and removed only
 *  when the conversion succeeds, so a half-arrived or unsupported expression is
 *  styled as source without a second code path. */
const MATH_ATTR = "data-math";
const MATH_RAW_ATTR = "data-math-raw";

/** Turn a closed equation host's LaTeX into MathML in place.
 *
 *  Runs at end_token, not at add_token: the parser hands the expression over as
 *  text (possibly across several chunks), so the full source only exists once
 *  the closing delimiter arrives. An expression the converter rejects keeps the
 *  raw text it already holds — the degradation the equation tokens gave for
 *  free before there was a converter at all. */
function finalizeMath(host: Element): void {
  const src = host.textContent;
  const math = latexToMathML(src, host.getAttribute(MATH_ATTR) === "block");
  if (math === null) {
    return;
  }
  host.replaceChildren(math);
  host.removeAttribute(MATH_RAW_ATTR);
}

function add_token_dom(data: DomRendererData, type: Token): void {
  // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
  const parent = data.nodes[data.index]!;

  if (type === DOCUMENT) {
    return;
  }

  switch (type) {
    case CHECKBOX: {
      const cb = makeEl("input") as HTMLInputElement;
      cb.type = "checkbox";
      cb.disabled = true;
      cb.setAttribute("aria-label", "Task item");
      data.nodes[++data.index] = parent.appendChild(cb);
      return;
    }
    case CODE_BLOCK:
    case CODE_FENCE: {
      const pre = parent.appendChild(makeEl("pre"));
      pre.className = "code";
      const slot = makeEl("code");
      data.nodes[++data.index] = pre.appendChild(slot);
      return;
    }
    case EQUATION_BLOCK:
    case EQUATION_INLINE: {
      // A plain host in the HTML namespace, holding the raw LaTeX until the
      // expression closes. It cannot BE the `<math>` element: makeEl is
      // document.createElement, and a `math` element outside the MathML
      // namespace is an unknown inline element rather than mathematics. The
      // namespaced subtree is built by mathml.ts and swapped in at end_token.
      const host = makeEl("span");
      host.setAttribute(MATH_ATTR, type === EQUATION_BLOCK ? "block" : "inline");
      host.setAttribute(MATH_RAW_ATTR, "");
      data.nodes[++data.index] = parent.appendChild(host);
      return;
    }
    case LINK:
    case RAW_URL: {
      const a = makeEl("a") as HTMLAnchorElement;
      a.target = "_blank";
      a.rel = "noopener";
      data.nodes[++data.index] = parent.appendChild(a);
      return;
    }
    case TABLE_ROW: {
      let tableParent: Element;
      switch (parent.children.length) {
        case 0:
          tableParent = parent.appendChild(makeEl("thead"));
          break;
        case 1:
          tableParent = parent.appendChild(makeEl("tbody"));
          break;
        default:
          // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
          tableParent = parent.children[1]!;
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

  const tag = TOKEN_TAG_MAP[type];
  if (tag === undefined) {
    return;
  }
  data.nodes[++data.index] = parent.appendChild(makeEl(tag));
}

function end_token_dom(data: DomRendererData): void {
  const closing = data.nodes[data.index];
  data.index -= 1;
  if (closing?.hasAttribute(MATH_ATTR) === true) {
    finalizeMath(closing);
  }
  // If decrementing brought us back to the root (index 0), the node
  // that just closed was a top-level block. Fire the callback so
  // callers can decorate / animate the freshly-completed block.
  // Code blocks are special: the "closing" node is the inner <code>,
  // but the visible block is its <pre> parent — surface that instead.
  if (
    data.index === 0 &&
    closing !== null &&
    closing !== undefined &&
    data.onBlockComplete !== undefined
  ) {
    const tag = closing.tagName;
    const target =
      tag === "CODE" && closing.parentElement?.tagName === "PRE"
        ? closing.parentElement
        : (closing as HTMLElement);
    data.onBlockComplete(target);
  }
}

/** The streaming caret's home: the element text last landed in carries
 *  `data-vk-caret`, so the CSS caret (13-messages.css) renders inline after the
 *  last word rather than on its own line after the last block — a container
 *  `::after` is what put the cursor "on the next line". One holder at a time,
 *  moved only when the insertion element changes; gated on `animateText`, so a
 *  replay never pays the attribute writes. The bubble's `.streaming` class
 *  scopes the CSS, so the attribute is inert once the bubble seals. */
const CARET_ATTR = "data-vk-caret";

function moveCaret(data: DomRendererData, target: Element): void {
  if (!data.animateText || data.caretEl === target) {
    return;
  }
  data.caretEl?.removeAttribute(CARET_ATTR);
  target.setAttribute(CARET_ATTR, "");
  data.caretEl = target;
}

function add_text_dom(data: DomRendererData, text: string): void {
  const parent = data.nodes[data.index];
  if (parent === null || parent === undefined) {
    return;
  }

  const tag = parent.tagName;

  // IMG is void — text inside an image node is the alt text per
  // markdown syntax `![alt](url)`. Append to the alt attribute rather
  // than creating (ignored) child text nodes.
  if (tag === "IMG") {
    const img = parent as HTMLImageElement;
    // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition
    img.alt = (img.alt ?? "") + text;
    return;
  }

  moveCaret(data, parent);

  // For streaming render, wrap the text in an inline span so per-chunk
  // fade-in CSS can animate each delta as it arrives. Skipped inside
  // <code>/<pre> (their syntax highlighter expects unwrapped text
  // children). Replay path leaves animateText=false so historical
  // content paints flat.
  // An equation host is skipped too: its children are replaced wholesale when
  // the expression closes, so a per-chunk wrapper is work thrown away.
  if (data.animateText && tag !== "CODE" && tag !== "PRE" && !parent.hasAttribute(MATH_ATTR)) {
    const span = makeEl("span");
    span.setAttribute("data-vk-chunk-enter", "");
    span.appendChild(document.createTextNode(text));
    parent.appendChild(span);
    return;
  }
  parent.appendChild(document.createTextNode(text));
}

function set_attr_dom(data: DomRendererData, attr: Attr, value: string): void {
  const node = data.nodes[data.index];
  if (node === null || node === undefined) {
    return;
  }
  const attrName = attr_to_html_attr(attr);
  if (attrName === "") {
    return;
  }
  if ((attrName === "href" || attrName === "src") && !isSafeUrl(value)) {
    node.setAttribute(attrName, "#");
    return;
  }
  // An agent-written workspace path is a real file, not a URL the SPA can serve.
  // Rewritten AFTER the safety gate so the rewrite can never launder a scheme
  // isSafeUrl just rejected.
  if (attrName === "src" && node.tagName === "IMG") {
    // The FILE-ROLE swap, and it has to happen here rather than at add_token:
    // the tag is chosen when `![` opens and the path only arrives when `)`
    // closes, so this is the first moment the role is knowable. `![clip](x.mp3)`
    // rendered a broken `<img>` before this.
    const swapped = mediaElementFor(value, node.getAttribute("alt") ?? "");
    if (swapped !== null) {
      node.replaceWith(swapped);
      // Re-point the node stack, or end_token would close a node no longer in
      // the document and any later emission would land on the departed `<img>`.
      data.nodes[data.index] = swapped;
      return;
    }
    node.setAttribute(attrName, rewriteWorkspaceImageSrc(value));
    // Name the file when it fails to load. The browser's default broken-image
    // icon identifies nothing, and "the picture did not appear" is the
    // characteristic failure of the agent's screenshot loop — the one fact
    // worth having then is which path was asked for. Swapping to text also
    // avoids leaving a broken-icon-sized hole mid-transcript.
    node.addEventListener(
      "error",
      () => {
        const src = node.getAttribute("src") ?? "";
        const alt = node.getAttribute("alt") ?? "";
        const shown = alt !== "" ? alt : decodeURIComponent(src.replace(/^.*[?&]path=/, ""));
        const miss = document.createElement("span");
        miss.className = "img-missing";
        miss.textContent = `Image not available: ${shown}`;
        node.replaceWith(miss);
      },
      { once: true },
    );
    return;
  }
  if (attrName === "class" && node.tagName === "CODE") {
    node.setAttribute("class", "language-" + value);
    return;
  }
  node.setAttribute(attrName, value);
}

/** Factory for a DOM renderer rooted at `root`. Caller can register
 *  `onBlockComplete` to receive a callback when each top-level block
 *  finishes (paragraph, pre/code, heading, list, blockquote, table).
 *  `animateText` (default false) opts into per-chunk fade-in: each
 *  text emission is wrapped in `<span data-vk-chunk-enter>` so the
 *  CSS animation in 13-messages.css runs as each delta lands. */
export function domRenderer(
  root: HTMLElement,
  options: { onBlockComplete?: (block: HTMLElement) => void; animateText?: boolean } = {},
): Renderer<DomRendererData> {
  return {
    add_token: add_token_dom,
    end_token: end_token_dom,
    add_text: add_text_dom,
    set_attr: set_attr_dom,
    data: {
      nodes: [root],
      index: 0,
      onBlockComplete: options.onBlockComplete,
      animateText: options.animateText ?? false,
    },
  };
}
