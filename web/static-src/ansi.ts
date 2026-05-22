// ANSI escape code → HTML renderer for agent terminal output.
// Uses ansi_up (vendored at build time, MIT license, zero deps).
// Configured with use_classes=true so styling is via CSS classes
// (CSP-clean, no inline styles).

import { AnsiUp } from "ansi_up";

const converter = new AnsiUp();
converter.use_classes = true;
converter.escape_html = true;

/**
 * Convert text containing ANSI escape codes to HTML with CSS classes.
 * Safe to call on text that has no ANSI codes (returns escaped HTML).
 */
export function ansiToHtml(text: string): string {
  return converter.ansi_to_html(text);
}
