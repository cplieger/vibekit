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
