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
    // The next two leave the OUTER run unclosed. It is unwrapped at block close,
    // so the whole run is literal. CommonMark pairs the runs differently
    // (`<em>x__y</em>_` and `_<em>x_y</em>`), which needs the delimiter stack
    // the close half does; what these pin is that no character is lost and
    // nothing is emphasised that the author did not close.
    {
      name: "a trailing __ inside _ stays literal",
      input: "_x__y__",
      expected: "<p>_x__y__</p>",
    },
    {
      name: "a trailing _ inside __ stays literal",
      input: "__x_y_",
      expected: "<p>__x_y_</p>",
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

  // The close half of the rule: a `_` run with a word character on both sides
  // cannot close either, so the run that opened stays open and is restored as
  // literal text when the block ends.
  const closeCases: { name: string; input: string; expected: string }[] = [
    { name: "_foo_bar stays literal", input: "_foo_bar", expected: "<p>_foo_bar</p>" },
    { name: "_foo bar_baz stays literal", input: "_foo bar_baz", expected: "<p>_foo bar_baz</p>" },
    {
      name: "_internal_state stays literal",
      input: "_internal_state",
      expected: "<p>_internal_state</p>",
    },
    {
      name: "a run that does close later still emphasises",
      input: "_internal_state_here_",
      expected: "<p><em>internal_state_here</em></p>",
    },
    { name: "_foo_ still closes at end of input", input: "_foo_", expected: "<p><em>foo</em></p>" },
    {
      name: "_foo_. closes before punctuation",
      input: "_foo_.",
      expected: "<p><em>foo</em>.</p>",
    },
    {
      name: "_foo_ bar closes before a space",
      input: "_foo_ bar",
      expected: "<p><em>foo</em> bar</p>",
    },
    { name: "a _b c_ d is unaffected", input: "a _b c_ d", expected: "<p>a <em>b c</em> d</p>" },
    {
      name: "* is exempt by design",
      input: "*foo*bar",
      expected: "<p><em>foo</em>bar</p>",
    },
    {
      // The `__` close decides on the second `_` of the run, before the character
      // after it exists, so gating it needs a character of right-context this
      // does not have. Characterization.
      name: "__x_y_ stays literal (the __ close is out of scope)",
      input: "__x_y_",
      expected: "<p>__x_y_</p>",
    },
  ];

  it.each(closeCases)("$name", ({ input, expected }) => {
    expect(renderMarkdown(input)).toBe(expected);
  });

  // A run of three or more opens for its tail, because the rule is evaluated per
  // delimiter and the blocked pair lands in the text buffer, so the remainder of
  // the run reads a `_` — punctuation — as what precedes it. The token it opens
  // never closes, so the unwrap restores the whole run: CommonMark-correct.
  it("a run of three or more underscores stays literal", () => {
    expect(renderMarkdown("a___b")).toBe("<p>a___b</p>");
    expect(renderMarkdown("a____b")).toBe("<p>a____b</p>");
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

  it("restores the delimiter of an expression that never closes", () => {
    // An unclosed `$` is not an equation, it is a dollar sign. The host is
    // unwrapped at block close and the delimiter comes back as text, where it
    // used to be deleted and the rest of the paragraph styled as source.
    const el = document.createElement("div");
    renderMarkdownInto(el, "unfinished $x^2 + y");
    expect(el.querySelector("math")).toBeNull();
    expect(el.querySelector("[data-math]")).toBeNull();
    expect(el.textContent).toBe("unfinished $x^2 + y");
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

  it("does not throw on a table nested past the depth limit", () => {
    const table = " | a | b |\n| - | - |\n| 1 | 2 |";
    expect(() => renderMarkdown(">".repeat(21) + table)).not.toThrow();
    expect(() => renderMarkdown(">".repeat(200) + table)).not.toThrow();
    expect(() => renderMarkdown("> ".repeat(30) + table.slice(1))).not.toThrow();
  });

  it("renders a table nested 20 blockquotes deep, the last working depth", () => {
    const html = renderMarkdown(">".repeat(20) + " | a | b |\n| - | - |\n| 1 | 2 |");
    expect(html).toContain("<table><thead><tr><th> a </th><th> b </th></tr></thead>");
    expect(html).toContain("<tbody><tr><td> 1 </td><td> 2 </td></tr></tbody>");
  });

  // A table needs three tokens. Where only some of them fit, the cells used to
  // land as text nodes directly inside the `<table>` — invalid HTML — and one
  // depth lower the row handler re-fed its character forever.
  it.each([21, 22])(
    "keeps every character as text when depth %i leaves no room for a table's row and cell",
    (depth) => {
      const html = renderMarkdown(">".repeat(depth) + " | a | b |\n| - | - |\n| 1 | 2 |");
      expect(html).not.toContain("<table");
      expect(html).toContain("| a | b |");
      expect(html).toContain("| - | - |");
      expect(html).toContain("| 1 | 2 |");
    },
  );

  // Past the cap no delimiter can become markup, so every one of them has to
  // read as the literal text that was typed. The opener used to be dropped: the
  // push was refused and the handler then overwrote `pending` with the next
  // character regardless.
  it("keeps a bare hash run as text when the token stack is saturated", () => {
    const html = renderMarkdown(">".repeat(23) + " ##");
    expect(html).not.toContain("<h2");
    expect(html).toContain("##");
  });

  it.each([
    ["*a*", "*"],
    ["**a**", "**"],
    ["_a_", "_"],
    ["__a__", "__"],
    ["~~a~~", "~~"],
    ["`a`", "`"],
    ["[a](b)", "["],
    ["![a](b)", "!["],
    ["$a$", "$"],
  ])("keeps the %s opener as text when the token stack is saturated", (body, opener) => {
    const html = renderMarkdown(">".repeat(22) + " " + body);
    expect(html).toContain(opener + "a");
    expect(html).not.toContain("<em");
    expect(html).not.toContain("<strong");
    expect(html).not.toContain("<code");
    expect(html).not.toContain("<a ");
    expect(html).not.toContain("<img");
  });

  // An attribute set after a refused push reaches whatever element is current,
  // which past the cap is the enclosing blockquote: a fence's info string
  // arrived as its `class` and an ordered list's number as its `start`.
  it.each([
    ["```js\ncode\n```", "class", "```js"],
    ["3. item", "start", "3. item"],
    ["3) item", "start", "3) item"],
  ])("puts no attribute on the blockquote for %j past the cap", (body, attr, literal) => {
    const html = renderMarkdown(">".repeat(23) + " " + body);
    expect(html).not.toContain(attr + "=");
    expect(html).toContain(literal);
  });
});

// ---------------------------------------------------------------------------
// A held character at the end of the input, or at the end of a line, is
// literal text. It used to be deleted: `parser_end` writes a synthetic newline
// to flush `pending`, and the arms that received it consumed the held
// character as the opener of a construct the input never completes.
// ---------------------------------------------------------------------------

describe("renderMarkdown end-of-input characters", () => {
  const cases: { name: string; input: string; expected: string | RegExp }[] = [
    { name: "trailing <", input: "ab<", expected: "<p>ab&lt;</p>" },
    { name: "lone <", input: "<", expected: "<p>&lt;</p>" },
    { name: "trailing [", input: "ab[", expected: "<p>ab[</p>" },
    { name: "trailing backslash", input: "ab\\", expected: "<p>ab\\</p>" },
    { name: "trailing backtick", input: "ab`", expected: "<p>ab`</p>" },
    { name: "[ before a newline", input: "ab[\ncd", expected: "<p>ab[<br>cd</p>" },
    { name: "backtick before a newline", input: "ab`\ncd", expected: "<p>ab`<br>cd</p>" },
    {
      name: "backslash newline is still a hard break",
      input: "ab\\\ncd",
      expected: "<p>ab<br>cd</p>",
    },
    { name: "< followed by text is unchanged", input: "ab<x", expected: "<p>ab&lt;x</p>" },
    { name: "< followed by a space is unchanged", input: "ab< ", expected: "<p>ab&lt; </p>" },
    { name: "<br> is still a line break", input: "a<br>b", expected: "<p>a<br>b</p>" },
    {
      name: "a closed code span is unchanged",
      input: "`code`",
      expected: "<p><code>code</code></p>",
    },
    {
      name: "a closed link is unchanged",
      input: "[a](https://e.com)",
      expected: '<p><a target="_blank" rel="noopener" href="https://e.com">a</a></p>',
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
// An ordered list may be marked `1)` as well as `1.` (CommonMark 5.2), and
// changing the delimiter mid-list starts a new list rather than continuing the
// old one. A marker of more than nine digits is not a list marker at all.
// ---------------------------------------------------------------------------

describe("renderMarkdown ordered list delimiters", () => {
  const cases: { name: string; input: string; expected: string }[] = [
    {
      name: "a paren-delimited list",
      input: "1) first\n2) second",
      expected: "<ol><li>first</li><li>second</li></ol>",
    },
    {
      name: "a paren-delimited list starting past one",
      input: "3) three\n4) four",
      expected: '<ol start="3"><li>three</li><li>four</li></ol>',
    },
    { name: "a single paren-delimited item", input: "1) only", expected: "<ol><li>only</li></ol>" },
    {
      name: "a delimiter change from a dot starts a new list",
      input: "1. first\n2) second",
      expected: '<ol><li>first</li></ol><ol start="2"><li>second</li></ol>',
    },
    {
      name: "a delimiter change from a paren starts a new list",
      input: "1) first\n2. second",
      expected: '<ol><li>first</li></ol><ol start="2"><li>second</li></ol>',
    },
    {
      name: "an indented paren marker nests inside a dot list",
      input: "1. a\n   1) b",
      expected: "<ol><li>a<ol><li>b</li></ol></li></ol>",
    },
    {
      name: "a nine-digit marker is still a list",
      input: "123456789. x",
      expected: '<ol start="123456789"><li>x</li></ol>',
    },
    {
      name: "a ten-digit marker is not a list",
      input: "1234567890. x",
      expected: "<p>1234567890. x</p>",
    },
    { name: "a paren with no space is not a list", input: "1)first", expected: "<p>1)first</p>" },
    {
      name: "a paren inside prose is not a list",
      input: "see 1) this",
      expected: "<p>see 1) this</p>",
    },
    {
      name: "a dot-delimited list is unchanged",
      input: "1. first\n2. second",
      expected: "<ol><li>first</li><li>second</li></ol>",
    },
  ];

  it.each(cases)("$name", ({ input, expected }) => {
    expect(renderMarkdown(input)).toBe(expected);
  });
});

// ---------------------------------------------------------------------------
// An ATX heading's optional closing sequence (CommonMark 4.2): a run of `#`
// preceded by a space or tab and followed by nothing but spaces and tabs is
// syntax, not content. A `#` run with no such whitespace, or with text after
// it, is content. A bare `#` run opens an empty heading.
// ---------------------------------------------------------------------------

describe("renderMarkdown ATX closing sequence", () => {
  const cases: { name: string; input: string; expected: string }[] = [
    { name: "a two-hash closing run", input: "## heading ##", expected: "<h2>heading</h2>" },
    { name: "a one-hash closing run", input: "## heading #", expected: "<h2>heading</h2>" },
    { name: "a longer closing run", input: "## heading ###", expected: "<h2>heading</h2>" },
    { name: "two spaces before the run", input: "## heading  ##", expected: "<h2>heading</h2>" },
    {
      name: "trailing spaces after the run",
      input: "## heading ##   ",
      expected: "<h2>heading</h2>",
    },
    { name: "a tab before the run", input: "## heading\t##", expected: "<h2>heading</h2>" },
    { name: "a tab after the run", input: "## heading ##\t", expected: "<h2>heading</h2>" },
    {
      name: "only the last run closes the heading",
      input: "## heading ## #",
      expected: "<h2>heading ##</h2>",
    },
    {
      name: "a single hash before the closing run is content",
      input: "## heading # #",
      expected: "<h2>heading #</h2>",
    },
    {
      name: "inline code survives the strip",
      input: "## a `#` b ##",
      expected: "<h2>a <code>#</code> b</h2>",
    },
    {
      name: "emphasis survives the strip",
      input: "## a *b* ##",
      expected: "<h2>a <em>b</em></h2>",
    },
    { name: "an escaped hash is content", input: "## \\## ##", expected: "<h2>##</h2>" },
    { name: "two headings in a row", input: "# h #\n# i #", expected: "<h1>h</h1><h1>i</h1>" },
    {
      name: "a heading inside a blockquote",
      input: "> ## h ##",
      expected: "<blockquote><h2>h</h2></blockquote>",
    },
    {
      name: "a following paragraph is unaffected",
      input: "## h ##\ntext",
      expected: "<h2>h</h2><p>text</p>",
    },

    // --- a bare run opens an empty heading ---
    { name: "two hashes alone", input: "##", expected: "<h2></h2>" },
    { name: "one hash alone", input: "#", expected: "<h1></h1>" },
    { name: "six hashes alone", input: "######", expected: "<h6></h6>" },
    { name: "seven hashes alone is a paragraph", input: "#######", expected: "<p>#######</p>" },
    { name: "an opening run and a closing run", input: "## ##", expected: "<h2></h2>" },
    { name: "an opening run and one hash", input: "## #", expected: "<h2></h2>" },
    { name: "a bare run then a line", input: "##\nnext", expected: "<h2></h2><p>next</p>" },
  ];

  it.each(cases)("$name", ({ input, expected }) => {
    expect(renderMarkdown(input)).toBe(expected);
  });

  // A `#` run that is not a closing sequence stays exactly where it was typed.
  const unchanged: { name: string; input: string; expected: string }[] = [
    { name: "a run followed by text", input: "## heading #foo", expected: "<h2>heading #foo</h2>" },
    {
      name: "a run with no space before it",
      input: "## heading##",
      expected: "<h2>heading##</h2>",
    },
    {
      name: "a run with text after it",
      input: "## heading ## x",
      expected: "<h2>heading ## x</h2>",
    },
    { name: "a trailing space with no run", input: "## heading ", expected: "<h2>heading</h2>" },
    { name: "a plain heading", input: "## heading", expected: "<h2>heading</h2>" },
    { name: "a hash-space-only heading", input: "## ", expected: "<h2></h2>" },
    // A heading inside a list item is the nested-block family, which this
    // parser does not open; the marker run stays literal text.
    {
      name: "a heading inside a list item",
      input: "- ## h ##",
      expected: "<ul><li>## h ##</li></ul>",
    },
  ];

  it.each(unchanged)("leaves $name unchanged", ({ input, expected }) => {
    expect(renderMarkdown(input)).toBe(expected);
  });
});

// ---------------------------------------------------------------------------
// A closing code fence must be alone on its line (CommonMark 4.5): at most
// three spaces of indent, a run of at least as many backticks as the opener,
// then nothing but spaces and tabs.
// ---------------------------------------------------------------------------

describe("renderMarkdown code fence close rule", () => {
  const cases: { name: string; input: string; expected: string }[] = [
    {
      name: "a backtick run after content does not close the fence",
      input: "```\nfoo ```\nbar\n",
      expected: '<pre class="code"><code>foo ```\nbar\n</code></pre>',
    },
    {
      name: "a backtick run glued to content keeps the content intact",
      input: "```\nfoo```\nbar\n",
      expected: '<pre class="code"><code>foo```\nbar\n</code></pre>',
    },
    {
      name: "a four-space indented fence is code content",
      input: "```\nfoo\n    ```\nbar\n",
      expected: '<pre class="code"><code>foo\n    ```\nbar\n</code></pre>',
    },
    {
      name: "a three-space indented fence still closes",
      input: "```\nfoo\n   ```\nbar\n",
      expected: '<pre class="code"><code>foo</code></pre><p>bar</p>',
    },
    {
      name: "a longer run than the opener closes",
      input: "```\nfoo\n`````\nbar\n",
      expected: '<pre class="code"><code>foo</code></pre><p>bar</p>',
    },
    {
      name: "a shorter run than the opener does not close",
      input: "````\nfoo\n```\nbar\n",
      expected: '<pre class="code"><code>foo\n```\nbar\n</code></pre>',
    },
    {
      name: "trailing space after the run still closes",
      input: "```\nfoo\n``` \nbar\n",
      expected: '<pre class="code"><code>foo</code></pre><p>bar</p>',
    },
    {
      name: "trailing tab after the run still closes",
      input: "```\nfoo\n```\t\nbar\n",
      expected: '<pre class="code"><code>foo</code></pre><p>bar</p>',
    },
    {
      name: "a run followed by text does not close",
      input: "```\nfoo ``` bar\n```\nend\n",
      expected: '<pre class="code"><code>foo ``` bar</code></pre><p>end</p>',
    },
    {
      name: "an info string mentioning backticks is unaffected",
      input: "```md\nuse ``` to fence\n```\nafter\n",
      expected:
        '<pre class="code"><code class="language-md">use ``` to fence</code></pre><p>after</p>',
    },
    {
      name: "an unterminated fence keeps its language",
      input: "```js\nlet x=1;",
      expected: '<pre class="code"><code class="language-js">let x=1;</code></pre>',
    },
    {
      name: "an empty fence closes on the first line",
      input: "```\n```\nafter\n",
      expected: '<pre class="code"><code></code></pre><p>after</p>',
    },
  ];

  it.each(cases)("$name", ({ input, expected }) => {
    expect(renderMarkdown(input)).toBe(expected);
  });
});

// ---------------------------------------------------------------------------
// Balanced brackets in a link or image label (CommonMark 6.3).
// ---------------------------------------------------------------------------

describe("renderMarkdown brackets inside a link label", () => {
  const A = '<a target="_blank" rel="noopener"';

  it("keeps a bracketed run inside the label and keeps the href", () => {
    expect(renderMarkdown("[a [b] c](https://example.com)")).toBe(
      `<p>${A} href="https://example.com">a [b] c</a></p>`,
    );
  });

  it("handles brackets nested more than one deep", () => {
    expect(renderMarkdown("[a [b [c] d] e](https://e.com)")).toBe(
      `<p>${A} href="https://e.com">a [b [c] d] e</a></p>`,
    );
  });

  it("keeps a bracketed run inside an image alt", () => {
    expect(renderMarkdown("![alt [x] y](https://e.com/i.png)")).toBe(
      '<p><img alt="alt [x] y" src="https://e.com/i.png"></p>',
    );
  });

  it("leaves a plain link unchanged", () => {
    expect(renderMarkdown("[a](https://e.com)")).toBe(`<p>${A} href="https://e.com">a</a></p>`);
  });

  it("closes the label on a bracket with no destination", () => {
    expect(renderMarkdown("[a]b")).toBe(`<p>${A}>a</a>b</p>`);
  });
});

// ---------------------------------------------------------------------------
// A link or image destination and its optional title (CommonMark 6.3): the
// title may be delimited by quotes or parentheses, the destination may be
// wrapped in angle brackets, and a destination's parentheses may be balanced.
// A run that has none of those shapes keeps the whole run as the href.
// ---------------------------------------------------------------------------

describe("renderMarkdown link destinations and titles", () => {
  const A = '<a target="_blank" rel="noopener"';

  const cases: { name: string; input: string; expected: string }[] = [
    {
      name: "a double-quoted title",
      input: '[a](http://e.com "the title")',
      expected: `<p>${A} href="http://e.com" title="the title">a</a></p>`,
    },
    {
      name: "a single-quoted title",
      input: "[a](http://e.com 'the title')",
      expected: `<p>${A} href="http://e.com" title="the title">a</a></p>`,
    },
    {
      name: "a parenthesised title",
      input: "[a](http://e.com (the title))",
      expected: `<p>${A} href="http://e.com" title="the title">a</a></p>`,
    },
    {
      name: "a tab separating the title",
      input: '[a](http://e.com\t"t")',
      expected: `<p>${A} href="http://e.com" title="t">a</a></p>`,
    },
    {
      name: "an image title",
      input: '![a](http://e.com/i.png "t")',
      expected: '<p><img alt="a" src="http://e.com/i.png" title="t"></p>',
    },
    {
      name: "an escaped quote inside a title",
      input: '[a](http://e.com "a \\" b")',
      expected: `<p>${A} href="http://e.com" title="a &quot; b">a</a></p>`,
    },
    {
      name: "an angle-bracketed destination",
      input: "[a](<http://e.com>)",
      expected: `<p>${A} href="http://e.com">a</a></p>`,
    },
    {
      // A space inside the brackets is a legal destination character. The
      // reference percent-encodes it; this parser never rewrites a URL.
      name: "a space inside an angle-bracketed destination",
      input: "[a](<http://e.com/a b>)",
      expected: `<p>${A} href="http://e.com/a b">a</a></p>`,
    },
    {
      name: "an angle-bracketed destination with a title",
      input: '[a](<http://e.com> "t")',
      expected: `<p>${A} href="http://e.com" title="t">a</a></p>`,
    },
    {
      name: "balanced parentheses in a destination",
      input: "[a](http://e.com/x(1))",
      expected: `<p>${A} href="http://e.com/x(1)">a</a></p>`,
    },
    {
      name: "parentheses nested two deep in a destination",
      input: "[a](http://e.com/x(y(z)))",
      expected: `<p>${A} href="http://e.com/x(y(z))">a</a></p>`,
    },
    {
      // A DIVERGENCE, recorded: both references render this literally. Keeping
      // the title is the reading that loses nothing.
      name: "a title with no destination",
      input: '[a]( "the title")',
      expected: `<p>${A} href="" title="the title">a</a></p>`,
    },
    {
      name: "the scheme gate still fires on a link that carries a title",
      input: '[a](javascript:alert(1) "t")',
      expected: `<p>${A} href="#" title="t">a</a></p>`,
    },
  ];

  it.each(cases)("$name", ({ input, expected }) => {
    expect(renderMarkdown(input)).toBe(expected);
  });

  // A run that does not parse as `destination [whitespace title]` keeps the
  // whole run as the href, which is what it did before titles were read.
  const characterization: { name: string; input: string; expected: string }[] = [
    {
      name: "an unterminated title",
      input: '[a](http://e.com "the title)',
      expected: `<p>${A} href="http://e.com &quot;the title">a</a></p>`,
    },
    {
      name: "a space in a destination",
      input: "[a](http://e.com/a b)",
      expected: `<p>${A} href="http://e.com/a b">a</a></p>`,
    },
    {
      name: "text after a title",
      input: '[a](http://e.com "t" x)',
      expected: `<p>${A} href="http://e.com &quot;t&quot; x">a</a></p>`,
    },
    {
      name: "an empty destination",
      input: "[a]()",
      expected: `<p>${A} href="">a</a></p>`,
    },
    {
      name: "a plain link",
      input: "[a](http://e.com)",
      expected: `<p>${A} href="http://e.com">a</a></p>`,
    },
  ];

  it.each(characterization)("keeps today's reading for $name", ({ input, expected }) => {
    expect(renderMarkdown(input)).toBe(expected);
  });
});

// ---------------------------------------------------------------------------
// A bare URL followed by CJK punctuation.
//
// A CJK punctuation character is not on its own evidence that a URL ended —
// real URLs carry it raw. The one decidable case is a separator immediately
// followed by a backtick: RFC 3986 excludes the backtick, so it cannot be in
// the URL, and swallowing it eats the opening delimiter of the code span that
// follows and shifts every later backtick pairing in the paragraph.
// ---------------------------------------------------------------------------

describe("renderMarkdown bare URL with CJK punctuation", () => {
  it("cuts the URL at a separator followed by a backtick", () => {
    expect(renderMarkdown("见 https://example.com/2137，`96ed647b`）")).toBe(
      '<p>见 <a target="_blank" rel="noopener" href="https://example.com/2137">' +
        "https://example.com/2137</a>，<code>96ed647b</code>）</p>",
    );
  });

  it("leaves a separator followed by text in the URL (characterization)", () => {
    // Declined on Crew's evidence rule: …/wiki/苹果（公司）, …/wiki/我，机器人 and
    // …/wiki/モーニング娘。 are real URLs, so the character alone proves nothing.
    expect(renderMarkdown("https://e.com/a，b")).toBe(
      '<p><a target="_blank" rel="noopener" href="https://e.com/a，b">https://e.com/a，b</a></p>',
    );
  });

  it("keeps CJK punctuation inside a URL that a space terminates", () => {
    expect(renderMarkdown("https://zh.wikipedia.org/wiki/苹果（公司） ok")).toBe(
      '<p><a target="_blank" rel="noopener" href="https://zh.wikipedia.org/wiki/苹果（公司">' +
        "https://zh.wikipedia.org/wiki/苹果（公司</a>） ok</p>",
    );
  });

  it("keeps a sentence-ender out of the href when a space follows", () => {
    expect(renderMarkdown("见 https://example.com。 然后")).toBe(
      '<p>见 <a target="_blank" rel="noopener" href="https://example.com">' +
        "https://example.com</a>。 然后</p>",
    );
  });

  it("leaves a URL abutting its opening ** unlinked (characterization)", () => {
    // The `h` is consumed by handleCommon's emphasis arm, which leaves `**` in
    // `pending`, so parser_write's raw-URL entry (pending must be "" or " ")
    // is never reached. A word before the URL is what makes it a link.
    expect(renderMarkdown("**https://example.com**")).toBe(
      "<p><strong>https://example.com</strong></p>",
    );
  });
});

// ---------------------------------------------------------------------------
// An inline opener with no closer.
//
// Every inline opener commits its token on sight, so one that never closed used
// to format the rest of the block AND delete its own delimiter. The parser
// cannot un-open a token, but the renderer still holds the element when the
// block ends, so it unwraps it and restores the literal.
// ---------------------------------------------------------------------------

describe("renderMarkdown unclosed inline openers", () => {
  const cases: { name: string; input: string; expected: string }[] = [
    { name: "unclosed **", input: "a **b", expected: "<p>a **b</p>" },
    { name: "unclosed *", input: "a *b", expected: "<p>a *b</p>" },
    { name: "unclosed _", input: "a _b", expected: "<p>a _b</p>" },
    { name: "unclosed __", input: "a __b", expected: "<p>a __b</p>" },
    { name: "unclosed ~~", input: "a ~~b", expected: "<p>a ~~b</p>" },
    { name: "unclosed backtick", input: "the ` char", expected: "<p>the ` char</p>" },
    { name: "unclosed double backtick", input: "a ``b", expected: "<p>a ``b</p>" },
    { name: "unclosed [", input: "random[0,500)", expected: "<p>random[0,500)</p>" },
    { name: "unclosed ![", input: "an ![img", expected: "<p>an ![img</p>" },
    { name: "unclosed $", input: "a $x^2", expected: "<p>a $x^2</p>" },
    // The delimiter restored is the one the parser consumed. vibekit reads
    // `\\(` as a math opener rather than an escaped paren, so `\\(` comes back.
    { name: "unclosed \\(", input: "a \\(x^2", expected: "<p>a \\(x^2</p>" },
    {
      name: "the next paragraph is unaffected",
      input: "a **b\n\nnext para",
      expected: "<p>a **b</p><p>next para</p>",
    },
    { name: "inside a heading", input: "# a *b", expected: "<h1>a *b</h1>" },
    {
      name: "inside a blockquote",
      input: "> a *b",
      expected: "<blockquote><p>a *b</p></blockquote>",
    },
    {
      name: "inside a list item",
      input: "- a ` b\n- c",
      expected: "<ul><li>a ` b</li><li>c</li></ul>",
    },
    { name: "two nested unclosed runs", input: "a **b *c", expected: "<p>a **b *c</p>" },
    {
      name: "a soft break does not close a code span",
      input: "a ` b\nc",
      expected: "<p>a ` b<br>c</p>",
    },
    {
      // CommonMark resolves the link before emphasis, so the `*` is literal
      // INSIDE the label and the link keeps its href.
      name: "an unclosed run inside a link label leaves the link intact",
      input: "[a *b](https://e.com)",
      expected: '<p><a target="_blank" rel="noopener" href="https://e.com">a *b</a></p>',
    },
  ];

  it.each(cases)("$name", ({ input, expected }) => {
    expect(renderMarkdown(input)).toBe(expected);
  });

  const unchanged: { name: string; input: string; expected: string }[] = [
    { name: "closed **", input: "a **b** c", expected: "<p>a <strong>b</strong> c</p>" },
    { name: "closed *", input: "*em*", expected: "<p><em>em</em></p>" },
    { name: "closed _", input: "_em_", expected: "<p><em>em</em></p>" },
    { name: "closed ~~", input: "~~gone~~", expected: "<p><s>gone</s></p>" },
    { name: "closed backtick", input: "`code`", expected: "<p><code>code</code></p>" },
    {
      name: "closed link",
      input: "[x](http://e.com)",
      expected: '<p><a target="_blank" rel="noopener" href="http://e.com">x</a></p>',
    },
    {
      name: "closed image",
      input: "![r](http://e.com/r.png)",
      expected: '<p><img alt="r" src="http://e.com/r.png"></p>',
    },
    {
      name: "closed emphasis inside a link label",
      input: "[a *b* c](https://e.com)",
      expected: '<p><a target="_blank" rel="noopener" href="https://e.com">a <em>b</em> c</a></p>',
    },
  ];

  it.each(unchanged)("leaves $name unchanged", ({ input, expected }) => {
    expect(renderMarkdown(input)).toBe(expected);
  });

  it("keeps a mid-arrival link streaming as it does today", () => {
    // The block has not closed, so nothing about a partially arrived construct
    // changes: the label renders inside an href-less anchor until `)` lands.
    const el = document.createElement("div");
    const r = createMarkdownStream(el, { flushIntervalMs: 0 });
    r.writeDelta("see [the label");
    // One character short: the parser holds the last one to disambiguate.
    expect(el.querySelector("a")?.textContent).toBe("the labe");
    r.writeDelta("](https://e.com) end");
    r.end();
    expect(el.querySelector("a")?.getAttribute("href")).toBe("https://e.com");
    expect(el.textContent).toBe("see the label end");
  });

  it("keeps a mid-arrival code span streaming as it does today", () => {
    const el = document.createElement("div");
    const r = createMarkdownStream(el, { flushIntervalMs: 0 });
    r.writeDelta("run `npm ");
    expect(el.querySelector("code")?.textContent).toBe("npm");
    r.writeDelta("test` now");
    r.end();
    expect(el.querySelector("code")?.textContent).toBe("npm test");
    expect(el.textContent).toBe("run npm test now");
  });
});

// ---------------------------------------------------------------------------
// GFM tables: the delimiter row is required, column alignment is emitted, and
// trailing whitespace on a row is not a cell.
// ---------------------------------------------------------------------------

describe("renderMarkdown GFM tables", () => {
  const HEAD = "<table><thead><tr><th> a </th><th> b </th></tr></thead>";
  const BODY = "<tbody><tr><td> 1 </td><td> 2 </td></tr></tbody></table>";

  const cases: { name: string; input: string; expected: string }[] = [
    { name: "a real table", input: "| a | b |\n| - | - |\n| 1 | 2 |", expected: HEAD + BODY },
    {
      name: "a trailing space on the header row",
      input: "| a | b | \n| - | - |\n| 1 | 2 |",
      expected: HEAD + BODY,
    },
    {
      name: "a trailing tab on the header row",
      input: "| a | b |\t\n| - | - |\n| 1 | 2 |",
      expected: HEAD + BODY,
    },
    {
      name: "two trailing spaces on the header row",
      input: "| a | b |  \n| - | - |\n| 1 | 2 |",
      expected: HEAD + BODY,
    },
    {
      name: "a trailing space on a body row",
      input: "| a |\n| - |\n| 1 | \n| 2 |",
      expected:
        "<table><thead><tr><th> a </th></tr></thead>" +
        "<tbody><tr><td> 1 </td></tr><tr><td> 2 </td></tr></tbody></table>",
    },
    {
      name: "an intentionally empty middle cell",
      input: "| a | | b |\n| - | - | - |\n| 1 | 2 | 3 |",
      expected:
        "<table><thead><tr><th> a </th><th> </th><th> b </th></tr></thead>" +
        "<tbody><tr><td> 1 </td><td> 2 </td><td> 3 </td></tr></tbody></table>",
    },

    // --- an escaped pipe is content, not a cell boundary ---
    {
      // GFM's own way to put a pipe in a cell, and the parser already honours
      // the escape when it splits a row. Counting the header's cells any other
      // way measures the delimiter row against a count it cannot match, which
      // loses the whole table rather than one cell.
      name: "an escaped pipe in the header",
      input: "| a \\| b |\n| - |\n| 1 |",
      expected:
        "<table><thead><tr><th> a | b </th></tr></thead>" +
        "<tbody><tr><td> 1 </td></tr></tbody></table>",
    },
    {
      name: "an escaped pipe in a header and a body cell",
      input: "| cmd \\| grep | note |\n| - | - |\n| ls \\| wc | ok |",
      expected:
        "<table><thead><tr><th> cmd | grep </th><th> note </th></tr></thead>" +
        "<tbody><tr><td> ls | wc </td><td> ok </td></tr></tbody></table>",
    },
    {
      name: "an escaped pipe in a body cell keeps the row's cell count",
      input: "| a | b |\n| - | - |\n| 1 \\| x | 2 |",
      expected: HEAD + "<tbody><tr><td> 1 | x </td><td> 2 </td></tr></tbody></table>",
    },
    {
      // One header cell over a two-cell delimiter row, which the GFM reference
      // reads as a paragraph too.
      name: "an escaped pipe does not add a header cell",
      input: "| a \\| b |\n| - | - |\n| 1 | 2 |",
      expected: "<p>| a | b |<br>| - | - |<br>| 1 | 2 |</p>",
    },
    {
      name: "an escaped pipe closing the header row",
      input: "| a \\| b | c |\n| - | - |\n| 1 | 2 |",
      expected:
        "<table><thead><tr><th> a | b </th><th> c </th></tr></thead>" +
        "<tbody><tr><td> 1 </td><td> 2 </td></tr></tbody></table>",
    },
    {
      // The row handlers open a cell for it, so the count has to as well.
      name: "an empty header cell is still a cell",
      input: "||\n| - |\n| 1 |",
      expected:
        "<table><thead><tr><th></th></tr></thead>" + "<tbody><tr><td> 1 </td></tr></tbody></table>",
    },

    // --- the delimiter row is required ---
    {
      name: "a leading pipe in prose is not a table",
      input: "| leading pipe in prose",
      expected: "<p>| leading pipe in prose</p>",
    },
    { name: "a lone pipe is not a table", input: "|", expected: "<p>|</p>" },
    {
      name: "a pipe line with no delimiter row is not a table",
      input: "| a | b |\nnot a table",
      expected: "<p>| a | b |<br>not a table</p>",
    },
    {
      name: "a delimiter row with the wrong cell count is not a delimiter row",
      input: "| a | b |\n| - |\n| 1 | 2 |",
      expected: "<p>| a | b |<br>| - |<br>| 1 | 2 |</p>",
    },
    {
      name: "a pipeless line cannot match a two-cell header",
      input: "| a | b |\n---\n| 1 | 2 |",
      expected: "<p>| a | b |</p><hr><p>| 1 | 2 |</p>",
    },
    {
      // A pipe is not required of a delimiter row, so this is a table — which is
      // what the GFM reference renders for it too.
      name: "a single-column delimiter row needs no pipe",
      input: "| a |\n---\n| 1 |",
      expected:
        "<table><thead><tr><th> a </th></tr></thead>" +
        "<tbody><tr><td> 1 </td></tr></tbody></table>",
    },
    {
      name: "a table starting on the second line of a rejected candidate still opens",
      input: "| a | b |\n| x |\n| - |\n| 1 |",
      expected:
        "<p>| a | b |</p><table><thead><tr><th> x </th></tr></thead>" +
        "<tbody><tr><td> 1 </td></tr></tbody></table>",
    },
    {
      name: "colon forms are valid delimiter cells",
      input: "| a |\n| :- |\n| 1 |",
      expected:
        '<table><thead><tr><th style="text-align:left"> a </th></tr></thead>' +
        '<tbody><tr><td style="text-align:left"> 1 </td></tr></tbody></table>',
    },
    {
      // A tab is whitespace inside a delimiter cell, so each of these is a table
      // for the GFM reference too. The character gate that keeps a candidate
      // alive has to admit every character the validity test accepts, or the
      // whole table is lost rather than merely mis-aligned.
      name: "tabs around a delimiter cell",
      input: "| a |\n|\t-\t|\n| 1 |",
      expected:
        "<table><thead><tr><th> a </th></tr></thead>" +
        "<tbody><tr><td> 1 </td></tr></tbody></table>",
    },
    {
      name: "a tab inside a two-column delimiter row",
      input: "| a | b |\n|\t- | - |\n| 1 | 2 |",
      expected: HEAD + BODY,
    },
    {
      name: "a trailing tab on the delimiter row",
      input: "| a | b |\n| - | - |\t\n| 1 | 2 |",
      expected: HEAD + BODY,
    },
    {
      name: "a tab does not stop alignment being read",
      input: "| a |\n| :-\t|\n| 1 |",
      expected:
        '<table><thead><tr><th style="text-align:left"> a </th></tr></thead>' +
        '<tbody><tr><td style="text-align:left"> 1 </td></tr></tbody></table>',
    },
    {
      name: "a header-only table has no tbody",
      input: "| h |\n| - |",
      expected: "<table><thead><tr><th> h </th></tr></thead></table>",
    },
    { name: "an inline pipe is not a table", input: "a | b", expected: "<p>a | b</p>" },
    {
      name: "a lone pipe line after a table is not a second table",
      input: "| a |\n| - |\n| 1 |\n\n| 2 |",
      expected:
        "<table><thead><tr><th> a </th></tr></thead>" +
        "<tbody><tr><td> 1 </td></tr></tbody></table><p>| 2 |</p>",
    },
    {
      name: "a table still interrupts a paragraph",
      input: "text\n| a | b |\n| - | - |\n| 1 | 2 |",
      expected: "<p>text</p>" + HEAD + BODY,
    },
    {
      name: "an indented table still parses",
      input: "   | a | b |\n   | - | - |\n| 1 | 2 |",
      expected: HEAD + BODY,
    },

    // --- alignment ---
    {
      name: "all three alignments",
      input: "| a | b | c |\n|:--|:-:|--:|\n| 1 | 2 | 3 |",
      expected:
        '<table><thead><tr><th style="text-align:left"> a </th>' +
        '<th style="text-align:center"> b </th><th style="text-align:right"> c </th></tr></thead>' +
        '<tbody><tr><td style="text-align:left"> 1 </td>' +
        '<td style="text-align:center"> 2 </td><td style="text-align:right"> 3 </td></tr></tbody></table>',
    },
    {
      name: "a colon-free delimiter row asks for no alignment",
      input: "| a | b |\n| --- | --- |\n| 1 | 2 |",
      expected: HEAD + BODY,
    },
    {
      name: "alignment is per column",
      input: "| a | b |\n|:--| --- |\n| 1 | 2 |",
      expected:
        '<table><thead><tr><th style="text-align:left"> a </th><th> b </th></tr></thead>' +
        '<tbody><tr><td style="text-align:left"> 1 </td><td> 2 </td></tr></tbody></table>',
    },
    {
      name: "a short body row keeps its own columns' alignment",
      input: "| a | b | c |\n|:--|:-:|--:|\n| 1 |",
      expected:
        '<table><thead><tr><th style="text-align:left"> a </th>' +
        '<th style="text-align:center"> b </th><th style="text-align:right"> c </th></tr></thead>' +
        '<tbody><tr><td style="text-align:left"> 1 </td></tr></tbody></table>',
    },

    // --- cells are split before their content is parsed ---
    {
      name: "an unclosed run does not swallow the cell delimiters",
      input: "| a **b | c |\n| - | - |\n| 1 | 2 |",
      expected: "<table><thead><tr><th> a **b </th><th> c </th></tr></thead>" + BODY,
    },
    {
      name: "an intraword underscore in a cell stays literal",
      input: "\n| a_b | c |\n|---|---|\n| d_e | f |\n",
      expected:
        "<table><thead><tr><th> a_b </th><th> c </th></tr></thead>" +
        "<tbody><tr><td> d_e </td><td> f </td></tr></tbody></table>",
    },
    {
      // Not GFM (which nests the table in the blockquote) and not what this used
      // to be either, which was three nested blockquotes each holding a
      // header-only table. Routing a block prefix through a held row is upstream
      // #42 and is not attempted here; every character survives.
      name: "a pipe line inside a blockquote is text (characterization)",
      input: "> | a | b |\n> | - | - |\n> | 1 | 2 |",
      expected: "<blockquote><p>| a | b |<br>| - | - |<br>| 1 | 2 |</p></blockquote>",
    },
    {
      // The reference reads the lone `-` as a bullet and the header as a
      // paragraph. Matching it needs setext headings, which this parser does not
      // have, so the rule is left off rather than half applied.
      name: "a bullet-marker delimiter row still opens a table (characterization)",
      input: "| a | b |\n- | -\n| 1 | 2 |",
      expected: HEAD + BODY,
    },
    {
      // GFM pads a short row and truncates a long one. Truncating deletes text,
      // which nothing else in this parser does, so rows keep their own cells.
      name: "a body row is not normalised to the header's cell count (characterization)",
      input: "| a | b |\n| - | - |\n| 1 |\n| 1 | 2 | 3 |",
      expected:
        HEAD +
        "<tbody><tr><td> 1 </td></tr>" +
        "<tr><td> 1 </td><td> 2 </td><td> 3 </td></tr></tbody></table>",
    },
  ];

  it.each(cases)("$name", ({ input, expected }) => {
    expect(renderMarkdown(input)).toBe(expected);
  });

  it("never paints the header row as paragraph text while streaming", () => {
    const el = document.createElement("div");
    const r = createMarkdownStream(el, { flushIntervalMs: 0 });
    r.writeDelta("| a | b |\n");
    expect(el.querySelector("table")).toBeNull();
    expect(el.textContent).toBe("");
    r.writeDelta("| - | - |\n");
    expect(el.querySelector("table")).not.toBeNull();
    r.writeDelta("| 1 | 2 |\n");
    r.end();
    expect(el.querySelectorAll("tbody tr")).toHaveLength(1);
  });

  it("falls back to a paragraph when the stream ends on the header row", () => {
    const el = document.createElement("div");
    const r = createMarkdownStream(el, { flushIntervalMs: 0 });
    r.writeDelta("| a | b |");
    expect(el.querySelector("table")).toBeNull();
    r.end();
    expect(el.querySelector("table")).toBeNull();
    expect(el.querySelectorAll("p")).toHaveLength(1);
    expect(el.textContent).toBe("| a | b |");
  });
});
