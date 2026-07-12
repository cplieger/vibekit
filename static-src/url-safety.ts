// ---------------------------------------------------------------------------
// URL scheme safety guard.
//
// Shared by the MCP OAuth pill (mcp-ui.ts) and the open_external_url banner
// (handlers/open-external-url.ts + banner-stack.ts): before rendering a
// server-supplied URL as a clickable link, confirm it uses http/https so a
// file:, javascript:, data:, or custom app scheme can't be surfaced to the
// user. The server applies the same check before broadcasting; this is the
// client-side belt-and-braces guard.
// ---------------------------------------------------------------------------

/** Returns true when `url` parses and uses the http or https scheme. */
export function isSafeURL(url: string): boolean {
  try {
    const u = new URL(url);
    return u.protocol === "http:" || u.protocol === "https:";
  } catch {
    return false;
  }
}
