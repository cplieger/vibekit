// Table-driven tests for highlight.ts tokenizer across all supported languages.
import { describe, it, expect } from "vitest";
import { highlightByLang, detectLang, normalizeLang } from "./highlight.js";

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
      expectSpans: [],
      desc: "Dockerfile: falls through to plain text (no KEYWORDS entry)",
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
    "dockerfile",
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
