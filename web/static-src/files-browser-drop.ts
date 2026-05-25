// ---------------------------------------------------------------------------
// File browser: drag-drop upload into the browser view (current dir or
// hovered folder). Uses the shared drop-zone helper for the boilerplate.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { type FileEntry, joinPath } from "./files-shared.js";
import { attachPathToActiveChat } from "./chat.js";
import { installDropZone } from "./drop-zone.js";
import { uploadAction } from "./actions/files.js";

export interface DragDropContext {
  getCurrentPath: () => string;
  getEntryMap: () => Map<string, FileEntry>;
  reload: () => void;
}

export function initBrowserDragDrop(ctx: DragDropContext): void {
  const wrap = $.fbList.parentElement;
  if (wrap === null) return;
  let dropTargetFolder = "";

  const clearDropTarget = (): void => {
    dropTargetFolder = "";
    for (const row of [...$.fbList.children]) {
      (row as HTMLElement).classList.remove("fb-drop-target");
    }
  };

  installDropZone({
    container: wrap,
    overlay: $.fbDropOverlay,
    onDragOver: (e) => {
      const row = (e.target as HTMLElement).closest(".fb-row") as HTMLDivElement | null;
      if (row !== null) {
        const name = row.dataset["name"];
        const entryMap = ctx.getEntryMap();
        const entry = name !== undefined ? entryMap.get(name) : undefined;
        if (entry?.isDir === true) {
          if (dropTargetFolder !== entry.name) {
            clearDropTarget();
            dropTargetFolder = entry.name;
            row.classList.add("fb-drop-target");
          }
          return;
        }
      }
      if (dropTargetFolder !== "") clearDropTarget();
    },
    onDragLeave: clearDropTarget,
    onDrop: (files) => {
      const currentPath = ctx.getCurrentPath();
      const targetDir = dropTargetFolder !== ""
        ? joinPath(currentPath, dropTargetFolder) : currentPath;
      dropTargetFolder = "";
      void uploadAction.dispatch({ files, targetDir }).then((paths) => {
        if (paths === null) return;
        ctx.reload();
        for (const p of paths) attachPathToActiveChat(p);
      });
    },
  });
}
