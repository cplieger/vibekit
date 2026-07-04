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

/** Split on \n, keeping empty trailing line if text ended with \n. */
function splitLines(s: string): string[] {
  if (s === "") {
    return [];
  }
  return s.split("\n");
}

/**
 * Space threshold: if m*n exceeds this, use linear-space Hirschberg.
 * 4M cells ≈ 32MB for the dense table — safe for browser tabs.
 */
const SPACE_THRESHOLD = 4_000_000;

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
  const normalize =
    opts.ignoreWhitespace === true
      ? (s: string): string => s.replace(/\s+/g, " ").trim()
      : (s: string): string => s;
  const a = splitLines(oldText);
  const b = splitLines(newText);
  const aNorm = opts.ignoreWhitespace === true ? a.map(normalize) : a;
  const bNorm = opts.ignoreWhitespace === true ? b.map(normalize) : b;

  // Use linear-space Hirschberg for large inputs to avoid OOM.
  if (aNorm.length * bNorm.length > SPACE_THRESHOLD) {
    return hirschbergDiff(aNorm, 0, aNorm.length, bNorm, 0, bNorm.length, a, b, 0, 0);
  }

  const t = lcsTable(aNorm, bNorm);
  const out: DiffLine[] = [];

  let i = 0;
  let j = 0;
  while (i < a.length && j < b.length) {
    if (aNorm[i] === bNorm[j]) {
      out.push({ kind: "ctx", oldNo: i + 1, newNo: j + 1, text: a[i]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
      i++;
      j++;
    } else if ((t[i + 1]?.[j] ?? 0) >= (t[i]?.[j + 1] ?? 0)) {
      out.push({ kind: "del", oldNo: i + 1, newNo: 0, text: a[i]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
      i++;
    } else {
      out.push({ kind: "add", oldNo: 0, newNo: j + 1, text: b[j]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
      j++;
    }
  }
  while (i < a.length) {
    out.push({ kind: "del", oldNo: i + 1, newNo: 0, text: a[i]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
    i++;
  }
  while (j < b.length) {
    out.push({ kind: "add", oldNo: 0, newNo: j + 1, text: b[j]! }); // eslint-disable-line @typescript-eslint/no-non-null-assertion
    j++;
  }
  return out;
}

/** Keep only the first `n` changed lines (adds/dels) plus their interleaved
 *  context. Used for the 3-line inline preview in chat tool-call cards.
 *  Returns the trimmed list plus how many extra changes were dropped. */
export function truncateChanged(lines: DiffLine[], n: number): { lines: DiffLine[]; more: number } {
  let changed = 0;
  const out: DiffLine[] = [];
  for (const l of lines) {
    if (l.kind !== "ctx") {
      if (changed >= n) {
        return { lines: out, more: countChanged(lines) - changed };
      }
      changed++;
    }
    out.push(l);
  }
  return { lines: out, more: 0 };
}

function countChanged(lines: DiffLine[]): number {
  let c = 0;
  for (const l of lines) {
    if (l.kind !== "ctx") {
      c++;
    }
  }
  return c;
}
