// ---------------------------------------------------------------------------
// The shell precache's two pure decisions: which manifest documents are usable,
// and which request paths the cache may answer.
//
// Here rather than in sw.ts so they can be driven: sw.ts is coverage-excluded (it
// runs in ServiceWorkerGlobalScope). The worker still ships as one classic script
// — cmd/bundle bundles it with esbuild, IIFE — so this import costs it no syntax.
// ---------------------------------------------------------------------------

/** The build-emitted asset list (cmd/bundle writePrecacheManifest). `stamp` moves
 *  when any listed asset's bytes or name move; `assets` are root-relative URL
 *  paths, each already carrying its leading slash. */
export interface PrecacheManifest {
  readonly stamp: string;
  readonly assets: readonly string[];
}

/** The one name shape whose bytes its own name pins: esbuild's
 *  `chunks/[name]-[hash]`, the hash 8 uppercase base32 characters.
 *  `contentHashedAsset` (internal/server/server_static.go) is the same regex for
 *  the same reason, and a test reads cmd/bundle's template — a looser test would
 *  admit a name a release replaces, and this one degrades to the network if that
 *  template ever changes. */
const CONTENT_HASHED_CHUNK = /^\/chunks\/[^/]+-[A-Z0-9]{8}\.js$/u;

/** Whether the shell cache may answer `pathname`.
 *
 *  Content-hashed names only: the server marks `/app.js` and `/style.css`
 *  `no-cache` because a release replaces their bytes under those names, so a
 *  cache-first answer for either pairs a fresh index.html with the previous
 *  build's bundle. And SYNCHRONOUS, because `respondWith` has to be decided before
 *  anything can be awaited — asking the cache instead meant taking over every
 *  same-origin GET, `/api/events` included. */
export function isShellPath(pathname: string): boolean {
  return CONTENT_HASHED_CHUNK.test(pathname);
}

/** The manifest a document just served, or null when it is unusable.
 *
 *  Every field is checked rather than cast — a half-written or foreign document
 *  must leave the existing cache alone rather than emptying it — and an asset name
 *  is rejected outright if it is absolute or climbs, so the caller cannot be
 *  talked into caching a path off the build's own output. */
export function parseManifest(d: unknown): PrecacheManifest | null {
  if (typeof d !== "object" || d === null) {
    return null;
  }
  const rec = d as Record<string, unknown>;
  const stamp = rec["stamp"];
  const assets = rec["assets"];
  if (typeof stamp !== "string" || stamp === "" || !Array.isArray(assets)) {
    return null;
  }
  const paths: string[] = [];
  for (const a of assets as unknown[]) {
    if (typeof a !== "string" || a === "" || a.startsWith("/") || a.includes("..")) {
      return null;
    }
    paths.push(`/${a}`);
  }
  return { stamp, assets: paths };
}
