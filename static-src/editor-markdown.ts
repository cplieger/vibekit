// ---------------------------------------------------------------------------
// The editor's rendered-markdown read surface.
//
// Every document on /docs opens in the editor, and the whole `.kiro` inventory
// is markdown with YAML front-matter, so the primary reading surface for every
// steering doc, skill and agent spec was raw source. The edit toggle still shows
// the source; only the READ state renders.
//
// # Nodes, never innerHTML
//
// `renderMarkdown(md): string` exists and is safe to re-parse today — the
// renderer's emittable tag set is fixed and namespace-flat, so no `svg`, `math`,
// `template` or `noscript` can be produced and an innerHTML round trip is an
// equivalent tree. It is still the wrong call. The property the transcript's
// renderer earns its keep with is architectural and grep-checkable — NO renderer
// output ever passes through an HTML parser, pinned by markdown.test.ts's
// fast-check properties over `<script`, `javascript:` and `on*` — and one
// `host.innerHTML = renderMarkdown(md)` here reintroduces exactly one parse step
// with no failing test behind it. CSP does not block innerHTML either (there is
// no trusted-types directive), so the invariant is the only thing standing
// between the next parser change and an injection; `script-src 'self' <hash>`
// and `object-src 'none'` are containment, not permission.
//
// `renderMarkdownInto(host, md)` builds the DOM directly, which is what the
// transcript's replay path already uses, and it gets code-block highlighting and
// path linkification for free through its own onBlockComplete hook.
//
// # The prose skin is the transcript's, not a copy
//
// The container carries `.message.assistant` on purpose. Despite the name that
// element is not a chat bubble — text-bubble.ts says so in as many words — it is
// the app's ONE rendered-markdown prose container, and its ~40rem cap is the
// reading measure where reading speed peaks, which is exactly what a reader of a
// steering document wants. The alternative was a second copy of ~180 lines of
// prose CSS in 20-editor.css, drifting from the first.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { renderMarkdownInto } from "./markdown.js";
import { splitFrontMatter, type FrontMatterField } from "./front-matter.js";

/** Render a markdown document into `host`: its front-matter as a metadata block,
 *  then its body as markdown. Replaces whatever `host` held. */
export function renderMarkdownDoc(host: HTMLElement, text: string): void {
  const doc = splitFrontMatter(text);
  const children: HTMLElement[] = [];
  if (doc.present && doc.fields.length > 0) {
    children.push(frontMatterBlock(doc.fields));
  }
  const prose = el("div", { className: "message assistant" });
  renderMarkdownInto(prose, doc.body);
  children.push(prose);
  host.replaceChildren(...children);
}

/** The front-matter metadata block.
 *
 *  Rendered rather than stripped because the header IS the interesting part of a
 *  steering doc: the inclusion mode answers "is this costing me tokens every
 *  session", `fileMatchPattern` answers "when does it load", and an agent's
 *  `model` and `tools` are the whole spec. Stripping it would hide the one thing
 *  a reader came for, and rendering it as markdown is not an option — a `---`
 *  fence is a horizontal rule and `key: value` lines are a paragraph.
 *
 *  A `<dl>` because that is what this is: declared terms and their values. Keys
 *  keep the author's order and spelling; nothing is defaulted in (an absent
 *  `inclusion` means the file did not declare one, and saying "always" here
 *  would be this surface inventing a steering rule for a skill). */
function frontMatterBlock(fields: FrontMatterField[]): HTMLElement {
  const list = el("dl", { className: "editor-fm-list" });
  for (const f of fields) {
    list.append(
      el("dt", { className: "editor-fm-key" }, f.key),
      el("dd", { className: "editor-fm-val" }, valueText(f)),
    );
  }
  return el(
    "section",
    { className: "editor-fm", "aria-label": "Document front-matter" },
    el("span", { className: "editor-fm-title" }, "Front-matter"),
    list,
  );
}

function valueText(f: FrontMatterField): string {
  return f.items.length > 0 ? f.items.join(", ") : f.value;
}
