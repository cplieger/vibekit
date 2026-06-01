// ---------------------------------------------------------------------------
// Checkpoint restore: event delegation on messages container for restore
// buttons + two-phase confirm flow with dirty-file intersection.
// ---------------------------------------------------------------------------

import { getActiveId } from "../store.js";
import { restoreCheckpoint } from "../actions/chat.js";
import { apiAction } from "../actions/index.js";
import { API_CHECKPOINTS } from "../actions/conflicts.js";

/** Wire checkpoint restore buttons via event delegation. Called once at
 *  startup from app.ts. */
export function wireCheckpointRestore(messagesEl: HTMLElement): void {
  messagesEl.addEventListener("click", (e: MouseEvent) => {
    const btn = (e.target as HTMLElement).closest<HTMLElement>(".checkpoint-restore");
    if (btn === null) {
      return;
    }
    const tag: string | undefined = btn.dataset["tag"];
    const chatID = getActiveId();
    if (tag === undefined || chatID === "") {
      return;
    }
    void confirmAndRestore(chatID, tag);
  });
}

async function confirmAndRestore(chatID: string, tag: string): Promise<void> {
  const preview = await fetchRestorePreview(chatID, tag);
  const dirty = await intersectDirty(preview);
  if (dirty.length > 0) {
    const sample = dirty.slice(0, 3).join(", ");
    const more = dirty.length > 3 ? ` (+${String(dirty.length - 3)} more)` : "";
    const ok = await confirmDestructive(
      `Restore would overwrite unsaved edits in ${sample}${more}. Continue anyway?`,
      "Discard and restore",
    );
    if (!ok) {
      return;
    }
  } else if (
    !(await confirmDestructive(
      "Restore to this checkpoint? Current file changes will be reverted.",
      "Restore",
    ))
  ) {
    return;
  }
  void restoreCheckpoint.dispatch({ chatID, tag });
}

/** Action for fetching restore preview (best-effort, no toast). */
const fetchRestorePreviewAction = apiAction<{ chatID: string; tag: string }, { files?: string[] }>({
  name: "checkpoint.preview",
  scope: ({ chatID }) => `chat:${chatID}`,
  request: ({ chatID, tag }) => ({
    method: "GET",
    path: `${API_CHECKPOINTS}/${encodeURIComponent(chatID)}/restore-preview?tag=${encodeURIComponent(tag)}`,
  }),
  success: false,
  error: false,
});

async function fetchRestorePreview(chatID: string, tag: string): Promise<string[]> {
  const resp = await fetchRestorePreviewAction.dispatch({ chatID, tag });
  return resp?.files ?? [];
}

async function intersectDirty(preview: string[]): Promise<string[]> {
  if (preview.length === 0) {
    return [];
  }
  const { getDirtyEditorPaths } = await import("../editor-core.js");
  const dirty = new Set(getDirtyEditorPaths());
  return preview.filter((p) => dirty.has(p));
}

async function confirmDestructive(msg: string, btn: string): Promise<boolean> {
  const { confirm: confirmDialog } = await import("../confirm.js");
  return confirmDialog(msg, btn, "destructive");
}
