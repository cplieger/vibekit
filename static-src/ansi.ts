// ANSI escape code → HTML renderer for agent terminal output.
// Uses ansi_up (vendored at build time, MIT license, zero deps).
// Configured with use_classes=true so styling is via CSS classes
// (CSP-clean, no inline styles).

import { AnsiUp } from "ansi_up";

/** A stateful ANSI→HTML converter. Open SGR state (colours, bold, …) carries
 *  across calls, so one instance renders one logical output stream. Concurrent
 *  streams (e.g. several agent terminals) must NOT share an instance or their
 *  SGR state bleeds together — give each stream its own converter. */
export interface AnsiConverter {
  /** Convert a chunk of (possibly ANSI-coded) text to HTML with CSS classes.
   *  Safe on plain text (returns HTML-escaped output). */
  toHtml(text: string): string;
}

/** Create an independent ANSI→HTML converter with its own SGR state. Styling
 *  is emitted as CSS classes (`use_classes`) and HTML is escaped
 *  (`escape_html`). Give each concurrent output stream its own converter. */
export function createAnsiConverter(): AnsiConverter {
  const converter = new AnsiUp();
  converter.use_classes = true;
  converter.escape_html = true;
  return {
    toHtml(text: string): string {
      return converter.ansi_to_html(text);
    },
  };
}

// Shared converter for non-concurrent, one-shot callers (tool cards, message
// tool output): a single logical stream per call site, so shared SGR state is
// fine. Anything rendering multiple concurrent streams must instead create its
// own via createAnsiConverter().
const sharedConverter = createAnsiConverter();

/**
 * Convert text containing ANSI escape codes to HTML with CSS classes.
 * Safe to call on text that has no ANSI codes (returns escaped HTML).
 * Uses a process-shared converter; for isolated SGR state (concurrent
 * streams) use createAnsiConverter() instead.
 */
export function ansiToHtml(text: string): string {
  return sharedConverter.toHtml(text);
}
