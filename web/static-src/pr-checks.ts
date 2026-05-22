// ---------------------------------------------------------------------------
// CI pill next to the current branch in the git view. Reads combined
// commit status via /api/forges/{id}/repos/{owner}/{name}/checks?ref=…
// and expands into an inline per-check breakdown on click.
//
// Pill states (colour-coded):
//   success  → green dot
//   pending  → amber pulsing dot
//   failure  → red dot
//   error    → red dot
//   canceled → grey dot
//   unknown  → hidden (no CI configured)
// ---------------------------------------------------------------------------

import { apiGet } from "./api-client.js";
import { onSelectionChange, hasForgeCredential } from "./repo-picker.js";
import { $ } from "./dom.js";
import type { RepoEntry } from "./forge-types.js";

type CIState = "success" | "failure" | "error" | "pending" | "canceled" | "unknown";

interface CheckStatus {
  name: string;
  state: CIState;
  url?: string;
}

interface CombinedStatus {
  state: CIState | "";
  checks: CheckStatus[];
}

/** Exhaustive pill-state → CSS class map. Adding a new CIState without a
 *  style entry is a compile error. */
const CI_PILL_STYLES: Record<Exclude<CIState, "unknown">, string> = {
  success: "git-ci-pill-success",
  failure: "git-ci-pill-failure",
  error: "git-ci-pill-error",
  pending: "git-ci-pill-pending",
  canceled: "git-ci-pill-canceled",
};

class CIPillController {
  private activeEntry: RepoEntry | null = null;
  private initialized = false;
  private expandedState: CombinedStatus | null = null;
  private refreshController: AbortController | null = null;

  init(): void {
    if (this.initialized) return;
    this.initialized = true;
    this.ensurePill();
    onSelectionChange((e) => {
      this.activeEntry = e;
      if (e !== null && this.shouldShow(e)) void this.refresh(e);
      else this.hidePill();
    });
  }

  async refreshPill(): Promise<void> {
    if (this.activeEntry !== null && this.shouldShow(this.activeEntry)) {
      await this.refresh(this.activeEntry);
    }
  }

  private shouldShow(e: RepoEntry | null): boolean {
    return e !== null
      && hasForgeCredential(e)
      && e.default_branch !== undefined && e.default_branch !== "";
  }

  private ensurePill(): void {
    const existing = document.getElementById("git-ci-pill");
    if (existing !== null) return;
    const pill = document.createElement("button");
    pill.id = "git-ci-pill";
    pill.type = "button";
    pill.className = "git-ci-pill hidden";
    pill.setAttribute("aria-haspopup", "true");
    pill.setAttribute("aria-expanded", "false");
    pill.addEventListener("click", () => { this.togglePanel(); });
    $.gitBranchBtn.insertAdjacentElement("afterend", pill);
  }

  private hidePill(): void {
    const pill = document.getElementById("git-ci-pill");
    pill?.classList.add("hidden");
    this.closePanel();
  }

  private async refresh(entry: RepoEntry): Promise<void> {
    const ref = entry.local_branch !== undefined && entry.local_branch !== ""
      ? entry.local_branch
      : (entry.default_branch ?? "");
    if (ref === "") return;
    this.refreshController?.abort();
    this.refreshController = new AbortController();
    const { signal } = this.refreshController;
    if (entry.forge_id === undefined || entry.forge_id === "") {
      this.expandedState = null;
      this.renderPill(null);
      return;
    }
    const url = `/api/forges/${encodeURIComponent(entry.forge_id)}/repos/${encodeURIComponent(entry.owner)}/${encodeURIComponent(entry.name)}/checks?ref=${encodeURIComponent(ref)}`;
    const res = await apiGet<{ checks: Array<{ name: string; status: string; conclusion: string; url?: string }> }>(url, signal);
    if (signal.aborted) return;
    if (res === null) {
      this.expandedState = null;
      this.renderPill(null);
      return;
    }
    // Map the new ForgeOps Check shape to the existing CombinedStatus.
    const checks: CheckStatus[] = res.checks.map((c) => {
      const out: CheckStatus = {
        name: c.name,
        state: mapConclusionToState(c.status, c.conclusion),
      };
      if (c.url !== undefined) out.url = c.url;
      return out;
    });
    const combined: CombinedStatus = {
      state: aggregateCIState(checks),
      checks,
    };
    this.expandedState = combined;
    this.renderPill(combined);
  }

  private renderPill(s: CombinedStatus | null): void {
    const pill = document.getElementById("git-ci-pill");
    if (pill === null) return;
    if (s === null || s.state === "" || s.state === "unknown" || s.checks.length === 0) {
      pill.classList.add("hidden");
      return;
    }
    pill.classList.remove("hidden");
    const styleClass = CI_PILL_STYLES[s.state as Exclude<CIState, "unknown">];
    pill.className = `git-ci-pill ${styleClass}`;
    const passing = s.checks.filter((c) => c.state === "success").length;
    pill.innerHTML = `
      <span class="git-ci-dot" aria-hidden="true"></span>
      <span class="git-ci-label">${passing}/${String(s.checks.length)}</span>
    `;
    pill.setAttribute("title", `CI: ${s.state} · ${String(passing)}/${String(s.checks.length)} checks passing`);
  }

  private togglePanel(): void {
    const pill = document.getElementById("git-ci-pill");
    if (pill === null || this.expandedState === null) return;
    const existing = document.getElementById("git-ci-panel");
    if (existing !== null) {
      this.closePanel();
      return;
    }
    const panel = buildPanel(this.expandedState);
    pill.insertAdjacentElement("afterend", panel);
    pill.setAttribute("aria-expanded", "true");
  }

  private closePanel(): void {
    document.getElementById("git-ci-panel")?.remove();
    document.getElementById("git-ci-pill")?.setAttribute("aria-expanded", "false");
  }
}

const controller = new CIPillController();

/** Wire the CI pill. Called once from initGitPanel. */
export function initCIPill(): void {
  controller.init();
}

/** Force a refresh; called by callers after a push so the pill
 *  updates promptly. */
export async function refreshCIPill(): Promise<void> {
  await controller.refreshPill();
}

function buildPanel(s: CombinedStatus): HTMLDivElement {
  const panel = document.createElement("div");
  panel.id = "git-ci-panel";
  panel.className = "git-ci-panel";
  const head = document.createElement("div");
  head.className = "git-ci-panel-head";
  head.textContent = `Checks: ${s.state}`;
  panel.appendChild(head);
  const list = document.createElement("ul");
  list.className = "git-ci-panel-list";
  for (const check of s.checks) {
    const row = document.createElement("li");
    row.className = `git-ci-panel-row git-ci-panel-row-${check.state}`;
    const dot = document.createElement("span");
    dot.className = "git-ci-dot";
    row.appendChild(dot);
    const name = document.createElement("span");
    name.className = "git-ci-panel-name";
    name.textContent = check.name || "(unnamed)";
    row.appendChild(name);
    if (check.url !== undefined && check.url !== "") {
      const link = document.createElement("a");
      link.href = check.url;
      link.target = "_blank";
      link.rel = "noopener";
      link.className = "git-ci-panel-link";
      link.textContent = "logs";
      row.appendChild(link);
    }
    list.appendChild(row);
  }
  panel.appendChild(list);
  return panel;
}


// --- Adapter: ForgeOps Check shape → legacy CombinedStatus shape -----

/** Convert (status, conclusion) from forges.Check → CIState. */
export function mapConclusionToState(status: string, conclusion: string): CIState {
  if (status !== "completed") {
    if (status === "in_progress" || status === "queued") return "pending";
    return "unknown";
  }
  switch (conclusion) {
    case "success": return "success";
    case "failure": return "failure";
    case "cancelled":
    case "canceled": return "canceled";
    case "skipped": return "success";
  }
  return "error";
}

/** Aggregate per-check states into a single CIState. */
export function aggregateCIState(checks: CheckStatus[]): CIState | "" {
  if (checks.length === 0) return "";
  let pending = false;
  let success = true;
  for (const c of checks) {
    if (c.state === "failure" || c.state === "error") return c.state;
    if (c.state === "pending") pending = true;
    if (c.state !== "success") success = false;
  }
  if (pending) return "pending";
  if (success) return "success";
  return "unknown";
}
