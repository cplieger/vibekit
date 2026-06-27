// @vitest-environment happy-dom
// Tests for linkify.ts — drives the REAL linkifyPaths() against a live DOM.
//
// Earlier revisions of this file re-declared a private copy of FILE_EXTS and
// the PATH_RX regex and asserted the copy against itself, which exercised zero
// production code (and had already drifted from the real extension list). These
// tests instead build DOM subtrees, run linkifyPaths(), and assert on the
// emitted <button class="inline-file-link"> elements — so any change to the
// real pattern, extension list, or DOM walk is caught.

import { describe, it, expect, vi } from "vitest";
import fc from "fast-check";

// openFile pulls in the whole editor subsystem and is only invoked on click;
// stub it so we can both keep the import light and assert the click wiring.
vi.mock("./editor-openers.js", () => ({ openFile: vi.fn() }));

import { linkifyPaths } from "./linkify.js";
import { openFile } from "./editor-openers.js";
import { FILE_EXTS } from "./file-extensions.js";

/** Render text into a fresh detached <div> and linkify it. */
function linkify(text: string): HTMLDivElement {
  const root = document.createElement("div");
  root.textContent = text;
  linkifyPaths(root);
  return root;
}

/** Collect the generated file-link buttons. */
function links(root: HTMLElement): HTMLButtonElement[] {
  return [...root.querySelectorAll<HTMLButtonElement>("button.inline-file-link")];
}

describe("linkifyPaths: explicit cases (table-driven)", () => {
  it("turns a path mention in prose into a single button", () => {
    const root = linkify("see src/foo.ts for details");
    const ls = links(root);
    expect(ls).toHaveLength(1);
    expect(ls[0]!.title).toBe("src/foo.ts");
    expect(ls[0]!.textContent).toContain("foo.ts");
    // Surrounding prose is preserved.
    expect(root.textContent).toContain("see ");
    expect(root.textContent).toContain(" for details");
  });

  it("captures a line number and shows it in the label and title", () => {
    const root = linkify("open src/app.ts:42 now");
    const ls = links(root);
    expect(ls).toHaveLength(1);
    expect(ls[0]!.title).toBe("src/app.ts:42");
    expect(ls[0]!.textContent).toBe("app.ts:42");
  });

  it("consumes a line:col suffix but only the line is surfaced", () => {
    const root = linkify("at src/app.ts:42:7 here");
    const ls = links(root);
    expect(ls).toHaveLength(1);
    expect(ls[0]!.title).toBe("src/app.ts:42");
    expect(ls[0]!.textContent).toBe("app.ts:42");
  });

  it("linkifies multiple distinct paths in one text node", () => {
    const root = linkify("see a/b.ts and c/d.go");
    const ls = links(root);
    expect(ls).toHaveLength(2);
    expect(ls.map((b) => b.title)).toEqual(["a/b.ts", "c/d.go"]);
  });

  it("does not linkify a bare filename with no directory segment", () => {
    expect(links(linkify("just foo.ts here"))).toHaveLength(0);
  });

  it("does not linkify an unknown extension", () => {
    expect(links(linkify("see src/foo.xyz here"))).toHaveLength(0);
  });

  it("does not match a partial extension glued to trailing word chars", () => {
    // 'ts' is valid but 'tsdoc' is not; the negative lookahead must reject the
    // partial match rather than linkifying 'src/foo.ts' inside 'src/foo.tsdoc'.
    expect(links(linkify("src/foo.tsdoc text"))).toHaveLength(0);
  });

  it("does not strip trailing punctuation into the path", () => {
    const root = linkify("(see src/foo.ts).");
    const ls = links(root);
    expect(ls).toHaveLength(1);
    expect(ls[0]!.title).toBe("src/foo.ts");
  });
});

describe("linkifyPaths: skip zones", () => {
  it("leaves paths inside <code> untouched", () => {
    const root = document.createElement("div");
    root.innerHTML = `<code>src/foo.ts</code>`;
    linkifyPaths(root);
    expect(links(root)).toHaveLength(0);
    expect(root.querySelector("code")!.textContent).toBe("src/foo.ts");
  });

  it("leaves paths inside <pre> untouched", () => {
    const root = document.createElement("div");
    root.innerHTML = `<pre>run src/main.go now</pre>`;
    linkifyPaths(root);
    expect(links(root)).toHaveLength(0);
  });

  it("linkifies prose but skips an adjacent <code> sibling", () => {
    const root = document.createElement("div");
    root.innerHTML = `<span>edit src/foo.ts</span><code>src/bar.go</code>`;
    linkifyPaths(root);
    const ls = links(root);
    expect(ls).toHaveLength(1);
    expect(ls[0]!.title).toBe("src/foo.ts");
  });
});

describe("linkifyPaths: click wiring", () => {
  it("clicking a link opens the file at its line", () => {
    const root = linkify("open src/app.ts:42 now");
    links(root)[0]!.click();
    expect(openFile).toHaveBeenCalledWith("src/app.ts", 42);
  });

  it("clicking a path without a line opens with no line argument", () => {
    const root = linkify("open src/app.ts now");
    links(root)[0]!.click();
    expect(openFile).toHaveBeenCalledWith("src/app.ts", undefined);
  });
});

// ---------------------------------------------------------------------------
// Property-based: any path built from real extensions, embedded with safe
// boundary characters, is captured exactly once with the right title. This
// drives the real regex + the real FILE_EXTS list (imported, not copied).
// ---------------------------------------------------------------------------

describe("linkifyPaths property-based", () => {
  const segment = fc
    .array(fc.constantFrom(..."abcdefghijklmnopqrstuvwxyz0123456789".split("")), {
      minLength: 1,
      maxLength: 8,
    })
    .map((cs) => cs.join(""));

  const ext = fc.constantFrom(...FILE_EXTS);

  const validPath = fc
    .tuple(fc.array(segment, { minLength: 1, maxLength: 3 }), segment, ext)
    .map(([dirs, base, e]) => `${dirs.join("/")}/${base}.${e}`);

  // Line/col suffix paired with the line value the production code surfaces.
  const lineSuffix = fc.oneof(
    fc.constant<{ suffix: string; line: number | undefined }>({ suffix: "", line: undefined }),
    fc.nat({ max: 9999 }).map((n) => ({ suffix: `:${String(n)}`, line: n })),
    fc
      .tuple(fc.nat({ max: 9999 }), fc.nat({ max: 200 }))
      .map(([l, c]) => ({ suffix: `:${String(l)}:${String(c)}`, line: l })),
  );

  // Boundary chars that are NOT in [\w/.-], so the lookbehind/lookahead pass.
  const before = fc.constantFrom(" ", "\n", "\t", "(", '"', "'", ",", ";", "[", "{");
  const after = fc.constantFrom(" ", "\n", "\t", ")", '"', "'", ",", ";", "]", "}");

  it("captures exactly one path with the expected title", () => {
    fc.assert(
      fc.property(validPath, lineSuffix, before, after, (path, { suffix, line }, pre, post) => {
        const root = linkify(`${pre}${path}${suffix}${post}`);
        const ls = links(root);
        expect(ls).toHaveLength(1);
        const expectedTitle = line === undefined ? path : `${path}:${String(line)}`;
        expect(ls[0]!.title).toBe(expectedTitle);
      }),
      { numRuns: 500 },
    );
  });

  it("never throws and never linkifies an extension-less segment", () => {
    fc.assert(
      fc.property(fc.array(segment, { minLength: 1, maxLength: 4 }), (segs) => {
        // A slash-joined path with no '.ext' must never produce a link.
        const root = linkify(` ${segs.join("/")} `);
        expect(links(root)).toHaveLength(0);
      }),
      { numRuns: 300 },
    );
  });
});
