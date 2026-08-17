// ---------------------------------------------------------------------------
// The workspace root, and the one conversion between the client's two path
// spaces.
//
// There are two, and both are legitimate:
//
//   RELATIVE  every path the agent supplies. translate.relPath strips the
//             workspace prefix server-side on purpose, so a turn's changed-file
//             ledger reads "hello.sh" and a tool card says "static-src/tabs.ts"
//             instead of leaking the container layout into the transcript.
//   ABSOLUTE  every path the file surface uses. /api/files lists
//             container-absolute paths, and /api/file resolves its `path`
//             against the granted-roots allow-list, which has no notion of a
//             relative path and denies one as outside every root.
//
// Opening a changed file crosses from the first space into the second, and the
// client had no way to make the crossing: it does not know where the workspace
// is, and inventing the answer is forbidden — a hardcoded "/workspace" is wrong
// the moment the mount changes, and the file browser genuinely spans several
// granted mounts. So the server states it once, on the SSE `connected`
// handshake, and this module is the only holder.
//
// It is deliberately ONE conversion in ONE place. The last time this rule got
// copied, git-status-store.ts built its index keys as "<repoName>/<relPath>"
// and then looked them up with absolute paths, so no key ever matched and the
// file browser's git-status letters were silently dead for every file.
// ---------------------------------------------------------------------------

/** Absolute workspace root, or "" until the handshake arrives. */
let root = "";

/** Consumers that derive something from the root and must recompute when it
 *  lands. See onWorkspaceRoot for why this exists at all. */
const listeners = new Set<() => void>();

/** Record the workspace root from the `connected` handshake.
 *
 *  A no-op when the value is unchanged, which is every reconnect: the handshake
 *  repeats on each one, and a listener that rebuilds an index must not be woken
 *  by a frame that told it nothing new. */
export function setWorkspaceRoot(abs: string): void {
  if (abs === root) {
    return;
  }
  root = abs;
  for (const fn of [...listeners]) {
    fn();
  }
}

/** The absolute workspace root, or "" when the handshake has not landed. */
export function workspaceRoot(): string {
  return root;
}

/** Subscribe to the root becoming known. Returns an unsubscribe.
 *
 *  Needed because the handshake and the consumers race, with no ordering
 *  between them: the SSE connection opens during boot, but `pollAction` fires
 *  its first tick synchronously on start, so a status poll can resolve before
 *  the `connected` frame is dispatched. An index built in that window is keyed
 *  in the wrong space and would stay wrong until the next poll — 15 seconds of
 *  a feature silently doing nothing. Subscribing removes the ordering question
 *  instead of betting on it. */
export function onWorkspaceRoot(fn: () => void): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

/** Resolve a path into the ABSOLUTE space the file surface uses.
 *
 *  A path that is already absolute is returned unchanged, so a caller holding a
 *  file-browser path needs no branch of its own. A relative path is joined onto
 *  the workspace root.
 *
 *  Before the handshake the root is unknown and the input is returned as-is:
 *  the request then fails exactly as it did before, rather than being silently
 *  rewritten into a path that names something else. */
export function absPath(path: string): string {
  if (path === "" || path.startsWith("/") || root === "") {
    return path;
  }
  return `${root}/${path}`;
}

/** Strip the workspace root from an absolute path, giving the RELATIVE form the
 *  agent's own paths use. Returns the input unchanged when it is not under the
 *  workspace (another granted mount, or the root is still unknown), which is
 *  what keeps a /config path addressable rather than mangled.
 *
 *  The separator test is what stops a sibling mount whose name merely begins
 *  with the root ("/workspace-old/x") reporting as "-old/x". */
export function relToWorkspace(abs: string): string {
  if (root === "" || !abs.startsWith(root + "/")) {
    return abs;
  }
  return abs.slice(root.length + 1);
}

/** @internal Test seam: reset the root between cases.
 *
 *  Deliberately does NOT clear the listeners and does NOT notify them. They are
 *  module wiring rather than state — clearing them would silently switch off the
 *  behaviour a test may be about to assert — and a reset is a test's bookkeeping,
 *  not a claim that the workspace moved. */
export function _resetForTest(): void {
  root = "";
}
