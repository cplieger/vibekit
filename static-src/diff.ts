// ---------------------------------------------------------------------------
// Line-level diff: LCS-based, produces a flat array of add / del / ctx
// entries suitable for two-pane rendering. Not Myers, but good enough
// for the file sizes the editor actually shows (typically < 10k lines).
//
// The entries are emitted in reading order so the renderer can walk
// them once and place lines into old/new columns.
// ---------------------------------------------------------------------------

type DiffKind = "add" | "del" | "ctx";

export interface DiffLine {
  kind: DiffKind;
  /** 1-based line number in the old text (0 for pure adds). */
  oldNo: number;
  /** 1-based line number in the new text (0 for pure dels). */
  newNo: number;
  /** Raw line content without trailing newline. */
  text: string;
}

export interface DiffStats {
  adds: number;
  dels: number;
  ctx: number;
}

export function stats(lines: DiffLine[]): DiffStats {
  const s: DiffStats = { adds: 0, dels: 0, ctx: 0 };
  for (const l of lines) {
    if (l.kind === "add") {
      s.adds++;
    } else if (l.kind === "del") {
      s.dels++;
    } else {
      s.ctx++;
    }
  }
  return s;
}

/** Split on \n, keeping empty trailing line if text ended with \n.
 *  CRLF content is normalized by stripping the trailing \r from each
 *  line: consumers own the rejoin EOL (buildPartialMergeText joins with
 *  detectEOL(original)), so lines carrying \r would double the CR on
 *  reconstruction — and the \r is invisible-but-real in rendered diffs. */
function splitLines(s: string): string[] {
  if (s === "") {
    return [];
  }
  const lines = s.split("\n");
  if (s.includes("\r")) {
    for (let i = 0; i < lines.length; i++) {
      const l = lines[i]!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
      if (l.endsWith("\r")) {
        lines[i] = l.slice(0, -1);
      }
    }
  }
  return lines;
}

/**
 * Space threshold: if m*n exceeds this, use linear-space Hirschberg.
 * 4M cells ≈ 32MB for the dense table — safe for browser tabs.
 */
const SPACE_THRESHOLD = 4_000_000;

/**
 * Time budget for the exact algorithms, in m×n cells. SPACE_THRESHOLD
 * only bounds memory — both LCS and Hirschberg are O(mn) TIME on the
 * main thread, and the 2 MiB server file cap admits inputs far past
 * the "<10k lines" header comment. Inputs whose (prefix/suffix-trimmed)
 * middle exceeds this fall back to a coarse but valid del-all/add-all
 * edit script instead of freezing the tab. ~25M cells ≈ tens of ms.
 */
const TIME_BUDGET_CELLS = 25_000_000;

/** Dense LCS table, bottom-up. O(mn) space — used only for small inputs. */
function lcsTable(a: string[], b: string[]): number[][] {
  const m = a.length;
  const n = b.length;
  const t: number[][] = Array.from({ length: m + 1 }, () => new Array<number>(n + 1).fill(0));
  for (let i = m - 1; i >= 0; i--) {
    for (let j = n - 1; j >= 0; j--) {
      if (a[i] === b[j]) {
        t[i]![j] = (t[i + 1]?.[j + 1] ?? 0) + 1; // eslint-disable-line @typescript-eslint/no-non-null-assertion
      } else {
        t[i]![j] = Math.max(t[i + 1]?.[j] ?? 0, t[i]?.[j + 1] ?? 0); // eslint-disable-line @typescript-eslint/no-non-null-assertion
      }
    }
  }
  return t;
}

/**
 * Compute the last row of the LCS length table for a[aLo..aHi) vs b[bLo..bHi).
 * Uses O(bHi-bLo) space (two rows). Returns an array of length (bHi-bLo+1).
 */
function lcsLastRow(
  a: string[],
  aLo: number,
  aHi: number,
  b: string[],
  bLo: number,
  bHi: number,
): number[] {
  const cols = bHi - bLo;
  let prev = new Array<number>(cols + 1).fill(0);
  let curr = new Array<number>(cols + 1).fill(0);
  for (let i = aLo; i < aHi; i++) {
    for (let j = bLo; j < bHi; j++) {
      if (a[i] === b[j]) {
        curr[j - bLo + 1] = prev[j - bLo]! + 1; // eslint-disable-line @typescript-eslint/no-non-null-assertion
      } else {
        curr[j - bLo + 1] = Math.max(curr[j - bLo]!, prev[j - bLo + 1]!); // eslint-disable-line @typescript-eslint/no-non-null-assertion
      }
    }
    [prev, curr] = [curr, prev];
    curr.fill(0);
  }
  return prev;
}

/**
 * Hirschberg linear-space diff. Produces DiffLine[] using O(min(m,n)) space.
 * Recursively splits the problem at the midpoint of `a` and finds the optimal
 * split in `b` using forward + reverse last-row computations.
 */
function hirschbergDiff(
  a: string[],
  aLo: number,
  aHi: number,
  b: string[],
  bLo: number,
  bHi: number,
  aOrig: string[],
  bOrig: string[],
  aOffset: number,
  bOffset: number,
): DiffLine[] {
  const m = aHi - aLo;
  const n = bHi - bLo;

  if (m === 0) {
    const out: DiffLine[] = [];
    for (let j = bLo; j < bHi; j++) {
      out.push({ kind: "add", oldNo: 0, newNo: bOffset + j + 1, text: bOrig[j]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
    }
    return out;
  }
  if (n === 0) {
    const out: DiffLine[] = [];
    for (let i = aLo; i < aHi; i++) {
      out.push({ kind: "del", oldNo: aOffset + i + 1, newNo: 0, text: aOrig[i]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
    }
    return out;
  }
  if (m === 1) {
    // Base case: single line in a vs b[bLo..bHi)
    const line = a[aLo]!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
    let matchIdx = -1;
    for (let j = bLo; j < bHi; j++) {
      if (b[j] === line) {
        matchIdx = j;
        break;
      }
    }
    const out: DiffLine[] = [];
    if (matchIdx === -1) {
      out.push({ kind: "del", oldNo: aOffset + aLo + 1, newNo: 0, text: aOrig[aLo]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
      for (let j = bLo; j < bHi; j++) {
        out.push({ kind: "add", oldNo: 0, newNo: bOffset + j + 1, text: bOrig[j]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
      }
    } else {
      for (let j = bLo; j < matchIdx; j++) {
        out.push({ kind: "add", oldNo: 0, newNo: bOffset + j + 1, text: bOrig[j]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
      }
      out.push({
        kind: "ctx",
        oldNo: aOffset + aLo + 1,
        newNo: bOffset + matchIdx + 1,
        text: aOrig[aLo]!, // eslint-disable-line @typescript-eslint/no-non-null-assertion
      });
      for (let j = matchIdx + 1; j < bHi; j++) {
        out.push({ kind: "add", oldNo: 0, newNo: bOffset + j + 1, text: bOrig[j]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
      }
    }
    return out;
  }

  // Split a at midpoint
  const aMid = aLo + Math.floor(m / 2);

  // Forward: LCS last row for a[aLo..aMid) vs b[bLo..bHi)
  const fwd = lcsLastRow(a, aLo, aMid, b, bLo, bHi);

  // Reverse: LCS last row for reversed a[aMid..aHi) vs reversed b[bLo..bHi)
  // We reverse by creating temporary reversed slices
  const aRev = a.slice(aMid, aHi).reverse();
  const bRev = b.slice(bLo, bHi).reverse();
  const rev = lcsLastRow(aRev, 0, aRev.length, bRev, 0, bRev.length);

  // Find optimal split point in b
  let bestJ = bLo;
  let bestScore = -1;
  for (let j = bLo; j <= bHi; j++) {
    const score = fwd[j - bLo]! + rev[bHi - j]!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
    if (score > bestScore) {
      bestScore = score;
      bestJ = j;
    }
  }

  // Recurse on both halves
  const left = hirschbergDiff(a, aLo, aMid, b, bLo, bestJ, aOrig, bOrig, aOffset, bOffset);
  const right = hirschbergDiff(a, aMid, aHi, b, bestJ, bHi, aOrig, bOrig, aOffset, bOffset);
  return left.concat(right);
}

/** Compute a line-level diff. Returns an ordered list of DiffLines.
 *  When opts.ignoreWhitespace is true, lines that differ only in
 *  leading/trailing/internal whitespace collapse to context — useful
 *  for reviewing code diffs where tab/space drift would otherwise
 *  dominate the output. */
export function lineDiff(
  oldText: string,
  newText: string,
  opts: { ignoreWhitespace?: boolean } = {},
): DiffLine[] {
  return diffLineArrays(splitLines(oldText), splitLines(newText), opts);
}

/** Diff two already-split line arrays.
 *
 *  The array seam exists for `lineDelta`, whose line vocabulary differs from the
 *  renderer's and cannot be reached through a string: a lone "\n" is ONE empty
 *  line, and stripping that newline leaves "", which `splitLines` reads as none. */
function diffLineArrays(
  a: string[],
  b: string[],
  opts: { ignoreWhitespace?: boolean },
): DiffLine[] {
  const normalize =
    opts.ignoreWhitespace === true
      ? (s: string): string => s.replace(/\s+/g, " ").trim()
      : (s: string): string => s;
  const aNorm = opts.ignoreWhitespace === true ? a.map(normalize) : a;
  const bNorm = opts.ignoreWhitespace === true ? b.map(normalize) : b;

  // Common prefix/suffix trim (compared on normalized lines): real edits
  // cluster, so this collapses most of the m×n area before any exact
  // algorithm runs, and bounds the budget check below to the genuinely
  // differing middle.
  let p = 0;
  const maxTrim = Math.min(a.length, b.length);
  while (p < maxTrim && aNorm[p] === bNorm[p]) {
    p++;
  }
  let s = 0;
  while (s < maxTrim - p && aNorm[a.length - 1 - s] === bNorm[b.length - 1 - s]) {
    s++;
  }

  const out: DiffLine[] = [];
  for (let k = 0; k < p; k++) {
    out.push({ kind: "ctx", oldNo: k + 1, newNo: k + 1, text: a[k]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
  }
  out.push(...diffMiddle(a, b, aNorm, bNorm, p, s));
  for (let k = 0; k < s; k++) {
    const ai = a.length - s + k;
    const bi = b.length - s + k;
    out.push({ kind: "ctx", oldNo: ai + 1, newNo: bi + 1, text: a[ai]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
  }
  return out;
}

/** Diff the trimmed middle a[p..len-s) vs b[p..len-s), picking the exact
 *  algorithm by size — or the bounded del-all/add-all fallback when even
 *  the trimmed middle would blow the main-thread time budget. */
function diffMiddle(
  a: string[],
  b: string[],
  aNorm: string[],
  bNorm: string[],
  p: number,
  s: number,
): DiffLine[] {
  const aHi = a.length - s;
  const bHi = b.length - s;
  const m = aHi - p;
  const n = bHi - p;
  if (m === 0 && n === 0) {
    return [];
  }

  // Time-budget fallback: a coarse but valid edit script (delete the
  // whole old middle, add the whole new middle). Reconstruction
  // consumers (buildPartialMergeText) hold for any valid script; the
  // cost is hunk granularity, which is the honest trade at this size.
  if (m * n > TIME_BUDGET_CELLS) {
    const out: DiffLine[] = [];
    for (let i = p; i < aHi; i++) {
      out.push({ kind: "del", oldNo: i + 1, newNo: 0, text: a[i]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
    }
    for (let j = p; j < bHi; j++) {
      out.push({ kind: "add", oldNo: 0, newNo: j + 1, text: b[j]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
    }
    return out;
  }

  // Use linear-space Hirschberg for large inputs to avoid OOM.
  if (m * n > SPACE_THRESHOLD) {
    return hirschbergDiff(
      aNorm.slice(p, aHi),
      0,
      m,
      bNorm.slice(p, bHi),
      0,
      n,
      a.slice(p, aHi),
      b.slice(p, bHi),
      p,
      p,
    );
  }

  const aNormMid = aNorm.slice(p, aHi);
  const bNormMid = bNorm.slice(p, bHi);
  const t = lcsTable(aNormMid, bNormMid);
  const out: DiffLine[] = [];

  let i = 0;
  let j = 0;
  while (i < m && j < n) {
    if (aNormMid[i] === bNormMid[j]) {
      out.push({ kind: "ctx", oldNo: p + i + 1, newNo: p + j + 1, text: a[p + i]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
      i++;
      j++;
    } else if ((t[i + 1]?.[j] ?? 0) >= (t[i]?.[j + 1] ?? 0)) {
      out.push({ kind: "del", oldNo: p + i + 1, newNo: 0, text: a[p + i]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
      i++;
    } else {
      out.push({ kind: "add", oldNo: 0, newNo: p + j + 1, text: b[p + j]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
      j++;
    }
  }
  while (i < m) {
    out.push({ kind: "del", oldNo: p + i + 1, newNo: 0, text: a[p + i]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
    i++;
  }
  while (j < n) {
    out.push({ kind: "add", oldNo: 0, newNo: p + j + 1, text: b[p + j]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
    j++;
  }
  return out;
}

/** Split for COUNTING: `splitLines` minus the element a final newline produces.
 *
 *  Its Go twin is `splitDiffLines` in `internal/buffer/linediff.go`, and the two
 *  must agree on every input or the footers below disagree about one file.
 *  `splitLines` keeps that element because the diff RENDERER draws its row, so
 *  "a\nb\n" is three lines there and two here. */
function splitDeltaLines(s: string): string[] {
  const lines = splitLines(s);
  if (s.endsWith("\n")) {
    lines.pop();
  }
  return lines;
}

/** How many lines a change added and removed — the numbers a footer states.
 *
 *  Its Go twin is `lineDelta` in `internal/buffer/linediff.go`, which computes
 *  the same counts for the turn footer; the two footers render the same
 *  component, so they must agree on the same file or one of them is lying. The
 *  shared fixture is `internal/buffer/testdata/line_delta.json`. */
export function lineDelta(oldText: string, newText: string): { added: number; removed: number } {
  const s = stats(diffLineArrays(splitDeltaLines(oldText), splitDeltaLines(newText), {}));
  return { added: s.adds, removed: s.dels };
}

// ---------------------------------------------------------------------------
// Intra-line (word-level) diff.
//
// A line-level diff says a line changed; it does not say WHERE. On a modified
// line the reader's question is which characters moved, and the row tint cannot
// answer it. VS Code paints two layers for this: the line background plus a
// stronger background on the changed characters only.
// ---------------------------------------------------------------------------

/** A half-open character range `[start, end)` within one line. */
export interface CharRange {
  /** 0-based index of the first character in the range. */
  readonly start: number;
  /** 0-based index one past the last character. */
  readonly end: number;
}

/** The changed character ranges of one del/add line pair. */
export interface WordDiff {
  /** Ranges into the OLD line that the new line does not have. */
  readonly del: CharRange[];
  /** Ranges into the NEW line that the old line did not have. */
  readonly add: CharRange[];
}

/** Token budget per side. Past this a line is long enough that no reader is
 *  hunting individual words in it, and the m×n table stops being free. */
const MAX_WORD_TOKENS = 400;

/** Changed-range budget per side. Past this the two lines share so little that
 *  per-word marks read as confetti and say less than the row tint alone. */
const MAX_WORD_RUNS = 8;

function isWordCharCode(c: number): boolean {
  return (
    (c >= 48 && c <= 57) || // 0-9
    (c >= 65 && c <= 90) || // A-Z
    (c >= 97 && c <= 122) || // a-z
    c === 95 || // _
    c === 36 || // $
    c >= 0x80 // keep non-ASCII identifiers whole
  );
}

function isSpaceCode(c: number): boolean {
  return c === 32 || c === 9;
}

interface WordToken {
  readonly text: string;
  readonly start: number;
}

/** Split a line into identifier runs, whitespace runs, and single other
 *  characters. Every character lands in exactly one token, so a token's `start`
 *  plus its length addresses the original line. */
function splitWords(s: string): WordToken[] {
  const out: WordToken[] = [];
  let i = 0;
  while (i < s.length) {
    const start = i;
    const c = s.charCodeAt(i);
    if (isWordCharCode(c)) {
      while (i < s.length && isWordCharCode(s.charCodeAt(i))) {
        i++;
      }
    } else if (isSpaceCode(c)) {
      while (i < s.length && isSpaceCode(s.charCodeAt(i))) {
        i++;
      }
    } else {
      i++;
    }
    out.push({ text: s.slice(start, i), start });
  }
  return out;
}

/** Append a token's span to `out`, merging it into the previous range when the
 *  two touch — so `foo(` and `bar` produce one mark, not three. */
function pushRange(out: CharRange[], tok: WordToken): void {
  const end = tok.start + tok.text.length;
  const last = out[out.length - 1];
  if (last?.end === tok.start) {
    out[out.length - 1] = { start: last.start, end };
    return;
  }
  out.push({ start: tok.start, end });
}

/** Character ranges that differ between two versions of ONE line, or null when
 *  a per-word answer would not beat the row tint.
 *
 *  Null is returned when the lines share no non-blank token (the whole line IS
 *  the change) or when the change is scattered past `MAX_WORD_RUNS`. A null
 *  result is not a failure: the caller keeps the line-level tint and paints no
 *  word marks. */
export function wordDiff(oldLine: string, newLine: string): WordDiff | null {
  if (oldLine === newLine || oldLine === "" || newLine === "") {
    return null;
  }
  const a = splitWords(oldLine);
  const b = splitWords(newLine);
  if (a.length > MAX_WORD_TOKENS || b.length > MAX_WORD_TOKENS) {
    return null;
  }

  const at = a.map((t) => t.text);
  const bt = b.map((t) => t.text);
  const t = lcsTable(at, bt);

  const del: CharRange[] = [];
  const add: CharRange[] = [];
  let matchedInk = 0;
  let i = 0;
  let j = 0;
  while (i < a.length && j < b.length) {
    if (at[i] === bt[j]) {
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- i < a.length
      const tok = a[i]!;
      if (tok.text.trim() !== "") {
        matchedInk += tok.text.length;
      }
      i++;
      j++;
    } else if ((t[i + 1]?.[j] ?? 0) >= (t[i]?.[j + 1] ?? 0)) {
      pushRange(del, a[i]!); // eslint-disable-line @typescript-eslint/no-non-null-assertion
      i++;
    } else {
      pushRange(add, b[j]!); // eslint-disable-line @typescript-eslint/no-non-null-assertion
      j++;
    }
  }
  while (i < a.length) {
    pushRange(del, a[i]!); // eslint-disable-line @typescript-eslint/no-non-null-assertion
    i++;
  }
  while (j < b.length) {
    pushRange(add, b[j]!); // eslint-disable-line @typescript-eslint/no-non-null-assertion
    j++;
  }

  // No shared ink means every token on both sides changed, so the marks would
  // just restate the row tint. This also covers the whole-line rewrite: a
  // matched token always leaves a gap in its side's ranges, so "one range
  // spanning the entire line" cannot happen with matched ink — a separate gate
  // for it was written, measured over 607,620 short line pairs, never fired
  // once, and was deleted rather than left as a guard nothing can reach.
  if (matchedInk === 0) {
    return null;
  }
  if (del.length > MAX_WORD_RUNS || add.length > MAX_WORD_RUNS) {
    return null;
  }
  return { del, add };
}

/** Word-level marks for every modified line in a line diff, keyed by the line
 *  itself. A del and an add are one MODIFICATION when they sit in the same run
 *  of changed lines at the same offset within it; anything unpaired is a pure
 *  insert or delete and gets no marks.
 *
 *  Pairing by offset within a run rather than by adjacency is what survives the
 *  del/add interleaving `lineDiff` is free to emit. */
export function wordMarks(lines: readonly DiffLine[]): Map<DiffLine, CharRange[]> {
  const marks = new Map<DiffLine, CharRange[]>();
  let dels: DiffLine[] = [];
  let adds: DiffLine[] = [];

  const flush = (): void => {
    const pairs = Math.min(dels.length, adds.length);
    for (let k = 0; k < pairs; k++) {
      const d = dels[k]!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
      const s = adds[k]!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
      const wd = wordDiff(d.text, s.text);
      if (wd !== null) {
        marks.set(d, wd.del);
        marks.set(s, wd.add);
      }
    }
    dels = [];
    adds = [];
  };

  for (const l of lines) {
    if (l.kind === "del") {
      dels.push(l);
    } else if (l.kind === "add") {
      adds.push(l);
    } else {
      flush();
    }
  }
  flush();
  return marks;
}

/** Window a diff to WHOLE HUNKS with surrounding context, capped on total rows.
 *
 *  This replaced `truncateChanged(diff, 3)`, whose unit was wrong rather than
 *  merely small: keeping the first three CHANGED LINES shows a 12-line rewrite
 *  as a quarter of itself, cut mid-thought. A hunk is the unit a reader thinks
 *  in, so the window keeps as many complete hunks as fit and says how many it
 *  dropped.
 *
 *  `context` lines of `ctx` are kept on each side of a hunk; runs longer than
 *  2×context collapse, with the elided middle counted. Returns the windowed
 *  lines plus the number of whole HUNKS omitted (0 when everything fit). */
export function windowHunks(
  lines: DiffLine[],
  opts: { maxRows?: number; context?: number } = {},
): { lines: DiffLine[]; hunksOmitted: number } {
  const maxRows = opts.maxRows ?? 24;
  const context = opts.context ?? 2;

  // Segment into runs, tagging each as a hunk (has a change) or context.
  const runs: { changed: boolean; lines: DiffLine[] }[] = [];
  for (const l of lines) {
    const changed = l.kind !== "ctx";
    const last = runs[runs.length - 1];
    if (last?.changed === changed) {
      last.lines.push(l);
    } else {
      runs.push({ changed, lines: [l] });
    }
  }

  const totalHunks = runs.filter((r) => r.changed).length;
  if (totalHunks === 0) {
    return { lines: [], hunksOmitted: 0 };
  }

  const out: DiffLine[] = [];
  let kept = 0;
  for (let i = 0; i < runs.length; i++) {
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
    const run = runs[i]!;
    if (!run.changed) {
      // Context adjoining a hunk is kept, up to `context` lines on the side that
      // touches it; a long run between two hunks keeps both ends and elides the
      // middle. A run touching no hunk on a side contributes nothing there.
      const head = runs[i - 1]?.changed === true ? run.lines.slice(0, context) : [];
      const tail = runs[i + 1]?.changed === true ? run.lines.slice(-context) : [];
      if (head.length + tail.length >= run.lines.length) {
        out.push(...run.lines); // the two ends meet; nothing to elide
      } else {
        out.push(...head, ...tail);
      }
      continue;
    }
    // A hunk goes in WHOLE or not at all — that is the point of the unit.
    if (out.length + run.lines.length > maxRows && kept > 0) {
      break;
    }
    out.push(...run.lines);
    kept++;
  }
  return { lines: out, hunksOmitted: totalHunks - kept };
}
