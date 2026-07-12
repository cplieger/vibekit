// ---------------------------------------------------------------------------
// Chat export: trigger a browser download of a chat's transcript.
//
// The server renders the persisted chat — vibekit's canonical chat store, no
// live ACP bridge — to Markdown (default) or raw JSON at
// GET /api/chats/{id}/export?format=md|json, setting Content-Disposition.
//
// We trigger the download with a transient same-origin anchor (the browser's
// native download path). A fetch would go through api-client.ts, which
// discards the body on non-2xx and can't stream a file to disk; a direct
// anchor is the right primitive for a GET that returns an attachment, and it
// carries the session cookie automatically, so no auth plumbing is needed.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";

type ExportFormat = "md" | "json";

const EXT: Record<ExportFormat, string> = { md: ".md", json: ".json" };

/** Build the export endpoint URL for a chat + format. */
function chatExportURL(chatID: string, format: ExportFormat): string {
  return `/api/chats/${encodeURIComponent(chatID)}/export?format=${format}`;
}

/** Strip control chars and filesystem-/header-unsafe punctuation, then trim. */
function sanitizeFilenamePart(s: string): string {
  // eslint-disable-next-line no-control-regex -- intentionally scrubbing control chars from a filename
  return s.replace(/[\x00-\x1f\x7f"\\/:*?<>|]/g, "_").trim();
}

/** Build a filesystem-safe download filename: "<name>-<id><ext>", falling
 *  back to "<id><ext>" (empty name) or "chat<ext>" (both empty). Mirrors the
 *  server's exportFilename so the downloaded name is predictable even when a
 *  browser prefers the anchor's download attribute over Content-Disposition. */
function exportFilename(name: string, chatID: string, format: ExportFormat): string {
  const ext = EXT[format];
  let stem = sanitizeFilenamePart(name);
  const runes = Array.from(stem);
  if (runes.length > 80) {
    stem = runes.slice(0, 80).join("").trim();
  }
  const id = sanitizeFilenamePart(chatID);
  if (stem === "" && id === "") {
    return `chat${ext}`;
  }
  if (stem === "") {
    return `${id}${ext}`;
  }
  if (id === "") {
    return `${stem}${ext}`;
  }
  return `${stem}-${id}${ext}`;
}

/** Trigger a browser download of chatID's transcript in the given format
 *  (default Markdown). Uses a transient same-origin anchor click. No-op for
 *  an empty chatID. */
export function downloadChatExport(
  chatID: string,
  name: string,
  format: ExportFormat = "md",
): void {
  if (chatID === "") {
    return;
  }
  const a = el("a", {
    href: chatExportURL(chatID, format),
    download: exportFilename(name, chatID, format),
    rel: "noopener",
  });
  document.body.appendChild(a);
  a.click();
  a.remove();
}
