/** Format a tool title for the collapsed-row activity line.
 *  Strips "Running: " prefix and truncates to 50 chars. */
export function formatToolActivity(title: string): string {
  const clean = title.startsWith("Running: ") ? title.slice(9) : title;
  return clean.length > 50 ? clean.slice(0, 47) + "\u2026" : clean;
}
