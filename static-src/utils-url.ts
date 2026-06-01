// ---------------------------------------------------------------------------
// URL safety utilities.
// ---------------------------------------------------------------------------

/** URL safety predicate: blocks javascript:, vbscript:, data:, file: schemes.
 *  Strips internal whitespace before checking to prevent bypass via embedded
 *  tabs/newlines (e.g. "java\tscript:alert(1)"). */
export function isSafeUrl(url: string): boolean {
  const lower = url
    .trim()
    // eslint-disable-next-line no-control-regex
    .replace(/[\t\n\r\x00]/g, "")
    .toLowerCase();
  return !(
    lower.startsWith("javascript:") ||
    lower.startsWith("vbscript:") ||
    lower.startsWith("data:") ||
    lower.startsWith("file:")
  );
}
