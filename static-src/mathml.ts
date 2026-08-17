// ---------------------------------------------------------------------------
// LaTeX subset -> MathML, zero dependencies.
//
// MathML Core is native in Chrome, Firefox and Safari, so an equation needs no
// katex/remark-math/rehype-katex tax: it needs a converter from the notation an
// agent actually writes in a coding chat (fractions, super/subscripts, roots,
// sums, greek letters, the relation and function vocabulary around them) to the
// element tree the browser already renders.
//
// TWO RULES SHAPE EVERY DECISION HERE:
//
//   1. `createElementNS`, never `createElement`. An element named `math` in the
//      XHTML namespace is an unknown inline element, not mathematics — it
//      renders as its own text content with no layout. `el()` / `makeEl()` in
//      smd-renderer.ts are `document.createElement`, so they cannot build this
//      subtree; that is why this module exists as a leaf rather than as a branch
//      in the renderer's tag map. Text still goes through `createTextNode`, so
//      the renderer's no-`innerHTML` property is preserved.
//
//   2. An expression this converter does not understand degrades to its RAW
//      STRING, visibly, and the whole expression degrades together. A partial
//      render is worse than a raw one: `\begin{pmatrix}` half-converted is a
//      formula that lies, while `\begin{pmatrix}...` as text is something the
//      reader can still read and copy. So any unknown command, unbalanced
//      brace, or missing argument fails the entire conversion, and the caller
//      keeps the LaTeX it already had in the DOM.
//
// The font is a CSS concern, not this module's: Chromium ships no math font, so
// 13-messages.css names a stack. Without it fractions and roots still lay out
// correctly and only stretchy operators degrade (to a smaller glyph, not to
// nothing).
// ---------------------------------------------------------------------------

/** The MathML namespace. Exported so a test can assert the tree is actually IN
 *  it — the failure this module exists to prevent is invisible otherwise, since
 *  a wrong-namespace `<math>` still has the right tag name. */
export const MATHML_NS = "http://www.w3.org/1998/Math/MathML";

/** Longest expression converted. A formula longer than this in a chat message
 *  is a paste rather than mathematics, and the raw degradation reads better
 *  than a wall of MathML. */
const MAX_SRC = 4096;

/** Recursion ceiling for nested groups. Guards a pathological
 *  `{{{{{...}}}}}` without a stack overflow. */
const MAX_DEPTH = 32;

// ---------------------------------------------------------------------------
// Symbol tables
// ---------------------------------------------------------------------------

/** Greek letters and named symbols that are IDENTIFIERS (`<mi>`): a value, not
 *  an operation. */
const IDENT_CMDS: Readonly<Record<string, string>> = {
  alpha: "\u03b1",
  beta: "\u03b2",
  gamma: "\u03b3",
  delta: "\u03b4",
  epsilon: "\u03b5",
  varepsilon: "\u03b5",
  zeta: "\u03b6",
  eta: "\u03b7",
  theta: "\u03b8",
  vartheta: "\u03d1",
  iota: "\u03b9",
  kappa: "\u03ba",
  lambda: "\u03bb",
  mu: "\u03bc",
  nu: "\u03bd",
  xi: "\u03be",
  pi: "\u03c0",
  varpi: "\u03d6",
  rho: "\u03c1",
  varrho: "\u03f1",
  sigma: "\u03c3",
  varsigma: "\u03c2",
  tau: "\u03c4",
  upsilon: "\u03c5",
  phi: "\u03c6",
  varphi: "\u03d5",
  chi: "\u03c7",
  psi: "\u03c8",
  omega: "\u03c9",
  Gamma: "\u0393",
  Delta: "\u0394",
  Theta: "\u0398",
  Lambda: "\u039b",
  Xi: "\u039e",
  Pi: "\u03a0",
  Sigma: "\u03a3",
  Upsilon: "\u03a5",
  Phi: "\u03a6",
  Psi: "\u03a8",
  Omega: "\u03a9",
  infty: "\u221e",
  partial: "\u2202",
  nabla: "\u2207",
  emptyset: "\u2205",
  varnothing: "\u2205",
  aleph: "\u2135",
  hbar: "\u210f",
  ell: "\u2113",
};

/** Relations, binary operators and punctuation (`<mo>`). */
const OP_CMDS: Readonly<Record<string, string>> = {
  times: "\u00d7",
  cdot: "\u22c5",
  div: "\u00f7",
  pm: "\u00b1",
  mp: "\u2213",
  leq: "\u2264",
  le: "\u2264",
  geq: "\u2265",
  ge: "\u2265",
  neq: "\u2260",
  ne: "\u2260",
  approx: "\u2248",
  equiv: "\u2261",
  sim: "\u223c",
  simeq: "\u2243",
  cong: "\u2245",
  propto: "\u221d",
  to: "\u2192",
  rightarrow: "\u2192",
  leftarrow: "\u2190",
  leftrightarrow: "\u2194",
  Rightarrow: "\u21d2",
  Leftarrow: "\u21d0",
  Leftrightarrow: "\u21d4",
  mapsto: "\u21a6",
  in: "\u2208",
  notin: "\u2209",
  ni: "\u220b",
  subset: "\u2282",
  subseteq: "\u2286",
  supset: "\u2283",
  supseteq: "\u2287",
  cup: "\u222a",
  cap: "\u2229",
  setminus: "\u2216",
  forall: "\u2200",
  exists: "\u2203",
  nexists: "\u2204",
  neg: "\u00ac",
  land: "\u2227",
  wedge: "\u2227",
  lor: "\u2228",
  vee: "\u2228",
  oplus: "\u2295",
  ominus: "\u2296",
  otimes: "\u2297",
  ast: "\u2217",
  star: "\u22c6",
  circ: "\u2218",
  bullet: "\u2219",
  ldots: "\u2026",
  dots: "\u2026",
  cdots: "\u22ef",
  vdots: "\u22ee",
  ddots: "\u22f1",
  angle: "\u2220",
  perp: "\u22a5",
  parallel: "\u2225",
  prime: "\u2032",
  bmod: "mod",
  lceil: "\u2308",
  rceil: "\u2309",
  lfloor: "\u230a",
  rfloor: "\u230b",
  langle: "\u27e8",
  rangle: "\u27e9",
};

/** Function names, upright by MathML's own multi-character `<mi>` rule. */
const FUNC_CMDS: ReadonlySet<string> = new Set([
  "sin",
  "cos",
  "tan",
  "cot",
  "sec",
  "csc",
  "arcsin",
  "arccos",
  "arctan",
  "sinh",
  "cosh",
  "tanh",
  "log",
  "ln",
  "exp",
  "det",
  "gcd",
  "deg",
  "dim",
  "ker",
  "arg",
]);

/** Operators whose scripts stack UNDER and OVER in display mode, the way TeX
 *  sets `\sum_{i=1}^{n}` on its own line and beside itself inline. */
const UNDEROVER_CMDS: Readonly<Record<string, string>> = {
  sum: "\u2211",
  prod: "\u220f",
  coprod: "\u2210",
  bigcup: "\u22c3",
  bigcap: "\u22c2",
  lim: "lim",
  limsup: "lim sup",
  liminf: "lim inf",
  max: "max",
  min: "min",
  sup: "sup",
  inf: "inf",
};

/** Integrals. Deliberately NOT in UNDEROVER_CMDS: TeX sets integral limits to
 *  the side even in display mode. */
const INTEGRAL_CMDS: Readonly<Record<string, string>> = {
  int: "\u222b",
  iint: "\u222c",
  iiint: "\u222d",
  oint: "\u222e",
};

/** Explicit spacing commands, in ems. */
const SPACE_CMDS: Readonly<Record<string, string>> = {
  ",": "0.167em",
  ":": "0.222em",
  ";": "0.278em",
  "!": "-0.167em",
  " ": "0.25em",
  thinspace: "0.167em",
  enspace: "0.5em",
  quad: "1em",
  qquad: "2em",
};

/** Commands whose argument is literal TEXT rather than mathematics, so the
 *  tokenizer hands it over unparsed. */
const TEXT_CMDS: ReadonlySet<string> = new Set(["text", "textrm", "mathrm", "operatorname"]);

/** Delimiters `\left` / `\right` accept, keyed by the token text that follows.
 *  A `.` is the null delimiter, so it maps to the empty string. */
const DELIMS: Readonly<Record<string, string>> = {
  "(": "(",
  ")": ")",
  "[": "[",
  "]": "]",
  "|": "|",
  "{": "{",
  "}": "}",
  ".": "",
  langle: "\u27e8",
  rangle: "\u27e9",
  lceil: "\u2308",
  rceil: "\u2309",
  lfloor: "\u230a",
  rfloor: "\u230b",
  vert: "|",
  Vert: "\u2016",
};

/** Characters that only mean something in constructs this converter does not
 *  support (alignment, macro parameters, comments), so seeing one degrades the
 *  expression rather than rendering it as an operator. */
const REJECTED_CHARS: ReadonlySet<string> = new Set(["&", "#", "%", "$"]);

// ---------------------------------------------------------------------------
// Tokens
// ---------------------------------------------------------------------------

type TokKind =
  "cmd" | "raw" | "num" | "ident" | "op" | "open" | "close" | "obrack" | "cbrack" | "sup" | "sub";

interface Tok {
  readonly k: TokKind;
  readonly v: string;
}

function isAlpha(ch: string): boolean {
  return (ch >= "a" && ch <= "z") || (ch >= "A" && ch <= "Z");
}

function isDigit(ch: string): boolean {
  return ch >= "0" && ch <= "9";
}

/** Read a balanced `{...}` starting at `from`, returning its inner text and the
 *  index just past the closing brace. Null when there is no braced group. */
function readBraced(src: string, from: number): { text: string; next: number } | null {
  let i = from;
  while (i < src.length && (src[i] === " " || src[i] === "\n" || src[i] === "\t")) {
    i++;
  }
  if (src.charAt(i) !== "{") {
    return null;
  }
  let depth = 0;
  const start = i + 1;
  for (; i < src.length; i++) {
    if (src[i] === "{") {
      depth++;
    } else if (src[i] === "}") {
      depth--;
      if (depth === 0) {
        return { text: src.slice(start, i), next: i + 1 };
      }
    }
  }
  return null;
}

function tokenize(src: string): Tok[] | null {
  const out: Tok[] = [];
  let i = 0;
  // charAt rather than indexing: it is typed `string`, so a bounded walk needs
  // neither a cast nor a non-null assertion.
  while (i < src.length) {
    const ch = src.charAt(i);
    if (ch === " " || ch === "\n" || ch === "\t" || ch === "\r") {
      i++;
      continue;
    }
    if (ch === "\\") {
      let j = i + 1;
      while (j < src.length && isAlpha(src.charAt(j))) {
        j++;
      }
      if (j === i + 1) {
        // A single-character control sequence: `\,` `\{` `\\` `\ `.
        if (j >= src.length) {
          return null;
        }
        out.push({ k: "cmd", v: src.charAt(j) });
        i = j + 1;
        continue;
      }
      const name = src.slice(i + 1, j);
      out.push({ k: "cmd", v: name });
      i = j;
      if (TEXT_CMDS.has(name)) {
        const arg = readBraced(src, i);
        if (arg === null) {
          return null;
        }
        out.push({ k: "raw", v: arg.text });
        i = arg.next;
      }
      continue;
    }
    if (isDigit(ch)) {
      let j = i;
      while (j < src.length) {
        const c = src.charAt(j);
        if (isDigit(c)) {
          j++;
          continue;
        }
        // A decimal point only belongs to the number when a digit follows it,
        // so `f(1).x` keeps its `.` as punctuation.
        if (c === "." && j + 1 < src.length && isDigit(src.charAt(j + 1))) {
          j += 2;
          continue;
        }
        break;
      }
      out.push({ k: "num", v: src.slice(i, j) });
      i = j;
      continue;
    }
    if (isAlpha(ch)) {
      out.push({ k: "ident", v: ch });
      i++;
      continue;
    }
    if (REJECTED_CHARS.has(ch)) {
      return null;
    }
    switch (ch) {
      case "{":
        out.push({ k: "open", v: ch });
        break;
      case "}":
        out.push({ k: "close", v: ch });
        break;
      case "[":
        out.push({ k: "obrack", v: ch });
        break;
      case "]":
        out.push({ k: "cbrack", v: ch });
        break;
      case "^":
        out.push({ k: "sup", v: ch });
        break;
      case "_":
        out.push({ k: "sub", v: ch });
        break;
      default:
        out.push({ k: "op", v: ch });
    }
    i++;
  }
  return out;
}

// ---------------------------------------------------------------------------
// Element helpers
// ---------------------------------------------------------------------------

function mel(tag: string, ...children: (Element | string)[]): Element {
  const node = document.createElementNS(MATHML_NS, tag);
  for (const c of children) {
    node.appendChild(typeof c === "string" ? document.createTextNode(c) : c);
  }
  return node;
}

/** Wrap a run in an `<mrow>`, except a single element which is already one
 *  thing. An `<mrow>` around one child changes nothing and makes the tree
 *  harder to read in devtools. */
function row(items: Element[]): Element {
  const [only] = items;
  return items.length === 1 && only !== undefined ? only : mel("mrow", ...items);
}

/** A parsed atom plus whether its scripts stack (a large operator in display
 *  mode). Carried alongside the node rather than as an attribute so nothing has
 *  to be stripped from the output tree afterwards. */
interface Atom {
  node: Element;
  underover: boolean;
}

interface Cursor {
  /** Mutable: parseArg splits a multi-digit number in place. See there. */
  toks: Tok[];
  readonly display: boolean;
  i: number;
  depth: number;
}

// ---------------------------------------------------------------------------
// Parser
// ---------------------------------------------------------------------------

/** Parse items until `stop` (or the end of the tokens). Null propagates: one
 *  unsupported item degrades the whole expression, by design. */
function parseSeq(c: Cursor, stop: TokKind | null): Element[] | null {
  const out: Element[] = [];
  while (c.i < c.toks.length) {
    const t = c.toks[c.i];
    if (t === undefined || (stop !== null && t.k === stop)) {
      break;
    }
    const item = parseItem(c);
    if (item === null) {
      return null;
    }
    out.push(item);
  }
  return out;
}

function parseItem(c: Cursor): Element | null {
  const base = parseAtom(c);
  if (base === null) {
    return null;
  }
  return applyScripts(c, base);
}

/** Attach `_` and `^` to a base, in either written order. */
function applyScripts(c: Cursor, base: Atom): Element | null {
  let sub: Element | null = null;
  let sup: Element | null = null;
  for (;;) {
    const t = c.toks[c.i];
    if (t === undefined) {
      break;
    }
    if (t.k === "sub" && sub === null) {
      c.i++;
      sub = parseArg(c);
      if (sub === null) {
        return null;
      }
      continue;
    }
    if (t.k === "sup" && sup === null) {
      c.i++;
      sup = parseArg(c);
      if (sup === null) {
        return null;
      }
      continue;
    }
    break;
  }
  if (sub === null && sup === null) {
    return base.node;
  }
  const stacked = c.display && base.underover;
  if (sub !== null && sup !== null) {
    return mel(stacked ? "munderover" : "msubsup", base.node, sub, sup);
  }
  if (sub !== null) {
    return mel(stacked ? "munder" : "msub", base.node, sub);
  }
  if (sup !== null) {
    return mel(stacked ? "mover" : "msup", base.node, sup);
  }
  return base.node;
}

/** One command/script argument: a braced group, or the single atom that follows
 *  (TeX's `x^2` and `\frac12`). Scripts are NOT consumed here — `x^2^3` is
 *  ill-formed in TeX too and falls out as a parse failure upstream. */
function parseArg(c: Cursor): Element | null {
  const t = c.toks[c.i];
  if (t === undefined) {
    return null;
  }
  if (t.k === "open") {
    return parseGroup(c);
  }
  // TeX takes exactly ONE token as an unbraced argument, so `\frac12` is
  // `\frac{1}{2}` and `x^12` is x¹2. The tokenizer groups digit runs (`12` is
  // one number everywhere else, which is what makes `3.14` work), so the split
  // happens here: the first digit becomes the argument and the remainder is put
  // back for the next read.
  if (t.k === "num" && t.v.length > 1) {
    c.toks[c.i] = { k: "num", v: t.v.slice(1) };
    return mel("mn", t.v.slice(0, 1));
  }
  const atom = parseAtom(c);
  return atom === null ? null : atom.node;
}

function parseGroup(c: Cursor): Element | null {
  if (c.depth >= MAX_DEPTH) {
    return null;
  }
  c.i++; // consume `{`
  c.depth++;
  const items = parseSeq(c, "close");
  c.depth--;
  if (items === null) {
    return null;
  }
  if (c.toks[c.i]?.k !== "close") {
    return null; // unbalanced
  }
  c.i++; // consume `}`
  return items.length === 0 ? mel("mrow") : row(items);
}

function parseAtom(c: Cursor): Atom | null {
  const t = c.toks[c.i];
  if (t === undefined) {
    return null;
  }
  switch (t.k) {
    case "num":
      c.i++;
      return { node: mel("mn", t.v), underover: false };
    case "ident":
      c.i++;
      return { node: mel("mi", t.v), underover: false };
    case "op":
      c.i++;
      return { node: mel("mo", t.v === "'" ? "\u2032" : t.v), underover: false };
    case "obrack":
    case "cbrack":
      c.i++;
      return { node: mel("mo", t.v), underover: false };
    case "open": {
      const g = parseGroup(c);
      return g === null ? null : { node: g, underover: false };
    }
    case "cmd":
      return parseCommand(c, t.v);
    // A stray `}`, a script with no base, or a text argument with no command
    // are all malformed rather than renderable.
    case "close":
    case "sup":
    case "sub":
    case "raw":
      return null;
  }
}

function parseCommand(c: Cursor, name: string): Atom | null {
  c.i++; // consume the command
  switch (name) {
    case "frac":
    case "dfrac":
    case "tfrac": {
      const num = parseArg(c);
      if (num === null) {
        return null;
      }
      const den = parseArg(c);
      if (den === null) {
        return null;
      }
      return { node: mel("mfrac", num, den), underover: false };
    }
    case "sqrt": {
      let index: Element | null = null;
      if (c.toks[c.i]?.k === "obrack") {
        c.i++;
        const items = parseSeq(c, "cbrack");
        if (items === null || c.toks[c.i]?.k !== "cbrack") {
          return null;
        }
        c.i++;
        index = items.length === 0 ? mel("mrow") : row(items);
      }
      const arg = parseArg(c);
      if (arg === null) {
        return null;
      }
      return {
        node: index === null ? mel("msqrt", arg) : mel("mroot", arg, index),
        underover: false,
      };
    }
    case "left":
    case "right": {
      const d = c.toks[c.i];
      if (d === undefined) {
        return null;
      }
      const ch = DELIMS[d.v];
      if (ch === undefined) {
        return null;
      }
      c.i++;
      if (ch === "") {
        return { node: mel("mrow"), underover: false }; // `\left.` is the null fence
      }
      const mo = mel("mo", ch);
      mo.setAttribute("stretchy", "true");
      return { node: mo, underover: false };
    }
    default:
      break;
  }
  if (TEXT_CMDS.has(name)) {
    const arg = c.toks[c.i];
    if (arg?.k !== "raw") {
      return null;
    }
    c.i++;
    if (name === "text" || name === "textrm") {
      return { node: mel("mtext", arg.v), underover: false };
    }
    const mi = mel("mi", arg.v);
    // A single-character `<mi>` is italic by default; `\mathrm{d}` means the
    // upright differential, so the variant is stated rather than inferred.
    mi.setAttribute("mathvariant", "normal");
    return { node: mi, underover: false };
  }
  const space = SPACE_CMDS[name];
  if (space !== undefined) {
    const ms = mel("mspace");
    ms.setAttribute("width", space);
    return { node: ms, underover: false };
  }
  const large = UNDEROVER_CMDS[name];
  if (large !== undefined) {
    return { node: mel("mo", large), underover: true };
  }
  const integral = INTEGRAL_CMDS[name];
  if (integral !== undefined) {
    return { node: mel("mo", integral), underover: false };
  }
  const ident = IDENT_CMDS[name];
  if (ident !== undefined) {
    return { node: mel("mi", ident), underover: false };
  }
  const op = OP_CMDS[name];
  if (op !== undefined) {
    return { node: mel("mo", op), underover: false };
  }
  if (FUNC_CMDS.has(name)) {
    return { node: mel("mi", name), underover: false };
  }
  return null;
}

// ---------------------------------------------------------------------------
// Public entry point
// ---------------------------------------------------------------------------

/**
 * Convert a LaTeX expression to a MathML `<math>` element, or null when the
 * expression uses anything outside the supported subset.
 *
 * Null is the DEGRADATION SIGNAL, not an error: the caller already holds the raw
 * LaTeX as a text node and simply leaves it there. Every rejection path below is
 * therefore silent — logging one per unconverted expression would fill the
 * console on any transcript that quotes a matrix.
 *
 * `display` selects `display="block"` and the stacked limits that go with it.
 */
export function latexToMathML(src: string, display: boolean): Element | null {
  if (src.length === 0 || src.length > MAX_SRC || src.trim() === "") {
    return null;
  }
  const toks = tokenize(src);
  if (toks === null || toks.length === 0) {
    return null;
  }
  const c: Cursor = { toks, display, i: 0, depth: 0 };
  const items = parseSeq(c, null);
  if (items === null || items.length === 0 || c.i !== toks.length) {
    return null;
  }
  const math = mel("math", row(items));
  if (display) {
    math.setAttribute("display", "block");
  }
  return math;
}
