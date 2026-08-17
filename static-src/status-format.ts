export function formatTokens(n: number): string {
  if (n >= 1_000_000) {
    return `${(n / 1_000_000).toFixed(1)}M`;
  }
  if (n >= 1_000) {
    return `${(n / 1_000).toFixed(1)}K`;
  }
  return String(n);
}

export function formatMetering(v: number): string {
  if (v >= 1_000_000) {
    return `${(v / 1_000_000).toFixed(2)}M`;
  }
  if (v >= 1_000) {
    return `${(v / 1_000).toFixed(1)}K`;
  }
  if (Number.isInteger(v)) {
    return String(v);
  }
  return v.toFixed(2);
}

/** The context ring's compaction reserve, derived from kiro-cli's own
 *  `compaction.excludeContextWindowPercent`.
 *
 *  Pure, and here rather than in status.ts, for the same reason `windowOutput`
 *  is not in the DOM builder: a test can only reach a method on the context-bar
 *  controller through a DOM fixture and a mock of the settings fetch, and the
 *  thing worth pinning is the arithmetic.
 *
 *  `buffer` is the percentage of the window kiro-cli holds BACK, so the
 *  threshold — the point auto-compaction fires — is `100 - buffer` and the
 *  reserve is the slice from there round to 12 o'clock. Its length in percent is
 *  therefore the buffer itself; what the SVG needs on top of that is where to
 *  START, which is a rotation.
 *
 *  `rotateDeg` is offset by -90 because the ring's 0% is 12 o'clock while SVG's
 *  0deg is 3 o'clock — the same -90 the usage pie carries as a static attribute.
 *
 *  The CLAMP is not decoration. `fetchKiroSetting` accepts [0, 100] while the
 *  settings input caps at [0, 50], so a value above 50 cannot be typed here but
 *  can arrive from kiro-cli's own config, the TUI, or a hand-edited settings
 *  file. Both ends have to render: `buffer` 0 gives a zero-length dash, which
 *  paints nothing under butt caps (a round cap would leave a dot at 12 o'clock,
 *  claiming a reserve that does not exist), and `buffer` 100 gives a
 *  full-circle dash with the threshold at 0%. */
export function contextReserve(buffer: number): {
  thresholdPct: number;
  lengthPct: number;
  rotateDeg: number;
} {
  const clamped = Math.max(0, Math.min(100, buffer));
  const thresholdPct = 100 - clamped;
  return {
    thresholdPct,
    lengthPct: clamped,
    rotateDeg: (thresholdPct / 100) * 360 - 90,
  };
}
