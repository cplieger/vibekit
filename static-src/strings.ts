// ---------------------------------------------------------------------------
// Tiny string utilities. Lives in a leaf module so any file can import them
// without pulling in modals.ts's modal machinery.
// ---------------------------------------------------------------------------

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

/** Window a command's output to its first and last N lines with a marker
 *  between, which is where the information is: a build's first lines say what it
 *  did and its last say how it ended. The middle is one click further (depth 2).
 *
 *  Returns the windowed text plus how many lines it elided (0 when it all fit),
 *  so the caller can decide whether a depth 2 exists at all. */
export function windowOutput(
  text: string,
  n = OUTPUT_WINDOW_LINES,
): { text: string; elided: number } {
  const lines = text.split("\n");
  // Trailing newline produces a final empty element that is not a line.
  if (lines.length > 0 && lines[lines.length - 1] === "") {
    lines.pop();
  }
  if (lines.length <= n * 2) {
    return { text, elided: 0 };
  }
  const head = lines.slice(0, n);
  const tail = lines.slice(-n);
  const elided = lines.length - head.length - tail.length;
  return { text: head.join("\n") + "\n" + tail.join("\n"), elided };
}
