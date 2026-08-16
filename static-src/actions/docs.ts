// Actions for the Kiro configuration browser (/docs).
// ---------------------------------------------------------------------------
//
// ONE delete affordance covering all four categories — skill, steering, hook,
// agent — rather than four. They are all files under a scanned `.kiro` tree, the
// row already carries the path, and `POST /api/files/action` already refuses a
// granted mount root and a protected directory, so there is nothing per-category
// to decide.
//
// It routes through an action definition rather than a raw apiPost because
// actions/lint.test.ts fails on the latter, and because the shared framework is
// what gives this its idempotency key and its toast.

import { apiAction, retryNetwork, RETRY_STANDARD } from "./index.js";
import { join as joinKey } from "@cplieger/keyenc";

/** The file-surface action endpoint. Deliberately the same one the file browser
 *  deletes through: a `.kiro` document is an ordinary file, and a second delete
 *  path would be a second place for the mount-root and protected-directory
 *  refusals to drift out of. */
const API_FILES_ACTION = "/api/files/action";

/** Delete one `.kiro` document.
 *
 *  `path` is the row's own `doc.path` verbatim — the server builds it as
 *  `<workdir-without-leading-slash>/.kiro/...`, which is the same spelling
 *  `openFile` already hands the editor, so there is no path derivation here and
 *  no second copy of the prefix rule.
 *
 *  Cache invalidation needs no plumbing: the docs scan is memoized behind a
 *  signature of each category directory's mtime AND its entry names, and a delete
 *  changes the name set, so the next fetch rescans. */
export const deleteDoc = apiAction<{ path: string; name: string }>({
  name: "docs.delete",
  scope: (args) => "doc:" + args.path,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  idempotencyKey: (args) => joinKey("docs.delete", args.path),
  request: (args) => ({
    method: "POST",
    path: API_FILES_ACTION,
    body: { action: "delete", path: args.path },
  }),
  success: (args) => `Deleted ${args.name}`,
  error: (args) => `Couldn't delete ${args.name}`,
});
