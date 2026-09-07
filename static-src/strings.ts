// ---------------------------------------------------------------------------
// Tiny string utilities. Lives in a leaf module so any file can import them
// without pulling in modals.ts's modal machinery.
// ---------------------------------------------------------------------------

// Type-only, so it is erased at compile time and this module keeps no runtime
// edge. The alternative was restating TextSpan's five fields inline, twice —
// which is a second declaration of a wire shape the generator owns.
import type { TextSpan } from "./types.js";

/** HTML-escape a string for safe interpolation into innerHTML. */
export function escText(t: string): string {
  return t
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

/** HTML-escape for use inside attribute values (superset of escText). */
export function escAttr(t: string): string {
  return escText(t).replace(/'/g, "&#39;");
}

/** Humanize a kebab-case or snake_case name for display. */
export function humanName(s: string): string {
  return s.replace(/[-_]/g, " ");
}

/** Truncate a string to `max` characters with an ellipsis (…). */
export function truncate(s: string, max = 40): string {
  return s.length > max ? s.slice(0, max - 3) + "\u2026" : s;
}

/** How many lines each end of a windowed command output keeps. */
const OUTPUT_WINDOW_LINES = 20;

/** One source range kept by a window, and where it landed in the result. */
export interface KeptRange {
  /** Inclusive start offset in the source text. */
  from: number;
  /** Exclusive end offset in the source text. */
  to: number;
  /** Offset in the windowed text where this range begins. */
  at: number;
}

/** A windowed output: the text, how many lines it dropped, and the source
 *  ranges it kept so style spans can be mapped onto it. */
export interface OutputWindow {
  text: string;
  elided: number;
  kept: KeptRange[];
}

/** Window a command's output to its first and last N lines with a marker
 *  between, which is where the information is: a build's first lines say what it
 *  did and its last say how it ended. The middle is one click further (depth 2).
 *
 *  Returns the windowed text, how many lines it elided (0 when it all fit) so
 *  the caller can decide whether a depth 2 exists at all, and the source ranges
 *  it kept. The ranges exist because the result is two slices joined rather than
 *  one contiguous cut, so a style span cannot be carried across by subtracting a
 *  single offset. */
export function windowOutput(text: string, n = OUTPUT_WINDOW_LINES): OutputWindow {
  // Track each line's start offset while splitting, so the kept ranges are read
  // off the source rather than reconstructed by arithmetic over joined strings.
  const starts: number[] = [];
  const lines: string[] = [];
  let cursor = 0;
  for (;;) {
    const nl = text.indexOf("\n", cursor);
    if (nl === -1) {
      // A trailing newline leaves no final line, only an empty remainder.
      if (cursor < text.length) {
        starts.push(cursor);
        lines.push(text.slice(cursor));
      }
      break;
    }
    starts.push(cursor);
    lines.push(text.slice(cursor, nl));
    cursor = nl + 1;
  }
  if (lines.length <= n * 2) {
    return { text, elided: 0, kept: [{ from: 0, to: text.length, at: 0 }] };
  }

  // Indices are safe by the length check above (lines.length > n*2 implies both
  // n-1 and lines.length-n are in range), but read through a local so the
  // arithmetic is stated once and no non-null assertion is needed.
  const lastHead = n - 1;
  const firstTail = lines.length - n;
  const headFrom = 0;
  const headTo = (starts[lastHead] ?? 0) + (lines[lastHead] ?? "").length;
  const tailFrom = starts[firstTail] ?? 0;
  const tailTo = text.length;
  const head = text.slice(headFrom, headTo);
  const tail = text.slice(tailFrom, tailTo);
  return {
    text: head + "\n" + tail,
    elided: lines.length - n * 2,
    kept: [
      { from: headFrom, to: headTo, at: 0 },
      { from: tailFrom, to: tailTo, at: head.length + 1 },
    ],
  };
}

/** Map style spans onto a windowed text, clipping each to the kept ranges and
 *  rebasing it onto where that range landed. A span straddling the elision
 *  boundary yields one piece per side. */
export function windowSpans(spans: readonly TextSpan[], kept: readonly KeptRange[]): TextSpan[] {
  const out: TextSpan[] = [];
  for (const range of kept) {
    for (const span of spans) {
      const start = Math.max(span.start, range.from);
      const end = Math.min(span.end, range.to);
      if (end <= start) {
        continue;
      }
      out.push({
        ...span,
        start: start - range.from + range.at,
        end: end - range.from + range.at,
      });
    }
  }
  return out;
}

/** A wall-clock span, for a reader rather than a machine.
 *
 *  One tenth of a second below a minute, because a turn or a step that took 0.4s
 *  and one that took 4s read differently and the difference is the point; whole
 *  seconds above it, because nobody reads a tenth off "2m 31.4s". Zero returns
 *  "0.0s" rather than an empty string — a caller decides whether a zero span is
 *  worth showing, and every current one checks first.
 *
 *  Lives here rather than in the turn footer that used to own it because the run
 *  card states the same kind of value in three places (the run's clock, a step's
 *  duration, the ledger) and a second copy of the thresholds would drift. Pure
 *  text, so it is testable without a DOM. */
export function formatElapsed(ms: number): string {
  if (ms >= 3_600_000) {
    const h = Math.floor(ms / 3_600_000);
    const m = Math.floor((ms % 3_600_000) / 60_000);
    return `${String(h)}h ${String(m)}m`;
  }
  if (ms >= 60_000) {
    const m = Math.floor(ms / 60_000);
    const s = Math.floor((ms % 60_000) / 1000);
    return `${String(m)}m ${String(s)}s`;
  }
  return `${(ms / 1000).toFixed(1)}s`;
}

/** The same span as an ISO 8601 duration, for a `<time datetime>`.
 *
 *  Beside `formatElapsed` rather than in the footer that renders it, because the two
 *  are the machine and human spellings of ONE value and a `<time>` element is wrong
 *  unless they agree: `datetime` must be a machine-readable form of the element's
 *  own CONTENTS, not of some more precise value behind them.
 *
 *  So every component follows `formatElapsed`'s split exactly — a tenth of a second
 *  below a minute, whole seconds below an hour, and NO seconds at or above one,
 *  where the text reads `1h 1m`. Keeping the seconds up there made the attribute
 *  more precise than the words beside it (`PT1H1M1S` against `1h 1m`), which is the
 *  one thing this pairing exists to prevent.
 *
 *  `PT0.0S` for zero, which is a valid duration; a caller that does not want to
 *  show a zero span checks before asking. */
export function isoDuration(ms: number): string {
  const total = Math.max(0, ms);
  const hours = Math.floor(total / 3_600_000);
  const minutes = Math.floor((total % 3_600_000) / 60_000);
  const seconds = (total % 60_000) / 1000;
  let out = "PT";
  if (hours > 0) {
    out += `${String(hours)}H`;
  }
  if (minutes > 0) {
    out += `${String(minutes)}M`;
  }
  if (total >= 3_600_000) {
    // `1h 0m` is a whole hour spelled `PT1H`, and a duration with no seconds
    // component is still a conforming duration.
    return out;
  }
  // Whole seconds above a minute, a tenth below it — `formatElapsed`'s own split,
  // so `PT2M31S` sits beside "2m 31s" and `PT0.4S` beside "0.4s".
  if (seconds > 0 || out === "PT") {
    out += `${total >= 60_000 ? String(Math.floor(seconds)) : seconds.toFixed(1)}S`;
  }
  return out;
}
