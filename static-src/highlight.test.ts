// Table-driven tests for highlight.ts tokenizer across all supported languages.
import { describe, it, expect } from "vitest";
import {
  highlightByLang,
  highlight,
  detectLang,
  normalizeLang,
  resolveLangHint,
  highlightMarked,
} from "./highlight.js";

// ---------------------------------------------------------------------------
// Helper: extract spans from highlightByLang output.
// The tokenizer emits plain escaped text for "text" tokens and wraps all
// others in <span class="hl-TYPE">VALUE</span>. We extract the spans.
// ---------------------------------------------------------------------------
interface Span {
  type: "keyword" | "string" | "comment" | "number" | "punctuation";
  value: string;
}

function extractSpans(html: string): Span[] {
  const spans: Span[] = [];
  const re = /<span class="hl-(\w+)">([^<]*)<\/span>/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(html)) !== null) {
    spans.push({
      type: m[1] as Span["type"],
      value: m[2]!
        .replace(/&amp;/g, "&")
        .replace(/&lt;/g, "<")
        .replace(/&gt;/g, ">")
        .replace(/&quot;/g, '"'),
    });
  }
  return spans;
}

// ---------------------------------------------------------------------------
// Table-driven: one row per language with representative snippet
// ---------------------------------------------------------------------------
describe("tokenize per language", () => {
  const cases: {
    lang: string;
    input: string;
    expectSpans: Span[];
    desc: string;
  }[] = [
    {
      lang: "go",
      input: `func main() { return 42 }`,
      expectSpans: [
        { type: "keyword", value: "func" },
        { type: "punctuation", value: "(" },
        { type: "punctuation", value: ")" },
        { type: "punctuation", value: "{" },
        { type: "keyword", value: "return" },
        { type: "number", value: "42" },
        { type: "punctuation", value: "}" },
      ],
      desc: "Go: keywords, number, punctuation",
    },
    {
      lang: "ts",
      input: `const x: string = "hello";`,
      expectSpans: [
        { type: "keyword", value: "const" },
        { type: "punctuation", value: ":" },
        { type: "punctuation", value: "=" },
        { type: "string", value: '"hello"' },
        { type: "punctuation", value: ";" },
      ],
      desc: "TypeScript: const keyword, string literal, punctuation",
    },
    {
      lang: "js",
      input: `let n = 3.14; // pi`,
      expectSpans: [
        { type: "keyword", value: "let" },
        { type: "punctuation", value: "=" },
        { type: "number", value: "3.14" },
        { type: "punctuation", value: ";" },
        { type: "comment", value: "// pi" },
      ],
      desc: "JavaScript: let keyword, number, line comment",
    },
    {
      lang: "py",
      input: `def foo(): # comment`,
      expectSpans: [
        { type: "keyword", value: "def" },
        { type: "punctuation", value: "(" },
        { type: "punctuation", value: ")" },
        { type: "punctuation", value: ":" },
        { type: "comment", value: "# comment" },
      ],
      desc: "Python: def keyword, hash comment",
    },
    {
      lang: "sh",
      input: `export PATH="/usr/bin"`,
      expectSpans: [
        { type: "keyword", value: "export" },
        { type: "punctuation", value: "=" },
        { type: "string", value: '"/usr/bin"' },
      ],
      desc: "Shell: export keyword, equals, string",
    },
    {
      lang: "yaml",
      input: `key: "value" # comment`,
      expectSpans: [
        { type: "punctuation", value: ":" },
        { type: "string", value: '"value"' },
        { type: "comment", value: "# comment" },
      ],
      desc: "YAML: colon punctuation, string, hash comment",
    },
    {
      lang: "json",
      input: `{"count": 10}`,
      expectSpans: [
        { type: "punctuation", value: "{" },
        { type: "string", value: '"count"' },
        { type: "punctuation", value: ":" },
        { type: "number", value: "10" },
        { type: "punctuation", value: "}" },
      ],
      desc: "JSON: braces, string key, number value",
    },
    {
      lang: "css",
      input: `.cls { color: red; }`,
      expectSpans: [
        { type: "punctuation", value: "." },
        { type: "punctuation", value: "{" },
        { type: "punctuation", value: ":" },
        { type: "punctuation", value: ";" },
        { type: "punctuation", value: "}" },
      ],
      desc: "CSS: dot, braces, colon, semicolon",
    },
    {
      lang: "docker",
      input: `# install deps\nRUN npm ci`,
      expectSpans: [
        { type: "comment", value: "# install deps" },
        { type: "keyword", value: "RUN" },
      ],
      desc: "Dockerfile: hash comment and an instruction keyword",
    },
    {
      lang: "toml",
      input: `# config\nport = 8080`,
      expectSpans: [
        { type: "comment", value: "# config" },
        { type: "punctuation", value: "=" },
        { type: "number", value: "8080" },
      ],
      desc: "TOML: hash comment, equals, number",
    },
    {
      lang: "rs",
      input: `fn main() { let x = 5; }`,
      expectSpans: [
        { type: "keyword", value: "fn" },
        { type: "punctuation", value: "(" },
        { type: "punctuation", value: ")" },
        { type: "punctuation", value: "{" },
        { type: "keyword", value: "let" },
        { type: "punctuation", value: "=" },
        { type: "number", value: "5" },
        { type: "punctuation", value: ";" },
        { type: "punctuation", value: "}" },
      ],
      desc: "Rust: fn/let keywords, number, punctuation",
    },
  ];

  it.each(cases)("$desc", ({ lang, input, expectSpans }) => {
    const html = highlightByLang(input, lang);
    const spans = extractSpans(html);
    expect(spans).toEqual(expectSpans);
  });
});

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------
describe("edge cases", () => {
  it("python: backtick is not a string delimiter", () => {
    // Regression: a stray backtick used to open a "string" that swallowed
    // everything up to the next backtick, mis-highlighting whole blocks.
    const html = highlightByLang("x = 1  # a `word` in a comment\ny = 2", "py");
    const spans = extractSpans(html);
    expect(spans.filter((s) => s.type === "string")).toEqual([]);
    // The line after the backtick still tokenizes normally.
    expect(spans.some((s) => s.type === "number" && s.value === "2")).toBe(true);
  });

  it("go: backtick raw string still highlights", () => {
    const html = highlightByLang("s := `raw`", "go");
    const spans = extractSpans(html);
    expect(spans.some((s) => s.type === "string" && s.value === "`raw`")).toBe(true);
  });

  it("arithmetic: 1-2 is two number tokens, not one", () => {
    // Regression: the number loop consumed +/- unconditionally, merging
    // `1-2` into a single "number" token.
    const html = highlightByLang("1-2", "go");
    const spans = extractSpans(html);
    const numbers = spans.filter((s) => s.type === "number").map((s) => s.value);
    expect(numbers).toEqual(["1", "2"]);
  });

  it("exponent signs still bind to the number", () => {
    const html = highlightByLang("x = 1e-5 + 2E+3", "go");
    const spans = extractSpans(html);
    const numbers = spans.filter((s) => s.type === "number").map((s) => s.value);
    expect(numbers).toEqual(["1e-5", "2E+3"]);
  });

  it("empty input returns empty string", () => {
    expect(highlightByLang("", "go")).toBe("");
  });

  it("unknown language returns escaped plain text", () => {
    const input = '<script>alert("xss")</script>';
    const result = highlightByLang(input, "unknownlang");
    expect(result).not.toContain("<script>");
    expect(result).toContain("&lt;script&gt;");
  });

  it("backtick strings in JS are tokenized as string", () => {
    const html = highlightByLang("`hello ${world}`", "js");
    const spans = extractSpans(html);
    expect(spans).toContainEqual({ type: "string", value: "`hello ${world}`" });
  });

  it("backtick strings in TS are tokenized as string", () => {
    const html = highlightByLang("`template`", "ts");
    const spans = extractSpans(html);
    expect(spans).toContainEqual({ type: "string", value: "`template`" });
  });

  it("block comment is tokenized correctly", () => {
    const html = highlightByLang("/* block */", "go");
    const spans = extractSpans(html);
    expect(spans[0]).toEqual({ type: "comment", value: "/* block */" });
  });

  it("hex number literal", () => {
    const html = highlightByLang("0xFF", "go");
    const spans = extractSpans(html);
    expect(spans[0]).toEqual({ type: "number", value: "0xFF" });
  });

  it("binary number literal", () => {
    const html = highlightByLang("0b1010", "rs");
    const spans = extractSpans(html);
    expect(spans[0]).toEqual({ type: "number", value: "0b1010" });
  });

  it("octal number literal", () => {
    const html = highlightByLang("0o777", "py");
    const spans = extractSpans(html);
    expect(spans[0]).toEqual({ type: "number", value: "0o777" });
  });

  it("escaped quotes in strings", () => {
    const html = highlightByLang(String.raw`"hello \"world\""`, "go");
    const spans = extractSpans(html);
    expect(spans[0]).toEqual({ type: "string", value: String.raw`"hello \"world\""` });
  });

  it("single-quoted string", () => {
    const html = highlightByLang("'hello'", "py");
    const spans = extractSpans(html);
    expect(spans[0]).toEqual({ type: "string", value: "'hello'" });
  });

  it("empty string lang returns escaped text (markdown passthrough)", () => {
    const result = highlightByLang("# heading", "md");
    // md is treated as passthrough — no spans
    expect(result).not.toContain("<span");
    expect(result).toContain("# heading");
  });
});

// ---------------------------------------------------------------------------
// detectLang
// ---------------------------------------------------------------------------
describe("detectLang", () => {
  it.each([
    ["main.go", "go"],
    ["app.ts", "ts"],
    ["app.tsx", "ts"],
    ["index.js", "js"],
    ["index.jsx", "js"],
    ["index.mjs", "js"],
    ["script.py", "py"],
    ["run.sh", "sh"],
    ["config.yaml", "yaml"],
    ["config.yml", "yaml"],
    ["data.json", "json"],
    ["Dockerfile", "docker"],
    ["style.css", "css"],
    ["page.html", "html"],
    ["page.htm", "html"],
    ["config.toml", "toml"],
    ["lib.rs", "rs"],
    ["main.c", "c"],
    ["main.cpp", "c"],
    ["App.java", "java"],
    ["script.rb", "rb"],
    ["index.php", "php"],
    ["query.sql", "sql"],
    ["unknown.xyz", ""],
  ])("detectLang(%s) = %s", (filename, expected) => {
    expect(detectLang(filename)).toBe(expected);
  });
});

// ---------------------------------------------------------------------------
// normalizeLang
// ---------------------------------------------------------------------------
describe("normalizeLang", () => {
  it.each([
    ["go", "go"],
    ["javascript", "js"],
    ["typescript", "ts"],
    ["python", "py"],
    ["shell", "sh"],
    ["bash", "sh"],
    ["rust", "rs"],
    ["ruby", "rb"],
    ["c++", "c"],
    ["cplusplus", "c"],
    // "docker" is the only language key that is not also its own extension, so
    // it is the one tag that has to be found in SUPPORTED_LANGUAGES itself.
    ["docker", "docker"],
    // The fenced tag, which resolves through the extension registry instead.
    ["dockerfile", "docker"],
    ["", ""],
    ["  Go  ", "go"],
    ["nonexistent", ""],
  ])("normalizeLang(%s) = %s", (tag, expected) => {
    expect(normalizeLang(tag)).toBe(expected);
  });
});

// ---------------------------------------------------------------------------
// Property-based fuzz: no-throw, HTML-safety, content-preservation (tarch-b15-c7-p1)
// ---------------------------------------------------------------------------
import fc from "fast-check";

describe("highlightByLang property-based fuzz", () => {
  const allLangs = [
    "go",
    "ts",
    "js",
    "py",
    "sh",
    "rs",
    "rb",
    "c",
    "yaml",
    "json",
    "css",
    "html",
    "docker",
    "toml",
    "",
  ];

  it("never throws for any (input, lang) pair", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.string(), fc.constantFrom(...allLangs), (input, lang) => {
        highlightByLang(input, lang);
        return true;
      }),
      { numRuns: 500 },
    );
    expect(result.failed).toBe(false);
  });

  it("output never contains unescaped < or > outside of hl-* spans", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.string(), fc.constantFrom(...allLangs), (input, lang) => {
        const out = highlightByLang(input, lang);
        const stripped = out.replace(/<span class="hl-\w+">/g, "").replace(/<\/span>/g, "");
        return !stripped.includes("<") && !stripped.includes(">");
      }),
      { numRuns: 500 },
    );
    expect(result.failed).toBe(false);
  });

  it("stripping spans and unescaping recovers original input", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.string(), fc.constantFrom(...allLangs), (input, lang) => {
        const out = highlightByLang(input, lang);
        const text = out
          .replace(/<span class="hl-\w+">/g, "")
          .replace(/<\/span>/g, "")
          .replace(/&amp;/g, "&")
          .replace(/&lt;/g, "<")
          .replace(/&gt;/g, ">")
          .replace(/&quot;/g, '"');
        return text === input;
      }),
      { numRuns: 500 },
    );
    expect(result.failed).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// Where a token STOPS, which extractSpans cannot see: it drops text tokens, so
// two adjacent text tokens and one merged token look identical through it. These
// assert the whole rendered string instead.
// ---------------------------------------------------------------------------
describe("token boundaries", () => {
  const cases: { input: string; lang: string; expected: string; desc: string }[] = [
    {
      input: "9",
      lang: "go",
      expected: '<span class="hl-number">9</span>',
      desc: "a bare 9 is a number (the top of the digit range)",
    },
    {
      input: "A1",
      lang: "go",
      expected: "A1",
      desc: "an identifier may start with A",
    },
    {
      input: "Z1",
      lang: "go",
      expected: "Z1",
      desc: "an identifier may start with Z",
    },
    {
      input: "a1",
      lang: "go",
      expected: "a1",
      desc: "an identifier may start with a",
    },
    {
      input: "z1",
      lang: "go",
      expected: "z1",
      desc: "an identifier may start with z",
    },
    {
      input: "_1",
      lang: "go",
      expected: "_1",
      desc: "an identifier may start with an underscore",
    },
    {
      input: "$1",
      lang: "go",
      expected: "$1",
      desc: "an identifier may start with a dollar",
    },
    {
      input: "x0",
      lang: "go",
      expected: "x0",
      desc: "a trailing 0 belongs to the identifier, not to a number of its own",
    },
    {
      input: "x9",
      lang: "go",
      expected: "x9",
      desc: "a trailing 9 belongs to the identifier",
    },
    {
      input: "x{}",
      lang: "go",
      expected: 'x<span class="hl-punctuation">{</span><span class="hl-punctuation">}</span>',
      desc: "a brace ends an identifier and is punctuation",
    },
    {
      input: "#if",
      lang: "go",
      expected: '#<span class="hl-keyword">if</span>',
      desc: "a character the language does not use does not swallow the token after it",
    },
    {
      input: " 'a'",
      lang: "py",
      expected: " <span class=\"hl-string\">'a'</span>",
      desc: "an indented string literal is still a string",
    },
  ];

  it.each(cases)("$desc", ({ input, lang, expected }) => {
    expect(highlightByLang(input, lang)).toBe(expected);
  });
});

describe("keyword tables", () => {
  // The only capitalised keywords in the tables, and the reason the ident-start
  // range has to cover A-Z at all.
  it("highlights Python's capitalised literals", () => {
    const spans = extractSpans(highlightByLang("None", "py"));
    expect(spans).toEqual([{ type: "keyword", value: "None" }]);
  });

  it("highlights Rust's capitalised result constructors", () => {
    const spans = extractSpans(highlightByLang("Err", "rs"));
    expect(spans).toEqual([{ type: "keyword", value: "Err" }]);
  });
});

describe("comment delimiters", () => {
  it("treats a lone slash as punctuation, not a comment", () => {
    expect(highlightByLang("a / b", "go")).toBe('a <span class="hl-punctuation">/</span> b');
  });

  it("treats a star between operands as punctuation, not a comment", () => {
    expect(highlightByLang("a*b", "go")).toBe('a<span class="hl-punctuation">*</span>b');
  });

  it("ends a line comment at the newline", () => {
    expect(highlightByLang("// c\nx", "go")).toBe('<span class="hl-comment">// c</span>\nx');
  });

  it("ends a block comment at its terminator", () => {
    expect(highlightByLang("x /* c */ 1", "go")).toBe(
      'x <span class="hl-comment">/* c */</span> <span class="hl-number">1</span>',
    );
  });

  it("runs an unterminated block comment to the end of the input", () => {
    expect(highlightByLang("x /* oops", "go")).toBe('x <span class="hl-comment">/* oops</span>');
  });

  // The terminator cannot overlap the opener: `/*/` is an unterminated comment,
  // not an empty one, so the search for `*/` starts past both characters.
  it("does not let a block comment close on its own opener", () => {
    expect(highlightByLang("/*/ x */", "go")).toBe('<span class="hl-comment">/*/ x */</span>');
  });

  it("treats # as a comment in shell", () => {
    expect(highlightByLang("# c", "sh")).toBe('<span class="hl-comment"># c</span>');
  });

  it("does not treat # as a comment in Go", () => {
    expect(highlightByLang("# c", "go")).toBe("# c");
  });
});

describe("string delimiters", () => {
  it("ends a raw string at its closing backtick", () => {
    expect(highlightByLang("`raw` x", "go")).toBe('<span class="hl-string">`raw`</span> x');
  });

  // Go raw strings have no escapes, which is why the backtick scan is separate
  // from the escape-aware one: a backslash before the closing backtick does not
  // extend the string.
  it("does not let a backslash escape a closing backtick", () => {
    expect(highlightByLang("`a\\`b`", "go")).toBe(
      '<span class="hl-string">`a\\`</span>b<span class="hl-string">`</span>',
    );
  });
});

describe("number literals", () => {
  it("reads a leading dot as part of the number", () => {
    expect(highlightByLang(".5", "go")).toBe('<span class="hl-number">.5</span>');
  });

  // The 0x/0o/0b prefix skip belongs to a leading zero only; applying it to any
  // digit merged an identifier into the number before it.
  it("does not apply the radix prefix to a non-zero digit", () => {
    expect(highlightByLang("1x2", "go")).toBe('<span class="hl-number">1</span>x2');
  });

  it("ends a number at whitespace", () => {
    expect(highlightByLang("1e x", "go")).toBe('<span class="hl-number">1e</span> x');
  });
});

describe("language routing", () => {
  // Markdown is prose: tokenizing it would highlight ordinary English words that
  // happen to be keywords.
  it("passes markdown through without highlighting its prose", () => {
    expect(highlightByLang("if you want", "md")).toBe("if you want");
  });

  // highlight() lowercases the extension before the lookup, so a shouted
  // filename still finds its language.
  it("detects the language from an uppercase extension", () => {
    const spans = extractSpans(highlight("func f()", "MAIN.GO"));
    expect(spans).toEqual([
      { type: "keyword", value: "func" },
      { type: "punctuation", value: "(" },
      { type: "punctuation", value: ")" },
    ]);
  });

  // The path the editor takes, and the one that was broken: a Dockerfile has no
  // extension, so `filename.split(".").pop()` yields the whole name, which the
  // extension registry maps to the language key "docker". detectLang returning
  // "docker" proves nothing about what gets rendered — for a long time nothing
  // held that key, the gate in highlightByLang rejected it, and Dockerfiles came
  // out as plain escaped text.
  it("highlights a Dockerfile opened by filename", () => {
    const spans = extractSpans(highlight("# install deps\nRUN npm ci", "Dockerfile"));
    expect(spans).toEqual([
      { type: "comment", value: "# install deps" },
      { type: "keyword", value: "RUN" },
    ]);
  });
});

// ---------------------------------------------------------------------------
// The punctuation table, whole.
//
// `PUNCT_CODES` is built once when the module is evaluated, and the tests above
// reach only the four or five characters their snippets happen to contain — so
// the table's contents are asserted here, one character at a time, against the
// comment that documents it beside the codes.
//
// The module is loaded DYNAMICALLY for the same reason `platform.pwa.test.ts`
// does it: a module-scope initializer runs at import time, which a static import
// has already done before the first test is collected, so a test that means to
// observe the table has to be the thing that builds it.
// ---------------------------------------------------------------------------

import { vi } from "vitest";

describe("the punctuation table", () => {
  // The set's own trailing comment, in order:
  //   { } ( ) [ ] ; , . : ! & | < > = + - * / % ^ ~ ? @
  const PUNCT = [..."{}()[];,.:!&|<>=+-*/%^~?@"];

  it("tokenizes every character it lists as punctuation", async () => {
    vi.resetModules();
    const { highlightByLang: fresh } = await import("./highlight.js");
    for (const ch of PUNCT) {
      expect(extractSpans(fresh(ch, "go"))).toEqual([{ type: "punctuation", value: ch }]);
    }
  });

  it("leaves a character outside the table as plain text", async () => {
    vi.resetModules();
    const { highlightByLang: fresh } = await import("./highlight.js");
    // Neither punctuation nor an identifier start nor a digit nor a quote: the
    // tokenizer's last branch, which is what "not in the table" has to mean.
    for (const ch of [..."\\\u00a7\u00b0"]) {
      expect(extractSpans(fresh(ch, "go"))).toEqual([]);
      expect(fresh(ch, "go")).toBe(ch);
    }
  });
});

// ---------------------------------------------------------------------------
// resolveLangHint: a fence tag, a bare extension, or a file PATH.
//
// The path arm is the one that had never worked. Both diff call sites pass a
// path, `normalizeLang` compares the whole string, so it matched nothing and
// every diff in the app rendered unhighlighted while the code and its comments
// claimed otherwise.
// ---------------------------------------------------------------------------

describe("resolveLangHint", () => {
  const cases: { name: string; tag: string; want: string }[] = [
    { name: "a bare extension", tag: "go", want: "go" },
    { name: "a fence alias", tag: "typescript", want: "ts" },
    { name: "a bare filename", tag: "main.go", want: "go" },
    { name: "a repo-relative path", tag: "internal/git/exec.go", want: "go" },
    { name: "an absolute path", tag: "/workspace/vibekit/static-src/diff.ts", want: "ts" },
    { name: "a dotted path with an unknown extension", tag: "notes/todo.zzz", want: "" },
    { name: "a dotfile with no extension", tag: ".gitignore", want: "" },
    { name: "a path whose directory holds the dot", tag: "v1.2/README", want: "" },
    { name: "an unknown bare word", tag: "nonsense", want: "" },
    { name: "the empty string", tag: "", want: "" },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(resolveLangHint(tc.tag)).toBe(tc.want);
    });
  }
});

// ---------------------------------------------------------------------------
// highlightMarked: two independent channels on one line. A token straddling a
// mark boundary is SPLIT and each piece keeps its syntax class, so the classes
// compose on one element rather than one displacing the other.
// ---------------------------------------------------------------------------

describe("highlightMarked", () => {
  it("is exactly highlightByLang when there is nothing to mark", () => {
    const code = `x := "s" // c`;
    expect(highlightMarked(code, "go", [], "m")).toBe(highlightByLang(code, "go"));
  });

  it("keeps the same syntax classes as the unmarked path", () => {
    // The fast path and the marked walk must produce the same syntax markup, or
    // a line's colours would depend on whether it happened to pair with a
    // counterpart. A split token repeats its class, so runs collapse first.
    const code = `x := "abcd" // c`;
    const hlClasses = (html: string): string[] =>
      Array.from(html.matchAll(/<span class="([^"]*)"/g), (m) =>
        (m[1] ?? "")
          .split(" ")
          .filter((c) => c.startsWith("hl-"))
          .join(" "),
      ).filter((c) => c !== "");
    const collapseRuns = (xs: string[]): string[] => xs.filter((x, i) => x !== xs[i - 1]);
    const marked = highlightMarked(code, "go", [{ start: 6, end: 8 }], "m");
    expect(collapseRuns(hlClasses(marked))).toEqual(
      collapseRuns(hlClasses(highlightByLang(code, "go"))),
    );
  });

  it("carries both classes on one span when a mark covers a whole token", () => {
    // `"s"` is one string token at offsets 5..8.
    const html = highlightMarked(`x := "s"`, "go", [{ start: 5, end: 8 }], "m");
    expect(html).toContain('class="hl-string m"');
  });

  it("splits a token at a mark boundary and keeps its syntax class on both", () => {
    // Mark only `ab` of the 4-character string token `"abcd"` starts at 1.
    const html = highlightMarked(`"abcd"`, "go", [{ start: 1, end: 3 }], "m");
    expect(html).toBe(
      '<span class="hl-string">&quot;</span><span class="hl-string m">ab</span>' +
        '<span class="hl-string">cd&quot;</span>',
    );
  });

  it("marks plain text with the mark class alone", () => {
    expect(highlightMarked("abcd", "go", [{ start: 1, end: 3 }], "m")).toBe(
      'a<span class="m">bc</span>d',
    );
  });

  it("marks an unhighlightable language too, so a mark never depends on lang", () => {
    expect(highlightMarked("abcd", "", [{ start: 0, end: 2 }], "m")).toBe(
      '<span class="m">ab</span>cd',
    );
    expect(highlightMarked("abcd", "md", [{ start: 0, end: 2 }], "m")).toBe(
      '<span class="m">ab</span>cd',
    );
  });

  it("applies several marks in one line", () => {
    const html = highlightMarked(
      "aXbXc",
      "",
      [
        { start: 1, end: 2 },
        { start: 3, end: 4 },
      ],
      "m",
    );
    expect(html).toBe('a<span class="m">X</span>b<span class="m">X</span>c');
  });

  it("escapes marked text, so a mark is never an HTML injection", () => {
    const html = highlightMarked(`<script>`, "", [{ start: 0, end: 8 }], "m");
    expect(html).toBe('<span class="m">&lt;script&gt;</span>');
    expect(html).not.toContain("<script>");
  });

  it("reproduces the input exactly once the tags are stripped", () => {
    const code = `\tname := strings.TrimSpace(os.Getenv("NAME"))`;
    const html = highlightMarked(
      code,
      "go",
      [
        { start: 9, end: 27 },
        { start: 43, end: 44 },
      ],
      "m",
    );
    const text = html
      .replace(/<[^>]*>/g, "")
      .replace(/&lt;/g, "<")
      .replace(/&gt;/g, ">")
      .replace(/&quot;/g, '"')
      .replace(/&#39;/g, "'")
      .replace(/&amp;/g, "&");
    expect(text).toBe(code);
  });

  it("tolerates a mark that runs to the end of the line", () => {
    expect(highlightMarked("abc", "", [{ start: 1, end: 3 }], "m")).toBe(
      'a<span class="m">bc</span>',
    );
  });
});
