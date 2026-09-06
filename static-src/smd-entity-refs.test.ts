// The character-reference half that ships in the initial bundle, exercised with
// the lazy WHATWG table ABSENT.
//
// The premise is the import list, so read it before changing it: this file
// reaches `smd-parser.js` and `smd-renderer.js` directly and must NEVER import
// `markdown.js`, which fires `entitiesReady()` at module scope. Browser Mode
// isolates per test file, so that omission is what gives the unloaded state — and
// it also proves the parser itself never triggers the fetch. `entitiesReady()` is
// called in exactly one place, the last test.

import { describe, it, expect } from "vitest";
import { el } from "@cplieger/reactive";
import { parser, parser_end, parser_write } from "./smd-parser.js";
import { domRenderer } from "./smd-renderer.js";
import { MAX_ENTITY_NAME_LENGTH, entitiesReady, namedEntitiesLoaded } from "./smd-entity-refs.js";

/** `renderMarkdown` from markdown.ts, inlined because importing that module
 *  would install the table this file exists to do without. */
function render(md: string): string {
  const host = el("div");
  const p = parser(domRenderer(host));
  parser_write(p, md);
  parser_end(p);
  return host.innerHTML;
}

const A = '<a target="_blank" rel="noopener"';

describe("character references with the lazy table absent", () => {
  it("starts with the full table not installed", () => {
    expect(namedEntitiesLoaded()).toBe(false);
  });

  // -------------------------------------------------------------------------
  // The five XML predefined names. Inline, so `&amp;` and `&lt;` never wait on a
  // fetch — they are the spellings escaping turns on.
  // -------------------------------------------------------------------------

  it("decodes &amp; with no chunk loaded", () => {
    expect(render("a &amp; b")).toBe("<p>a &amp; b</p>");
  });

  it("decodes &lt; with no chunk loaded", () => {
    expect(render("5 &lt; 6")).toBe("<p>5 &lt; 6</p>");
  });

  it("decodes &gt; with no chunk loaded", () => {
    expect(render("7 &gt; 6")).toBe("<p>7 &gt; 6</p>");
  });

  it("decodes &quot; with no chunk loaded", () => {
    expect(render("say &quot;hi&quot;")).toBe('<p>say "hi"</p>');
  });

  it("decodes &apos; with no chunk loaded", () => {
    expect(render("it&apos;s")).toBe("<p>it's</p>");
  });

  // -------------------------------------------------------------------------
  // Every other name is unresolved, which is this parser's own
  // invalid-reference rule: the run renders as the text that was typed.
  // -------------------------------------------------------------------------

  it("leaves &copy; literal with no chunk loaded", () => {
    expect(render("&copy; 2026")).toBe("<p>&amp;copy; 2026</p>");
  });

  it("leaves &nbsp; literal with no chunk loaded", () => {
    expect(render("a&nbsp;b")).toBe("<p>a&amp;nbsp;b</p>");
  });

  it("leaves the longest name in the table literal with no chunk loaded", () => {
    expect(render("&CounterClockwiseContourIntegral;")).toBe(
      "<p>&amp;CounterClockwiseContourIntegral;</p>",
    );
  });

  it("leaves a name that is in no table literal either", () => {
    expect(render("&nosuchname;")).toBe("<p>&amp;nosuchname;</p>");
  });

  it("leaves a name only Object.prototype carries literal", () => {
    // A bare index into either table answers with an INHERITED member, so
    // `&constructor;` would render this parser's own `Object` constructor source
    // as text. Every name here is alphanumeric, so both `decode_entities`' regex
    // and `can_continue_entity` accept it as a candidate.
    expect(render("&constructor;")).toBe("<p>&amp;constructor;</p>");
    expect(render("&toString;")).toBe("<p>&amp;toString;</p>");
    expect(render("&valueOf;")).toBe("<p>&amp;valueOf;</p>");
    expect(render("&hasOwnProperty;")).toBe("<p>&amp;hasOwnProperty;</p>");
    expect(render("&isPrototypeOf;")).toBe("<p>&amp;isPrototypeOf;</p>");
    expect(render("&propertyIsEnumerable;")).toBe("<p>&amp;propertyIsEnumerable;</p>");
    expect(render("&toLocaleString;")).toBe("<p>&amp;toLocaleString;</p>");
  });

  it("leaves an inherited member literal in a link destination too", () => {
    // The whole-string path has its own reader, `decode_entities`, and writes its
    // answer into an attribute rather than a text node.
    expect(render("[a](&constructor;)")).toBe(`<p>${A} href="&amp;constructor;">a</a></p>`);
  });

  // -------------------------------------------------------------------------
  // Numeric decoding is complete without the chunk: it is arithmetic plus
  // CommonMark 6.2's scalar rules, and consults no table.
  // -------------------------------------------------------------------------

  it("decodes a decimal reference with no chunk loaded", () => {
    expect(render("&#35; hash")).toBe("<p># hash</p>");
  });

  it("decodes a hex reference with no chunk loaded", () => {
    expect(render("&#x1F600; emoji")).toBe("<p>😀 emoji</p>");
  });

  it("decodes a hex reference spelled with an uppercase X", () => {
    expect(render("&#X23; hash")).toBe("<p># hash</p>");
  });

  it("decodes a numeric reference to a character that is itself markup", () => {
    // Straight to the text buffer, never re-parsed: `&#42;` is a literal
    // asterisk, not an emphasis delimiter.
    expect(render("a&#42;b&#42;c")).toBe("<p>a*b*c</p>");
  });

  it("maps &#0; to U+FFFD", () => {
    expect(render("&#0;")).toBe("<p>\ufffd</p>");
  });

  it("maps a surrogate code point to U+FFFD", () => {
    expect(render("&#xD800;")).toBe("<p>\ufffd</p>");
  });

  it("maps a code point past U+10FFFF to U+FFFD", () => {
    expect(render("&#x110000;")).toBe("<p>\ufffd</p>");
  });

  it("leaves an empty numeric body literal", () => {
    expect(render("&#;")).toBe("<p>&amp;#;</p>");
  });

  it("leaves an empty hex body literal", () => {
    expect(render("&#x;")).toBe("<p>&amp;#x;</p>");
  });

  // -------------------------------------------------------------------------
  // The URL gate, with the table absent. These four spellings are why numeric
  // decoding is inline: the destination is decoded BEFORE `isSafeUrl` reads it,
  // so a missing chunk must not be able to smuggle a scheme past it.
  // -------------------------------------------------------------------------

  it("blocks an encoded colon in a javascript scheme with no chunk loaded", () => {
    expect(render("[a](javascript&#58;alert(1))")).toBe(`<p>${A} href="#">a</a></p>`);
  });

  it("blocks a hex-encoded first letter of a scheme with no chunk loaded", () => {
    expect(render("[a](&#x6a;avascript:alert(1))")).toBe(`<p>${A} href="#">a</a></p>`);
  });

  it("blocks a scheme behind encoded leading spaces and a control", () => {
    expect(render("[a](&#32;&#1;&#32;javascript:alert(1))")).toBe(`<p>${A} href="#">a</a></p>`);
  });

  it("blocks a scheme split by an encoded control character", () => {
    expect(render("[a](java&#1;script:alert(1))")).toBe(`<p>${A} href="#">a</a></p>`);
  });

  // -------------------------------------------------------------------------
  // The two duplicated bounds. `MAX_ENTITY_NAME_LENGTH` is spelled inline here,
  // derived in the generated table, and hard-coded as the `{1,30}` quantifier in
  // `decode_entities`' regex; all three drift silently on a regenerate.
  // -------------------------------------------------------------------------

  it("accepts a name at the maximum length and refuses one past it", () => {
    const at = `a${"b".repeat(MAX_ENTITY_NAME_LENGTH - 1)}`;
    const past = `a${"b".repeat(MAX_ENTITY_NAME_LENGTH)}`;
    expect(at).toHaveLength(31);
    expect(past).toHaveLength(32);
    // Neither names anything, so both render literally — what differs is where
    // the hold ENDS, which the destination path shows: the regex bound accepts
    // the 31-character body as a candidate and refuses the 32-character one.
    expect(render(`&${at};`)).toBe(`<p>&amp;${at};</p>`);
    expect(render(`&${past};`)).toBe(`<p>&amp;${past};</p>`);
  });

  // -------------------------------------------------------------------------
  // The state transition, last so it cannot leak into anything above it.
  // -------------------------------------------------------------------------

  it("decodes every name once the chunk lands", async () => {
    await entitiesReady();
    expect(namedEntitiesLoaded()).toBe(true);
    expect(render("&copy; 2026")).toBe("<p>© 2026</p>");
    expect(render("&CounterClockwiseContourIntegral;")).toBe("<p>∳</p>");
    // The installed table is a second object literal, so it inherits the same
    // members the inline map does.
    expect(render("&constructor;")).toBe("<p>&amp;constructor;</p>");
    const table = await import("./smd-entities.js");
    expect(MAX_ENTITY_NAME_LENGTH).toBe(table.MAX_ENTITY_NAME_LENGTH);
  });
});
