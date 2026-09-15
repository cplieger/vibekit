import { fold } from "./fold.js";

/** A query prepared once per search. */
export interface Needle {
  /** The query as it is compared: folded for a case-insensitive scan, literal
   *  for a case-sensitive one. Its length is the query's length, so a hit spans
   *  `[at, at + text.length)` in the original. */
  readonly text: string;
  readonly caseSensitive: boolean;
}

export function prepare(query: string, caseSensitive: boolean): Needle {
  return { text: caseSensitive ? query : fold(query), caseSensitive };
}

/**
 * Non-overlapping occurrences of the needle in text order, each a UTF-16 index
 * into the ORIGINAL text. An empty needle yields nothing.
 */
export function occurrences(text: string, n: Needle): number[] {
  const hits: number[] = [];
  if (n.text.length === 0) {
    return hits;
  }
  const hay = n.caseSensitive ? text : fold(text);
  let from = 0;
  for (;;) {
    const at = hay.indexOf(n.text, from);
    if (at < 0) {
      return hits;
    }
    hits.push(at);
    from = at + n.text.length;
  }
}
