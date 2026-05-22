// ---------------------------------------------------------------------------
// Git panel rendering: status display, file lists, branch info, push/pull
// buttons, stash section. Extracted from git.ts for single-reason-to-change.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { showConfirm } from "./modals.js";
import {
  ICON_PLUS, ICON_MINUS, ICON_TRASH,
  ICON_GIT_UP_ARROW, ICON_GIT_DOWN_ARROW,
  iconEl,
} from "./icons.js";
import { openFileGitDiff } from "./editor-openers.js";
import type { GitFileEntry } from "./git-types.js";
import { gitPost, selectedRepoKey, getStatusData, refreshGitStatus } from "./git-core.js";

export function renderStagedSection(staged: GitFileEntry[]): void {
  if (staged.length > 0) {
    $.gitStagedSection.classList.remove("hidden");
    $.gitStagedList.replaceChildren();
    for (const f of staged) $.gitStagedList.appendChild(makeGitFileRow(f, true));
    $.gitCommitBtn.disabled = false;
    $.gitUnstageAllBtn.disabled = false;
  } else {
    $.gitStagedSection.classList.add("hidden");
    $.gitCommitBtn.disabled = true;
    $.gitUnstageAllBtn.disabled = true;
  }
}

export function renderChangedList(unstaged: GitFileEntry[]): void {
  $.gitChangedList.replaceChildren();
  for (const f of unstaged) $.gitChangedList.appendChild(makeGitFileRow(f, false));
  if (unstaged.length === 0) {
    const empty = document.createElement("div");
    empty.className = "git-empty"; empty.textContent = "Working tree clean";
    $.gitChangedList.appendChild(empty);
  }
}

export function renderPushPullButtons(): void {
  const d = getStatusData();
  if (d === null) return;
  $.gitPushBtn.disabled = d.remote === "" || d.ahead === 0;
  $.gitPushBtn.replaceChildren(...arrowButtonNodes(ICON_GIT_UP_ARROW, d.ahead));
  $.gitPullBtn.disabled = d.behind === 0;
  $.gitPullBtn.replaceChildren(...arrowButtonNodes(ICON_GIT_DOWN_ARROW, d.behind));
}



export function arrowButtonNodes(icon: string, count: number): Node[] {
  const nodes: Node[] = [iconEl(icon)];
  if (count > 0) {
    const pill = document.createElement("span");
    pill.className = "pill-count";
    pill.textContent = String(count);
    nodes.push(pill);
  }
  return nodes;
}

export function renderStashButtons(): void {
  const d = getStatusData();
  if (d === null) return;
  $.gitStashBtn.disabled = !d.has_dirty;
  $.gitStashPopBtn.disabled = d.stashes === 0;
  $.gitStashPopBtn.title = d.stashes > 0
    ? `Pop stash (${String(d.stashes)})` : "Pop stash";
}

function makeGitFileRow(f: GitFileEntry, staged: boolean): HTMLDivElement {
  const row = document.createElement("div");
  row.className = "git-file-row";
  const statusBadge = document.createElement("span");
  statusBadge.className = `git-file-status git-st-${f.status.toLowerCase()}`;
  statusBadge.textContent = f.status;
  const name = document.createElement("span");
  name.className = "git-file-name"; name.textContent = f.path; name.title = f.path;
  const repoKey = selectedRepoKey();
  name.addEventListener("click", () => openFileGitDiff(f.path, "HEAD", repoKey));
  const actionBtn = document.createElement("button");
  actionBtn.className = "list-row-btn";
  actionBtn.title = staged ? "Unstage" : "Stage";
  actionBtn.appendChild(iconEl(staged ? ICON_MINUS : ICON_PLUS));
  actionBtn.addEventListener("click", () => {
    gitPost(staged ? "/api/git/unstage" : "/api/git/stage", { files: [f.path] })
      .then(() => refreshGitStatus()).catch(() => {});
  });
  row.appendChild(statusBadge); row.appendChild(name); row.appendChild(actionBtn);
  if (!staged) {
    const discardBtn = document.createElement("button");
    discardBtn.className = "list-row-btn git-discard-btn";
    discardBtn.setAttribute("data-tooltip", "Discard changes");
    discardBtn.appendChild(iconEl(ICON_TRASH));
    discardBtn.addEventListener("click", () => {
      showConfirm(`Discard changes to ${f.path}? This cannot be undone.`, () => {
        gitPost("/api/git/discard", { files: [f.path] })
          .then(() => refreshGitStatus()).catch(() => {});
      }, "Discard");
    });
    row.appendChild(discardBtn);
  }
  return row;
}

export function updateGitBadge(): void {
  const hasChanges = (getStatusData()?.files ?? []).length > 0;
  $.gitBadge.classList.toggle("hidden", !hasChanges);
}
