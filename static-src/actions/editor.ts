// ---------------------------------------------------------------------------
// Actions: editor + diff pane user-initiated mutations.
// ---------------------------------------------------------------------------

import { apiAction, defineAction, ActionError, retryNetwork, RETRY_STANDARD } from "./index.js";

import { routeForPath } from "../editor-types.js";

/** `internal/git.KindNotInRepo` — no discovered repository owns the path, so
 *  there is no committed revision of it to show. NOT a failure: a file in the
 *  workspace but outside every repo simply has no "before", and rendering it as
 *  an all-add diff is correct. It is a separate kind precisely so a real git
 *  failure cannot render as "this file is brand new". */
const GIT_ERR_NOT_IN_REPO = "not_in_repo";

/** `/api/file` statuses that are ANSWERS about a changed file rather than
 *  failures to read one:
 *
 *    404  the working copy is gone. That IS the change — a deletion — and the
 *         diff is every line of the base removed.
 *    415  the file is binary (filebrowse sniffs for a NUL), so no text diff
 *         exists and the base blob must not be rendered as if it were source.
 *
 *  Both are ordinary members of a `git status` list, which is why they are read
 *  rather than collapsed: `apiGet` maps every non-2xx to null, so a click on a
 *  deleted file reported "Could not read the working copy" and showed nothing. */
const HTTP_NOT_FOUND = 404;
const HTTP_BINARY = 415;

/** Save the active editor file (PUT). Inline error surface in the editor pane;
 *  framework toast suppressed.
 *
 *  No auto-retry: content is captured at dispatch time. If the user types
 *  more after the first attempt fails, a retry would overwrite their new
 *  edits with the stale snapshot. The manual Retry button (retryable:
 *  "network") is kept so the user can consciously re-save, but auto-retry
 *  is intentionally omitted. */
export interface SaveFileResult {
  ok?: boolean;
  error?: string;
  /** Present only on a refused stale write: the file's CURRENT content, so the
   *  caller can show what changed rather than telling the user to reload. */
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
    // expected_hash is the digest the file had when this buffer loaded it. The
    // server refuses the write with 409 when the file changed since, which is
    // the routine case here rather than an exotic one: the agent writes to the
    // same tree the editor reads.
    body: expectedHash === undefined ? { content } : { content, expected_hash: expectedHash },
  }),
  // A 409 is the stale-write refusal, and its body carries the current content.
  // Resolved as a SUCCESS payload (the same seam tools.ts uses for its
  // has_dependents cascade) so the caller can branch on `error` and show the
  // difference; every other status keeps the default error mapping.
  decodeError: (info) =>
    info.status === 409 ? { kind: "success", value: info.body ?? {} } : undefined,
  error: false,
});

// There is no per-hunk resolve action. KAS decides per ACTION, not per hunk
// (a multi-file rename shares one toolCallId), so a merged-text reply had
// nowhere to go. Editing after approval is the replacement.

/** Request AI conflict resolution suggestion. Inline error; no retry (not idempotent). */
export const suggestResolution = apiAction<
  { ours: string; theirs: string; context: string },
  { output?: string; error?: string }
>({
  name: "editor.suggest_resolution",
  // No dedupe: the per-file suggestionGen counter in editor-conflict.ts
  // already handles supersession (only the latest dispatch's result is
  // rendered). dedupe would also collapse same-args calls into one
  // promise, but rapid clicks on the same hunk are guarded earlier by
  // requestSuggestion's `existing?.loading` check.
  request: (body) => ({ method: "POST", path: "/api/utility/resolve-conflict", body }),
  error: false,
});

/** Fetch agent-modified line ranges for gutter highlighting. Retry on network failure. */
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

/** Fetch git diff sources for the editor diff view. Toast on failure.
 *
 *  The two sides speak different path languages, and conflating them broke this
 *  action for every file, in both directions:
 *
 *    /api/file      container-ABSOLUTE, resolved against the granted-roots
 *                   allow-list. A relative path is denied 403.
 *    /api/git/show  repo-relative, or workspace-relative with no `repo`, which
 *                   the server then resolves to the owning repository.
 *                   validateFilePath REFUSES a leading "/", so an absolute path
 *                   is a 400.
 *
 *  `path` arrives absolute (the editor's own form, and what the seam in
 *  navigate.ts now guarantees), and each side is given the form it accepts. One
 *  spelling sent to both meant whichever endpoint disagreed returned null and the
 *  whole diff failed: an agent-supplied relative path failed on /api/file, and a
 *  browser-supplied absolute one failed on /api/git/show.
 *
 *  `baseLabel` is returned because the left pane's caption is a claim about what
 *  it holds: `not_in_repo` means git owns no revision of this file, so labelling
 *  the empty pane "HEAD" would assert that HEAD has it and is empty. That
 *  distinction is also why a missing base is NOT an error here — a file outside
 *  every repo renders correctly as an all-add diff, and only a real git failure
 *  earns the error surface.
 *
 *  `workingLabel` is the same claim about the RIGHT pane, and it exists for the
 *  same reason: an empty pane captioned "working tree" says the file is there and
 *  empty, where "deleted" says it is gone. Each caller sets a placeholder before
 *  the fetch so the pane has a caption while loading; both labels are overwritten
 *  by what the load FOUND. */
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
    // With an explicit repo the caller already holds the repo-relative path; with
    // none, the server resolves the owner from a workspace-relative one.
    const gitPath = repo !== "" ? path : relToWorkspace(path);
    // The working copy goes through apiGetOrError because two of its failure
    // STATUSES are answers (see HTTP_NOT_FOUND / HTTP_BINARY above) and the
    // collapsing helper cannot tell them from a dead request. The cost is that a
    // genuine failure no longer logs a console line of its own; the ActionError
    // below carries the same fact to the toast and the action log.
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
    // Each side is named, because "one of them failed" is not something the
    // reader can act on. This reached users as a bare "Could not load base/new
    // revision" whichever side died and whatever the status was.
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
