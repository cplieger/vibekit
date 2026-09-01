// ---------------------------------------------------------------------------
// Actions: editor + diff pane user-initiated mutations.
// ---------------------------------------------------------------------------

import { apiAction, defineAction, ActionError, retryNetwork, RETRY_STANDARD } from "./index.js";

import { routeForPath } from "../editor-types.js";

/** `internal/git.KindNotInRepo`: no discovered repository owns the path, so
 *  there is no committed revision to show. Not a failure — a file outside
 *  every repo has no "before", and rendering it as an all-add diff is
 *  correct. */
const GIT_ERR_NOT_IN_REPO = "not_in_repo";

/** `/api/file` statuses that are answers about a changed file, not failures:
 *  404 means the working copy is gone (that IS the change); 415 means
 *  binary (no text diff exists). Both are ordinary members of a `git
 *  status` list. */
const HTTP_NOT_FOUND = 404;
const HTTP_BINARY = 415;

/** Saves the active editor file (PUT). No auto-retry: content is captured
 *  at dispatch time, and a retry after further edits would overwrite them
 *  with the stale snapshot. The manual Retry button stays for a conscious
 *  re-save. */
export interface SaveFileResult {
  ok?: boolean;
  error?: string;
  /** Present only on a refused stale write: the file's current content, so
   *  the caller can show the diff rather than telling the user to reload. */
  content?: string;
  content_hash?: string;
}

export const saveFile = apiAction<
  { path: string; content: string; expectedHash?: string },
  SaveFileResult
>({
  name: "editor.save_file",
  scope: (args) => "file:" + args.path,
  retryable: retryNetwork,
  request: ({ path, content, expectedHash }) => ({
    method: "PUT",
    path: routeForPath(path).writeURL,
    // expected_hash is the digest the file had when this buffer loaded it;
    // the server refuses with 409 when it has changed since.
    body: expectedHash === undefined ? { content } : { content, expected_hash: expectedHash },
  }),
  // A 409 carries the current content; recovered as a success payload so
  // the caller can branch on `error` and show the difference.
  decodeError: (info) =>
    info.status === 409 ? { kind: "success", value: info.body ?? {} } : undefined,
  error: false,
});

// There is no per-hunk resolve action: KAS decides per action, not per hunk.

/** Requests AI conflict resolution. No retry (not idempotent). */
export const suggestResolution = apiAction<
  { ours: string; theirs: string; context: string },
  { output?: string; error?: string }
>({
  name: "editor.suggest_resolution",
  // No dedupe: editor-conflict.ts's per-file generation counter already
  // handles supersession.
  request: (body) => ({ method: "POST", path: "/api/utility/resolve-conflict", body }),
  error: false,
});

/** Fetches agent-modified line ranges for gutter highlighting. */
export const fetchAgentLines = apiAction<
  { chatID: string; path: string },
  { changes: { start_line: number; end_line: number }[] }
>({
  name: "editor.fetch_agent_lines",
  dedupe: (args) => JSON.stringify([args.chatID, args.path]),
  request: ({ chatID, path }) => ({
    method: "GET",
    path: `/api/file-changes?chat_id=${encodeURIComponent(chatID)}&path=${encodeURIComponent(path)}`,
  }),
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  error: false,
});

/** Fetches git diff sources for the editor diff view.
 *
 *  The two endpoints speak different path languages: `/api/file` wants
 *  container-absolute (against the granted-roots allow-list), `/api/git/show`
 *  wants repo-relative or workspace-relative with no `repo`. `path` arrives
 *  absolute; each side gets the form it accepts.
 *
 *  `baseLabel`/`workingLabel` are claims about what each pane holds:
 *  `not_in_repo` means git owns no revision, so labelling the pane "HEAD"
 *  would claim HEAD has it and is empty; "deleted" vs "working tree" makes
 *  the same distinction on the right pane. */
export const loadDiff = defineAction<
  { path: string; repo: string; ref: string },
  { oldContent: string; newContent: string; error: string; baseLabel: string; workingLabel: string }
>({
  name: "editor.load_diff",
  retryable: retryNetwork,
  run: async ({ path, repo, ref }, signal) => {
    const { apiGet, apiGetOrError } = await import("../api-client.js");
    const { relToWorkspace } = await import("../workspace.js");
    const repoParam = repo !== "" ? `&repo=${encodeURIComponent(repo)}` : "";
    // With an explicit repo the path is already repo-relative; with none,
    // the server resolves the owner from a workspace-relative path.
    const gitPath = repo !== "" ? path : relToWorkspace(path);
    // apiGetOrError because two of the working-copy's failure statuses are
    // answers (HTTP_NOT_FOUND / HTTP_BINARY above), not dead requests.
    const [oldD, newD] = await Promise.all([
      apiGet<{ content?: string; error?: string; detail?: string }>(
        `/api/git/show?path=${encodeURIComponent(gitPath)}&ref=${encodeURIComponent(ref)}${repoParam}`,
        signal,
      ),
      apiGetOrError<{ content?: string; error?: string }>(
        `/api/file?path=${encodeURIComponent(path)}`,
        signal,
      ),
    ]);
    if (signal.aborted) {
      throw new ActionError("cancelled", { code: "cancelled" });
    }
    const deleted = newD.status === HTTP_NOT_FOUND;
    const binary = newD.status === HTTP_BINARY;
    // Each side is named — "one of them failed" is not actionable.
    if (!newD.ok && !deleted && !binary) {
      throw new ActionError(`Could not read the working copy of ${gitPath}`, { code: "network" });
    }
    if (oldD === null) {
      throw new ActionError(`Could not read ${ref} for ${gitPath}`, { code: "network" });
    }
    const gitErr = oldD.error ?? "";
    if (gitErr !== "" && gitErr !== GIT_ERR_NOT_IN_REPO) {
      throw new ActionError(`git could not read ${ref} for ${gitPath}: ${oldD.detail ?? gitErr}`, {
        code: "network",
      });
    }
    return {
      oldContent: oldD.content ?? "",
      newContent: newD.data?.content ?? "",
      error: binary
        ? `${gitPath} is a binary file — there is no text diff to show.`
        : (newD.data?.error ?? ""),
      baseLabel: gitErr === GIT_ERR_NOT_IN_REPO ? "not in git" : ref,
      workingLabel: deleted ? "deleted" : "working tree",
    };
  },
  error: "Could not load diff",
});
