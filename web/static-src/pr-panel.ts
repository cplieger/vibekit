// ---------------------------------------------------------------------------
// Pull request panel. Reacts to repo-picker selection changes; when a
// registry entry with a forge credential is active, lists open PRs and
// offers a two-stage create modal (edit title/body → submit). Per-row
// actions: merge, close, reopen, checkout (local fetch + branch swap),
// and open-on-forge.
//
// CI status is fetched separately by pr-checks.ts — the PR list only
// carries the mergeable state + base/head. Merging the dedicated
// checks into each row would muddy the minimum-useful PR summary.
// ---------------------------------------------------------------------------

import { apiGet, apiPost } from "./api-client.js";
import { showConfirm } from "./modals.js";
import { relativeTime } from "./files-shared.js";
import { onSelectionChange, hasForgeCredential } from "./repo-picker.js";
import { load as loadUIState, save as saveUIState } from "./ui-state.js";
import { $ } from "./dom.js";
import type { RepoEntry } from "./forge-types.js";

// --- Types (matches forges.PR via wire/types.gen.ts) ---

interface PullRequestSummary {
  number: number;
  title: string;
  body?: string;
  state: string;
  draft?: boolean;
  mergeable?: boolean;
  source_branch: string;
  target_branch: string;
  url?: string;
  author?: string;
  created_at?: number;
  updated_at?: number;
}

// prsBaseURL builds the new API base for PR operations from a RepoEntry.
// Returns "" for entries that aren't forge-tracked (no PR ops possible).
function prsBaseURL(e: RepoEntry): string {
  if (e.forge_id === undefined || e.forge_id === "") return "";
  return `/api/forges/${encodeURIComponent(e.forge_id)}/repos/${encodeURIComponent(e.owner)}/${encodeURIComponent(e.name)}/prs`;
}

// --- Controller ---

const FETCH_TIMEOUT_MS = 10_000;

class PRPanelController {
  private activeEntry: RepoEntry | null = null;
  private initialized = false;
  private refreshController: AbortController | null = null;
  private createDialogController: AbortController | null = null;

  init(): void {
    if (this.initialized) return;
    this.initialized = true;
    this.wireDialog();
    this.wireNewButton();
    onSelectionChange((e) => {
      this.activeEntry = e;
      this.renderFrame();
      if (e !== null && this.shouldShow(e)) void this.refreshList(e);
    });
  }

  private shouldShow(e: RepoEntry | null): boolean {
    return e !== null
      && hasForgeCredential(e)
      && e.full_name !== "";
  }

  private renderFrame(): void {
    $.prSection.classList.toggle("hidden", !this.shouldShow(this.activeEntry));
  }

  // --- List ---

  private async refreshList(entry: RepoEntry): Promise<void> {
    const list = $.prList;
    const empty = $.prEmpty;

    // Supersede any in-flight request
    this.refreshController?.abort();
    this.refreshController = new AbortController();
    const signal = AbortSignal.any([
      this.refreshController.signal,
      AbortSignal.timeout(FETCH_TIMEOUT_MS),
    ]);

    list.replaceChildren();
    empty.classList.add("hidden");
    const skeleton = document.createElement("div");
    skeleton.className = "git-pr-skeleton";
    skeleton.textContent = "Loading…";
    list.appendChild(skeleton);

    const data = await apiGet<{ prs: PullRequestSummary[] }>(
      `${prsBaseURL(entry)}?state=open`, signal,
    );
    if (signal.aborted) return;
    list.replaceChildren();
    if (data === null) {
      empty.classList.remove("hidden");
      empty.textContent = "Couldn't load pull requests (check the forge credential).";
      return;
    }
    const prs = data.prs ?? [];
    if (prs.length === 0) {
      empty.classList.remove("hidden");
      empty.textContent = "No open pull requests.";
      return;
    }
    for (const pr of prs) list.appendChild(this.buildRow(entry, pr));
  }

  private buildRow(entry: RepoEntry, pr: PullRequestSummary): HTMLElement {
    const row = document.createElement("div");
    row.className = "git-pr-row";

    // Main line: #number + title + state badge.
    const head = document.createElement("div");
    head.className = "git-pr-row-head";
    const num = document.createElement("span");
    num.className = "git-pr-row-num";
    num.textContent = `#${String(pr.number)}`;
    head.appendChild(num);
    const title = document.createElement("a");
    title.className = "git-pr-row-title";
    title.textContent = pr.title;
    if (pr.url !== undefined && pr.url !== "") {
      title.href = pr.url;
      title.target = "_blank";
      title.rel = "noopener";
    }
    head.appendChild(title);
    if (pr.draft === true) head.appendChild(badge("draft", "git-pr-badge-draft"));
    if (pr.mergeable === false) {
      head.appendChild(badge("conflicts", "git-pr-badge-conflicting"));
    }
    row.appendChild(head);

    // Secondary line: author + base/head.
    const meta = document.createElement("div");
    meta.className = "git-pr-row-meta";
    const parts: string[] = [];
    if (pr.author !== undefined && pr.author !== "") parts.push(`@${pr.author}`);
    parts.push(`${pr.source_branch} → ${pr.target_branch}`);
    if (pr.updated_at !== undefined && pr.updated_at > 0) parts.push(relativeTime(pr.updated_at));
    meta.textContent = parts.join(" · ");
    row.appendChild(meta);

    // Actions.
    const actions = document.createElement("div");
    actions.className = "git-pr-row-actions";

    const mergeBtn = document.createElement("button");
    mergeBtn.type = "button";
    mergeBtn.className = "btn-small btn-primary";
    mergeBtn.textContent = "Merge";
    mergeBtn.disabled = pr.draft === true;
    mergeBtn.addEventListener("click", () => void this.mergePR(entry, pr));

    const mergeMethod = document.createElement("select");
    mergeMethod.className = "git-pr-merge-method";
    mergeMethod.setAttribute("aria-label", "Merge method");
    for (const m of ["merge", "squash", "rebase"] as const) {
      const opt = document.createElement("option");
      opt.value = m; opt.textContent = m;
      mergeMethod.appendChild(opt);
    }
    mergeMethod.value = loadUIState().merge_method[entry.id] ?? "squash";
    mergeMethod.addEventListener("change", () => {
      saveUIState({ merge_method: { ...loadUIState().merge_method, [entry.id]: mergeMethod.value } });
    });
    mergeBtn.dataset["prNumber"] = String(pr.number);
    mergeBtn.dataset["methodSelectorId"] = `pr-method-${String(pr.number)}`;
    mergeMethod.id = `pr-method-${String(pr.number)}`;

    const closeBtn = document.createElement("button");
    closeBtn.type = "button";
    closeBtn.className = "btn-small";
    closeBtn.textContent = "Close";
    closeBtn.addEventListener("click", () => void this.closePR(entry, pr));

    const checkoutBtn = document.createElement("button");
    checkoutBtn.type = "button";
    checkoutBtn.className = "btn-small";
    checkoutBtn.textContent = "Checkout";
    checkoutBtn.addEventListener("click", () => void this.checkoutPR(entry, pr));

    actions.appendChild(mergeMethod);
    actions.appendChild(mergeBtn);
    actions.appendChild(checkoutBtn);
    actions.appendChild(closeBtn);
    row.appendChild(actions);

    return row;
  }

  // --- Merge / close / checkout ---

  private async mergePR(entry: RepoEntry, pr: PullRequestSummary): Promise<void> {
    const method = (document.getElementById(`pr-method-${String(pr.number)}`) as HTMLSelectElement | null)?.value ?? "squash";
    const deleteBranch = loadUIState().merge_delete_branch[entry.id] !== false; // default on
    const confirmed = await new Promise<boolean>((resolve) => {
      showConfirm(
        `Merge #${String(pr.number)} "${pr.title}" using ${method}?${deleteBranch ? " The source branch will be deleted." : ""}`,
        () => resolve(true),
        "Merge",
      );
      // showConfirm has no "cancel" callback hook in its current API;
      // the confirm dialog's dismiss is equivalent to no-op. We use the
      // promise only to serialise the flow.
      setTimeout(() => resolve(false), 30_000);
    });
    if (!confirmed) return;
    const ok = await apiPost<{ ok: boolean }>(
      `${prsBaseURL(entry)}/${String(pr.number)}/merge`,
      { method, delete_source_branch: deleteBranch },
    );
    if (ok === null) return;
    if (this.activeEntry !== null) void this.refreshList(this.activeEntry);
  }

  private async closePR(entry: RepoEntry, pr: PullRequestSummary): Promise<void> {
    showConfirm(
      `Close #${String(pr.number)} "${pr.title}" without merging?`,
      async () => {
        await apiPost(
          `${prsBaseURL(entry)}/${String(pr.number)}/close`,
          {},
        );
        if (this.activeEntry !== null) void this.refreshList(this.activeEntry);
      },
      "Close",
    );
  }

  private async checkoutPR(entry: RepoEntry, pr: PullRequestSummary): Promise<void> {
    const repo = entry.name !== "" ? entry.name : ".";
    const fetchResult = await apiPost<{ output?: string; error?: string }>(
      "/api/git/pr-fetch",
      { repo, number: pr.number, head: pr.source_branch },
    );
    if (fetchResult === null || fetchResult.error !== undefined) {
      console.warn("PR checkout failed:", fetchResult?.error ?? "Fetch failed");
      return;
    }
    await apiPost("/api/git/checkout", { repo, branch: `pr-${String(pr.number)}` });
  }

  // --- Create modal ---

  private wireNewButton(): void {
    $.prNewBtn.addEventListener("click", () => void this.openCreateDialog());
  }

  private wireDialog(): void {
    const dlg = $.prCreateDialog;
    for (const el of dlg.querySelectorAll("[data-pr-close]")) {
      el.addEventListener("click", () => dlg.close());
    }
    $.prSubmitBtn.addEventListener("click", () => void this.submitCreate());
    $.prGenerateBtn.addEventListener("click", () => void this.regenerateBody());
  }

  private getDialog(): HTMLDialogElement {
    return $.prCreateDialog;
  }

  private async openCreateDialog(): Promise<void> {
    if (this.activeEntry === null) return;
    this.createDialogController?.abort();
    this.createDialogController = new AbortController();
    const signal = AbortSignal.any([
      this.createDialogController.signal,
      AbortSignal.timeout(FETCH_TIMEOUT_MS),
    ]);
    const dlg = this.getDialog();
    const status = $.prDialogStatus;
    status.textContent = ""; status.className = "forge-status";
    $.prBase.value = this.activeEntry.default_branch ?? "main";
    $.prHead.value = this.activeEntry.local_branch ?? "";
    $.prTitle.value = "";
    $.prBody.value = "";
    $.prDraft.checked = false;
    dlg.showModal();
    // Kick off title + body generation in parallel.
    void this.prefillTitle(signal);
    void this.regenerateBody(signal);
  }

  private async prefillTitle(signal?: AbortSignal): Promise<void> {
    if (this.activeEntry === null) return;
    const titleInput = $.prTitle;
    const repo = this.activeEntry.name !== "" ? this.activeEntry.name : ".";
    const res = await apiGet<{ entries: string[] }>(
      `/api/git/log?repo=${encodeURIComponent(repo)}`,
      signal,
    );
    if (signal?.aborted) return;
    const first = res?.entries?.[0];
    if (first !== undefined && first !== "") {
      const space = first.indexOf(" ");
      titleInput.value = space > 0 ? first.substring(space + 1) : first;
      return;
    }
    titleInput.value = this.activeEntry.local_branch ?? "";
  }

  private async regenerateBody(signal?: AbortSignal): Promise<void> {
    if (this.activeEntry === null) return;
    const body = $.prBody;
    const baseInput = $.prBase;
    body.value = "Generating…";
    body.disabled = true;
    const repo = this.activeEntry.name !== "" ? this.activeEntry.name : ".";
    const res = await apiPost<{ output?: string; error?: string }>(
      "/api/git/pr-description",
      { repo, branch: baseInput.value },
      signal,
    );
    if (signal?.aborted) return;
    body.disabled = false;
    if (res === null || res.error !== undefined) {
      body.value = res?.error ?? "Could not generate a description; write your own.";
      return;
    }
    body.value = res.output ?? "";
  }

  private async submitCreate(): Promise<void> {
    if (this.activeEntry === null) return;
    const status = $.prDialogStatus;
    const title = $.prTitle.value.trim();
    const bodyValue = $.prBody.value;
    const base = $.prBase.value.trim();
    const head = $.prHead.value.trim();
    const draft = $.prDraft.checked;

    if (title === "" || base === "" || head === "") {
      status.textContent = "Title, base, and head are required.";
      status.className = "forge-status forge-status-error";
      return;
    }
    status.textContent = "Opening pull request…";
    status.className = "forge-status forge-status-pending";
    const res = await apiPost<PullRequestSummary>(
      prsBaseURL(this.activeEntry),
      { title, body: bodyValue, target_branch: base, source_branch: head, draft },
    );
    if (res === null) {
      status.textContent = "Creation failed (check base/head branches and credential permissions).";
      status.className = "forge-status forge-status-error";
      return;
    }
    status.textContent = `Opened #${String(res.number)}`;
    status.className = "forge-status forge-status-ok";
    setTimeout(() => {
      this.getDialog().close();
      if (this.activeEntry !== null) void this.refreshList(this.activeEntry);
    }, 400);
  }
}

// --- Module-level helpers (stateless) ---

function badge(text: string, cls: string): HTMLSpanElement {
  const s = document.createElement("span");
  s.className = `git-pr-badge ${cls}`;
  s.textContent = text;
  return s;
}

// --- Singleton export ---

const controller = new PRPanelController();

export function initPRPanel(): void {
  controller.init();
}
