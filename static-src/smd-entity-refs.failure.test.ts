// The chunk that never arrives.
//
// `vi.mock` with a throwing factory is what makes the dynamic `import()` inside
// `entitiesReady()` reject, which no other test in the suite reaches. Its own
// file because a whole-module mock is per-file, and one mock is the smallest
// exposure to the Chromium interception race `vitest.config.ts` anchors around.
//
// Same import discipline as `smd-entity-refs.test.ts`: never `markdown.js`.

import { describe, it, expect, vi } from "vitest";
import { el } from "@cplieger/reactive";
import { parser, parser_end, parser_write } from "./smd-parser.js";
import { domRenderer } from "./smd-renderer.js";
import { entitiesReady, namedEntitiesLoaded } from "./smd-entity-refs.js";

// The factory itself must SUCCEED and the failure has to come out of the module
// it hands back: a factory that throws or returns a rejected promise fails inside
// vitest's own mocker RPC, which reports an unhandled rejection and kills the
// file instead of reaching the `import()` in `entitiesReady()`. A throwing getter
// rejects that promise chain at the same point a 404 does.
vi.mock("./smd-entities.js", () => ({
  get MAX_ENTITY_NAME_LENGTH(): number {
    throw new Error("chunk unavailable");
  },
  get NAMED_ENTITIES(): Readonly<Record<string, string>> {
    throw new Error("chunk unavailable");
  },
}));

function render(md: string): string {
  const host = el("div");
  const p = parser(domRenderer(host));
  parser_write(p, md);
  parser_end(p);
  return host.innerHTML;
}

const A = '<a target="_blank" rel="noopener"';

describe("a lazy entity chunk that fails to load", () => {
  it("resolves rather than rejecting", async () => {
    await expect(entitiesReady()).resolves.toBeUndefined();
  });

  it("reports the table as not installed after the failure", async () => {
    await entitiesReady();
    expect(namedEntitiesLoaded()).toBe(false);
  });

  it("settles the same way on a second call", async () => {
    await entitiesReady();
    await expect(entitiesReady()).resolves.toBeUndefined();
    expect(namedEntitiesLoaded()).toBe(false);
  });

  it("leaves a named reference literal", async () => {
    await entitiesReady();
    expect(render("&copy; 2026")).toBe("<p>&amp;copy; 2026</p>");
  });

  it("still decodes the five XML predefined names", async () => {
    await entitiesReady();
    expect(render("5 &lt; 6 &amp; 7 &gt; 6 &quot;x&quot; it&apos;s")).toBe(
      '<p>5 &lt; 6 &amp; 7 &gt; 6 "x" it\'s</p>',
    );
  });

  it("still decodes numeric references", async () => {
    await entitiesReady();
    expect(render("&#35; and &#x1F600; and &#0;")).toBe("<p># and 😀 and \ufffd</p>");
  });

  it("still refuses a scheme hidden behind a numeric reference", async () => {
    await entitiesReady();
    expect(render("[a](javascript&#58;alert(1))")).toBe(`<p>${A} href="#">a</a></p>`);
    expect(render("[a](&#x6a;avascript:alert(1))")).toBe(`<p>${A} href="#">a</a></p>`);
    expect(render("[a](&#32;&#1;&#32;javascript:alert(1))")).toBe(`<p>${A} href="#">a</a></p>`);
    expect(render("[a](java&#1;script:alert(1))")).toBe(`<p>${A} href="#">a</a></p>`);
  });

  it("renders a document full of references without throwing", async () => {
    await entitiesReady();
    expect(() =>
      render("# &copy; head\n\n&nbsp;&amp;&#35;&nosuch;&CounterClockwiseContourIntegral;\n"),
    ).not.toThrow();
  });
});
