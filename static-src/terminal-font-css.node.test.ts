// ---------------------------------------------------------------------------
// THE TERMINAL'S TWO WEB FACES, as the bundle ships them.
//
// The shell's font stack is authored in four places that have to agree and that
// no compiler joins: `css/00-fonts.css` declares the faces, `css/MANIFEST`
// decides whether that file reaches `static/style.css` at all, the Dockerfile
// writes the files the `src` urls name, and `shell.ts`'s SHELL_THEME asks for
// the families by name. Every disagreement between them fails the same way —
// the browser resolves the family against nothing, falls through to the platform
// monospace, and the terminal renders with the row-gap defect this stack exists
// to fix. Nothing errors, nothing logs, and a screenshot on a machine that
// happens to have Monaspace installed looks correct.
//
// So this reads the SHIPPED files rather than a fixture, and derives the
// Dockerfile side rather than restating it: the destinations come out of the
// `# repin:` markers exactly as `scripts/dev-fonts.sh` reads them, so a face
// added, renamed or repinned in one place fails here instead of shipping a
// stylesheet pointing at a 404.
//
// A NODE test rather than a browser one, for two reasons. The Dockerfile is
// outside `vitest.config.ts`'s `server.fs.allow` set (`../static`, `../internal`,
// `../scripts` — deliberately not the repo root), so a `?raw` import of it is
// refused. And the subject is a SOURCE fact: `static/style.css` is gitignored
// build output that need not exist, so the bundle is read the way
// `__test-helpers__/css-rules.ts` reads it — from `css/MANIFEST` in declared
// order, which is what `cmd/bundle` concatenates.
// ---------------------------------------------------------------------------

import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const read = (rel: string): string => readFileSync(join(here, rel), "utf8");

const FONTS_SHEET = "00-fonts.css";
const fontsCSS = read(`css/${FONTS_SHEET}`);
const manifest = read("css/MANIFEST");
const tokensCSS = read("css/01-tokens.css");
const dockerfile = read("../Dockerfile");

/** The two families, glyph overlay first — the order `shell.ts` asks for. */
const GLYPH_FAMILY = "Web Terminal Glyphs";
const TEXT_FAMILY = "Monaspace Neon NF";

/** The four descriptor sets each family declares. */
const PAIRS = [
  ["400", "normal"],
  ["700", "normal"],
  ["400", "italic"],
  ["700", "italic"],
] as const;

interface Face {
  family: string;
  weight: string;
  style: string;
  url: string;
  format: string;
  body: string;
}

/** Every `@font-face` block in the sheet, with the descriptors this suite rules
 *  on. Parsed rather than regex-matched whole, so a block that gains a
 *  descriptor is still one block and a missing one is a distinguishable failure
 *  rather than a silently unmatched rule. */
function faces(css: string): Face[] {
  const text = css.replace(/\/\*[\s\S]*?\*\//g, " ");
  const out: Face[] = [];
  const at = /@font-face\s*\{/g;
  let m: RegExpExecArray | null;
  while ((m = at.exec(text)) !== null) {
    const open = m.index + m[0].length;
    const close = text.indexOf("}", open);
    expect(close, "an @font-face block is not closed").toBeGreaterThan(open);
    const body = text.slice(open, close);
    const decl = (name: string): string =>
      new RegExp(`${name}\\s*:\\s*([^;]+)`).exec(body)?.[1]?.trim() ?? "";
    const src = decl("src");
    out.push({
      family: decl("font-family").replace(/^["']|["']$/g, ""),
      weight: decl("font-weight"),
      style: decl("font-style"),
      url: /url\(\s*["']?([^"')]+)/.exec(src)?.[1] ?? "",
      format: /format\(\s*["']?([^"')]+)/.exec(src)?.[1] ?? "",
      body,
    });
    at.lastIndex = close;
  }
  return out;
}

/**
 * The file names the Dockerfile lands in `static/vendor/fonts/`, read out of its
 * `# repin:` markers the way `scripts/dev-fonts.sh` reads them: `dest=` when the
 * marker overrides the name, else the url's last path segment.
 *
 * The markers rather than the `RUN` block, because the block's own paths are
 * shell-interpolated (`MonaspaceNeonNF-${face}.woff2`) and a test expanding a
 * for-loop's variable would be a second shell implementation. The markers carry
 * one row per asset with no interpolation in the part that decides the name.
 *
 * Scoped to the two font deps — the Dockerfile's other markers pin npm tarballs
 * the frontend build fetches itself, and `dev-fonts.sh` filters them the same
 * way through its own `version_arg_for` table.
 */
function dockerfileFontFiles(): string[] {
  const FONT_DEPS = ["githubnext/monaspace", "cplieger/web-terminal-glyphs"];
  const out: string[] = [];
  for (const line of dockerfile.split("\n")) {
    const marker = /^#\s*repin:\s*(.*)$/.exec(line.trim());
    if (marker === null) {
      continue;
    }
    const tok = (name: string): string | undefined =>
      marker[1]
        ?.split(/\s+/)
        .find((t) => t.startsWith(`${name}=`))
        ?.slice(name.length + 1);
    const dep = tok("dep");
    const url = tok("url");
    if (dep === undefined || url === undefined || !FONT_DEPS.includes(dep)) {
      continue;
    }
    out.push(tok("dest") ?? decodeURIComponent(url.split("/").pop() ?? ""));
  }
  return out;
}

describe("the faces reach the bundle at all", () => {
  it("lists 00-fonts.css in css/MANIFEST exactly once", () => {
    // `cmd/bundle` concatenates the manifest's entries in declared order and
    // nothing else, so a sheet absent from it is absent from style.css — the
    // @font-face rules simply do not exist and the terminal paints on the
    // platform monospace with no error anywhere. Twice would declare eight
    // faces twice, which is harmless and still a mistake worth naming.
    const entries = manifest
      .split("\n")
      .map((l) => l.trim())
      .filter((l) => l !== "" && !l.startsWith("#"));
    expect(entries.filter((e) => e === FONTS_SHEET)).toEqual([FONTS_SHEET]);
  });
});

describe("00-fonts.css declares both families in full", () => {
  const all = faces(fontsCSS);

  it("declares eight faces, four per family", () => {
    expect(all).toHaveLength(8);
    expect(all.filter((f) => f.family === TEXT_FAMILY)).toHaveLength(4);
    expect(all.filter((f) => f.family === GLYPH_FAMILY)).toHaveLength(4);
  });

  it.each([TEXT_FAMILY, GLYPH_FAMILY])("covers every weight/style pair for %s once", (family) => {
    // The glyph font needs all four for a reason its own comment states: the
    // glyphs are geometry, so declaring every descriptor set is what stops the
    // browser synthesising a bold or an oblique variant of a box-drawing corner.
    // `.term`'s `font-synthesis: none` is the other half, and one without the
    // other leaves either a synthesised face or an undeclared one.
    const got = faces(fontsCSS)
      .filter((f) => f.family === family)
      .map((f) => `${f.weight}/${f.style}`)
      .sort();
    expect(got).toEqual(PAIRS.map(([w, s]) => `${w}/${s}`).sort());
  });

  it("carries no metric override and no unicode-range on any face", () => {
    // ascent-override / descent-override were the mechanism this stack REPLACED
    // (web-terminal-ui 5.3.0 closed the row gap with them, and WebKit treats
    // both as preview, so on iOS they did nothing at all while every row
    // boundary showed an unpainted stripe). The cell background is the run
    // elements' `padding-block: 1px` now, so an override here would re-open the
    // question on the one engine that cannot answer it.
    //
    // unicode-range would make the glyph file's coverage a CLAIM this stylesheet
    // has to keep in step with the font's own tables; the file itself is the
    // range, so a letter falls through to Monaspace by ordinary family fallback.
    for (const f of faces(fontsCSS)) {
      const at = `${f.family} ${f.weight}/${f.style}`;
      expect(f.body, at).not.toMatch(/ascent-override|descent-override|line-gap-override/);
      expect(f.body, at).not.toMatch(/size-adjust/);
      expect(f.body, at).not.toMatch(/unicode-range/);
    }
  });

  it("blocks rather than swaps while a face loads", () => {
    // The terminal is a grid whose column and row counts are computed from the
    // face's cell metrics and then reported to the PTY. A swap-in after first
    // paint therefore does not merely reflow text: it changes the geometry the
    // server was sized from, mid-session.
    for (const f of faces(fontsCSS)) {
      expect(f.body, `${f.family} ${f.weight}/${f.style}`).toMatch(/font-display:\s*block/);
    }
  });
});

describe("every src names a file the Dockerfile writes", () => {
  const written = dockerfileFontFiles();

  it("finds the font pins in the Dockerfile", () => {
    // The precondition: with no markers matched, every membership assertion
    // below would be against an empty set and pass for the wrong reason.
    expect(written.length, "no # repin: marker names a font dep").toBeGreaterThan(0);
    expect(written).toContain("WebTerminalGlyphs.woff2");
  });

  it("serves each face from /vendor/fonts/ as woff2", () => {
    // The path prefix is what `internal/server/server_static.go` caches for
    // thirty days (`fontAssetPrefix`), so a face moved out of it would be served
    // revalidate-on-every-load; woff2 is what the Dockerfile fetches and what
    // the cache arm's own test cases name.
    for (const f of faces(fontsCSS)) {
      const at = `${f.family} ${f.weight}/${f.style}`;
      expect(f.url, at).toMatch(/^\/vendor\/fonts\/[^/]+$/);
      expect(f.format, at).toBe("woff2");
    }
  });

  it("points every face at a pinned, digest-verified file", () => {
    // The join the four authoring sites have no compiler for. A face whose url
    // names a file no marker pins is a 404 in production and a silent fallback
    // in the browser; `scripts/dev-fonts.sh` fetches exactly this derived set,
    // so a mismatch is also a local render that disagrees with the image's.
    const wanted = [...new Set(faces(fontsCSS).map((f) => f.url.split("/").pop() ?? ""))].sort();
    expect(wanted.filter((n) => !written.includes(n))).toEqual([]);
  });
});

describe("the terminal stack stays scoped to the terminal", () => {
  it("keeps both terminal families out of the app-wide --font-mono", () => {
    // `createTerminal` sets SHELL_THEME's `--font-mono` on the terminal ROOT, so
    // the app-wide token in 01-tokens.css governs roughly a hundred non-terminal
    // sites and must not gain a family drawn for a 17px terminal row. This is
    // the assertion that fails if the stack is ever "simplified" by moving it
    // into the token layer.
    const decls = [...tokensCSS.matchAll(/--font-mono\s*:\s*([^;]+)/g)].map((m) => m[1] ?? "");
    expect(decls.length, "01-tokens.css declares no --font-mono").toBeGreaterThan(0);
    for (const d of decls) {
      expect(d).not.toContain(GLYPH_FAMILY);
      expect(d).not.toContain(TEXT_FAMILY);
    }
  });
});
