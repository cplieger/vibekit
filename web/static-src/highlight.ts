// ---------------------------------------------------------------------------
// Minimal syntax highlighter: tokenizes source code into spans.
// No dependencies. Covers Go, TS/JS, Python, Shell, YAML, JSON, CSS, HTML,
// Dockerfile, TOML. Language detected from file extension.
// ---------------------------------------------------------------------------

import { escText } from "./strings.js";
import { KNOWN_EXTENSIONS, extToLang } from "./file-extensions.js";

// Unified language alias map: maps fenced-code tags to internal language keys.
// Extension-based lookups delegate to KNOWN_EXTENSIONS via extToLang().
const FENCED_ALIASES: Readonly<Record<string, string>> = {
  javascript: "js",
  typescript: "ts",
  python: "py",
  shell: "sh",
  rust: "rs",
  ruby: "rb",
  cplusplus: "c",
  "c++": "c",
};

const KEYWORDS: Readonly<Record<string, Set<string>>> = {
  go: new Set([
    "package",
    "import",
    "func",
    "var",
    "const",
    "type",
    "struct",
    "interface",
    "map",
    "chan",
    "go",
    "defer",
    "return",
    "if",
    "else",
    "for",
    "range",
    "switch",
    "case",
    "default",
    "select",
    "break",
    "continue",
    "fallthrough",
    "nil",
    "true",
    "false",
    "iota",
    "make",
    "new",
    "append",
    "len",
    "cap",
    "delete",
    "copy",
    "close",
    "panic",
    "recover",
    "error",
  ]),
  ts: new Set([
    "import",
    "export",
    "from",
    "const",
    "let",
    "var",
    "function",
    "class",
    "interface",
    "type",
    "enum",
    "return",
    "if",
    "else",
    "for",
    "while",
    "do",
    "switch",
    "case",
    "default",
    "break",
    "continue",
    "new",
    "this",
    "super",
    "extends",
    "implements",
    "async",
    "await",
    "yield",
    "throw",
    "try",
    "catch",
    "finally",
    "typeof",
    "instanceof",
    "in",
    "of",
    "void",
    "null",
    "undefined",
    "true",
    "false",
    "as",
    "is",
    "readonly",
    "abstract",
    "static",
    "private",
    "protected",
    "public",
    "declare",
    "module",
    "namespace",
  ]),
  js: new Set([
    "import",
    "export",
    "from",
    "const",
    "let",
    "var",
    "function",
    "class",
    "return",
    "if",
    "else",
    "for",
    "while",
    "do",
    "switch",
    "case",
    "default",
    "break",
    "continue",
    "new",
    "this",
    "super",
    "extends",
    "async",
    "await",
    "yield",
    "throw",
    "try",
    "catch",
    "finally",
    "typeof",
    "instanceof",
    "in",
    "of",
    "void",
    "null",
    "undefined",
    "true",
    "false",
    "delete",
  ]),
  py: new Set([
    "import",
    "from",
    "def",
    "class",
    "return",
    "if",
    "elif",
    "else",
    "for",
    "while",
    "break",
    "continue",
    "pass",
    "raise",
    "try",
    "except",
    "finally",
    "with",
    "as",
    "yield",
    "lambda",
    "and",
    "or",
    "not",
    "in",
    "is",
    "None",
    "True",
    "False",
    "global",
    "nonlocal",
    "del",
    "assert",
    "async",
    "await",
  ]),
  sh: new Set([
    "if",
    "then",
    "else",
    "elif",
    "fi",
    "for",
    "while",
    "do",
    "done",
    "case",
    "esac",
    "in",
    "function",
    "return",
    "exit",
    "local",
    "export",
    "readonly",
    "unset",
    "shift",
    "set",
    "true",
    "false",
    "source",
  ]),
  rs: new Set([
    "fn",
    "let",
    "mut",
    "const",
    "struct",
    "enum",
    "impl",
    "trait",
    "pub",
    "use",
    "mod",
    "crate",
    "self",
    "super",
    "return",
    "if",
    "else",
    "for",
    "while",
    "loop",
    "match",
    "break",
    "continue",
    "as",
    "in",
    "ref",
    "move",
    "async",
    "await",
    "unsafe",
    "where",
    "type",
    "true",
    "false",
    "None",
    "Some",
    "Ok",
    "Err",
  ]),
};

// Shared keywords for languages without specific sets
const GENERIC_KW = new Set([
  "if",
  "else",
  "for",
  "while",
  "return",
  "function",
  "class",
  "import",
  "export",
  "const",
  "let",
  "var",
  "true",
  "false",
  "null",
  "undefined",
  "new",
  "this",
]);

// Character classification helpers using charCode for hot-path performance.
function isDigitCode(c: number): boolean {
  return c >= 48 && c <= 57; // '0'-'9'
}

function isIdentStartCode(c: number): boolean {
  return (c >= 65 && c <= 90) || (c >= 97 && c <= 122) || c === 95 || c === 36; // A-Z, a-z, _, $
}

function isIdentCharCode(c: number): boolean {
  return (
    (c >= 65 && c <= 90) || (c >= 97 && c <= 122) || (c >= 48 && c <= 57) || c === 95 || c === 36
  );
}

const PUNCT_CODES = new Set<number>([
  123, 125, 40, 41, 91, 93, 59, 44, 46, 58, 33, 38, 124, 60, 62, 61, 43, 45, 42, 47, 37, 94, 126,
  63, 64,
  // { } ( ) [ ] ; , . : ! & | < > = + - * / % ^ ~ ? @
]);

function isPunctCode(c: number): boolean {
  return PUNCT_CODES.has(c);
}

function isTokenBoundaryCode(c: number): boolean {
  return (
    isIdentStartCode(c) ||
    isDigitCode(c) ||
    c === 34 ||
    c === 39 ||
    c === 96 ||
    c === 35 ||
    c === 47 ||
    isPunctCode(c)
  );
}

interface Token {
  type: "keyword" | "string" | "comment" | "number" | "punctuation" | "text";
  value: string;
}

function tokenize(code: string, lang: string): Token[] {
  const tokens: Token[] = [];
  const kw = KEYWORDS[lang] ?? GENERIC_KW;
  let i = 0;
  const len = code.length;

  while (i < len) {
    const cc = code.charCodeAt(i);

    // Line comments
    if (cc === 47 /* / */ && code.charCodeAt(i + 1) === 47) {
      const end = code.indexOf("\n", i);
      const slice = end === -1 ? code.substring(i) : code.substring(i, end);
      tokens.push({ type: "comment", value: slice });
      i += slice.length;
      continue;
    }
    // Block comments
    if (cc === 47 /* / */ && code.charCodeAt(i + 1) === 42 /* * */) {
      const end = code.indexOf("*/", i + 2);
      const slice = end === -1 ? code.substring(i) : code.substring(i, end + 2);
      tokens.push({ type: "comment", value: slice });
      i += slice.length;
      continue;
    }
    // Hash comments (Python, Shell, YAML, TOML)
    if (
      cc === 35 /* # */ &&
      (lang === "py" || lang === "sh" || lang === "yaml" || lang === "toml" || lang === "docker")
    ) {
      const end = code.indexOf("\n", i);
      const slice = end === -1 ? code.substring(i) : code.substring(i, end);
      tokens.push({ type: "comment", value: slice });
      i += slice.length;
      continue;
    }

    // Strings
    if (cc === 34 || cc === 39 || cc === 96) {
      // " ' `
      let j = i + 1;
      if (cc === 96) {
        while (j < len && code.charCodeAt(j) !== 96) {
          j++;
        }
        if (j < len) {
          j++;
        }
      } else {
        while (j < len && code.charCodeAt(j) !== cc) {
          if (code.charCodeAt(j) === 92 /* \\ */) {
            j++;
          }
          j++;
        }
        if (j < len) {
          j++;
        }
      }
      tokens.push({ type: "string", value: code.substring(i, j) });
      i = j;
      continue;
    }

    // Numbers
    if (
      isDigitCode(cc) ||
      (cc === 46 /* . */ && i + 1 < len && isDigitCode(code.charCodeAt(i + 1)))
    ) {
      let j = i;
      if (cc === 48 /* 0 */ && j + 1 < len && /[xXoObB]/.test(code[j + 1]!)) {
         
        j += 2;
      }
      while (j < len && /[\d.a-fA-F_eE+-]/.test(code[j]!)) {
         
        j++;
      }
      tokens.push({ type: "number", value: code.substring(i, j) });
      i = j;
      continue;
    }

    // Identifiers / keywords
    if (isIdentStartCode(cc)) {
      let j = i;
      while (j < len && isIdentCharCode(code.charCodeAt(j))) {
        j++;
      }
      const word = code.substring(i, j);
      tokens.push({ type: kw.has(word) ? "keyword" : "text", value: word });
      i = j;
      continue;
    }

    // Punctuation
    if (isPunctCode(cc)) {
      tokens.push({ type: "punctuation", value: code[i]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
      i++;
      continue;
    }

    // Whitespace and other
    let j = i;
    while (j < len && !isTokenBoundaryCode(code.charCodeAt(j))) {
      j++;
    }
    if (j === i) {
      j = i + 1;
    }
    tokens.push({ type: "text", value: code.substring(i, j) });
    i = j;
  }

  return tokens;
}

/** Highlight source code by language key, returning HTML with span-
 *  wrapped tokens. Unknown or empty lang returns plain-escaped text so
 *  callers can safely pass through. */
export function highlightByLang(code: string, lang: string): string {
  if (lang === "" || lang === "md") {
    return escText(code);
  }
  if (KEYWORDS[lang] === undefined && !(lang in KNOWN_EXTENSIONS) && !(lang in FENCED_ALIASES)) {
    // Unknown language; pass through as plain text.
    return escText(code);
  }
  const tokens = tokenize(code, lang);
  let html = "";
  for (const t of tokens) {
    if (t.type === "text") {
      html += escText(t.value);
    } else {
      html += `<span class="hl-${t.type}">${escText(t.value)}</span>`;
    }
  }
  return html;
}

/** Highlight source code by filename (extension-based language detection). */
export function highlight(code: string, filename: string): string {
  const ext = filename.split(".").pop()?.toLowerCase() ?? "";
  return highlightByLang(code, extToLang(ext));
}

/** Detect language from filename extension. */
export function detectLang(filename: string): string {
  const ext = filename.split(".").pop()?.toLowerCase() ?? "";
  return extToLang(ext);
}

/** Normalize a fenced-code language tag (e.g. "go", "javascript", "bash")
 *  to our internal key. Languages not recognized return "" so the caller
 *  can skip highlighting. */
export function normalizeLang(tag: string): string {
  const s = tag.trim().toLowerCase();
  if (s === "") {
    return "";
  }
  // Direct hits on our internal set.
  if (KEYWORDS[s] !== undefined) {
    return s;
  }
  // Lookup from file-extensions registry (covers extensions like "go", "ts", etc.).
  const fromExt = extToLang(s);
  if (fromExt !== "") {
    return fromExt;
  }
  // Fenced-code aliases (covers "javascript", "typescript", "python", etc.).
  return FENCED_ALIASES[s] ?? "";
}
