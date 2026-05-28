// ---------------------------------------------------------------------------
// Commit workflow rendering: commit message textarea + AI generate +
// recent commits collapsible section. Extracted from git-changes-tab.ts
// to isolate the commit-specific UI concern.
// ---------------------------------------------------------------------------

import { apiGet } from "./api-client.js";
import { withAsyncFeedback } from "./async-button.js";
import { bindLoadingState } from "./actions/index.js";
import {
  commit as commitAction,
  generateCommitMessage,
} from "./actions/git-changes.js";
import type { GitRepoStatus } from "./git-types.js";

type RepoStatus = GitRepoStatus;

/** Dependencies injected from git-changes-tab module state. */
export interface CommitDeps {
  commitMessages: Map<string, string>;
  bindingCleanups: (() => void)[];
  diffAbort: AbortController | null;
  refreshChanges: () => Promise<void>;
  assertOk: <T>(result: T) => asserts result is NonNullable<T>;
}

/** Render the recent-commits collapsible section for a repo. */
export function renderRecentCommits(r: RepoStatus, deps: CommitDeps): HTMLElement {
  const wrap = document.createElement("details");
  wrap.className = "git-recent-commits";

  const summary = document.createElement("summary");
  summary.className = "git-recent-commits-summary";
  summary.textContent = "Recent commits";
  wrap.appendChild(summary);

  const body = document.createElement("div");
  body.className = "git-recent-commits-body";
  body.textContent = "Loading…";
  wrap.appendChild(body);

  let loaded = false;
  wrap.addEventListener("toggle", () => {
    if (!wrap.open || loaded) {
      return;
    }
    loaded = true;
    void apiGet<{ entries?: string[]; remote?: string; behind?: number }>(
      `/api/git/log?repo=${encodeURIComponent(r.repo)}`,
      deps.diffAbort?.signal,
    ).then((data) => {
      if (deps.diffAbort?.signal.aborted) {
        return;
      }
      if (data === null) {
        body.textContent = "Failed to load.";
        return;
      }
      const entries = data.entries ?? [];
      if (entries.length === 0) {
        body.textContent = "No commits.";
        return;
      }
      body.replaceChildren();
      const list = document.createElement("ul");
      list.className = "git-recent-commits-list";
      for (const line of entries.slice(0, 20)) {
        const li = document.createElement("li");
        li.className = "git-recent-commits-row";
        // line shape: "<sha> <subject>"
        const sp = line.indexOf(" ");
        if (sp > 0) {
          const sha = document.createElement("code");
          sha.className = "git-recent-commits-sha";
          sha.textContent = line.slice(0, sp);
          li.appendChild(sha);
          const sub = document.createElement("span");
          sub.className = "git-recent-commits-subject";
          sub.textContent = line.slice(sp + 1);
          li.appendChild(sub);
        } else {
          li.textContent = line;
        }
        list.appendChild(li);
      }
      body.appendChild(list);
    });
  });

  return wrap;
}

/** Render the commit message textarea + AI generate + Commit button. */
export function renderCommitArea(r: RepoStatus, deps: CommitDeps): HTMLElement {
  const wrap = document.createElement("div");
  wrap.className = "git-commit-area";

  const ta = document.createElement("textarea");
  ta.className = "git-commit-input";
  ta.placeholder = "Commit message…";
  ta.rows = 2;
  ta.dataset["repo"] = r.repo;
  // Restore previously typed commit message.
  const saved = deps.commitMessages.get(r.repo);
  if (saved) {
    ta.value = saved;
  }
  wrap.appendChild(ta);

  const row = document.createElement("div");
  row.className = "git-commit-row";

  const ai = document.createElement("button");
  ai.type = "button";
  ai.className = "btn-small";
  ai.textContent = "✨ AI message";
  ai.setAttribute("data-tooltip", "Generate commit message from staged changes");
  ai.addEventListener("click", () => {
    void withAsyncFeedback(ai, async () => {
      const msg = await generateCommitMessage.dispatch({ repo: r.repo });
      deps.assertOk(msg);
      deps.commitMessages.set(r.repo, msg.message ?? "");
      if (ta.isConnected) {
        ta.value = msg.message ?? "";
      }
    });
  });
  row.appendChild(ai);
  deps.bindingCleanups.push(bindLoadingState("git.generate_message", ai));

  const commit = document.createElement("button");
  commit.type = "button";
  commit.className = "btn-small btn-primary";
  commit.textContent = "Commit";
  commit.addEventListener("click", () => {
    void withAsyncFeedback(commit, async () => {
      const message = ta.value.trim();
      if (message === "") {
        throw new Error("Commit message required");
      }
      deps.assertOk(await commitAction.dispatch({ repo: r.repo, message }));
      ta.value = "";
      deps.commitMessages.delete(r.repo);
      await deps.refreshChanges();
    });
  });
  row.appendChild(commit);
  deps.bindingCleanups.push(bindLoadingState("git.commit", commit));

  wrap.appendChild(row);
  return wrap;
}
