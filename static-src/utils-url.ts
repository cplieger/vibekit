// ---------------------------------------------------------------------------
// URL safety utilities.
// ---------------------------------------------------------------------------

import { isViewableImage } from "./file-extensions.js";

/** URL safety predicate for a rendered href/src: http, https and mailto are the
 *  only allowed absolute schemes, and a scheme-less value stays allowed because
 *  the browser resolves it against the document's own HTTP(S) location.
 *
 *  Strips every C0 control, then trims — at least what the WHATWG URL parser
 *  strips before it reads a scheme. Normalize less and `\x01javascript:` reaches
 *  the browser as a live scheme; run the trim first and a control between two
 *  spaces survives both passes. */
export function isSafeUrl(url: string): boolean {
  const cleaned = url
    // eslint-disable-next-line no-control-regex
    .replace(/[\x00-\x1f]/g, "")
    .trim()
    .toLowerCase();
  const scheme = /^[a-z][a-z0-9+.-]*:/.exec(cleaned)?.[0];
  return scheme === undefined || scheme === "http:" || scheme === "https:" || scheme === "mailto:";
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
