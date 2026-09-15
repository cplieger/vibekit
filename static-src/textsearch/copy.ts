/** What a scan reports beside its matches: the TypeScript twin of Go's
 *  `textsearch.Tally`, structural so every reply that embeds it satisfies it.
 *  `scanned` is how many units the scan read, `matched` how many rows the list
 *  would hold had nothing cut it (the list is cut iff `matched` exceeds its
 *  length), `truncated` that the scan did not read everything it was asked to. */
export interface Tally {
  readonly scanned: number;
  readonly matched: number;
  readonly truncated: boolean;
}

/** Why an answer holds no rows. One member per no-rows answer, decided once by
 *  `classify`; `none` is the answer only when the scan finished and found
 *  nothing. */
export type EmptyState =
  /** Scan complete, nothing matched. */
  | { readonly kind: "none" }
  /** Nothing matched in what was read, and not everything was read. */
  | { readonly kind: "partial"; readonly scanned?: number }
  /** It matched, and is not shown here: filtered out, another tab, a collapsed
   *  section. `matched` counts the rows that matched and are not shown. */
  | { readonly kind: "withheld"; readonly matched: number; readonly where?: string }
  /** The source could not be asked. */
  | { readonly kind: "failed"; readonly retryAfterS?: number }
  /** The subject exists and could not be read. */
  | { readonly kind: "unreadable" }
  /** The query is under the surface's floor. */
  | { readonly kind: "tooShort"; readonly min: number };

/** What a surface knows about an empty answer, as data. The inputs are not
 *  exclusive; `classify` decides the order once. */
export interface EmptyFacts {
  readonly failed?: { readonly retryAfterS?: number };
  readonly unreadable?: boolean;
  /** The floor, when the query is under it. */
  readonly tooShort?: number;
  readonly matched: number;
  readonly shown: number;
  readonly where?: string;
  readonly scanned?: number;
  readonly truncated: boolean;
}

/** Precedence: failed > unreadable > tooShort > withheld > partial > none. */
export function classify(f: EmptyFacts): EmptyState {
  if (f.failed !== undefined) {
    return f.failed.retryAfterS === undefined
      ? { kind: "failed" }
      : { kind: "failed", retryAfterS: f.failed.retryAfterS };
  }
  if (f.unreadable === true) {
    return { kind: "unreadable" };
  }
  if (f.tooShort !== undefined) {
    return { kind: "tooShort", min: f.tooShort };
  }
  if (f.matched > f.shown) {
    const matched = f.matched - f.shown;
    return f.where === undefined
      ? { kind: "withheld", matched }
      : { kind: "withheld", matched, where: f.where };
  }
  if (f.truncated) {
    return f.scanned === undefined ? { kind: "partial" } : { kind: "partial", scanned: f.scanned };
  }
  return { kind: "none" };
}

/** One unit noun in both numbers: `{ one: "file", many: "files" }`. */
interface Noun {
  readonly one: string;
  readonly many: string;
}

/** The two unit nouns a note needs: what a match is, and what was scanned.
 *  Cross-chat passes `conversations` for both. */
export interface Nouns {
  readonly match: Noun;
  readonly scanned: Noun;
}

const CHARACTERS: Noun = { one: "character", many: "characters" };

function num(n: number): string {
  return n.toLocaleString("en-US");
}

function count(n: number, noun: Noun): string {
  return `${num(n)} ${n === 1 ? noun.one : noun.many}`;
}

function capitalize(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

/** The counter over a NAVIGABLE list: `here` is the 1-based cursor in it, `total`
 *  its length, `matched` the server's whole-chat count, rendered as `· N in chat`
 *  whenever it is passed. `3 of 12 · 347 in chat`. Whether the second figure is
 *  worth showing is the caller's call, made by passing it or not: the function
 *  renders two facts it was handed and infers nothing from their equality. An
 *  empty list is an `EmptyState` and `emptyNote`'s to render. */
export function cursorCount(here: number, total: number, matched?: number): string {
  const cursor = `${num(here)} of ${num(total)}`;
  return matched === undefined ? cursor : `${cursor} · ${num(matched)} in chat`;
}

/** The note beside an answer with rows: the cut when there is one, the scan's
 *  reach, and whether it read everything.
 *  `12 of 340 matches shown; 1,204 files scanned, not everything was read`. */
export function scanNote(tally: Tally, shown: number, nouns: Nouns): string {
  const matches =
    tally.matched > shown
      ? `${num(shown)} of ${count(tally.matched, nouns.match)} shown`
      : count(tally.matched, nouns.match);
  const read = tally.truncated ? ", not everything was read" : "";
  return `${matches}; ${count(tally.scanned, nouns.scanned)} scanned${read}`;
}

type Renderer<K extends EmptyState["kind"]> = (
  state: Extract<EmptyState, { kind: K }>,
  nouns: Nouns,
) => string;

const EMPTY_NOTE: { readonly [K in EmptyState["kind"]]: Renderer<K> } = {
  none: () => "No matches",
  partial: (s, nouns) =>
    s.scanned === undefined
      ? "No matches; not everything was searched"
      : `No matches in ${count(s.scanned, nouns.scanned)}; not everything was searched`,
  withheld: (s) =>
    s.where === undefined
      ? `${num(s.matched)} matched, not shown here`
      : `0 here, ${num(s.matched)} on ${s.where}`,
  failed: (s) =>
    s.retryAfterS === undefined
      ? "Could not search"
      : `Could not search; try again in ${num(s.retryAfterS)}s`,
  unreadable: (_s, nouns) => `${capitalize(nouns.scanned.one)} not read`,
  tooShort: (s) => `Type at least ${count(s.min, CHARACTERS)}`,
};

/** One sentence per `EmptyState`. */
export function emptyNote(state: EmptyState, nouns: Nouns): string {
  // The mapped type proves one renderer per kind; the lookup by a union key
  // loses that correlation, so the call is widened back to the union.
  return (EMPTY_NOTE[state.kind] as Renderer<EmptyState["kind"]>)(state, nouns);
}
