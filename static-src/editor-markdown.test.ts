//
// The rendered-markdown read mode: what a `.md` file shows when it is not being
// edited, and that nothing else changed. renderReadSurface is the funnel every
// read paint goes through, so the mode assertions drive it rather than poking at
// the show* helpers.
import { describe, it, expect, beforeEach, vi } from "vitest";

// The editor's import graph is heavy; only the read surfaces in .editor-body and
// the gutter matter here, so dom.ts is a stub over real elements. Hoisted with
// the mock: vi.mock's factory runs before module-level statements.
const { surfaces } = vi.hoisted(() => ({
  surfaces: {
    editorHighlight: document.createElement("pre"),
    editorCode: document.createElement("code"),
    editorContent: document.createElement("textarea"),
    editorMarkdown: document.createElement("div"),
    editorImage: document.createElement("div"),
    editorDiffPane: document.createElement("div"),
    editorGutter: document.createElement("pre"),
  },
}));

vi.mock("./dom.js", () => ({
  $: surfaces,
  // `byId` is reached through this graph by page-title.ts. ESM links for real, so
  // a name any module in the graph imports must exist on the mock or the whole
  // FILE fails at link time, naming the export rather than the test. Inlined
  // rather than shared because a `vi.mock` factory is hoisted above every
  // top-level import, so it cannot reach a helper module. Resolve-or-create,
  // because the real `byId` throws and a suite mocking `dom.js` stages only what
  // its own subject needs.
  byId: (id: string): HTMLElement => {
    let el = document.getElementById(id);
    if (el === null) {
      el = document.createElement("span");
      el.id = id;
      document.body.appendChild(el);
    }
    return el;
  },
}));
// highlight() escapes its input by construction and emits only <span> wrappers;
// the identity stub keeps the raw-source assertion about what the editor SHOWS
// rather than about the highlighter's markup.
vi.mock("./highlight.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  highlightByLang: undefined,
  normalizeLang: undefined,
  resolveLangHint: undefined,
  highlightMarked: undefined,
  highlight: (s: string) => s,
}));
// `get` is reached through the tab factory, which reads the chat store for a chat
// tab's display NAME. Present-but-inert: no tab is materialized here.
//
// Spreads the REAL module rather than listing names. Browser Mode links ESM for
// real rather than reading properties off a namespace object, so every name any
// module in this graph imports has to exist on the mock even when nothing here
// calls it. Listing them was a treadmill: the tab projection made `tabs.ts` and
// `tab-materialize.ts` new importers, and each new name cost another red run to
// discover. Spreading makes the mock total by construction, and the two
// overrides below are the only behaviour this file actually needs pinned.
vi.mock("./store.js", async () => ({
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  ...(await vi.importActual<typeof import("./store.js")>("./store.js")),
  getActiveId: () => "",
  get: vi.fn(() => undefined),
}));
vi.mock("./actions/editor.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  loadDiff: undefined,
  suggestResolution: undefined,
  fetchAgentLines: { cancel: () => undefined, dispatch: () => Promise.resolve(null) },
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  getActive: vi.fn(() => undefined),
  getSessions: vi.fn(() => []),
  tabStatusFor: vi.fn(() => ""),
}));
vi.mock("./editor-scroll.js", () => ({
  scrollToEditorLine: () => undefined,
  flashEditorLine: () => undefined,
}));

import { renderMarkdownDoc } from "./editor-markdown.js";
import { renderReadSurface } from "./editor-ui.js";
import { freshState, rendersMarkdown } from "./editor-types.js";

function hidden(el: HTMLElement): boolean {
  return el.classList.contains("hidden");
}

function loaded(path: string, content: string): ReturnType<typeof freshState> {
  const state = freshState(path);
  state.original.value = content;
  state.current.value = content;
  state.loaded = true;
  return state;
}

beforeEach(() => {
  for (const el of Object.values(surfaces)) {
    el.className = "";
    el.replaceChildren();
  }
});

describe("rendersMarkdown", () => {
  it("covers the two markdown extensions, case-insensitively", () => {
    expect(rendersMarkdown(".kiro/steering/vibekit.md")).toBe(true);
    expect(rendersMarkdown("README.MD")).toBe(true);
    expect(rendersMarkdown("notes.markdown")).toBe(true);
  });

  it("leaves every other file alone", () => {
    expect(rendersMarkdown("static-src/tabs.ts")).toBe(false);
    expect(rendersMarkdown("main.go")).toBe(false);
    // Not a markdown file: the extension is the whole rule, and a name that
    // merely contains ".md" is a different file.
    expect(rendersMarkdown("notes.md.bak")).toBe(false);
  });
});

describe("read mode for a markdown document", () => {
  it("renders the markdown and hides the source surfaces", () => {
    renderReadSurface(loaded("docs/guide.md", "# Title\n\nSome **bold** prose.\n"));
    expect(hidden(surfaces.editorMarkdown)).toBe(false);
    expect(hidden(surfaces.editorHighlight)).toBe(true);
    expect(hidden(surfaces.editorContent)).toBe(true);
    expect(surfaces.editorMarkdown.querySelector("h1")?.textContent).toBe("Title");
    expect(surfaces.editorMarkdown.querySelector("strong")?.textContent).toBe("bold");
    // The raw markers are gone from the rendered text.
    expect(surfaces.editorMarkdown.textContent).not.toContain("**");
  });

  // Source line numbers beside rendered prose would number something that is no
  // longer on screen.
  it("hides the gutter, and restores it for a source file", () => {
    renderReadSurface(loaded("docs/guide.md", "# Title\n"));
    expect(hidden(surfaces.editorGutter)).toBe(true);
    renderReadSurface(loaded("main.go", "package main\n"));
    expect(hidden(surfaces.editorGutter)).toBe(false);
  });

  // Reuses the app's one rendered-markdown prose skin rather than a second copy
  // of it; the class is the contract that makes that true.
  it("hosts the prose in the shared prose container", () => {
    renderReadSurface(loaded("docs/guide.md", "text\n"));
    expect(surfaces.editorMarkdown.querySelector(".message.assistant")).not.toBeNull();
  });

  // The property markdown.test.ts pins for the transcript has to hold here too:
  // renderer output never passes through an HTML parser, so a script tag in a
  // document is text.
  it("does not interpret HTML in the document", () => {
    renderReadSurface(loaded("docs/guide.md", "<script>alert(1)</script>\n"));
    expect(surfaces.editorMarkdown.querySelector("script")).toBeNull();
    expect(surfaces.editorMarkdown.textContent).toContain("alert(1)");
  });

  it("repaints from scratch, leaving no trace of the previous document", () => {
    renderReadSurface(loaded("docs/a.md", "# First\n"));
    renderReadSurface(loaded("docs/b.md", "# Second\n"));
    expect(surfaces.editorMarkdown.textContent).not.toContain("First");
    expect(surfaces.editorMarkdown.querySelector("h1")?.textContent).toBe("Second");
  });
});

describe("read mode for a non-markdown file", () => {
  it("shows raw source and hides the markdown surface", () => {
    renderReadSurface(loaded("main.go", "package main\n"));
    expect(hidden(surfaces.editorMarkdown)).toBe(true);
    expect(hidden(surfaces.editorHighlight)).toBe(false);
    expect(surfaces.editorCode.textContent).toContain("package main");
  });

  // A markdown-looking file that is not markdown must not be rendered: the read
  // surface is keyed on the path, and a .ts file full of `#` comments is source.
  it("does not render a source file that happens to contain markdown", () => {
    renderReadSurface(loaded("notes.ts", "# Title\n"));
    expect(surfaces.editorMarkdown.querySelector("h1")).toBeNull();
    expect(surfaces.editorCode.textContent).toContain("# Title");
  });
});

describe("the front-matter block", () => {
  const doc = [
    "---",
    "inclusion: fileMatch",
    'fileMatchPattern: "vibekit/static-src/**"',
    "description: >",
    "  What this document is",
    "  for, folded.",
    "tools: [read, write]",
    "---",
    "# Heading",
    "",
    "Body.",
  ].join("\n");

  function rows(host: HTMLElement): [string, string][] {
    const keys = [...host.querySelectorAll(".editor-fm-key")].map((e) => e.textContent ?? "");
    const vals = [...host.querySelectorAll(".editor-fm-val")].map((e) => e.textContent ?? "");
    return keys.map((k, i) => [k, vals[i] ?? ""]);
  }

  it("renders the declared keys in the author's order, not as markdown", () => {
    const host = document.createElement("div");
    renderMarkdownDoc(host, doc);
    expect(rows(host)).toEqual([
      ["inclusion", "fileMatch"],
      ["fileMatchPattern", "vibekit/static-src/**"],
      ["description", "What this document is for, folded."],
      ["tools", "read, write"],
    ]);
    // The `---` fence is not a horizontal rule and the keys are not a paragraph,
    // which is why the block is lifted out before the body is parsed.
    expect(host.querySelector(".message.assistant hr")).toBeNull();
  });

  it("keeps the front-matter out of the rendered body", () => {
    const host = document.createElement("div");
    renderMarkdownDoc(host, doc);
    const prose = host.querySelector(".message.assistant");
    expect(prose?.textContent).not.toContain("fileMatchPattern");
    expect(prose?.querySelector("h1")?.textContent).toBe("Heading");
  });

  it("renders no block for a document without front-matter", () => {
    const host = document.createElement("div");
    renderMarkdownDoc(host, "# Requirements\n\nNo header here.\n");
    expect(host.querySelector(".editor-fm")).toBeNull();
    expect(host.querySelector("h1")?.textContent).toBe("Requirements");
  });
});

describe("the edit toggle still shows raw source", () => {
  // The decision: the READ state renders, the edit toggle still shows source.
  // Editing is entered by writing the buffer into the textarea, so the assertion
  // is that the buffer is the file's real bytes, front-matter included — nothing
  // may strip it, or a save would write the stripped version back.
  it("keeps the whole file including its front-matter in the buffer", () => {
    const src = "---\ninclusion: always\n---\n# Title\n";
    const state = loaded("docs/guide.md", src);
    renderReadSurface(state);
    expect(state.current.value).toBe(src);
    expect(state.original.value).toBe(src);
  });
});
