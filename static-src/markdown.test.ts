// Unit tests for render.ts — property-based XSS invariants + table-driven edge cases.
// Unit tests for markdown.ts — property-based XSS invariants + table-driven
// edge cases. markdown.ts renders via the smd-parser streaming state machine
// into real DOM nodes, so a document is required.

import { describe, it, expect, vi, afterEach } from "vitest";
import * as fc from "fast-check";
import { renderMarkdown, createMarkdownStream } from "./markdown.js";

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
    const mdWithLink = fc.tuple(safeText, jsUrl).map(([text, url]) => `[${text}](${url})`);

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
    const mdWithDataUri = fc
      .tuple(
        fc.string({ minLength: 1, maxLength: 50 }),
        fc.constantFrom(
          "data:text/html,<script>alert(1)</script>",
          "data:image/svg+xml,<svg onload=alert(1)>",
          "DATA:text/html,test",
        ),
      )
      .map(([text, url]) => `![${text}](${url})`);

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
    const vbUrl = fc.constantFrom("vbscript:msgbox", "VBSCRIPT:MsgBox", "vbscript:Execute");
    const mdWithVbscript = fc.tuple(safeText, vbUrl).map(([text, url]) => `[${text}](${url})`);

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
      fc.property(fc.string({ minLength: 1, maxLength: 200 }), fc.webUrl(), (text, url) => {
        const md = `[${text}](${url})`;
        const html = renderMarkdown(md);
        expect(html.toLowerCase()).not.toContain("<script");
      }),
      { numRuns: 100 },
    );
  });
});

// ---------------------------------------------------------------------------
// Table-driven edge cases (testarch-b15-p2)
// ---------------------------------------------------------------------------

describe("renderMarkdown edge cases (table-driven)", () => {
  const cases: { name: string; input: string; expected: string | RegExp }[] = [
    // --- Basic inline formatting ---
    { name: "bold with **", input: "**bold**", expected: "<p><strong>bold</strong></p>" },
    { name: "bold with __", input: "__bold__", expected: "<p><strong>bold</strong></p>" },
    { name: "italic with *", input: "*italic*", expected: "<p><em>italic</em></p>" },
    { name: "italic with _", input: "_italic_", expected: "<p><em>italic</em></p>" },
    { name: "strikethrough", input: "~~strike~~", expected: "<p><s>strike</s></p>" },
    { name: "inline code", input: "`code`", expected: "<p><code>code</code></p>" },
    {
      name: "inline code with special chars",
      input: "`<script>alert(1)</script>`",
      expected: /&lt;script&gt;/,
    },
    {
      name: "nested bold in italic",
      input: "*hello **world***",
      expected: /<em>.*<strong>world<\/strong>.*<\/em>/,
    },

    // --- Headings (ATX only — smd-parser does not implement setext headings,
    //     which are rare in kiro-cli output) ---
    { name: "h1 ATX", input: "# Heading", expected: /^<h1>.*Heading.*<\/h1>$/ },
    { name: "h2 ATX", input: "## Heading", expected: /^<h2>.*Heading.*<\/h2>$/ },
    { name: "h3 ATX", input: "### Heading", expected: /^<h3>.*Heading.*<\/h3>$/ },
    { name: "h4 ATX", input: "#### Heading", expected: /^<h4>.*Heading.*<\/h4>$/ },
    { name: "h5 ATX", input: "##### Heading", expected: /^<h5>.*Heading.*<\/h5>$/ },
    { name: "h6 ATX", input: "###### Heading", expected: /^<h6>.*Heading.*<\/h6>$/ },

    // --- Links ---
    {
      name: "basic link",
      input: "[text](https://example.com)",
      expected: /href="https:\/\/example\.com"/,
    },
    { name: "link with target blank", input: "[x](https://a.com)", expected: /target="_blank"/ },
    { name: "link with rel noopener", input: "[x](https://a.com)", expected: /rel="noopener"/ },
    {
      name: "javascript: link blocked",
      input: "[click](javascript:alert(1))",
      expected: /href="#"/,
    },
    {
      name: "JAVASCRIPT: link blocked (case)",
      input: "[click](JAVASCRIPT:alert(1))",
      expected: /href="#"/,
    },
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
    {
      name: "fenced code block",
      input: "```\ncode\n```",
      expected: /<pre class="code"><code>code<\/code><\/pre>/,
    },
    { name: "fenced code with lang", input: "```js\nvar x;\n```", expected: /class="language-js"/ },
    {
      name: "code block escapes HTML",
      input: "```\n<script>alert(1)</script>\n```",
      expected: /&lt;script&gt;alert\(1\)&lt;\/script&gt;/,
    },
    {
      name: "nested fences",
      input: "````\n```\ninner\n```\n````",
      expected: /<pre.*<code>```\ninner\n```<\/code><\/pre>/,
    },

    // --- Lists ---
    {
      name: "unordered list",
      input: "- item1\n- item2",
      expected: /<ul>.*<li>.*item1.*<\/li>.*<li>.*item2.*<\/li>.*<\/ul>/s,
    },
    {
      name: "ordered list",
      input: "1. first\n2. second",
      expected: /<ol>.*<li>.*first.*<\/li>.*<li>.*second.*<\/li>.*<\/ol>/s,
    },
    { name: "bullet with +", input: "+ item", expected: /<ul>.*<li>.*item.*<\/li>.*<\/ul>/s },
    { name: "bullet with *", input: "* item", expected: /<ul>.*<li>.*item.*<\/li>.*<\/ul>/s },

    // --- Blockquotes ---
    { name: "blockquote", input: "> quoted", expected: /<blockquote>.*quoted.*<\/blockquote>/s },
    {
      name: "nested blockquote",
      input: "> > nested",
      expected: /<blockquote>.*<blockquote>.*nested.*<\/blockquote>.*<\/blockquote>/s,
    },

    // --- Horizontal rules (HTML5 void form: <hr>) ---
    { name: "hr with ---", input: "text\n\n---\n\nmore", expected: /<hr>/ },
    { name: "hr with * * *", input: "a\n\n* * *\n\nb", expected: /<hr>/ },

    // --- Tables (GFM) — smd-parser preserves cell padding whitespace ---
    {
      name: "basic table",
      input: "\n| A | B |\n|---|---|\n| 1 | 2 |\n",
      expected: /<table>.*<th[^>]*>\s*A\s*<\/th>.*<td[^>]*>\s*1\s*<\/td>/s,
    },
    {
      name: "table escapes cell content",
      input: "\n| <b> |\n|---|\n| <i> |\n",
      expected: /&lt;b&gt;/,
    },

    // --- Task lists (smd-parser emits HTML5 boolean attrs as attr="") ---
    {
      name: "checked task",
      input: "- [x] done",
      expected: /<input type="checkbox" disabled="" aria-label="Task item" checked=""/,
    },
    {
      name: "unchecked task",
      input: "- [ ] todo",
      expected: /<input type="checkbox" disabled="" aria-label="Task item"/,
    },
    {
      name: "task list XSS in content",
      input: "- [x] <img onerror=alert(1)>",
      expected: /&lt;img onerror=alert\(1\)&gt;/,
    },

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
    {
      name: "HTML in text is escaped",
      input: "<div>test</div>",
      expected: /&lt;div&gt;test&lt;\/div&gt;/,
    },
    { name: "ampersand in text is escaped", input: "a & b", expected: /a &amp; b/ },
    // Unclosed fences auto-close at EOF (CommonMark-correct).
    {
      name: "unclosed fence auto-closes",
      input: "```\nno close",
      expected: /<pre class="code"><code>no close<\/code><\/pre>/,
    },
    {
      name: "consecutive lists merge",
      input: "1. a\n\n2. b",
      expected: /<ol>.*<li>.*a.*<\/li>.*<li>.*b.*<\/li>.*<\/ol>/s,
    },
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
// Intraword underscores (CommonMark 6.2 delimiter runs)
//
// A `_` run preceded by a word character is both left- and right-flanking, so
// it can neither open nor close emphasis: `snake_case` is literal text. `*` has
// no such exclusion, which is why `foo*bar*baz` still emphasises.
// ---------------------------------------------------------------------------

describe("renderMarkdown intraword underscores (CommonMark 6.2)", () => {
  const cases: { name: string; input: string; expected: string | RegExp }[] = [
    // --- The reported defect ---
    {
      name: "the reported string renders literally",
      input: "run_progress shape, tool_call_update as a delta",
      expected: "<p>run_progress shape, tool_call_update as a delta</p>",
    },
    { name: "snake_case renders literally", input: "snake_case", expected: "<p>snake_case</p>" },
    { name: "a_b_c renders literally", input: "a_b_c", expected: "<p>a_b_c</p>" },
    { name: "a__b renders literally", input: "a__b", expected: "<p>a__b</p>" },
    {
      name: "MAX_RETRIES and MIN_WAIT render literally",
      input: "MAX_RETRIES and MIN_WAIT",
      expected: "<p>MAX_RETRIES and MIN_WAIT</p>",
    },

    // --- What counts as a word character ---
    {
      name: "digits are word characters",
      input: "5_000_000 rows",
      expected: "<p>5_000_000 rows</p>",
    },
    {
      name: "non-ASCII letters are word characters",
      input: "über_wert und café_bar",
      expected: "<p>über_wert und café_bar</p>",
    },
    {
      name: "CJK characters are word characters",
      input: "日本_語 test",
      expected: "<p>日本_語 test</p>",
    },
    {
      // A symbol is punctuation for flanking purposes (`\p{S}`), so the run is
      // left-flanking only and opens. The BMP case is the control for the two
      // astral cases below: same rule, one code unit instead of two.
      name: "a symbol before the underscore still opens",
      input: "\u2705_yay_",
      expected: "<p>\u2705<em>yay</em></p>",
    },
    {
      // The lookbehind must read the last CODE POINT. A lone low surrogate is
      // category Cs, which matches neither `\p{P}` nor `\p{S}` nor `\s`, so a
      // single-unit read calls this emoji a word character and blocks the open.
      name: "an astral symbol before the underscore still opens",
      input: "\u{1F389}_yay_",
      expected: "<p>\u{1F389}<em>yay</em></p>",
    },
    {
      // The other half of the same read: an astral LETTER is a word character,
      // so it must still block. Both cases pass under a single-unit read only by
      // coincidence, which is why the pair is needed rather than either alone.
      name: "an astral letter before the underscore blocks the open",
      input: "\u{1D400}_yay_",
      expected: "<p>\u{1D400}_yay_</p>",
    },

    // --- Emphasis at a word boundary is untouched ---
    {
      name: "_emphasis_ at line start still emphasises",
      input: "_emphasis_",
      expected: "<p><em>emphasis</em></p>",
    },
    {
      name: "_em_ after a space still emphasises",
      input: "x _y_ z",
      expected: "<p>x <em>y</em> z</p>",
    },
    {
      name: "_em_ spanning words still emphasises",
      input: "a _b c_ d",
      expected: "<p>a <em>b c</em> d</p>",
    },
    {
      name: "__strong__ still emphasises",
      input: "__strong__",
      expected: "<p><strong>strong</strong></p>",
    },
    {
      name: "___tri___ still nests strong in em",
      input: "___tri___",
      expected: "<p><strong><em>tri</em></strong></p>",
    },

    // --- The asymmetry: `*` carries no both-flanking exclusion ---
    {
      name: "* is still allowed intraword",
      input: "foo*bar*baz",
      expected: "<p>foo<em>bar</em>baz</p>",
    },
    {
      name: "*intraword* still emphasises",
      input: "*intraword*",
      expected: "<p><em>intraword</em></p>",
    },

    // --- Contexts where `_` was already inert ---
    {
      name: "_ inside an inline code span stays literal",
      input: "`snake_case`",
      expected: "<p><code>snake_case</code></p>",
    },
    {
      name: "_ inside a fenced block stays literal",
      input: "```\nsnake_case\n```",
      expected: '<pre class="code"><code>snake_case</code></pre>',
    },
    {
      // A PRE-EXISTING divergence from CommonMark, caused by handleCommon's
      // STRONG_AST guard rather than by the delimiter rule. Characterization.
      name: "_ inside ** is literal today (characterization)",
      input: "**_both_**",
      expected: "<p><strong>_both_</strong></p>",
    },
    {
      name: "* inside __ still emphasises",
      input: "__*both*__",
      expected: "<p><strong><em>both</em></strong></p>",
    },

    // --- Underscores with no closer ---
    {
      name: "a lone trailing underscore stays literal",
      input: "trailing_",
      expected: "<p>trailing_</p>",
    },
    {
      name: "an underscore before a space stays literal",
      input: "text_ end",
      expected: "<p>text_ end</p>",
    },
    {
      name: "an escaped underscore stays literal",
      input: "escaped \\_not em\\_ here",
      expected: "<p>escaped _not em_ here</p>",
    },

    // --- The lookbehind is punctuation- and boundary-aware ---
    {
      name: "punctuation before the underscore still opens",
      input: "(_em_)",
      expected: "<p>(<em>em</em>)</p>",
    },
    {
      name: "punctuation after the emphasis still closes",
      input: "_em_.",
      expected: "<p><em>em</em>.</p>",
    },
    {
      name: "an underscore right after a closed strong token still opens",
      input: "**bold**_it_",
      expected: "<p><strong>bold</strong><em>it</em></p>",
    },
    {
      name: "an underscore right after a closed code span still opens",
      input: "`c`_x_",
      expected: "<p><code>c</code><em>x</em></p>",
    },

    // --- Every block context reached the same defect ---
    {
      name: "snake_case in a link label keeps the href",
      input: "[snake_case](https://e.com)",
      expected: '<p><a target="_blank" rel="noopener" href="https://e.com">snake_case</a></p>',
    },
    {
      name: "snake_case in a heading renders literally",
      input: "# snake_case heading",
      expected: "<h1>snake_case heading</h1>",
    },
    {
      name: "snake_case in a list item renders literally",
      input: "- item_name here",
      expected: "<ul><li>item_name here</li></ul>",
    },
    {
      name: "snake_case in an ordered list renders literally",
      input: "1. num_one and _em_",
      expected: "<ol><li>num_one and <em>em</em></li></ol>",
    },
    {
      name: "snake_case in a blockquote renders literally",
      input: "> quote_with_underscore",
      expected: "<blockquote><p>quote_with_underscore</p></blockquote>",
    },
    {
      // The `_` used to swallow the closing `|`, so the whole table collapsed
      // into headings and a paragraph. Structure is part of the fix.
      name: "snake_case in a table cell renders literally",
      input: "\n| a_b | c |\n|---|---|\n| d_e | f |\n",
      expected:
        "<table><thead><tr><th> a_b </th><th> c </th></tr></thead>" +
        "<tbody><tr><td> d_e </td><td> f </td></tr></tbody></table>",
    },
    {
      name: "snake_case in a task list renders literally",
      input: "- [x] task_name _em_",
      expected:
        '<ul><li><input type="checkbox" disabled="" aria-label="Task item" checked=""> ' +
        "task_name <em>em</em></li></ul>",
    },

    // --- The lookbehind resets at every boundary the parser produces ---
    {
      name: "a soft line break resets the lookbehind",
      input: "a_b\n_real_",
      expected: "<p>a_b<br><em>real</em></p>",
    },
    { name: "a <br> resets the lookbehind", input: "a<br>_x_", expected: "<p>a<br><em>x</em></p>" },
    {
      name: "a paragraph break resets the lookbehind",
      input: "a_b\n\n_real_",
      expected: "<p>a_b</p><p><em>real</em></p>",
    },
    {
      name: "an underscore after a horizontal rule still opens",
      input: "---\n\n_em_ after rule",
      expected: "<hr><p><em>em</em> after rule</p>",
    },
    {
      name: "strikethrough closes over an intraword underscore",
      input: "~~a_b~~",
      expected: "<p><s>a_b</s></p>",
    },
    {
      name: "an intraword underscore outside a code span stays literal",
      input: "a_b `c_d` e_f",
      expected: "<p>a_b <code>c_d</code> e_f</p>",
    },

    // --- Inside an already-open `_` token. The rule is applied at all three
    //     places a `_` run can open emphasis, so an intraword run is literal
    //     here too; only the CLOSE is left ungated, because refusing a close
    //     would leave the token open to the end of the line.
    {
      name: "the reported string inside __ renders literally",
      input: "__run_progress__",
      expected: "<p><strong>run_progress</strong></p>",
    },
    {
      name: "snake_case inside __ renders literally",
      input: "__snake_case in strong__",
      expected: "<p><strong>snake_case in strong</strong></p>",
    },
    {
      name: "a single intraword _ inside __ renders literally",
      input: "__a_b__",
      expected: "<p><strong>a_b</strong></p>",
    },
    {
      name: "an intraword __ inside _ stays literal",
      input: "_a__b__c_",
      expected: "<p><em>a__b__c</em></p>",
    },
    {
      name: "an unbalanced __ inside _ stays literal",
      input: "_a__b_",
      expected: "<p><em>a__b</em></p>",
    },
    {
      name: "an intraword _ inside __ stays literal",
      input: "__a_b_c__",
      expected: "<p><strong>a_b_c</strong></p>",
    },
    {
      name: "_em_ at a word boundary inside __ still emphasises",
      input: "__a _b_ c__",
      expected: "<p><strong>a <em>b</em> c</strong></p>",
    },
    // The next two leave the OUTER run unclosed, which an append-only parser
    // renders as emphasis where CommonMark falls back to literal text. That gap
    // predates the delimiter rule and needs the same delimiter stack the close
    // half does; what these pin is that the inner intraword run is literal.
    {
      name: "a trailing __ inside _ stays literal",
      input: "_x__y__",
      expected: "<p><em>x__y__</em></p>",
    },
    {
      name: "a trailing _ inside __ stays literal",
      input: "__x_y_",
      expected: "<p><strong>x_y_</strong></p>",
    },
  ];

  it.each(cases)("$name", ({ input, expected }) => {
    const html = renderMarkdown(input);
    if (typeof expected === "string") {
      expect(html).toBe(expected);
    } else {
      expect(html).toMatch(expected);
    }
  });

  // The close half of the CommonMark rule is deliberately not implemented: an
  // append-only parser cannot un-open a token, so refusing the close would leave
  // the emphasis running to the end of the line — louder than closing it early.
  // CommonMark renders both of these literally.
  it("_foo_bar closes early (known limitation)", () => {
    expect(renderMarkdown("_foo_bar")).toBe("<p><em>foo</em>bar</p>");
  });

  it("_foo bar_baz closes early (known limitation)", () => {
    expect(renderMarkdown("_foo bar_baz")).toBe("<p><em>foo bar</em>baz</p>");
  });

  // A run of three or more only goes literal for its first two characters. The
  // rule is evaluated per delimiter, and the blocked pair lands in the text
  // buffer, so the remainder of the run reads a `_` — punctuation — as what
  // precedes it and may open after all. Closer to CommonMark (which renders the
  // whole run literally) than the `a<strong><em>b` this used to produce, and the
  // shortfall needs the same delimiter stack the close half does.
  it("a run of three or more underscores goes partly literal (known limitation)", () => {
    expect(renderMarkdown("a___b")).toBe("<p>a__<em>b</em></p>");
    expect(renderMarkdown("a____b")).toBe("<p>a__<strong>b</strong></p>");
  });
});

// ---------------------------------------------------------------------------
// Property-based streaming invariant: MarkdownRenderer append-only contract
// (tarch-b14-c4-p2)
// ---------------------------------------------------------------------------

describe("createMarkdownStream streaming/one-shot equivalence", () => {
  it("streaming writeDelta at random split points produces same output as one-shot", () => {
    fc.assert(
      fc.property(
        fc.string({ minLength: 1, maxLength: 300 }),
        fc.array(fc.double({ min: 0, max: 1, noNaN: true }), { minLength: 1, maxLength: 8 }),
        (markdown, splitPoints) => {
          // Generate non-decreasing delta lengths by splitting at random fractions.
          const sorted = [...splitPoints].sort((a, b) => a - b);
          const indices = sorted.map((f) => Math.floor(f * markdown.length));
          const cuts = [...new Set([...indices, markdown.length])].sort((a, b) => a - b);

          // Streaming path: feed deltas computed from cumulative cuts.
          const streamEl = document.createElement("div");
          const renderer = createMarkdownStream(streamEl);
          let lastCut = 0;
          for (const cut of cuts) {
            if (cut > lastCut) {
              renderer.writeDelta(markdown.slice(lastCut, cut));
              lastCut = cut;
            }
          }
          renderer.end();

          // One-shot path.
          const oneShotHtml = renderMarkdown(markdown);

          // Compare text content.
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

  it("end() is idempotent — second call is a no-op", () => {
    const el = document.createElement("div");
    const renderer = createMarkdownStream(el);
    renderer.writeDelta("hello world");
    renderer.end();
    const after = el.innerHTML;
    renderer.end(); // should not change anything
    expect(el.innerHTML).toBe(after);
  });

  it("writeDelta after end() is ignored", () => {
    const el = document.createElement("div");
    const renderer = createMarkdownStream(el);
    renderer.writeDelta("hello");
    renderer.end();
    const after = el.innerHTML;
    renderer.writeDelta(" more"); // should not change anything
    expect(el.innerHTML).toBe(after);
  });
});

// ---------------------------------------------------------------------------
// Surface contracts: renderMarkdown is pure structure; renderMarkdownInto
// decorates; createMarkdownStream decorates + animates.
// ---------------------------------------------------------------------------

import { renderMarkdownInto } from "./markdown.js";

describe("markdown surface contracts", () => {
  it("renderMarkdown returns pure structure (no decoration, no animation marker)", () => {
    const html = renderMarkdown("```js\nconsole.log(1)\n```");
    // Pure parser output — no .code-wrap, no .code-actions, no
    // data-vk-block-enter.
    expect(html).not.toContain("code-wrap");
    expect(html).not.toContain("code-actions");
    expect(html).not.toContain("data-vk-block-enter");
    expect(html).toContain("<pre");
    expect(html).toContain("<code");
  });

  it("renderMarkdown returns pure structure for paragraphs (no path linkify)", () => {
    const html = renderMarkdown("see foo/bar.ts for details");
    // Pure parser — bare text, no <a> linkification.
    expect(html).not.toContain('href="');
    expect(html).toContain("foo/bar.ts");
  });

  it("renderMarkdownInto decorates code blocks (replay path)", () => {
    const el = document.createElement("div");
    renderMarkdownInto(el, "```js\nconsole.log(1)\n```");
    // Replay path runs decorateCodeBlocks: should wrap pre in
    // .code-wrap and add .code-actions buttons.
    expect(el.querySelector(".code-wrap")).not.toBeNull();
    // No animation marker on replay.
    expect(el.querySelector("[data-vk-block-enter]")).toBeNull();
  });

  it("createMarkdownStream end() decorates AND tags blocks for animation", () => {
    const el = document.createElement("div");
    const r = createMarkdownStream(el);
    r.writeDelta("```js\ncode\n```");
    r.end();
    // Streaming path: decoration AND entry animation marker present.
    expect(el.querySelector(".code-wrap")).not.toBeNull();
    expect(el.querySelector("[data-vk-block-enter]")).not.toBeNull();
  });

  it("createMarkdownStream end() is a synchronous drain", () => {
    const el = document.createElement("div");
    const r = createMarkdownStream(el);
    // Write a chunk larger than PARSE_SLICE_BYTES (4096) to force
    // the async drain path. end() must complete the parse synchronously.
    const big = "a".repeat(10_000);
    r.writeDelta(big);
    r.end();
    // After end(), all text is parsed and rendered.
    expect(el.textContent?.length).toBe(10_000);
  });
});

// ---------------------------------------------------------------------------
// The streaming tail: a fence the model has not closed.
//
// The renderer's per-block callback fires only on CLOSE, and `parser_end` does
// not close an open token — so before the sweeps below, an unterminated fence
// carried no highlight, no language and no Copy button, permanently.
// ---------------------------------------------------------------------------

describe("markdown code-block decoration while streaming", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("decorates a fence that is still open, provisionally", () => {
    vi.useFakeTimers();
    const el = document.createElement("div");
    const r = createMarkdownStream(el);
    r.writeDelta("```go\nfunc main() {\n");
    // The write buffer flushes on its own interval; nothing has closed yet.
    vi.advanceTimersByTime(250);
    const wrap = el.querySelector(".code-wrap");
    expect(wrap?.getAttribute("data-code-state")).toBe("streaming");
    expect(el.querySelector(".code-lang")?.textContent).toBe("go");
  });

  it("promotes the same block to final once the fence closes", () => {
    vi.useFakeTimers();
    const el = document.createElement("div");
    const r = createMarkdownStream(el);
    r.writeDelta("```go\nfunc main() {\n");
    vi.advanceTimersByTime(250);
    r.writeDelta("}\n```\n");
    vi.advanceTimersByTime(250);
    expect(el.querySelectorAll(".code-head")).toHaveLength(1);
    expect(el.querySelector(".code-wrap")?.getAttribute("data-code-state")).toBe("final");
  });

  it("finalizes a fence the model never closed, at end()", () => {
    const el = document.createElement("div");
    const r = createMarkdownStream(el);
    r.writeDelta("```go\nfunc main() {\n");
    r.end();
    expect(el.querySelector(".code-wrap")?.getAttribute("data-code-state")).toBe("final");
    expect(el.querySelector(".code-lang")?.textContent).toBe("go");
  });

  it("decorates an unterminated fence on the replay path too", () => {
    const el = document.createElement("div");
    renderMarkdownInto(el, "```bash\necho hi");
    expect(el.querySelector(".code-wrap")).not.toBeNull();
    expect(el.querySelector(".code-lang")?.textContent).toBe("bash");
  });
});

// ---------------------------------------------------------------------------
// Math: the whole path, parser through converter.
// ---------------------------------------------------------------------------

const MATHML_NS = "http://www.w3.org/1998/Math/MathML";

describe("markdown math rendering", () => {
  const cases: { name: string; md: string; display: boolean }[] = [
    { name: "$…$ inline", md: "the value $x^2$ here", display: false },
    { name: "\\(…\\) inline", md: "the value \\(x^2\\) here", display: false },
    { name: "$$…$$ block", md: "$$\nx^2\n$$\n", display: true },
    { name: "\\[…\\] block", md: "\\[\nx^2\n\\]\n", display: true },
  ];

  for (const tc of cases) {
    it(`renders ${tc.name} as native MathML`, () => {
      const el = document.createElement("div");
      renderMarkdownInto(el, tc.md);
      const math = el.querySelector("math");
      expect(math).not.toBeNull();
      expect(math?.namespaceURI).toBe(MATHML_NS);
      expect(math?.hasAttribute("display")).toBe(tc.display);
    });
  }

  it("survives a delimiter split across write chunks", () => {
    // The stream slices at a fixed byte budget regardless of content, so any
    // delimiter can arrive in two pieces. The parser's `pending` field is what
    // makes that work; this pins it end to end.
    const el = document.createElement("div");
    const r = createMarkdownStream(el);
    r.writeDelta("area $");
    r.writeDelta("\\pi r^2");
    r.writeDelta("$ done");
    r.end();
    const math = el.querySelector("math");
    expect(math?.namespaceURI).toBe(MATHML_NS);
    expect(math?.textContent).toBe("\u03c0r2");
  });

  it("leaves an unsupported expression as its raw LaTeX", () => {
    const el = document.createElement("div");
    renderMarkdownInto(el, "a matrix $\\begin{pmatrix} a & b \\end{pmatrix}$ inline");
    expect(el.querySelector("math")).toBeNull();
    const host = el.querySelector("[data-math]");
    expect(host?.hasAttribute("data-math-raw")).toBe(true);
    expect(host?.textContent).toBe("\\begin{pmatrix} a & b \\end{pmatrix}");
  });

  it("leaves an unclosed expression as its raw LaTeX", () => {
    const el = document.createElement("div");
    renderMarkdownInto(el, "unfinished $x^2 + y");
    expect(el.querySelector("math")).toBeNull();
    expect(el.querySelector("[data-math-raw]")?.textContent).toBe("x^2 + y");
  });

  it("needs a newline after the block opener, and says so by rendering the source", () => {
    // A KNOWN LIMITATION of the parser this port carries: `\[` only opens a
    // block when a newline follows, because `\[` is also a legitimate escaped
    // bracket. Pinned so the degradation is a decision rather than a surprise —
    // the reader sees the literal text, which is honest.
    const el = document.createElement("div");
    renderMarkdownInto(el, "\\[x^2\\]\n");
    expect(el.querySelector("[data-math]")).toBeNull();
    expect(el.textContent).toBe("[x^2]");
  });

  it("does not treat a price or a bare dollar as math", () => {
    const el = document.createElement("div");
    renderMarkdownInto(el, "it costs $5 or $ 10");
    expect(el.querySelector("[data-math]")).toBeNull();
    expect(el.textContent).toContain("$5");
  });
});

// ---------------------------------------------------------------------------
// Nesting past the token-stack cap.
//
// TOKEN_ARRAY_CAP is the intended depth limit. What is not intended is the
// saturating path failing to consume the character it was handed, which used to
// re-enter the same handler with byte-identical state until the JS stack blew.
// ---------------------------------------------------------------------------

describe("renderMarkdown deep nesting", () => {
  it("renders 22 nested blockquotes, the last working depth", () => {
    const html = renderMarkdown(">".repeat(22) + " x");
    expect(html).toBe("<blockquote>".repeat(22) + "<p>x</p>" + "</blockquote>".repeat(22));
  });

  it("keeps the text when blockquote nesting saturates the token stack", () => {
    const html = renderMarkdown(">".repeat(23) + " x");
    expect(html).toContain("x");
  });

  it("does not throw on absurd blockquote nesting", () => {
    expect(() => renderMarkdown(">".repeat(200) + " x")).not.toThrow();
    expect(() => renderMarkdown("> ".repeat(30) + "x")).not.toThrow();
    expect(() => renderMarkdown(">".repeat(1000) + " x")).not.toThrow();
  });

  it("does not throw on absurd list, indent or emphasis nesting", () => {
    expect(() => renderMarkdown("- ".repeat(1024) + "x")).not.toThrow();
    expect(() => renderMarkdown(" ".repeat(1024) + "- x")).not.toThrow();
    expect(() => renderMarkdown("*".repeat(1024))).not.toThrow();
    expect(() => renderMarkdown(">".repeat(30) + " ```js`x")).not.toThrow();
  });
});
