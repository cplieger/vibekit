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
} from "./repo-picker.js";
import { initCIPill, refreshCIPill } from "./pr-checks.js";
import type { RepoEntry } from "./forge-types.js";
import {
  renderStagedSection, renderChangedList, renderPushPullButtons,
  renderStashButtons, updateGitBadge,
} from "./git-render.js";
import { registerGitCore } from "./git-core.js";
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

/** Fire a mutating gitPost, show its output, then refresh status. */
function mutateAndRefresh(label: string, endpoint: string, body: Record<string, unknown> = {}): void {
  gitClearOutput(); gitShowOutput(`${label}...`);
  gitPost(endpoint, body).then((d) => {
    const raw = d.error ?? d.output ?? "Done";
    const friendly = friendlyGitError(raw);
    gitShowOutput(friendly ?? raw);
    if (friendly !== null && GIT_ERROR_RULES.some(r => r.triggerAuth === true && raw.includes(r.match))) {
      showForgeAuthBanner();
    }
    refreshGitStatus();
  }).catch(() => { gitShowOutput(`${label} failed`); });
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

/** Show a dismissible banner linking to forge settings when auth fails. */
function showForgeAuthBanner(): void {
  const existing = document.getElementById("forge-auth-banner");
  if (existing !== null) return; // already showing
  const banner = document.createElement("div");
  banner.id = "forge-auth-banner";
  banner.className = "forge-auth-banner";
  banner.innerHTML =
    '<span>⚠ Forge authentication issue — </span>' +
    '<button type="button" class="action-pill" data-action="open-forge-settings">Open Settings → Git</button>' +
    '<button type="button" class="forge-auth-banner-dismiss" aria-label="Dismiss">✕</button>';
  banner.querySelector("[data-action='open-forge-settings']")!.addEventListener("click", () => {
    // Navigate to settings git tab
    document.querySelector<HTMLElement>("[data-tab='settings']")?.click();
    banner.remove();
  });
  banner.querySelector(".forge-auth-banner-dismiss")!.addEventListener("click", () => banner.remove());
  const gitPanel = document.getElementById("git-panel") ?? document.getElementById("git-section");
  if (gitPanel !== null) gitPanel.prepend(banner);
}

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

  $.gitRefreshBtn.addEventListener("click", refreshGitStatus);
  $.gitStageAllBtn.addEventListener("click", () => mutateAndRefresh("Staging", "/api/git/stage", { files: ["."] }));
  $.gitUnstageAllBtn.addEventListener("click", gitUnstageAll);
  $.gitDiscardAllBtn.addEventListener("click", gitDiscardAll);
  $.gitCommitBtn.addEventListener("click", gitCommit);
  $.gitPushBtn.addEventListener("click", () => {
    mutateAndRefresh("Pushing", "/api/git/push");
    // After a push, the forge may have new PR checks; give them a
    // few seconds to spin up before asking for a fresh summary.
    setTimeout(() => void refreshCIPill(), 3000);
  });
  $.gitPullBtn.addEventListener("click", () => mutateAndRefresh("Pulling", "/api/git/pull"));
  $.gitCloneBtn.addEventListener("click", gitClone);
  $.gitCloneToggleBtn.addEventListener("click", gitReclone);
  $.gitInitBtn.addEventListener("click", () => {
    $.gitCloneSection.classList.toggle("hidden");
  });
  $.gitRemoveRepoBtn.addEventListener("click", gitRemoveRepo);
  $.gitAiMsgBtn.addEventListener("click", gitAiMessage);
  $.gitBranchBtn.addEventListener("click", openBranchModal);
  $.gitCreateBranchBtn.addEventListener("click", gitCreateBranch);
  $.gitStashBtn.addEventListener("click", () => mutateAndRefresh("Stashing", "/api/git/stash"));
  $.gitStashPopBtn.addEventListener("click", () => mutateAndRefresh("Popping stash", "/api/git/stash-pop"));
  $.gitGhAuthBtn.addEventListener("click", gitGhAuth);

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
    $.gitCloneSection.classList.remove("hidden");
    return;
  }
  panel.viewState = { status: "loading", repoKey: key };
  applyRepoState();
  $.gitCloneSection.classList.add("hidden");
  $.gitRepoSection.classList.remove("hidden");
  $.gitBranchBtn.textContent = entry.local_branch !== undefined && entry.local_branch !== ""
    ? entry.local_branch : "loading...";
  refreshGitStatusQuick();
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
  $.gitCloneToggleBtn.disabled = true;
  $.gitRemoveRepoBtn.disabled = true;
  $.gitRefreshBtn.disabled = true;
}

/** Button state when at least one repo is selected: re-enable all
 *  repo-level actions that were disabled by applyNoRepoState. */
function applyRepoState(): void {
  $.gitBranchBtn.classList.remove("hidden");
  $.gitCloneToggleBtn.disabled = false;
  $.gitRemoveRepoBtn.disabled = false;
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

export function refreshGitStatus(): void {
  panel.abortPending();
  panel.statusController = new AbortController();
  const { signal } = panel.statusController;
  void gitGet("/api/git/status", "", signal).then((d) => {
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
  const key = selectedRepoKey();
  $.gitRemoveRepoBtn.disabled = key === "" || key === ".";

  renderStagedSection(staged);
  renderChangedList(unstaged);
  renderPushPullButtons();
  renderStashButtons();

  $.gitGhSection.classList.toggle("hidden", d.has_gh);
  updateGitBadge();
  loadGitLog();
}

// --- Actions ---

function gitUnstageAll(): void {
  const staged = (getStatusData()?.files ?? []).filter((f) => f.staged).map((f) => f.path);
  if (staged.length === 0) return;
  gitPost("/api/git/unstage", { files: staged })
    .then(() => refreshGitStatus()).catch(() => {});
}

function gitCommit(): void {
  const msg = $.gitCommitMsg.value.trim();
  if (msg === "") return;
  gitClearOutput(); gitShowOutput("Committing...");
  gitPost("/api/git/commit", { message: msg }).then((d) => {
    gitShowOutput(d.error ?? d.output ?? "Done");
    $.gitCommitMsg.value = "";
    refreshGitStatus();
  }).catch(() => { gitShowOutput("Commit failed"); });
}

function gitClone(): void {
  const url = $.gitCloneUrl.value.trim();
  if (url === "") return;
  panel.abortClone();
  panel.cloneController = new AbortController();
  const { signal } = panel.cloneController;
  gitClearOutput(); gitShowOutput("Cloning...");
  void apiPost<GitPostResult>("/api/git/clone", { url }, signal).then((d) => {
    if (signal.aborted) return;
    const raw = d?.error ?? d?.output ?? (d === null ? "Clone failed" : "Done");
    const text = String(raw);
    const needsAuth =
      GIT_ERROR_RULES.some(r => r.triggerAuth === true && text.includes(r.match));
    if (needsAuth) {
      gitShowOutput(
        "Clone failed: this repo looks private. Run `gh auth login` in the shell below, then retry.",
      );
      gitGhAuth();
    } else {
      gitShowOutput(text);
      $.gitCloneUrl.value = "";
      $.gitCloneSection.classList.add("hidden");
      // Trigger a registry refresh so the new clone appears in the picker.
      void apiPost("/api/forges/refresh", {});
    }
  });
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

function gitAiMessage(): void {
  const btn = $.gitAiMsgBtn;
  const msg = $.gitCommitMsg;
  btn.disabled = true;
  btn.classList.add("btn-loading");
  msg.placeholder = "Generating commit message\u2026";
  gitPost("/api/git/commit-message", {}).then((d) => {
    const text = d.output ?? d.error ?? "";
    if (text !== "") {
      msg.value = text;
      msg.rows = Math.min(10, Math.max(3, text.split("\n").length + 1));
    } else {
      msg.placeholder = "Stage changes first";
    }
  }).catch(() => { msg.placeholder = "Generation failed"; }).finally(() => {
    btn.disabled = false;
    btn.classList.remove("btn-loading");
  });
}

function gitDiscardAll(): void {
  const unstaged = (getStatusData()?.files ?? []).filter((f) => !f.staged);
  if (unstaged.length === 0) return;
  showConfirm(
    `Discard all ${String(unstaged.length)} changed file(s)? This cannot be undone.`,
    () => {
      gitPost("/api/git/discard", { files: unstaged.map((f) => f.path) })
        .then(() => refreshGitStatus()).catch(() => {});
    },
    "Discard",
  );
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

function gitGhAuth(): void {
  const btn = $.gitGhAuthBtn;
  btn.disabled = true;
  btn.replaceChildren(spinnerNode(), document.createTextNode("Adding gh to tools..."));
  void ensureGhInstalled(btn);
}

function spinnerNode(): HTMLDivElement {
  const d = document.createElement("div");
  d.className = "spinner-sm";
  d.style.display = "inline-block";
  d.style.verticalAlign = "middle";
  d.style.marginRight = "6px";
  return d;
}

async function ensureGhInstalled(btn: HTMLButtonElement): Promise<void> {
  const tools = await apiGet<Record<string, Record<string, unknown>>>("/api/tools");
  if (tools === null) {
    btn.textContent = "Failed";
    btn.disabled = false;
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
  btn.replaceChildren(spinnerNode(), document.createTextNode("Installing gh (this may take a moment)..."));
  const installData = await apiPost<{ error?: string }>("/api/tools/install");
  if (installData === null || installData.error !== undefined) {
    btn.textContent = "Install failed";
    btn.disabled = false;
    return;
  }
  btn.textContent = "Authenticate with gh";
  btn.disabled = false;
  document.dispatchEvent(new CustomEvent("shell-run", { detail: "gh auth login" }));
  refreshGitStatus();
}
