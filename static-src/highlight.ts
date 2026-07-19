// ---------------------------------------------------------------------------
// Minimal syntax highlighter: tokenizes source code into spans.
// No dependencies. Language detected from file extension or fence alias.
//
// Three routing tiers (broader than the 10 keyword tables suggest):
//   1. Dedicated keyword tables (KEYWORDS in highlight-langs.ts): Go,
//      TS/JS, Python, Shell, YAML, JSON, CSS, HTML, Dockerfile, TOML.
//   2. Generic tokenizer (GENERIC_KW) for every other recognized
//      extension/alias — strings, numbers, comments, and a common
//      keyword set still highlight.
//   3. Escaped passthrough for unknown languages — plain text, safely
//      HTML-escaped, never mis-tokenized.
// ---------------------------------------------------------------------------

import { escText } from "./strings.js";
import { KNOWN_EXTENSIONS, extToLang } from "./file-extensions.js";
import { SUPPORTED_LANGUAGES, FENCED_ALIASES, KEYWORDS, GENERIC_KW } from "./highlight-langs.js";

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

/** Languages where a backtick opens a string (JS/TS template literals,
 *  Go raw strings). Everywhere else a backtick is punctuation. */
const BACKTICK_STRING_LANGS = new Set(["js", "ts", "go"]);

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

    // Strings. Backtick is a string delimiter only where the language
    // actually has backtick strings (JS/TS templates, Go raw strings);
    // treating it as one everywhere mis-tokenized languages like Python,
    // where a stray backtick swallowed everything up to the next one.
    if (cc === 34 || cc === 39 || (cc === 96 && BACKTICK_STRING_LANGS.has(lang))) {
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
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- defensive check
      if (cc === 48 /* 0 */ && j + 1 < len && /[xXoObB]/.test(code[j + 1]!)) {
        j += 2;
      }
      while (j < len) {
        const ch = code[j]!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
        if (/[\d.a-fA-F_eE]/.test(ch)) {
          j++;
          continue;
        }
        // `+`/`-` continue a number only as an exponent sign (1e-5);
        // consuming them unconditionally merged arithmetic like `1-2`
        // into a single mis-highlighted number token.
        // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- j > i inside the loop
        if ((ch === "+" || ch === "-") && j > i && /[eE]/.test(code[j - 1]!)) {
          j++;
          continue;
        }
        break;
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
  if (SUPPORTED_LANGUAGES.has(lang) || lang in KNOWN_EXTENSIONS || lang in FENCED_ALIASES) {
    const tokens = tokenize(code, lang);
    const parts: string[] = [];
    for (const t of tokens) {
      if (t.type === "text") {
        parts.push(escText(t.value));
      } else {
        parts.push(`<span class="hl-${t.type}">${escText(t.value)}</span>`);
      }
    }
    return parts.join("");
  }
  // Unknown language; pass through as plain text.
  return escText(code);
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
  if (SUPPORTED_LANGUAGES.has(s)) {
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
