// ---------------------------------------------------------------------------
// Editor: Supervised pending-change resolution from editor toolbar.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { countHunks } from "./diff-pane.js";
import { getActiveId, get } from "./store.js";
import { resolvePendingChangeAction } from "./actions/chat.js";
import { resolvePendingPartial } from "./actions/editor.js";
import type { FileState } from "./editor-types.js";
import { fileStates, getActiveFilePath, parsePendingPath, getCachedDiff, closeFile } from "./editor-types.js";
import { emitBus, BUS_ACTIVATE_CHAT } from "./bus.js";

/** Resolve the active pending-change tab. Works for both Accept and
 *  Reject; the server handles the rest. Closes the tab on success. */
export async function resolveActivePending(action: "accept" | "reject"): Promise<void> {
  const state = fileStates.get(getActiveFilePath());
  if (state === undefined) return;
  const { chatID, toolCallID } = parsePendingPath(state.path);
  if (chatID === "" || toolCallID === "") return;
  const path = state.path;
  $.editorPendingAcceptBtn.disabled = true;
  $.editorPendingRejectBtn.disabled = true;
  try {
    const result = await resolvePendingChangeAction.dispatch(
      { chatID, toolCallID, action },
    );
    if (result !== null) closeFile(path);
  } finally {
    $.editorPendingAcceptBtn.disabled = false;
    $.editorPendingRejectBtn.disabled = false;
  }
}

/** Refresh the per-hunk Apply-selected toolbar button state. */
export function refreshPendingToolbar(state: FileState): void {
  const btn = $.editorPendingApplyPartialBtn;
  const total = pendingHunkCountFor(state);
  const decided = state.pendingHunkDecisions.size;
  const visible = decided > 0;
  btn.classList.toggle("hidden", !visible);
  btn.disabled = decided < total;
  btn.title = btn.disabled
    ? `Decide every hunk (${String(decided)} / ${String(total)})`
    : "Apply only the accepted hunks";
}

/** Count the hunks in a pending diff source. Cached on state. */
function pendingHunkCountFor(state: FileState): number {
  if (state.pendingHunkCount !== null) return state.pendingHunkCount;
  if (state.mode.kind !== "diff") return 0;
  const count = countHunks(getCachedDiff(state));
  state.pendingHunkCount = count;
  return count;
}

/** Apply the active pending op with only the user-accepted hunks. */
export async function applyActivePendingPartial(): Promise<void> {
  const state = fileStates.get(getActiveFilePath());
  if (state === undefined) return;
  const { chatID, toolCallID } = parsePendingPath(state.path);
  if (chatID === "" || toolCallID === "") return;
  if (state.mode.kind !== "diff") return;

  const merged = buildPartialMergeText(state, state.pendingHunkDecisions);
  const approxBytes = merged.length * 4;
  if (approxBytes > 4 * 1024 * 1024) {
    const { showBanner } = await import("./banner-stack.js");
    showBanner(
      chatID,
      "partial-merge-too-large",
      "Merged result is too large (>4 MiB). Reject more hunks or use Accept for the full change.",
      "warning",
      true,
    );
    return;
  }
  const path = state.path;
  const result = await resolvePendingPartial.dispatch({ chatID, toolCallID, mergedText: merged });
  if (result !== null) closeFile(path);
}

/** @internal Exported for testing. */
export function buildPartialMergeText(
  state: FileState,
  decisions: Map<number, "accept" | "reject">,
): string {
  const diff = getCachedDiff(state);
  const out: string[] = [];
  let hunkIdx = 0;
  let inHunk = false;
  let hunkOld: string[] = [];
  let hunkNew: string[] = [];
  const flushHunk = (): void => {
    if (hunkOld.length === 0 && hunkNew.length === 0) return;
    const decision = decisions.get(hunkIdx) ?? "reject";
    out.push(...(decision === "reject" ? hunkOld : hunkNew));
    hunkIdx++;
    hunkOld = [];
    hunkNew = [];
  };
  for (const line of diff) {
    if (line.kind === "ctx") {
      if (inHunk) { flushHunk(); inHunk = false; }
      out.push(line.text);
    } else {
      inHunk = true;
      if (line.kind === "del") hunkOld.push(line.text);
      else hunkNew.push(line.text);
    }
  }
  if (inHunk) flushHunk();
  return out.join("\n");
}

/** Pre-fill the chat input with a discuss template for the staged change. */
export function openDiscussPrompt(path: string, hunkText: string): void {
  const { chatID, toolCallID } = parsePendingPath(path);
  if (chatID === "" || toolCallID === "") return;
  const state = fileStates.get(path) ?? null;
  const filePath = resolvePendingFilePath(chatID, toolCallID);
  const template = buildDiscussTemplate(filePath, hunkText, state);
  if (getActiveId() !== chatID) {
    emitBus(BUS_ACTIVATE_CHAT, { chatID, then: () => fillPromptInput(template) });
    return;
  }
  fillPromptInput(template);
}

function fillPromptInput(template: string): void {
  const input = $.promptInput;
  input.value = template;
  const placeholder = "Your question: ";
  const start = template.indexOf(placeholder);
  if (start >= 0) {
    input.focus();
    input.setSelectionRange(start + placeholder.length, start + placeholder.length);
  } else {
    input.focus();
  }
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

function resolvePendingFilePath(chatID: string, toolCallID: string): string {
  const s = get(chatID);
  const change = s?.pending_changes.find((c) => c.tool_call_id === toolCallID);
  return change?.path ?? "(unknown)";
}

function buildDiscussTemplate(
  filePath: string,
  hunkText: string,
  state: FileState | null,
): string {
  const lines: string[] = [];
  lines.push("Your question: ");
  lines.push("");
  lines.push(`(About pending change in \`${filePath}\`)`);
  lines.push("");
  if (hunkText !== "") {
    lines.push("```diff");
    lines.push(hunkText);
    lines.push("```");
  } else if (state !== null && state.mode.kind === "diff") {
    lines.push("```diff");
    for (const line of buildUnifiedDiffLines(state)) {
      lines.push(line);
    }
    lines.push("```");
  }
  return lines.join("\n");
}

function buildUnifiedDiffLines(state: FileState): string[] {
  const maxLines = 400;
  const diff = getCachedDiff(state);
  const out: string[] = [];
  for (const line of diff) {
    if (out.length >= maxLines) break;
    if (line.kind === "del") out.push("-" + line.text);
    else if (line.kind === "add") out.push("+" + line.text);
  }
  const totalChanges = diff.reduce(
    (n, l) => n + (l.kind === "add" || l.kind === "del" ? 1 : 0), 0);
  if (totalChanges > out.length) {
    out.push(`... ${String(totalChanges - out.length)} more changed lines omitted`);
  }
  return out;
}

/** Toolbar variant: discuss the whole change (no specific hunk). */
export function openDiscussPromptForActive(): void {
  const state = fileStates.get(getActiveFilePath());
  if (state === undefined) return;
  openDiscussPrompt(state.path, "");
}
