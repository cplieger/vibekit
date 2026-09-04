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

/** The two stable names the build emits. Content-hashed chunks are the third
 *  shape and are matched by prefix, since their names are unguessable by
 *  construction. Both halves of this contract are in different languages, so a
 *  test reads the Go writer's own literals against them. */
const STABLE_ASSETS: ReadonlySet<string> = new Set(["/app.js", "/style.css"]);
const CHUNK_PREFIX = "/chunks/";

/** Whether the shell cache may answer `pathname` at all.
 *
 *  A SYNCHRONOUS GATE, and that is the whole point: the worker has to decide
 *  whether to call `respondWith` before it can await anything, so asking the cache
 *  instead meant taking over every same-origin GET — `/api/*` reads and the
 *  `/api/events` stream included — to reach the answer. The cache still decides the
 *  RESPONSE, so a path this admits but the manifest never listed simply misses and
 *  goes to the network. */
export function isShellPath(pathname: string): boolean {
  return (
    STABLE_ASSETS.has(pathname) || (pathname.startsWith(CHUNK_PREFIX) && pathname.endsWith(".js"))
  );
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
