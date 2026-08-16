// ---------------------------------------------------------------------------
// URL safety utilities.
// ---------------------------------------------------------------------------

import { isViewableImage } from "./file-extensions.js";

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

/** The route that serves a workspace file's BYTES.
 *
 *  `/api/file` returns a JSON `{content}` envelope and refuses a binary with a
 *  415 (a NUL in the first 8 KiB), so it can never serve a picture. This one
 *  streams through the mount's confined `os.Root` with `Content-Disposition:
 *  attachment` — which is a SECURITY control, not a convenience: the response
 *  carries `Content-Type: image/svg+xml` for an `.svg`, and that is
 *  script-capable if it is ever NAVIGATED to rather than rendered in an `<img>`.
 *  Never hand this URL to an anchor the user is invited to open in a tab, an
 *  `<iframe>`, or `window.open`. */
export function fileDownloadURL(path: string): string {
  return `/api/file/download?path=${encodeURIComponent(path)}`;
}

/** Rewrite a workspace-absolute image `src` to the byte-serving file route.
 *
 *  The agent can already produce a PNG — it drives the Chromium sidecar — and
 *  writes `![shot](/workspace/out/shot.png)`. The markdown renderer emits that
 *  `src` verbatim, the browser asks the SPA for `/workspace/out/shot.png`, and
 *  the SPA fallback answers with index.html: a broken image every time. So the
 *  agent had better sight of its own artefacts than the operator did.
 *
 *  `/api/file/download` is the route that serves BYTES; `/api/file` returns JSON
 *  and would render nothing. Anything not workspace-rooted with an image
 *  extension is returned untouched, so ordinary remote images and links are
 *  unaffected.
 */
export function rewriteWorkspaceImageSrc(src: string): string {
  const trimmed = src.trim();
  if (!trimmed.startsWith("/workspace/") || !isViewableImage(trimmed)) {
    return src;
  }
  return fileDownloadURL(trimmed);
}
