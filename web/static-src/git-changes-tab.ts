// ---------------------------------------------------------------------------
// Git Changes tab: per-repo collapsible sections rendered into
// #git-changes-mount. Each section shows branch + ahead/behind, the
// list of file changes (staged + unstaged interleaved with their
// status letters), an action bar (pull / stash / pop / stage all /
// discard all), and a commit message + Commit button.
//
// Single fetch of /api/git/status-all produces the data; the section
// rendering is purely data-driven so re-renders are cheap. Re-render
// triggers: filter input, after a successful action, on tab activate.
// ---------------------------------------------------------------------------

import { apiGet, apiPost } from "./api-client.js";
import { onSSE } from "./bus.js";
import { ICON_REFRESH, ICON_SPINNER } from "./icons.js";
import { withAsyncFeedback } from "./async-button.js";
import { confirm as confirmDialog } from "./confirm.js";

// --- Wire types ---

interface FileEntry {
  path: string;
  status: string;
  staged: boolean;
  display: string;
}

interface RepoStatus {
  repo: string;
  is_repo: boolean;
  branch: string;
  remote: string;
  ahead: number;
  behind: number;
  files: FileEntry[];
  has_dirty: boolean;
  stashes: number;
}

interface StatusAllResponse {
  repos: RepoStatus[];
}

// --- State ---

let lastStatusAll: RepoStatus[] = [];
let filterText = "";

// --- Public API ---

/** Initialise the Changes tab. Wires the filter input, the global
 *  refresh button, and the SSE forge-changed event. Idempotent. */
export function initChangesTab(): void {
  const filterEl = document.getElementById("git-changes-filter") as HTMLInputElement | null;
  filterEl?.addEventListener("input", () => {
    filterText = filterEl.value.trim().toLowerCase();
    paint();
  });

  const refreshBtn = document.getElementById("git-refresh-all-btn") as HTMLButtonElement | null;
  if (refreshBtn !== null) {
    refreshBtn.innerHTML = ICON_REFRESH;
    refreshBtn.addEventListener("click", () => {
      void withAsyncFeedback(refreshBtn, () => refreshChanges(), { keepLabel: true });
    });
  }

  // Refetch when the agent emits anything that touches files (it
  // emits this after every turn that wrote something), and when forge
  // accounts change (clones / removes ripple into the repo list).
  onSSE("turn_ended", () => { void refreshChanges(); });
  onSSE("forges_changed", () => { void refreshChanges(); });

  // Initial paint.
  void refreshChanges();
}

/** Force a full /api/git/status-all refresh and repaint. */
export async function refreshChanges(): Promise<void> {
  const data = await apiGet<StatusAllResponse>("/api/git/status-all");
  if (data === null) {
    paintError("Failed to load git status.");
    return;
  }
  lastStatusAll = data.repos;
  paint();
}

// --- Render ---

function paint(): void {
  const root = document.getElementById("git-changes-mount");
  if (root === null) return;

  if (lastStatusAll.length === 0) {
    root.innerHTML = `<div class="git-multirepo-empty">No repositories cloned yet. Open the <strong>Sources</strong> tab to clone one.</div>`;
    return;
  }

  // Filter file entries by path (we keep the section if any file
  // matches OR if the search is empty).
  const sections: HTMLElement[] = [];
  for (const r of lastStatusAll) {
    const section = renderRepoSection(r);
    if (section !== null) sections.push(section);
  }

  root.replaceChildren();
  if (sections.length === 0) {
    const empty = document.createElement("div");
    empty.className = "git-multirepo-empty";
    empty.textContent = filterText !== ""
      ? "No matching changes."
      : "Nothing to commit. All repos clean.";
    root.appendChild(empty);
    return;
  }
  for (const s of sections) root.appendChild(s);
}

function paintError(msg: string): void {
  const root = document.getElementById("git-changes-mount");
  if (root === null) return;
  root.innerHTML = `<div class="git-multirepo-error">${msg}</div>`;
}

function renderRepoSection(r: RepoStatus): HTMLElement | null {
  // Apply filter: keep section if (a) filter empty, (b) repo name
  // matches, or (c) any file path matches.
  const filteredFiles = filterText === ""
    ? r.files
    : r.files.filter((f) => f.path.toLowerCase().includes(filterText));
  if (filterText !== "" && filteredFiles.length === 0 && !r.repo.toLowerCase().includes(filterText)) {
    return null;
  }
  // Hide clean repos by default unless filter is active or repo
  // matched the filter explicitly.
  const expandedDefault = r.has_dirty || r.ahead > 0 || r.behind > 0 || filterText !== "";

  const section = document.createElement("section");
  section.className = "git-repo-section";
  section.dataset["repo"] = r.repo;
  if (expandedDefault) section.classList.add("expanded");

  // Header
  const header = document.createElement("button");
  header.type = "button";
  header.className = "git-repo-section-header";
  header.setAttribute("aria-expanded", expandedDefault ? "true" : "false");
  header.innerHTML = renderHeaderHTML(r);
  header.addEventListener("click", () => {
    const open = section.classList.toggle("expanded");
    header.setAttribute("aria-expanded", open ? "true" : "false");
  });
  section.appendChild(header);

  // Body (the part that toggles)
  const body = document.createElement("div");
  body.className = "git-repo-section-body";

  if (!r.is_repo) {
    body.innerHTML = `<div class="git-repo-row-error">Not a git repository.</div>`;
    section.appendChild(body);
    return section;
  }

  // Action bar
  body.appendChild(renderActionBar(r));

  // File list (staged first, then unstaged)
  if (filteredFiles.length === 0 && !r.has_dirty) {
    const clean = document.createElement("div");
    clean.className = "git-repo-row-clean";
    clean.textContent = "Clean.";
    body.appendChild(clean);
  } else if (filteredFiles.length === 0) {
    const noMatch = document.createElement("div");
    noMatch.className = "git-repo-row-empty";
    noMatch.textContent = "No paths match the filter.";
    body.appendChild(noMatch);
  } else {
    body.appendChild(renderFileList(r, filteredFiles));
  }

  // Commit area (only if there are staged files)
  const stagedCount = r.files.filter((f) => f.staged).length;
  if (stagedCount > 0) {
    body.appendChild(renderCommitArea(r));
  }

  section.appendChild(body);
  return section;
}

function renderHeaderHTML(r: RepoStatus): string {
  const ahead = r.ahead > 0 ? ` <span class="git-repo-ahead">↑${r.ahead}</span>` : "";
  const behind = r.behind > 0 ? ` <span class="git-repo-behind">↓${r.behind}</span>` : "";
  const dirty = r.has_dirty
    ? ` <span class="git-repo-dirty-dot" title="Has uncommitted changes" aria-label="dirty"></span>`
    : "";
  const stashes = r.stashes > 0 ? ` <span class="git-repo-stashes" title="${r.stashes} stash${r.stashes === 1 ? "" : "es"}">📦${r.stashes}</span>` : "";
  const branch = escapeHTML(r.branch || "(detached)");
  return `
    <span class="git-repo-section-chevron" aria-hidden="true">▸</span>
    <span class="git-repo-section-name">${escapeHTML(r.repo)}</span>${dirty}
    <span class="git-repo-section-meta">${branch}${ahead}${behind}${stashes}</span>
  `;
}

function renderActionBar(r: RepoStatus): HTMLElement {
  const bar = document.createElement("div");
  bar.className = "git-repo-action-bar";

  const btn = (label: string, title: string, danger = false): HTMLButtonElement => {
    const b = document.createElement("button");
    b.type = "button";
    b.className = `btn-small${danger ? " btn-danger" : ""}`;
    b.textContent = label;
    b.title = title;
    return b;
  };

  const stageAll = btn("Stage all", "Stage every unstaged change");
  stageAll.addEventListener("click", () => {
    const files = r.files.filter((f) => !f.staged).map((f) => f.path);
    if (files.length === 0) return;
    void withAsyncFeedback(stageAll, () => mutateRepo("/api/git/stage", r.repo, { files }));
  });
  bar.appendChild(stageAll);

  const discardAll = btn("Discard all", "Throw away all uncommitted changes (irreversible)", true);
  discardAll.addEventListener("click", () => {
    void withAsyncFeedback(discardAll, async () => {
      const ok = await confirmDialog(
        `Discard ALL uncommitted changes in ${r.repo}? This cannot be undone.`,
        "Discard",
        "destructive",
      );
      if (!ok) return;
      const files = r.files.map((f) => f.path);
      if (files.length === 0) return;
      await mutateRepo("/api/git/discard", r.repo, { files });
    });
  });
  bar.appendChild(discardAll);

  const sep = document.createElement("span");
  sep.className = "action-bar-sep";
  sep.setAttribute("aria-hidden", "true");
  bar.appendChild(sep);

  const pull = btn("Pull", "git pull");
  pull.addEventListener("click", () => {
    void withAsyncFeedback(pull, () => mutateRepo("/api/git/pull", r.repo, {}));
  });
  bar.appendChild(pull);

  if (r.ahead > 0) {
    const push = btn("Push", `Push ${r.ahead} commit${r.ahead === 1 ? "" : "s"} to origin`);
    push.classList.add("btn-primary");
    push.addEventListener("click", () => {
      void withAsyncFeedback(push, () => mutateRepo("/api/git/push", r.repo, {}));
    });
    bar.appendChild(push);
  }

  const stash = btn("Stash", "Stash uncommitted changes");
  stash.addEventListener("click", () => {
    void withAsyncFeedback(stash, () => mutateRepo("/api/git/stash", r.repo, {}));
  });
  bar.appendChild(stash);

  if (r.stashes > 0) {
    const pop = btn("Pop", "Pop the most recent stash");
    pop.addEventListener("click", () => {
      void withAsyncFeedback(pop, () => mutateRepo("/api/git/stash-pop", r.repo, {}));
    });
    bar.appendChild(pop);
  }

  return bar;
}

function renderFileList(r: RepoStatus, files: FileEntry[]): HTMLElement {
  // Sort: staged first, then unstaged; within each group alphabetical.
  const sorted = [...files].sort((a, b) => {
    if (a.staged !== b.staged) return a.staged ? -1 : 1;
    return a.path.localeCompare(b.path);
  });
  const list = document.createElement("ul");
  list.className = "git-file-list";
  for (const f of sorted) list.appendChild(renderFileRow(r, f));
  return list;
}

function renderFileRow(r: RepoStatus, f: FileEntry): HTMLElement {
  const li = document.createElement("li");
  li.className = `git-file-row${f.staged ? " staged" : ""}`;

  const status = document.createElement("span");
  status.className = "git-file-status";
  status.textContent = f.display || statusLetter(f.status);
  status.title = describeStatus(f.status);
  li.appendChild(status);

  const path = document.createElement("span");
  path.className = "git-file-path";
  path.textContent = f.path;
  path.title = f.path;
  li.appendChild(path);

  const actions = document.createElement("span");
  actions.className = "git-file-actions";

  const action = (label: string, title: string, fn: () => Promise<unknown>, danger = false): HTMLButtonElement => {
    const b = document.createElement("button");
    b.type = "button";
    b.className = `btn-small${danger ? " btn-danger" : ""}`;
    b.textContent = label;
    b.title = title;
    b.addEventListener("click", (ev) => {
      ev.stopPropagation();
      void withAsyncFeedback(b, fn);
    });
    return b;
  };

  if (f.staged) {
    actions.appendChild(action("Unstage", "Move out of staged area",
      () => mutateRepo("/api/git/unstage", r.repo, { files: [f.path] })));
  } else {
    actions.appendChild(action("Stage", "Add to staged area",
      () => mutateRepo("/api/git/stage", r.repo, { files: [f.path] })));
    actions.appendChild(action("Discard", "Throw away this change",
      async () => {
        const ok = await confirmDialog(`Discard changes to ${f.path}? This cannot be undone.`, "Discard", "destructive");
        if (ok) await mutateRepo("/api/git/discard", r.repo, { files: [f.path] });
      }, true));
  }
  li.appendChild(actions);
  return li;
}

function renderCommitArea(r: RepoStatus): HTMLElement {
  const wrap = document.createElement("div");
  wrap.className = "git-commit-area";

  const ta = document.createElement("textarea");
  ta.className = "git-commit-input";
  ta.placeholder = "Commit message…";
  ta.rows = 2;
  ta.dataset["repo"] = r.repo;
  wrap.appendChild(ta);

  const row = document.createElement("div");
  row.className = "git-commit-row";

  const ai = document.createElement("button");
  ai.type = "button";
  ai.className = "btn-small";
  ai.textContent = "✨ AI message";
  ai.title = "Generate commit message from staged changes";
  ai.addEventListener("click", () => {
    void withAsyncFeedback(ai, async () => {
      const res = await apiPost<{ message?: string; error?: string }>(
        `/api/git/commit-message`,
        { repo: r.repo },
      );
      if (res !== null && res.message !== undefined && res.message !== "") {
        ta.value = res.message;
      } else if (res !== null && res.error !== undefined && res.error !== "") {
        throw new Error(res.error);
      }
    });
  });
  row.appendChild(ai);

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
      await mutateRepo("/api/git/commit", r.repo, { message });
      ta.value = "";
    });
  });
  row.appendChild(commit);

  wrap.appendChild(row);
  return wrap;
}

// --- API helper ---

async function mutateRepo(path: string, repo: string, body: Record<string, unknown>): Promise<void> {
  const res = await apiPost<{ output?: string; error?: string }>(path, { repo, ...body });
  if (res === null) throw new Error("Network error");
  if (res.error !== undefined && res.error !== "") throw new Error(res.error);
  await refreshChanges();
}

// --- Helpers ---

function statusLetter(s: string): string {
  if (s.length >= 1) return s.charAt(0);
  return "?";
}

function describeStatus(s: string): string {
  switch (s.charAt(0)) {
    case "M": return "Modified";
    case "A": return "Added";
    case "D": return "Deleted";
    case "R": return "Renamed";
    case "?": return "Untracked";
    case "U": return "Unmerged";
    default:  return s;
  }
}

function escapeHTML(s: string): string {
  const map: Record<string, string> = { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" };
  return s.replace(/[&<>"']/g, (c) => map[c] ?? c);
}

// Stub used to silence unused-import warning when ICON_SPINNER ends
// up unused in some build paths. Keep the import live so a future
// inline-spinner addition has no extra import churn.
void ICON_SPINNER;
