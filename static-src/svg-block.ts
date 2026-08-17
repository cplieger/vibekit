// ---------------------------------------------------------------------------
// One inert render path for agent-produced SVG, and it replaces every diagram
// renderer vibekit might otherwise have grown.
//
// # Why `<img>` and nothing else
//
// An SVG referenced AS AN IMAGE is inert by specification: it may not fetch any
// resource, run any script, apply any external stylesheet, or reach the embedding
// document. So `<img src="data:image/svg+xml,…">` renders a diagram from
// UNTRUSTED content with no script execution, no external fetches and no DOM
// access — with no `dompurify` and no allowlist sanitizer to carry forever.
//
// # The one implementation choice that must be refused
//
// It must be an ELEMENT CREATED AND ASSIGNED A `src`. The invariant this protects
// is narrow and exact: SVG SOURCE NEVER REACHES AN HTML PARSER SINK. It is not
// that the markdown path contains no such sink — `code-blocks.ts` `finalizeBlock`
// assigns `codeEl.innerHTML = highlightByLang(...)` on every completed ordinary
// code block, and that is fine, because the text it feeds the parser is already
// escaped. What must never happen is agent-authored SVG taking that route: an
// `<svg>` subtree parsed into the page brings its own `onload`, its own `<script>`
// and its own external references with it.
//
// Two things make that hold here. The conversion runs BEFORE code decoration
// (markdown.ts `decorate`), so an svg fence never reaches `finalizeBlock` at all;
// and the source leaves this module only percent-encoded inside a `src`. The rest
// of the renderer builds text with `document.createTextNode` (smd-renderer.ts) and
// namespaced subtrees with `replaceChildren` (`finalizeMath`), so nothing else on
// the path parses markup either. `tool-card.ts` (ANSI output, card details) and
// `editor-ui.ts` (`$.editorCode.innerHTML = highlight(...)`) have their own sinks;
// SVG must not reach those either.
//
// # A `data:` URL, not a blob URL
//
// The CSP is `img-src 'self' data:` (internal/server/security.go) — `blob:` is
// absent, so `URL.createObjectURL` would be blocked outright, and widening the
// policy to permit it is a security change nobody asked for. The `src` is also
// assigned OUTSIDE smd-renderer's `set_attr_dom` funnel, which gates `src`
// through `isSafeUrl` — and `isSafeUrl` rejects `data:`, so a diagram routed
// through the funnel would be rewritten to `"#"`.
//
// # The carrier is a closed code fence
//
// There is no HTML token and no SVG token in the parser: inline `<svg>…</svg>` in
// agent prose reaches `add_text_dom` and lands as literal TEXT, which is the
// property this module protects rather than changes. A fenced block is the only
// structured carrier, its info string is already captured as the `<code>`'s
// `language-*` class, and `onBlockComplete` already fires on a closed fence with
// the `<pre>` as its target. So this needs no parser change at all.
//
// # Accepted tradeoffs
//
// No interactivity (no hover, no click-through, no selectable label text), and
// worse layout quality than mermaid-plus-dagre. Mermaid itself is deferred, not
// answered: source and render are separable, so this is the render path, and
// model-authored SVG or a user-configured mermaid MCP server is a SOURCE question
// for later. Do not add `mermaid` to the language set here — that would render
// mermaid source as SVG, which it is not.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { extractLang } from "./code-blocks.js";

/** Fence languages this module renders. Deliberately one member. */
const SVG_FENCE_LANGS: ReadonlySet<string> = new Set(["svg"]);

/** Convert a closed ```svg fence into a rendered, inert diagram.
 *
 *  Returns the `<figure>` now standing where the `<pre>` was — or where its
 *  provisional `.code-wrap` was, when the fence streamed in and collected one — so
 *  the caller can skip the code-block decoration a diagram should not collect (a
 *  language label, Copy, syntax highlighting) and can tag the right element for the
 *  entry animation. Returns null — leaving the `<pre>` exactly as it found it — for
 *  any other fence and for an empty one, because a diagram that cannot be built is
 *  better read as its source. */
export function renderSvgBlock(pre: HTMLElement): HTMLElement | null {
  const code = pre.querySelector("code");
  if (code === null || !SVG_FENCE_LANGS.has(extractLang(pre, code))) {
    return null;
  }
  const source = code.textContent;
  if (source.trim() === "") {
    return null;
  }

  const img = el("img", { className: "svg-diagram" }) as HTMLImageElement;
  // A diagram with no description is an empty announcement to a screen reader.
  // There is nothing on the wire that describes it, so the alt names the KIND
  // rather than inventing content.
  img.alt = "Agent-produced diagram";
  const figure = el("figure", { className: "svg-figure" }, img);
  img.addEventListener(
    "error",
    () => {
      // Malformed or unsupported SVG. Degrade to a named message rather than a
      // broken-icon hole, matching the transcript's existing missing-image
      // affordance. The source is not restored: the fence was replaced, and
      // rebuilding it here would be a second renderer.
      img.replaceWith(el("span", { className: "img-missing" }, "Diagram could not be rendered"));
    },
    { once: true },
  );
  // REPLACE THE WRAPPER WHEN THERE IS ONE. A fence that arrives over several
  // deltas is decorated provisionally while it is still open — `.code-wrap` plus a
  // title bar carrying the language and Copy (code-blocks.ts
  // `decorateStreamingCodeTail`) — and swapping only the nested `<pre>` left that
  // wrapper and header standing around the figure. The replay path never takes the
  // provisional pass, so the two paths produced different DOM for the same finished
  // message and only the live one was wrong.
  const parent = pre.parentElement;
  const wrap = parent?.classList.contains("code-wrap") === true ? parent : null;
  (wrap ?? pre).replaceWith(figure);
  // After the swap, so a synchronous decode failure has a parent to replace into.
  img.src = `data:image/svg+xml,${encodeURIComponent(source)}`;
  return figure;
}
