// ---------------------------------------------------------------------------
// Merge-conflict parser. Extracts <<<<<<< / ======= / >>>>>>> hunks from
// file content and supports resolution operations that rewrite the file
// back without markers.
//
// We only handle the classic 3-marker form for RESOLUTION. `|||||||` base
// sections from git's diff3 style are recognized and EXCLUDED from the
// ours side (they are common-ancestor content that belongs in no 2-way
// resolution); the inline resolution UI stays 2-way. Folding the base
// section into "ours" — the previous behavior — corrupted the buffer on
// every "Ours"/"Both" click in a `merge.conflictStyle=diff3` repo: the
// `||||||| base` marker line and the ancestor lines were spliced into
// the resolved output and persisted on save.
// ---------------------------------------------------------------------------

export interface ConflictHunk {
  /** 0-based line index of the `<<<<<<<` line in the file. */
  startLine: number;
  /** 0-based line index of the `>>>>>>>` line in the file. */
  endLine: number;
  /** Label after `<<<<<<<` (typically "HEAD" or a branch name). */
  ourLabel: string;
  /** Label after `>>>>>>>`. */
  theirLabel: string;
  /** Lines between `<<<<<<<` and `=======` (exclusive). */
  oursLines: string[];
  /** Lines between `=======` and `>>>>>>>` (exclusive). */
  theirsLines: string[];
}

export interface ConflictFile {
  /** Original file split into lines (no trailing newline preserved). */
  lines: string[];
  /** Whether the original text ended with a newline. */
  trailingNewline: boolean;
  hunks: ConflictHunk[];
}

const HEAD_RX = /^<{7}( .*)?$/;
const BASE_RX = /^\|{7}( .*)?$/;
const SEP_RX = /^={7}$/;
const END_RX = /^>{7}( .*)?$/;

/** Parse a file's content into conflict hunks. Returns an empty hunks
 *  list when no markers are found. Safe to call on any text. */
export function parseConflicts(content: string): ConflictFile {
  const trailing = content.endsWith("\n");
  const text = trailing ? content.slice(0, -1) : content;
  const lines = text === "" ? [] : text.split("\n");
  const hunks: ConflictHunk[] = [];

  let i = 0;
  while (i < lines.length) {
    const line = lines[i]!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
    const headMatch = HEAD_RX.exec(line);
    if (headMatch === null) {
      i++;
      continue;
    }
    // Find (optional) diff3 base marker, separator, and end. The base
    // marker is only meaningful between the head and the separator.
    let base = -1;
    let sep = -1;
    let end = -1;
    for (let j = i + 1; j < lines.length; j++) {
      const l = lines[j]!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
      if (sep === -1 && base === -1 && BASE_RX.test(l)) {
        base = j;
      } else if (sep === -1 && SEP_RX.test(l)) {
        sep = j;
      } else if (sep !== -1 && END_RX.test(l)) {
        end = j;
        break;
      }
    }
    if (sep === -1 || end === -1) {
      i++;
      continue;
    }

    const ourLabel = (headMatch[1] ?? "").trim();
    const endLine = lines[end]!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
    const endMatch = END_RX.exec(endLine);
    const theirLabel = (endMatch?.[1] ?? "").trim();

    hunks.push({
      startLine: i,
      endLine: end,
      ourLabel,
      theirLabel,
      // A diff3 base section (`||||||| base` through the separator) is
      // ancestor content: cut ours at the base marker so no resolution
      // ever writes the marker line or the ancestor lines into the file.
      oursLines: lines.slice(i + 1, base === -1 ? sep : base),
      theirsLines: lines.slice(sep + 1, end),
    });
    i = end + 1;
  }

  return { lines, trailingNewline: trailing, hunks };
}

export type Resolution = "ours" | "theirs" | "both";

/** Rewrite the file with one hunk resolved. Returns the new file content.
 *  Does not modify input. */
export function resolveHunk(file: ConflictFile, hunkIndex: number, resolution: Resolution): string {
  const hunk = file.hunks[hunkIndex];
  if (hunk === undefined) {
    return joinFile(file.lines, file.trailingNewline);
  }

  const replacement =
    resolution === "ours"
      ? hunk.oursLines
      : resolution === "theirs"
        ? hunk.theirsLines
        : [...hunk.oursLines, ...hunk.theirsLines];

  const out = [
    ...file.lines.slice(0, hunk.startLine),
    ...replacement,
    ...file.lines.slice(hunk.endLine + 1),
  ];
  return joinFile(out, file.trailingNewline);
}

function joinFile(lines: string[], trailing: boolean): string {
  if (lines.length === 0) {
    return trailing ? "\n" : "";
  }
  return lines.join("\n") + (trailing ? "\n" : "");
}
