// Unit tests for render.ts — property-based XSS invariants + table-driven edge cases.
// @vitest-environment happy-dom
// Unit tests for markdown.ts — property-based XSS invariants + table-driven
// edge cases. markdown.ts renders via the smd-parser streaming state machine
// into real DOM nodes, so happy-dom is required.

import { describe, it, expect } from "vitest";
import * as fc from "fast-check";
import { renderMarkdown, MarkdownRenderer } from "./markdown.js";

// ---------------------------------------------------------------------------
// Property-based tests: XSS invariants (testarch-b15-p1)
// ---------------------------------------------------------------------------

describe("renderMarkdown XSS invariants (property-based)", () => {
  it("never produces <script in output", () => {
    fc.assert(
      fc.property(fc.string({ minLength: 0, maxLength: 500 }), (input) => {
        const html = renderMarkdown(input);
        expect(html.toLowerCase()).not.toContain("<script");
      }),
      { numRuns: 500 },
    );
  });

  it("never produces javascript: URIs in href/src attributes", () => {
    // Generate markdown with links that attempt javascript: injection.
    // Use alphanumeric text to avoid breaking markdown link syntax.
    const safeText = fc.stringMatching(/^[a-zA-Z0-9 ]{1,20}$/);
    const jsUrl = fc.constantFrom(
      "javascript:alert(1)",
      "JAVASCRIPT:alert(1)",
      "javascript:void(0)",
      "JavaScript:alert(document.cookie)",
    );
    const mdWithLink = fc.tuple(safeText, jsUrl)
      .map(([text, url]) => `[${text}](${url})`);

    fc.assert(
      fc.property(mdWithLink, (input) => {
        const html = renderMarkdown(input);
        const attrMatches = html.match(/(?:href|src)="([^"]*)"/g) ?? [];
        for (const attr of attrMatches) {
          const val = attr.replace(/^(?:href|src)="/, "").replace(/"$/, "");
          // The renderer replaces blocked schemes with "#"
          expect(val).not.toMatch(/^javascript:/i);
        }
        expect(attrMatches.length).toBeGreaterThan(0);
      }),
      { numRuns: 200 },
    );
  });

  it("never produces on* event handler attributes", () => {
    fc.assert(
      fc.property(fc.string({ minLength: 0, maxLength: 500 }), (input) => {
        const html = renderMarkdown(input);
        // Strip code blocks before checking
        const withoutCode = html
          .replace(/<pre[^>]*>[\s\S]*?<\/pre>/g, "")
          .replace(/<code>[\s\S]*?<\/code>/g, "");
        // No on* attributes in tags (onerror, onload, onclick, etc.)
        expect(withoutCode).not.toMatch(/<[^>]+\son\w+\s*=/i);
      }),
      { numRuns: 500 },
    );
  });

  it("never produces data: URIs in href/src attributes", () => {
    // Generate markdown with images/links that attempt data: injection
    const mdWithDataUri = fc.tuple(
      fc.string({ minLength: 1, maxLength: 50 }),
      fc.constantFrom(
        "data:text/html,<script>alert(1)</script>",
        "data:image/svg+xml,<svg onload=alert(1)>",
        "DATA:text/html,test",
      ),
    ).map(([text, url]) => `![${text}](${url})`);

    fc.assert(
      fc.property(mdWithDataUri, (input) => {
        const html = renderMarkdown(input);
        const attrMatches = html.match(/(?:href|src)="([^"]*)"/g) ?? [];
        for (const attr of attrMatches) {
          const val = attr.replace(/^(?:href|src)="/, "").replace(/"$/, "");
          expect(val.toLowerCase().replace(/\s/g, "")).not.toMatch(/^data:/);
        }
        // We do not assert the image parsed successfully — fast-check can
        // generate random bytes that break markdown syntax (stray ], etc.).
        // The XSS invariant is: *if* an href/src is emitted, it must not be data:.
      }),
      { numRuns: 200 },
    );
  });

  it("never produces vbscript: URIs in href/src attributes", () => {
    const safeText = fc.stringMatching(/^[a-zA-Z0-9 ]{1,20}$/);
    const vbUrl = fc.constantFrom(
      "vbscript:msgbox",
      "VBSCRIPT:MsgBox",
      "vbscript:Execute",
    );
    const mdWithVbscript = fc.tuple(safeText, vbUrl)
      .map(([text, url]) => `[${text}](${url})`);

    fc.assert(
      fc.property(mdWithVbscript, (input) => {
        const html = renderMarkdown(input);
        const attrMatches = html.match(/(?:href|src)="([^"]*)"/g) ?? [];
        for (const attr of attrMatches) {
          const val = attr.replace(/^(?:href|src)="/, "").replace(/"$/, "");
          // The renderer replaces blocked schemes with "#"
          expect(val).not.toMatch(/^vbscript:/i);
        }
        expect(attrMatches.length).toBeGreaterThan(0);
      }),
      { numRuns: 200 },
    );
  });

  it("XSS payloads in link text are escaped", () => {
    fc.assert(
      fc.property(
        fc.string({ minLength: 1, maxLength: 200 }),
        fc.webUrl(),
        (text, url) => {
          const md = `[${text}](${url})`;
          const html = renderMarkdown(md);
          expect(html.toLowerCase()).not.toContain("<script");
        },
      ),
      { numRuns: 200 },
    );
  });
});

// ---------------------------------------------------------------------------
// Table-driven edge cases (testarch-b15-p2)
// ---------------------------------------------------------------------------

describe("renderMarkdown edge cases (table-driven)", () => {
  const cases: Array<{ name: string; input: string; expected: string | RegExp }> = [
    // --- Basic inline formatting ---
    { name: "bold with **", input: "**bold**", expected: "<p><strong>bold</strong></p>" },
    { name: "bold with __", input: "__bold__", expected: "<p><strong>bold</strong></p>" },
    { name: "italic with *", input: "*italic*", expected: "<p><em>italic</em></p>" },
    { name: "italic with _", input: "_italic_", expected: "<p><em>italic</em></p>" },
    { name: "strikethrough", input: "~~strike~~", expected: "<p><s>strike</s></p>" },
    { name: "inline code", input: "`code`", expected: "<p><code>code</code></p>" },
    { name: "inline code with special chars", input: "`<script>alert(1)</script>`", expected: /&lt;script&gt;/ },
    { name: "nested bold in italic", input: "*hello **world***", expected: /<em>.*<strong>world<\/strong>.*<\/em>/ },

    // --- Headings (ATX only — smd-parser does not implement setext headings,
    //     which are rare in kiro-cli output) ---
    { name: "h1 ATX", input: "# Heading", expected: /^<h1>.*Heading.*<\/h1>$/ },
    { name: "h2 ATX", input: "## Heading", expected: /^<h2>.*Heading.*<\/h2>$/ },
    { name: "h3 ATX", input: "### Heading", expected: /^<h3>.*Heading.*<\/h3>$/ },
    { name: "h4 ATX", input: "#### Heading", expected: /^<h4>.*Heading.*<\/h4>$/ },
    { name: "h5 ATX", input: "##### Heading", expected: /^<h5>.*Heading.*<\/h5>$/ },
    { name: "h6 ATX", input: "###### Heading", expected: /^<h6>.*Heading.*<\/h6>$/ },

    // --- Links ---
    { name: "basic link", input: "[text](https://example.com)", expected: /href="https:\/\/example\.com"/ },
    { name: "link with target blank", input: "[x](https://a.com)", expected: /target="_blank"/ },
    { name: "link with rel noopener", input: "[x](https://a.com)", expected: /rel="noopener"/ },
    { name: "javascript: link blocked", input: "[click](javascript:alert(1))", expected: /href="#"/ },
    { name: "JAVASCRIPT: link blocked (case)", input: "[click](JAVASCRIPT:alert(1))", expected: /href="#"/ },
    { name: "data: link blocked", input: "[click](data:text/html,<script>)", expected: /href="#"/ },
    { name: "vbscript: link blocked", input: "[click](vbscript:msgbox)", expected: /href="#"/ },
    { name: "file: link blocked", input: "[click](file:///etc/passwd)", expected: /href="#"/ },

    // --- Images ---
    { name: "basic image", input: "![alt](https://img.png)", expected: /src="https:\/\/img\.png"/ },
    { name: "image alt text", input: "![my alt](https://img.png)", expected: /alt="my alt"/ },
    { name: "javascript: image blocked", input: "![x](javascript:alert(1))", expected: /src="#"/ },
    { name: "data: image blocked", input: "![x](data:image/svg+xml,<svg>)", expected: /src="#"/ },

    // --- Code blocks ---
    // (pre gets class="code"; code gets class="language-X" when a lang is set;
    //  plain fenced blocks omit the code-class attribute.)
    { name: "fenced code block", input: "```\ncode\n```", expected: /<pre class="code"><code>code<\/code><\/pre>/ },
    { name: "fenced code with lang", input: "```js\nvar x;\n```", expected: /class="language-js"/ },
    { name: "code block escapes HTML", input: "```\n<script>alert(1)</script>\n```", expected: /&lt;script&gt;alert\(1\)&lt;\/script&gt;/ },
    { name: "nested fences", input: "````\n```\ninner\n```\n````", expected: /<pre.*<code>```\ninner\n```<\/code><\/pre>/ },

    // --- Lists ---
    { name: "unordered list", input: "- item1\n- item2", expected: /<ul>.*<li>.*item1.*<\/li>.*<li>.*item2.*<\/li>.*<\/ul>/s },
    { name: "ordered list", input: "1. first\n2. second", expected: /<ol>.*<li>.*first.*<\/li>.*<li>.*second.*<\/li>.*<\/ol>/s },
    { name: "bullet with +", input: "+ item", expected: /<ul>.*<li>.*item.*<\/li>.*<\/ul>/s },
    { name: "bullet with *", input: "* item", expected: /<ul>.*<li>.*item.*<\/li>.*<\/ul>/s },

    // --- Blockquotes ---
    { name: "blockquote", input: "> quoted", expected: /<blockquote>.*quoted.*<\/blockquote>/s },
    { name: "nested blockquote", input: "> > nested", expected: /<blockquote>.*<blockquote>.*nested.*<\/blockquote>.*<\/blockquote>/s },

    // --- Horizontal rules (HTML5 void form: <hr>) ---
    { name: "hr with ---", input: "text\n\n---\n\nmore", expected: /<hr>/ },
    { name: "hr with * * *", input: "a\n\n* * *\n\nb", expected: /<hr>/ },

    // --- Tables (GFM) — smd-parser preserves cell padding whitespace ---
    { name: "basic table", input: "\n| A | B |\n|---|---|\n| 1 | 2 |\n", expected: /<table>.*<th[^>]*>\s*A\s*<\/th>.*<td[^>]*>\s*1\s*<\/td>/s },
    { name: "table escapes cell content", input: "\n| <b> |\n|---|\n| <i> |\n", expected: /&lt;b&gt;/ },

    // --- Task lists (smd-parser emits HTML5 boolean attrs as attr="") ---
    { name: "checked task", input: "- [x] done", expected: /<input type="checkbox" disabled="" aria-label="Task item" checked=""/ },
    { name: "unchecked task", input: "- [ ] todo", expected: /<input type="checkbox" disabled="" aria-label="Task item"/ },
    { name: "task list XSS in content", input: "- [x] <img onerror=alert(1)>", expected: /&lt;img onerror=alert\(1\)&gt;/ },

    // --- Paragraphs (no inter-paragraph newline in smd-parser output) ---
    { name: "single paragraph", input: "hello", expected: "<p>hello</p>" },
    { name: "two paragraphs", input: "one\n\ntwo", expected: /<p>one<\/p>\s*<p>two<\/p>/ },
    // Line breaks: HTML5 void form <br>.
    { name: "line break with two spaces", input: "a  \nb", expected: /a ?<br>/ },

    // --- Edge cases ---
    { name: "empty string", input: "", expected: "" },
    // Whitespace-only input emits nothing (CommonMark-correct; snarkdown
    // used to emit <p>   </p>, smd-parser treats it as empty document).
    { name: "only whitespace", input: "   ", expected: "" },
    // HTML in plain text is ESCAPED, not passed through. This is the
    // correct XSS-safe behaviour; the old snarkdown port passed it through
    // literally, which was a latent risk.
    { name: "HTML in text is escaped", input: "<div>test</div>", expected: /&lt;div&gt;test&lt;\/div&gt;/ },
    { name: "ampersand in text is escaped", input: "a & b", expected: /a &amp; b/ },
    // Unclosed fences auto-close at EOF (CommonMark-correct).
    { name: "unclosed fence auto-closes", input: "```\nno close", expected: /<pre class="code"><code>no close<\/code><\/pre>/ },
    { name: "consecutive lists merge", input: "1. a\n\n2. b", expected: /<ol>.*<li>.*a.*<\/li>.*<li>.*b.*<\/li>.*<\/ol>/s },
  ];

  it.each(cases)("$name", ({ input, expected }) => {
    const html = renderMarkdown(input);
    if (typeof expected === "string") {
      expect(html).toBe(expected);
    } else {
      expect(html).toMatch(expected);
    }
  });
});

// ---------------------------------------------------------------------------
// Property-based streaming invariant: MarkdownRenderer append-only contract
// (tarch-b14-c4-p2)
// ---------------------------------------------------------------------------

describe("MarkdownRenderer streaming/one-shot equivalence", () => {
  it("streaming write at random split points produces same output as one-shot", () => {
    fc.assert(
      fc.property(
        fc.string({ minLength: 1, maxLength: 300 }),
        fc.array(fc.double({ min: 0, max: 1, noNaN: true }), { minLength: 1, maxLength: 8 }),
        (markdown, splitPoints) => {
          // Generate cumulative prefixes by splitting at random fractions.
          const sorted = [...splitPoints].sort((a, b) => a - b);
          const indices = sorted.map((f) => Math.floor(f * markdown.length));
          // Always include the full string as the last prefix.
          const prefixLengths = [...new Set([...indices, markdown.length])].sort((a, b) => a - b);

          // Streaming path: feed cumulative prefixes.
          const streamEl = document.createElement("div");
          const renderer = new MarkdownRenderer(streamEl);
          for (const len of prefixLengths) {
            renderer.write(markdown.slice(0, len));
          }
          renderer.finalize();

          // One-shot path.
          const oneShotHtml = renderMarkdown(markdown);

          // Compare text content (ignore fade-in span wrappers that streaming adds).
          expect(streamEl.textContent).toBe(
            (() => {
              const tmp = document.createElement("div");
              tmp.innerHTML = oneShotHtml;
              return tmp.textContent;
            })(),
          );
        },
      ),
      { numRuns: 200 },
    );
  });

  it("write with non-append-only input throws", () => {
    const el = document.createElement("div");
    const renderer = new MarkdownRenderer(el);
    renderer.write("hello world");
    expect(() => renderer.write("different")).toThrow("not append-only");
  });
});
