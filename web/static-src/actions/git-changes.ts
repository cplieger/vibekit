// Actions for the Git Changes tab. Each user-initiated mutation gets
// its own action with typed args and a descriptive error prefix.
// All actions POST to /api/git/<op> with { repo, ...body } and expect
// { output?, error? } back. Server-side errors in the `error` field
// are surfaced as ActionError so the framework toasts them.
// ---------------------------------------------------------------------------

import { defineAction, ActionError } from "./index.js";

// --- Wire types ---

interface GitMutationResult {
  output?: string;
  error?: string;
}

interface CommitMessageResult {
  message?: string;
  error?: string;
}

// --- Helper: build a git repo action that POSTs and checks .error ---

interface GitRepoArgs {
  repo: string;
  [key: string]: unknown;
}

function gitRepoAction<TArgs extends GitRepoArgs>(opts: {
  name: string;
  path: string;
  error: string;
}) {
  return defineAction<TArgs, void>({
    name: opts.name,
    run: async (args, signal) => {
      const res = await doPost<GitMutationResult>(opts.path, args, signal);
      if (res.error !== undefined && res.error !== "") {
        throw new ActionError(res.error);
      }
    },
    error: opts.error,
  });
}

/** Low-level POST with signal + JSON parse + non-ok handling. */
async function doPost<T>(path: string, body: unknown, signal: AbortSignal): Promise<T> {
  let r: Response;
  try {
    r = await fetch(path, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
      signal,
    });
  } catch (e) {
    if (signal.aborted) {
      throw new ActionError("cancelled", { code: "cancelled", cause: e });
    }
    throw new ActionError(
      e instanceof Error ? e.message : "network error",
      { cause: e },
    );
  }
  if (!r.ok) {
    let serverMsg = "";
    try {
      const b = (await r.json()) as { error?: unknown };
      if (typeof b.error === "string") serverMsg = b.error;
    } catch { /* ignore */ }
    throw new ActionError(serverMsg || `HTTP ${String(r.status)}`, { status: r.status });
  }
  const text = await r.text();
  if (text === "") return {} as T;
  return JSON.parse(text) as T;
}

// --- Actions ---

export const stageAll = gitRepoAction<{ repo: string; files: string[] }>({
  name: "git.stage_all",
  path: "/api/git/stage",
  error: "Couldn't stage files",
});

export const discardAll = gitRepoAction<{ repo: string; files: string[] }>({
  name: "git.discard_all",
  path: "/api/git/discard",
  error: "Couldn't discard changes",
});

export const pull = gitRepoAction<{ repo: string }>({
  name: "git.pull",
  path: "/api/git/pull",
  error: "Pull failed",
});

export const push = gitRepoAction<{ repo: string }>({
  name: "git.push",
  path: "/api/git/push",
  error: "Push failed",
});

export const stash = gitRepoAction<{ repo: string }>({
  name: "git.stash",
  path: "/api/git/stash",
  error: "Stash failed",
});

export const stashPop = gitRepoAction<{ repo: string }>({
  name: "git.stash_pop",
  path: "/api/git/stash-pop",
  error: "Stash pop failed",
});

export const stageFile = gitRepoAction<{ repo: string; files: string[] }>({
  name: "git.stage_file",
  path: "/api/git/stage",
  error: "Couldn't stage file",
});

export const unstageFile = gitRepoAction<{ repo: string; files: string[] }>({
  name: "git.unstage",
  path: "/api/git/unstage",
  error: "Couldn't unstage file",
});

export const discardFile = gitRepoAction<{ repo: string; files: string[] }>({
  name: "git.discard_file",
  path: "/api/git/discard",
  error: "Couldn't discard file",
});

export const commit = defineAction<{ repo: string; message: string }, void>({
  name: "git.commit",
  run: async (args, signal) => {
    const res = await doPost<GitMutationResult>("/api/git/commit", args, signal);
    if (res.error !== undefined && res.error !== "") {
      throw new ActionError(res.error);
    }
  },
  error: "Commit failed",
});

export const generateCommitMessage = defineAction<{ repo: string }, string>({
  name: "git.generate_message",
  run: async (args, signal) => {
    const res = await doPost<CommitMessageResult>("/api/git/commit-message", args, signal);
    if (res.error !== undefined && res.error !== "") {
      throw new ActionError(res.error);
    }
    if (res.message === undefined || res.message === "") {
      throw new ActionError("No message generated");
    }
    return res.message;
  },
  error: "Couldn't generate commit message",
});
