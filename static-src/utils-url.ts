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

/** Image extensions the inline-image rewrite will point at the file route.
 *  A closed list, because the rewrite exists to render pictures — sending an
 *  arbitrary workspace path through it would turn any `![](…)` the model writes
 *  into a file read. */
const INLINE_IMAGE_EXT = /\.(png|jpe?g|gif|webp|svg|avif)$/i;

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
  if (!trimmed.startsWith("/workspace/") || !INLINE_IMAGE_EXT.test(trimmed)) {
    return src;
  }
  return `/api/file/download?path=${encodeURIComponent(trimmed)}`;
}
