// ---------------------------------------------------------------------------
// Messages shared: the late-bound conflict-chip renderer + badge refresh,
// extracted so conflicts.ts and messages-actions.ts don't form a static import
// cycle (conflicts.ts registers the renderer; messages-actions.ts reaches
// conflicts.ts via a dynamic import).
// ---------------------------------------------------------------------------

import { getActiveId } from "./store.js";

// Late-bound conflict chip renderer — registered by conflicts.ts at load time.
let renderChipFn: ((row: HTMLElement, chatID: string, path: string) => void) | null = null;

export function registerConflictChipRenderer(
  fn: (row: HTMLElement, chatID: string, path: string) => void,
): void {
  renderChipFn = fn;
}

/** Re-decorate any tool-edit action rows pointing at `path` with a
 *  freshly-landed conflict chip. */
export function refreshConflictBadges(chatID: string, path: string): void {
  if (getActiveId() !== chatID) {
    return;
  }
  if (renderChipFn === null) {
    return;
  }
  const rows = document.querySelectorAll<HTMLElement>(".tool-edit-actions");
  for (const row of rows) {
    const card = row.closest<HTMLElement>(".tool-call");
    if (card === null) {
      continue;
    }
    if (card.dataset["filePath"] !== path) {
      continue;
    }
    renderChipFn(row, chatID, path);
  }
}
