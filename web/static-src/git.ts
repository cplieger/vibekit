// ---------------------------------------------------------------------------
// Git panel: status, staging, commit, push/pull/clone/init, branches,
// stash, GitHub auth, log. Every action takes the currently-selected
// registry entry from repo-picker.ts and translates it into the repo
// query param the legacy /api/git/* endpoints still expect.
//
// Helpers:
//   gitPost(path, body) → POST, uniform error handling, auto-injects repo
//   gitGet(path, extra) → GET, uniform error handling, auto-injects repo
//   mutateAndRefresh(…) → "fire a write, show output, refresh status"
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { closeModal, showConfirm, RollingOutput } from "./modals.js";
import { patchSettings } from "./persist.js";
import { gitSkeleton } from "./skeleton.js";
import { apiGet, apiPost, apiPut } from "./api-client.js";
import {
  initRepoPicker, getSelectedEntry, onSelectionChange, selectById,
  openPickerDialog, onRegistryChange, getRegistrySummary,
  type RegistrySummary,
} from "./repo-picker.js";
import { initCIPill, refreshCIPill } from "./pr-checks.js";
import type { RepoEntry } from "./forge-types.js";
import {
  renderStagedSection, renderChangedList, renderPushPullButtons,
  renderStashButtons, updateGitBadge,
} from "./git-render.js";
import { registerGitCore } from "./git-core.js";
import {
  initStatusBanner, setBanner, clearBanner,
} from "./git-status-banner.js";
import {
  initGitEmptyState, showGitEmptyState, hideGitEmptyState,
} from "./git-empty-state.js";
import { openOverflowMenu } from "./overflow-menu.js";
import { withAsyncFeedback } from "./async-button.js";
import type {
  GitFileEntry, GitStatusData, GitViewState, GitPostResult, GitErrorRule,
} from "./git-types.js";

export type { GitFileEntry, GitStatusData, GitPostResult, GitErrorRule };

class GitPanelState {
  viewState: GitViewState = { status: "no-repo" };
  statusController: AbortController | null = null;
  cloneController: AbortController | null = null;
  gitOutput!: RollingOutput;

  setStatusData(d: GitStatusData): void {
    const key = selectedRepoKey();
    if (!d.is_repo) {
      this.viewState = { status: "not-git", repoKey: key };
    } else {
      this.viewState = { status: "ready", repoKey: key, data: d };
    }
  }

  getStatusData(): GitStatusData | null {
    return this.viewState.status === "ready" ? this.viewState.data : null;
  }

  abortPending(): void {
    this.statusController?.abort();
    this.statusController = null;
  }

  abortClone(): void {
    this.cloneController?.abort();
    this.cloneController = null;
  }

  showOutput(text: string): void { this.gitOutput.append(text); }
  clearOutput(): void { this.gitOutput.clear(); }
}

const panel = new GitPanelState();

/** Accessor for legacy code paths that read status data directly. */
export function getStatusData(): GitStatusData | null {
  return panel.getStatusData();
}

function setStatusData(d: GitStatusData): void {
  panel.setStatusData(d);
}

// --- External hooks ---

/** Mark the git badge as dirty (changes exist) without fetching status.
 *  Called by the SSE handler when a write tool completes successfully. */
export function markGitDirty(): void {
  $.gitBadge.classList.remove("hidden");
}

/** Lightweight status refresh for the badge; skips the full ahead/behind
 *  fetch that needs a network round-trip. Called on turn_end. */
export function refreshGitBadge(): void {
  if (selectedRepoKey() === "") return;
  panel.abortPending();
  panel.statusController = new AbortController();
  const { signal } = panel.statusController;
  void gitGet("/api/git/status", "&quick=1", signal).then((d) => {
    if (d === null || signal.aborted) return;
    setStatusData(d as GitStatusData);
    updateGitBadge();
  });
}

/** Restore the last-used repo from persisted settings. Called by
 *  settings.ts during boot once the app's own settings have been
 *  hydrated; the picker stays in sync for the rest of the session
 *  via its own state. */
export function syncGitRepo(repo: string): void {
  if (repo === "") return;
  selectById(repo);
}

// --- Request helpers ---

/** Return the path segment the legacy /api/git/* endpoints expect.
 *  They're rooted at workDir, so a local clone whose folder name is
 *  "homelab" maps to "homelab"; the bare workspace is ".". Remote-
 *  only entries have no local path and can't drive git-panel actions
 *  until cloned. */
export function selectedRepoKey(): string {
  const e = getSelectedEntry();
  if (e === null) return "";
  if (e.local_path === undefined || e.local_path === "") return "";
  // The Go side's repoDir expects a name relative to workDir. We
  // derive it from the last path segment since the picker carries
  // absolute paths but the legacy endpoints handle them by basename.
  return e.name !== "" ? e.name : ".";
}

function gitRepoParam(): string {
  const k = selectedRepoKey();
  return k !== "" ? `?repo=${encodeURIComponent(k)}` : "";
}

/** POST JSON to a git endpoint with the selected repo auto-injected. */
export function gitPost(endpoint: string, body: Record<string, unknown>): Promise<GitPostResult> {
  return apiPost<GitPostResult>(endpoint, { repo: selectedRepoKey(), ...body })
    .then((r) => r ?? { error: "request failed" });
}

/** GET a git endpoint with the repo query param. `extra` is appended
 *  verbatim (must start with `&` or `?`). */
function gitGet(path: string, extra = "", signal?: AbortSignal): Promise<unknown> {
  return apiGet<unknown>(`${path}${gitRepoParam()}${extra}`, signal);
}

/** Fire a mutating gitPost, show its output, then refresh status.
 *  Throws on logical failure (server returned an `error` field, or the
 *  request itself failed) so callers wiring buttons through
 *  withAsyncFeedback get an error glyph for free. */
async function mutateAndRefresh(label: string, endpoint: string, body: Record<string, unknown> = {}): Promise<void> {
  gitClearOutput(); gitShowOutput(`${label}...`);
  try {
    const d = await gitPost(endpoint, body);
    const raw = d.error ?? d.output ?? "Done";
    const friendly = friendlyGitError(raw);
    gitShowOutput(friendly ?? raw);
    if (friendly !== null && GIT_ERROR_RULES.some(r => r.triggerAuth === true && raw.includes(r.match))) {
      setBanner("forge-auth-failed");
    }
    refreshGitStatus();
    if (d.error !== undefined && d.error !== "") {
      throw new Error(d.error);
    }
  } catch (e) {
    // gitPost itself already mapped most failure shapes into d.error;
    // a thrown exception here means the request never reached or the
    // network layer rejected. Surface a friendly line and re-throw so
    // withAsyncFeedback shows the error glyph.
    if (!(e instanceof Error) || !String(e.message).startsWith("")) {
      gitShowOutput(`${label} failed`);
    }
    throw e;
  }
}

export const GIT_ERROR_RULES: readonly GitErrorRule[] = [
  { match: "Not possible to fast-forward", message: "Branches have diverged. Push your local commits first, or use Re-clone to reset from remote." },
  { match: "CONFLICT", message: "Merge conflict detected. Use Re-clone to reset, or resolve conflicts manually." },
  { match: "Permission denied", message: "Permission denied. Check your credentials or SSH key." },
  { match: "Could not resolve host", message: "Network error: could not reach the remote. Check your connection." },
  { match: "could not read Username", message: "Auth required — connect a forge in Settings → Git to enable push/pull.", triggerAuth: true },
  { match: "No such device or address", message: "Auth required — connect a forge in Settings → Git to enable push/pull.", triggerAuth: true },
  { match: "Authentication failed", message: "Auth expired — reconnect your forge in Settings → Git.", triggerAuth: true },
  { match: "fatal: Authentication", message: "Auth expired — reconnect your forge in Settings → Git.", triggerAuth: true },
  { match: "401", message: "Token expired or revoked — reconnect your forge in Settings → Git.", triggerAuth: true },
  { match: "403 Forbidden", message: "Access denied — check your forge permissions in Settings → Git.", triggerAuth: true },
];

/** Map common git error patterns to user-friendly messages. */
export function friendlyGitError(text: string): string | null {
  for (const { match, message } of GIT_ERROR_RULES) {
    if (text.includes(match)) return message;
  }
  return null;
}

function gitShowOutput(text: string): void { panel.showOutput(text); }
function gitClearOutput(): void { panel.clearOutput(); }

export function remoteToWebUrl(remote: string): string {
  let url = remote.replace(/\.git$/, "");
  if (url.startsWith("git@")) url = url.replace(":", "/").replace("git@", "https://");
  return url;
}

// --- Init ---

export function initGitPanel(): void {
  registerGitCore({ gitPost, selectedRepoKey, getStatusData, refreshGitStatus });
  panel.gitOutput = new RollingOutput($.gitOutputBar, "git-output-modal");
  initRepoPicker();
  initCIPill();

  $.gitRefreshBtn.addEventListener("click", () =>
    void withAsyncFeedback($.gitRefreshBtn, () => refreshGitStatus()));
  $.gitStageAllBtn.addEventListener("click", () =>
    void withAsyncFeedback($.gitStageAllBtn, () =>
      mutateAndRefresh("Staging", "/api/git/stage", { files: ["."] })));
  $.gitUnstageAllBtn.addEventListener("click", () =>
    void withAsyncFeedback($.gitUnstageAllBtn, gitUnstageAll));
  $.gitDiscardAllBtn.addEventListener("click", () =>
    void withAsyncFeedback($.gitDiscardAllBtn, gitDiscardAll));
  $.gitCommitBtn.addEventListener("click", () =>
    void withAsyncFeedback($.gitCommitBtn, gitCommit));
  $.gitPushBtn.addEventListener("click", () =>
    void withAsyncFeedback($.gitPushBtn, async () => {
      await mutateAndRefresh("Pushing", "/api/git/push");
      // After a push, the forge may have new PR checks; give them a
      // few seconds to spin up before asking for a fresh summary.
      setTimeout(() => void refreshCIPill(), 3000);
    }));
  $.gitPullBtn.addEventListener("click", () =>
    void withAsyncFeedback($.gitPullBtn, () => mutateAndRefresh("Pulling", "/api/git/pull")));
  $.gitOverflowBtn.addEventListener("click", () => {
    const key = selectedRepoKey();
    const isWorkspace = key === "" || key === ".";
    openOverflowMenu($.gitOverflowBtn, [
      {
        id: "reclone",
        label: "Re-clone…",
        // Re-clone needs an origin to fetch from, so it's only useful
        // for non-workspace clones.
        disabled: isWorkspace,
        onSelect: () => { gitReclone(); },
      },
      {
        id: "remove",
        label: "Remove from registry…",
        danger: true,
        // Workspace root isn't an entry to remove.
        disabled: isWorkspace,
        onSelect: () => { gitRemoveRepo(); },
      },
    ]);
  });
  $.gitAiMsgBtn.addEventListener("click", () =>
    void withAsyncFeedback($.gitAiMsgBtn, gitAiMessage));
  $.gitBranchBtn.addEventListener("click", openBranchModal);
  $.gitCreateBranchBtn.addEventListener("click", gitCreateBranch);
  $.gitStashBtn.addEventListener("click", () =>
    void withAsyncFeedback($.gitStashBtn, () => mutateAndRefresh("Stashing", "/api/git/stash")));
  $.gitStashPopBtn.addEventListener("click", () =>
    void withAsyncFeedback($.gitStashPopBtn, () => mutateAndRefresh("Popping stash", "/api/git/stash-pop")));

  initStatusBanner({
    onConnectForge: () => { window.location.assign("/settings/git"); },
    onAuthenticateGh: () => { void gitGhAuth(); },
  });

  initGitEmptyState({
    onConnectForge: () => { window.location.assign("/settings/git"); },
    onChooseRepo: () => { openPickerDialog(); },
  });

  // Re-evaluate empty-state variant whenever the registry refreshes
  // (e.g. user connects a new forge in another tab → forges_changed
  // SSE → repo-picker refetch → registry summary update). Only acts
  // when no repo is selected; selection-driven changes flow through
  // handleRepoSelection.
  onRegistryChange((_summary: RegistrySummary) => {
    if (getSelectedEntry() === null) showEmptyStateForCurrentRegistry();
  });

  onSelectionChange((entry) => handleRepoSelection(entry));
}

function handleRepoSelection(entry: RepoEntry | null): void {
  const key = selectedRepoKey();
  patchSettings({ git_repo: key });
  gitClearOutput();
  panel.abortClone();
  if (entry === null || key === "") {
    panel.viewState = { status: "no-repo" };
    applyNoRepoState();
    $.gitRepoSection.classList.add("hidden");
    showEmptyStateForCurrentRegistry();
    return;
  }
  panel.viewState = { status: "loading", repoKey: key };
  applyRepoState();
  hideGitEmptyState();
  $.gitRepoBar.classList.remove("hidden");
  $.gitRepoSection.classList.remove("hidden");
  $.gitBranchBtn.textContent = entry.local_branch !== undefined && entry.local_branch !== ""
    ? entry.local_branch : "loading...";
  refreshGitStatusQuick();
}

/** Choose the right empty-state variant based on the picker's
 *  registry summary and toggle the toolbar+panel visibility to make
 *  room for it. Called both when selection drops to null and when
 *  the registry changes while no selection exists. */
/** Choose the right empty-state variant based on the picker's
 *  registry summary and toggle the toolbar+panel visibility to make
 *  room for it. Called both when selection drops to null and when
 *  the registry changes while no selection exists. */
function showEmptyStateForCurrentRegistry(): void {
  const summary = getRegistrySummary();
  // Variant rule: if there's nothing pickable, route the user to
  // forge settings — regardless of *why* the registry is empty.
  // That covers no-forge-connected, forge-with-broken-listing
  // (e.g. a transient gh API failure), and brand-new-account-with-
  // zero-repos all in one. Asking the user to "Choose a repository"
  // when the picker has nothing to show is confusing — the only
  // useful next step is to add or reconnect a forge in settings.
  const variant = summary.entryCount === 0 ? "forges-needed" : "pick-or-clone";
  showGitEmptyState(variant);
  // The toolbar is irrelevant without a repo and crowds the empty
  // state.
  $.gitRepoBar.classList.add("hidden");
  $.gitRepoSection.classList.add("hidden");
}

// --- Load / render ---

/** Reset the git panel's DOM state when the view first opens. The
 *  actual selection and list now come from the picker; this is only
 *  responsible for clearing stale file lists while the first status
 *  fetch is in flight. */
export function loadGitRepos(): void {
  $.gitStagedSection.classList.add("hidden");
  $.gitStagedList.replaceChildren();
  const skel = gitSkeleton();
  $.gitChangedList.replaceChildren(skel);
  const entry = getSelectedEntry();
  if (entry === null) {
    skel.remove();
    applyNoRepoState();
    $.gitRepoSection.classList.add("hidden");
    return;
  }
  handleRepoSelection(entry);
}

/** Button state when no repo is selected: disable everything except
 *  "+" (Add external repository); hide the branch button entirely. */
function applyNoRepoState(): void {
  $.gitBranchBtn.classList.add("hidden");
  $.gitBranchBtn.textContent = "";
  $.gitRefreshBtn.disabled = true;
}

/** Button state when at least one repo is selected: re-enable all
 *  repo-level actions that were disabled by applyNoRepoState. */
function applyRepoState(): void {
  $.gitBranchBtn.classList.remove("hidden");
  $.gitRefreshBtn.disabled = false;
}

function refreshGitStatusQuick(): void {
  panel.abortPending();
  panel.statusController = new AbortController();
  const { signal } = panel.statusController;
  void gitGet("/api/git/status", "&quick=1", signal).then((d) => {
    if (d === null || signal.aborted) return;
    setStatusData(d as GitStatusData);
    renderGitPanel();
    refreshGitStatus(); // background full fetch for ahead/behind
  });
}

export function refreshGitStatus(): Promise<void> {
  panel.abortPending();
  panel.statusController = new AbortController();
  const { signal } = panel.statusController;
  return gitGet("/api/git/status", "", signal).then((d) => {
    if (d === null || signal.aborted) return;
    setStatusData(d as GitStatusData);
    renderGitPanel();
  });
}

function renderGitPanel(): void {
  switch (panel.viewState.status) {
    case "no-repo":
    case "loading":
      updateGitBadge();
      return;
    case "not-git":
      $.gitRepoSection.classList.add("hidden");
      updateGitBadge();
      return;
    case "ready":
      break;
  }

  const d = panel.viewState.data;
  $.gitRepoSection.classList.remove("hidden");
  $.gitBranchBtn.textContent = d.branch !== "" ? d.branch : "HEAD detached";

  const files = d.files ?? [];
  const staged = files.filter((f) => f.staged);
  const unstaged = files.filter((f) => !f.staged);

  $.gitStageAllBtn.disabled = unstaged.length === 0;
  $.gitDiscardAllBtn.disabled = unstaged.length === 0;
  // (Re-clone / Remove-from-registry live on the overflow menu and
  //  derive their disabled state at open-time from selectedRepoKey().)

  renderStagedSection(staged);
  renderChangedList(unstaged);
  renderPushPullButtons();
  renderStashButtons();

  if (d.has_gh) clearBanner("gh-cli-missing");
  else setBanner("gh-cli-missing");
  updateGitBadge();
  loadGitLog();
}

// --- Actions ---

async function gitUnstageAll(): Promise<void> {
  const staged = (getStatusData()?.files ?? []).filter((f) => f.staged).map((f) => f.path);
  if (staged.length === 0) return;
  const d = await gitPost("/api/git/unstage", { files: staged });
  refreshGitStatus();
  if (d.error !== undefined && d.error !== "") throw new Error(d.error);
}

async function gitCommit(): Promise<void> {
  const msg = $.gitCommitMsg.value.trim();
  if (msg === "") return;
  gitClearOutput(); gitShowOutput("Committing...");
  const d = await gitPost("/api/git/commit", { message: msg });
  gitShowOutput(d.error ?? d.output ?? "Done");
  if (d.error === undefined || d.error === "") {
    $.gitCommitMsg.value = "";
  }
  refreshGitStatus();
  if (d.error !== undefined && d.error !== "") throw new Error(d.error);
}


/** Re-clone the selected repo. Resolves its `origin` URL, nukes the
 *  local copy, and re-fetches. */
function gitReclone(): void {
  const key = selectedRepoKey();
  if (key === "") return;
  const remote = getStatusData()?.remote ?? "";
  if (remote === "") {
    gitShowOutput("No origin remote configured; cannot re-clone.");
    return;
  }
  const label = key === "." ? "workspace root" : key;
  showConfirm(
    `Re-clone "${label}" from origin? Local changes will be lost.`,
    () => {
      panel.abortClone();
      panel.cloneController = new AbortController();
      const { signal } = panel.cloneController;
      gitClearOutput();
      gitShowOutput("Re-cloning...");
      void apiPost<GitPostResult>("/api/git/reclone", { repo: key }, signal).then((d) => {
        if (signal.aborted) return;
        gitShowOutput(d?.error ?? d?.output ?? (d === null ? "Re-clone failed" : "Done"));
        void apiPost("/api/forges/refresh", {});
      });
    },
    "Re-clone",
  );
}

function gitRemoveRepo(): void {
  const key = selectedRepoKey();
  if (key === "" || key === ".") return;
  showConfirm(
    `Delete repository "${key}" from workspace? This cannot be undone.`,
    () => {
      gitPost("/api/git/remove", {}).then((d) => {
        if (d.error !== undefined) {
          gitShowOutput(d.error);
          return;
        }
        gitClearOutput();
        patchSettings({ git_repo: "" });
        void apiPost("/api/forges/refresh", {});
      }).catch(() => { gitShowOutput("Remove failed"); });
    },
    "Delete",
  );
}

async function gitAiMessage(): Promise<void> {
  const msg = $.gitCommitMsg;
  const prevPlaceholder = msg.placeholder;
  msg.placeholder = "Generating commit message\u2026";
  try {
    const d = await gitPost("/api/git/commit-message", {});
    const text = d.output ?? d.error ?? "";
    if (text !== "") {
      msg.value = text;
      msg.rows = Math.min(10, Math.max(3, text.split("\n").length + 1));
      msg.placeholder = prevPlaceholder;
    } else {
      msg.placeholder = "Stage changes first";
    }
    if (d.error !== undefined && d.error !== "") throw new Error(d.error);
  } catch (e) {
    msg.placeholder = "Generation failed";
    throw e;
  }
}

function gitDiscardAll(): Promise<void> {
  const unstaged = (getStatusData()?.files ?? []).filter((f) => !f.staged);
  if (unstaged.length === 0) return Promise.resolve();
  return new Promise<void>((resolve, reject) => {
    showConfirm(
      `Discard all ${String(unstaged.length)} changed file(s)? This cannot be undone.`,
      () => {
        gitPost("/api/git/discard", { files: unstaged.map((f) => f.path) })
          .then((d) => {
            refreshGitStatus();
            if (d.error !== undefined && d.error !== "") {
              reject(new Error(d.error));
            } else {
              resolve();
            }
          })
          .catch((e: unknown) => { reject(e instanceof Error ? e : new Error(String(e))); });
      },
      "Discard",
    );
  });
}

function loadGitLog(): void {
  void gitGet("/api/git/log").then((raw) => {
    if (raw === null) {
      const empty = document.createElement("div");
      empty.className = "git-empty";
      empty.textContent = "Failed to load log";
      $.gitLogList.replaceChildren(empty);
      return;
    }
    const d = raw as { entries: string[]; remote: string; behind?: number };
    $.gitLogList.replaceChildren();
    if (d.entries.length === 0) {
      const empty = document.createElement("div");
      empty.className = "git-empty";
      empty.textContent = "No commits yet";
      $.gitLogList.appendChild(empty);
      return;
    }
    const webUrl = d.remote !== "" ? remoteToWebUrl(d.remote) : "";
    const behind = d.behind ?? 0;
    for (let i = 0; i < d.entries.length; i++) {
      $.gitLogList.appendChild(makeLogRow(d.entries[i]!, i < behind, webUrl));
    }
  });
}

function makeLogRow(entry: string, local: boolean, webUrl: string): HTMLDivElement {
  const spaceIdx = entry.indexOf(" ");
  const sha = spaceIdx > 0 ? entry.substring(0, spaceIdx) : entry;
  const msg = spaceIdx > 0 ? entry.substring(spaceIdx + 1) : "";
  const row = document.createElement("div");
  row.className = `git-file-row git-log-row${local ? " git-log-local" : ""}`;
  const shaEl = document.createElement("span");
  shaEl.className = "git-log-sha";
  if (webUrl !== "") {
    const link = document.createElement("a");
    link.href = `${webUrl}/commit/${sha}`;
    link.target = "_blank"; link.rel = "noopener"; link.textContent = sha;
    shaEl.appendChild(link);
  } else {
    shaEl.textContent = sha;
  }
  const msgEl = document.createElement("span");
  msgEl.className = "git-log-msg"; msgEl.textContent = msg;
  row.appendChild(shaEl); row.appendChild(msgEl);
  return row;
}

function openBranchModal(): void {
  $.gitBranchList.replaceChildren();
  const loadingEl = document.createElement("div");
  loadingEl.className = "git-loading";
  loadingEl.textContent = "Loading...";
  $.gitBranchList.appendChild(loadingEl);
  $.gitNewBranch.value = "";
  $.gitBranchModal.classList.remove("hidden");
  void gitGet("/api/git/branches").then((raw) => {
    if (raw === null) {
      $.gitBranchList.replaceChildren();
      const errEl = document.createElement("div");
      errEl.className = "git-empty";
      errEl.textContent = "Failed to load branches";
      $.gitBranchList.appendChild(errEl);
      return;
    }
    const d = raw as { branches: Array<{ name: string; current: boolean }> };
    $.gitBranchList.replaceChildren();
    for (const b of d.branches) $.gitBranchList.appendChild(makeBranchRow(b));
    if (d.branches.length === 0) {
      const emptyEl = document.createElement("div");
      emptyEl.className = "git-empty";
      emptyEl.textContent = "No branches";
      $.gitBranchList.appendChild(emptyEl);
    }
  });
}

function makeBranchRow(b: { name: string; current: boolean }): HTMLDivElement {
  const row = document.createElement("div");
  row.className = `git-file-row${b.current ? " git-branch-current" : ""}`;
  const nameEl = document.createElement("span");
  nameEl.className = "git-file-name";
  nameEl.textContent = b.current ? `${b.name} (current)` : b.name;
  if (!b.current) {
    nameEl.addEventListener("click", () => {
      gitPost("/api/git/checkout", { branch: b.name }).then((d) => {
        if (d.error !== undefined) gitShowOutput(d.error);
        closeModal($.gitBranchModal);
        refreshGitStatus();
      }).catch(() => {});
    });
  }
  row.appendChild(nameEl);
  return row;
}

function gitCreateBranch(): void {
  const name = $.gitNewBranch.value.trim();
  if (name === "") return;
  gitPost("/api/git/checkout", { branch: name, create: true }).then((d) => {
    if (d.error !== undefined) gitShowOutput(d.error);
    closeModal($.gitBranchModal);
    refreshGitStatus();
  }).catch(() => {});
}

// --- GitHub CLI auth flow (install gh, then run gh auth login in the shell) ---

async function gitGhAuth(): Promise<void> {
  gitClearOutput();
  gitShowOutput("Adding gh to tools…");
  const tools = await apiGet<Record<string, Record<string, unknown>>>("/api/tools");
  if (tools === null) {
    gitShowOutput("Failed to read tools.json");
    return;
  }
  if (tools["binary"]?.["gh"] === undefined) {
    if (tools["binary"] === undefined) tools["binary"] = {};
    tools["binary"]["gh"] = {
      version: "v2.74.1",
      update: { method: "github", repo: "cli/cli" },
      install: "curl -fsSL https://github.com/cli/cli/releases/download/${VERSION}/gh_${VERSION_NOPFX}_linux_amd64.tar.gz | tar -xz --strip-components=1 -C ${TOOLS} gh_${VERSION_NOPFX}_linux_amd64/bin/gh",
    };
    await apiPut("/api/tools", tools);
  }
  gitShowOutput("Installing gh (this may take a moment)…");
  const installData = await apiPost<{ error?: string }>("/api/tools/install");
  if (installData === null || installData.error !== undefined) {
    gitShowOutput(`Install failed${installData?.error !== undefined ? `: ${installData.error}` : ""}`);
    return;
  }
  gitShowOutput("Running 'gh auth login' in the shell below…");
  document.dispatchEvent(new CustomEvent("shell-run", { detail: "gh auth login" }));
  refreshGitStatus();
}
