// ---------------------------------------------------------------------------
// YAML front-matter split for the editor's rendered-markdown read mode.
//
// Pure and DOM-free. Mirrors the VALUE syntax of internal/steering/frontmatter.go
// for the subset `.kiro` documents actually use — flat scalars, block scalars
// (`>` / `|` with chomping and indent indicators), quoted strings, and flow
// (`[a, b]`) or block (`- a`) sequences — and nothing else.
//
// It is stricter than the Go parser in exactly one place, on purpose: the closing
// fence must be a line that is exactly `---` (see findCloseFence). The stakes
// differ. A Go misread degrades to empty FIELDS and the row still renders; a
// misread here removes text from the BODY, which is the document itself.
//
// # Why the editor parses instead of asking the server
//
// The values are already parsed per row by GET /api/workspace/kiro-docs, but
// the editor cannot reach them: docs.ts keeps its rows in a module-local array
// and opens a document with `openFile(path)`, which carries a path and nothing
// else. Three of the editor's four entry points never touch docs.ts at all — a
// `/file/{path}` deep link, the boot restore from the server's tab set, and a
// file-browser click — so threading metadata through one of them would leave the
// other three showing a document with no header. Parsing the text the editor has
// already loaded covers every entry point, and covers a `.md` file OUTSIDE
// `.kiro` that happens to carry front-matter.
//
// # Deliberately schema-free
//
// The Go parser resolves seven named keys and defaults `inclusion` to "always"
// because a steering document that declares nothing IS always-loaded. That
// default is a STEERING fact: forwarding it to a skill once badged every skill
// in the docs browser as always-loaded, which was a false claim about token
// cost. The editor is showing one file's own header, so it reports the keys that
// file DECLARED, in the order it declared them, and invents no default. That is
// both simpler than a schema and the only version that cannot lie.
// ---------------------------------------------------------------------------

/** One declared front-matter key. A scalar carries `value`; a sequence carries
 *  `items`. A key declared with nothing after it carries neither. */
export interface FrontMatterField {
  key: string;
  value: string;
  items: string[];
}

export interface FrontMatterSplit {
  /** Whether a well-formed `---` fenced block opened the document. */
  present: boolean;
  fields: FrontMatterField[];
  /** The document with its front-matter block removed and line endings folded
   *  to "\n". This is what gets rendered as markdown. */
  body: string;
}

const OPEN_FENCE = "---\n";

/** Split a document into its declared front-matter fields and its markdown body.
 *
 *  A malformed header is never an error: the block is left as body text, which
 *  renders as ordinary markdown rather than vanishing. */
export function splitFrontMatter(text: string): FrontMatterSplit {
  const content = normalizeText(text);
  if (!content.startsWith(OPEN_FENCE)) {
    return { present: false, fields: [], body: content };
  }
  // The closing fence must be at least one line below the opening one, so an
  // index equal to the opening fence's length means an empty block.
  const close = findCloseFence(content);
  if (close <= OPEN_FENCE.length) {
    return { present: false, fields: [], body: content };
  }
  const block = content.slice(OPEN_FENCE.length, close);
  const afterFenceLine = content.indexOf("\n", close + 1);
  const body = afterFenceLine === -1 ? "" : content.slice(afterFenceLine + 1);
  return { present: true, fields: parseFields(block), body };
}

/** Index of the newline that begins the closing fence LINE, or -1 when the
 *  document has none.
 *
 *  The line has to be exactly `---`. A prefix search accepted `----` and
 *  `---draft` as a close, which contradicts the rule above: an unterminated
 *  header followed by a horizontal rule further down had everything between them
 *  silently promoted to metadata and removed from the rendered body. Trailing
 *  whitespace is tolerated because an editor leaves it behind invisibly. */
function findCloseFence(content: string): number {
  // Start at the newline that ends the opening fence: a fence needs its own line,
  // so the search is over line starts from there on.
  let from = OPEN_FENCE.length - 1;
  for (;;) {
    const at = content.indexOf("\n---", from);
    if (at === -1) {
      return -1;
    }
    const lineEnd = content.indexOf("\n", at + 1);
    const line = lineEnd === -1 ? content.slice(at + 1) : content.slice(at + 1, lineEnd);
    if (line.trimEnd() === "---") {
      return at;
    }
    from = at + 1;
  }
}

/** Strip a leading UTF-8 BOM and fold every line-ending convention to "\n".
 *  A LONE "\r" is folded too: without it a Mac-classic document's whole header
 *  is one line, so the fence check fails and the header renders as body text. */
function normalizeText(text: string): string {
  const s = text.startsWith("\ufeff") ? text.slice(1) : text;
  return s.replace(/\r\n?/g, "\n");
}

/** Walk the front-matter body and return its top-level fields.
 *
 *  The reason this is not a per-line `split(":")`: a key whose value is a block
 *  scalar or a block sequence owns every more-indented line that follows it, and
 *  those lines carry no colon of their own. */
function parseFields(block: string): FrontMatterField[] {
  const lines = block.split("\n");
  const out: FrontMatterField[] = [];
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i] ?? "";
    if (isSkippable(line) || leadingSpaces(line) > 0) {
      // An indented line at top level is a continuation the block readers below
      // already consumed, or it is malformed. Either way it is not a key.
      continue;
    }
    const colon = line.indexOf(":");
    if (colon < 0) {
      continue;
    }
    const key = line.slice(0, colon).trim();
    const val = line.slice(colon + 1).trim();
    if (isBlockScalarIndicator(val)) {
      const folded = readBlockScalar(lines, i + 1);
      out.push({ key, value: folded.value, items: [] });
      i = folded.lastIdx;
      continue;
    }
    if (val === "") {
      const seq = readBlockSequence(lines, i + 1);
      out.push({ key, value: "", items: seq.items });
      i = seq.lastIdx;
      continue;
    }
    if (val.startsWith("[")) {
      out.push({ key, value: "", items: parseFlowSequence(val) });
      continue;
    }
    out.push({ key, value: unquote(val), items: [] });
  }
  return out;
}

/** Whether a front-matter line carries no data: blank, or a full-line comment. */
function isSkippable(line: string): boolean {
  const t = line.trim();
  return t === "" || t.startsWith("#");
}

/** Whether a value is a YAML block-scalar header: `>` or `|`, optionally with a
 *  chomping (`-`/`+`) or explicit-indent digit. The value itself lives on the
 *  following indented lines, so `>foo` is not one. */
function isBlockScalarIndicator(val: string): boolean {
  // An empty value starts with neither, so the empty case falls out of these two.
  if (!val.startsWith(">") && !val.startsWith("|")) {
    return false;
  }
  for (const c of val.slice(1)) {
    if (c !== "-" && c !== "+" && (c < "0" || c > "9")) {
      return false;
    }
  }
  return true;
}

/** Fold the indented lines starting at `from` into one string, returning it and
 *  the index of the LAST line consumed.
 *
 *  Folding is `>`-style for both indicators — newlines become spaces. A `|`
 *  block preserves them strictly, but this value renders into one metadata row,
 *  and a literal newline there would break the row rather than honour the
 *  author's intent. */
function readBlockScalar(lines: string[], from: number): { value: string; lastIdx: number } {
  const parts: string[] = [];
  let i = from;
  for (; i < lines.length; i++) {
    const line = lines[i] ?? "";
    if (line.trim() === "") {
      continue;
    }
    if (leadingSpaces(line) === 0) {
      break; // a new top-level key
    }
    parts.push(line.trim());
  }
  return { value: parts.join(" "), lastIdx: i - 1 };
}

/** Read `- item` lines starting at `from`. Returns `lastIdx = from - 1` when the
 *  next content line is not a sequence entry, leaving the caller's cursor where
 *  it was. */
function readBlockSequence(lines: string[], from: number): { items: string[]; lastIdx: number } {
  const items: string[] = [];
  let i = from;
  for (; i < lines.length; i++) {
    const line = lines[i] ?? "";
    if (line.trim() === "") {
      continue;
    }
    const t = line.trim();
    if (leadingSpaces(line) === 0 || !t.startsWith("- ")) {
      break;
    }
    items.push(unquote(t.slice(2).trim()));
  }
  if (items.length === 0) {
    return { items: [], lastIdx: from - 1 };
  }
  return { items, lastIdx: i - 1 };
}

/** Parse a single-line `[a, b, c]` sequence. A value containing a comma inside
 *  quotes is not supported: no `.kiro` document uses one, and guessing would be
 *  worse than the simple split. */
function parseFlowSequence(val: string): string[] {
  let inner = val.startsWith("[") ? val.slice(1) : val;
  inner = inner.endsWith("]") ? inner.slice(0, -1) : inner;
  if (inner.trim() === "") {
    return [];
  }
  const out: string[] = [];
  for (const part of inner.split(",")) {
    const v = unquote(part.trim());
    if (v !== "") {
      out.push(v);
    }
  }
  return out;
}

/** Count a line's indentation, treating a tab as one level. */
function leadingSpaces(line: string): number {
  let n = 0;
  for (const c of line) {
    if (c !== " " && c !== "\t") {
      break;
    }
    n++;
  }
  return n;
}

/** Strip one matching pair of surrounding quotes. Deliberately not an escape
 *  decoder: `.kiro` front-matter quotes to protect a leading `*` or a colon,
 *  never to encode one. */
function unquote(s: string): string {
  if (s.length >= 2) {
    const first = s[0];
    const last = s[s.length - 1];
    if ((first === '"' && last === '"') || (first === "'" && last === "'")) {
      return s.slice(1, -1);
    }
  }
  return s;
}
