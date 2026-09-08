// How the 16px context indicator is DRAWN, DOM-free. Two halves in ONE unit:
// the wedge marks where KAS summarizes and the ramp warms toward it, both as a
// percentage of the window. Both contracts live in `vibekit-ui.md`, including
// why the ramp is not keyed on absolute tokens.

/** The percentage at which KAS 2.21.1 summarizes the conversation. Reached only
 *  by a chat whose session has never been loaded, since the streaming usage
 *  channel carries no threshold. */
export const KAS_SUMMARIZATION_PCT = 80;

/** The percentage at which KAS 2.21.1 truncates it, for the same fresh-chat
 *  case. */
export const KAS_TRUNCATION_PCT = 95;

/** The percentage the fill holds green up to, and the percentage from which it
 *  is fully red. Both sit below KAS_SUMMARIZATION_PCT, so red arrives BEFORE the
 *  compaction band rather than with it. */
export const CONTEXT_GREEN_PCT = 50;
export const CONTEXT_RED_PCT = 70;

/** Where the ramp hands off from green->yellow to yellow->red. Derived from the
 *  two thresholds rather than declared, so it is not a third number to keep in
 *  step with them. */
const CONTEXT_YELLOW_PCT = (CONTEXT_GREEN_PCT + CONTEXT_RED_PCT) / 2;

/** One decimal on an emitted mix: a hundredth of a point per step across a
 *  ten-point segment, so two nearby percentages resolve to different strokes. */
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

/** Tokens used at `pct` of a `contextSize`-token window: the expanded card's
 *  readout, and no longer an input to any colour. `contextSize` is 0 on every
 *  chat the wire has spoken for, so the card falls back to the percentage. */
export function tokensUsed(pct: number, contextSize: number): number {
  return (contextSize * clamp(pct, 0, 100)) / 100;
}

/** The fill's stroke at `pct` of the window: green up to `CONTEXT_GREEN_PCT`,
 *  then a continuous ramp through `--c-yellow`, saturating at `--c-red` from
 *  `CONTEXT_RED_PCT` rather than extrapolating.
 *
 *  Keyed on the percentage alone, so one number on screen resolves to one colour
 *  whatever the model's window is. */
export function contextStroke(pct: number): string {
  const clamped = clamp(pct, 0, 100);
  if (clamped <= CONTEXT_GREEN_PCT) {
    return "var(--c-green)";
  }
  if (clamped >= CONTEXT_RED_PCT) {
    return "var(--c-red)";
  }
  return clamped < CONTEXT_YELLOW_PCT
    ? mix(
        "--c-yellow",
        "--c-green",
        (clamped - CONTEXT_GREEN_PCT) / (CONTEXT_YELLOW_PCT - CONTEXT_GREEN_PCT),
      )
    : mix(
        "--c-red",
        "--c-yellow",
        (clamped - CONTEXT_YELLOW_PCT) / (CONTEXT_RED_PCT - CONTEXT_YELLOW_PCT),
      );
}
