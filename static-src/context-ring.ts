// How the 16px context indicator is DRAWN, DOM-free. Two halves whose UNITS
// differ on purpose: the wedge is a percentage, the ramp is absolute tokens.
// Both contracts, and the research under the ramp, live in `vibekit-ui.md`.

/** The percentage at which KAS 2.21.1 summarizes the conversation. Reached only
 *  by a chat whose session has never been loaded, since the streaming usage
 *  channel carries no threshold. */
export const KAS_SUMMARIZATION_PCT = 80;

/** The percentage at which KAS 2.21.1 truncates it, for the same fresh-chat
 *  case. */
export const KAS_TRUNCATION_PCT = 95;

/** Tokens used past which recall of early context starts to fade, and past
 *  which it is materially degraded. Tunable; the research behind the two
 *  figures is in `vibekit-ui.md`. */
export const CONTEXT_FADING_TOKENS = 100_000;
export const CONTEXT_DEGRADED_TOKENS = 200_000;

/** Where the fading threshold sits between 0 and the degraded one, reused as
 *  the split for the percentage fallback so that path invents no second pair of
 *  numbers. */
const FALLBACK_SPLIT_PCT = (CONTEXT_FADING_TOKENS / CONTEXT_DEGRADED_TOKENS) * 100;

/** One decimal on an emitted mix: 100 tokens per step in the first segment, so
 *  two nearby token counts resolve to different strokes. */
const MIX_DECIMALS = 1;

function clamp(n: number, lo: number, hi: number): number {
  return Math.min(hi, Math.max(lo, n));
}

/** `color-mix()` in oklch, the house idiom for a derived colour. Both operands
 *  are token names, never literals, so the ramp is theme-correct for free. */
function mix(toToken: string, fromToken: string, ratio: number): string {
  const pct = (clamp(ratio, 0, 1) * 100).toFixed(MIX_DECIMALS);
  return `color-mix(in oklch, var(${toToken}) ${pct}%, var(${fromToken}))`;
}

/** The dash pattern for the band running from `thresholdPct` round to 100%.
 *
 *  The pattern period is exactly 100, so it cannot repeat inside the path: the
 *  leading dash falls off the start, the gap covers 0..T and the second dash
 *  covers T..100. */
export function wedgeDash(thresholdPct: number): { dasharray: string; dashoffset: string } {
  const band = 100 - clamp(thresholdPct, 0, 100);
  return { dasharray: `${String(band)} ${String(100 - band)}`, dashoffset: String(band) };
}

/** Tokens used at `pct` of a `contextSize`-token window: the ramp's input, and
 *  the same number the expanded card reads out. */
export function tokensUsed(pct: number, contextSize: number): number {
  return (contextSize * clamp(pct, 0, 100)) / 100;
}

/** The fill's stroke for `pct` of a `contextSize`-token window, saturating at
 *  `--c-red` past the degraded threshold rather than extrapolating.
 *
 *  Keyed on tokens USED, so one percentage of a 200K window and of a 1M window
 *  resolve differently. A `contextSize` of 0 means the window is unknown and the
 *  ramp runs over `pct`: the wrong unit, and the only one available. */
export function contextStroke(pct: number, contextSize: number): string {
  const clamped = clamp(pct, 0, 100);
  if (contextSize <= 0) {
    return clamped < FALLBACK_SPLIT_PCT
      ? mix("--c-yellow", "--c-green", clamped / FALLBACK_SPLIT_PCT)
      : mix("--c-red", "--c-yellow", (clamped - FALLBACK_SPLIT_PCT) / (100 - FALLBACK_SPLIT_PCT));
  }
  const tokens = tokensUsed(pct, contextSize);
  if (tokens >= CONTEXT_DEGRADED_TOKENS) {
    return "var(--c-red)";
  }
  if (tokens < CONTEXT_FADING_TOKENS) {
    return mix("--c-yellow", "--c-green", tokens / CONTEXT_FADING_TOKENS);
  }
  return mix(
    "--c-red",
    "--c-yellow",
    (tokens - CONTEXT_FADING_TOKENS) / (CONTEXT_DEGRADED_TOKENS - CONTEXT_FADING_TOKENS),
  );
}
