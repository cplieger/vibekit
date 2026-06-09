// ---------------------------------------------------------------------------
// Chat-input drag-drop: drop files onto the chat view to upload them to
// the workspace root and auto-attach their paths to the prompt input.
//
// The paperclip button opens the file picker (files-picker.ts) instead of
// an OS file dialog. The user picks an existing workspace file OR clicks
// "Upload here" inside the picker to upload from the OS. Either way, the
// path ends up in the prompt input.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { openFilePicker } from "./files-picker.js";
import { upload } from "./actions/files.js";
import { attachPathToActiveChat } from "./chat.js";
import { byId } from "./dom.js";
import { iconEl } from "./icon-el.js";
import { installDropZone } from "./drop-zone.js";

// Upload glyph (Lucide upload). 32px to suit the drop-overlay; the down-arrow
// ICON_DOWNLOAD in icons.ts is the inverse and not interchangeable here.
const ICON_UPLOAD =
  '<svg width="32" height="32" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 01-2 2H5a2 2 0 01-2-2v-4"/><polyline points="17 8 12 3 7 8"/><line x1="12" y1="3" x2="12" y2="15"/></svg>';

export function initChatAttach(): void {
  const attachBtn = byId<HTMLButtonElement>("attach-btn");
  const chatView = byId<HTMLDivElement>("chat-view");

  attachBtn.addEventListener("click", () => {
    openFilePicker();
  });

  // Create the overlay lazily on first use.
  let overlay: HTMLDivElement | null = null;
  const getOverlay = (): HTMLDivElement => {
    if (overlay !== null) {
      return overlay;
    }
    overlay = el(
      "div",
      { className: "chat-drop-overlay hidden" },
      iconEl(ICON_UPLOAD),
      el("span", null, "Drop files to upload"),
    ) as HTMLDivElement;
    chatView.style.position = "relative";
    chatView.appendChild(overlay);
    return overlay;
  };

  installDropZone({
    container: chatView,
    get overlay() {
      return getOverlay();
    },
    onDrop: (files) => {
      void upload.dispatch(
        { files, targetDir: "." },
        {
          onSuccess: (paths) => {
            for (const p of paths) {
              attachPathToActiveChat(p);
            }
          },
        },
      );
    },
  });
}
