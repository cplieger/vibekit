// ---------------------------------------------------------------------------
// Chat-input drag-drop and paste: hand files to the chat view and they upload
// into the workspace uploads folder, with their paths auto-attached to the
// prompt input.
//
// The paperclip button opens the file picker (files-picker.ts) instead of
// an OS file dialog. The user picks an existing workspace file OR clicks
// "Upload here" inside the picker to upload from the OS. Either way, the
// path ends up in the prompt input.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { openFilePicker } from "./files-picker.js";
import { upload, partialUploadOf } from "./actions/files.js";
import { attachPathToActiveChat } from "./chat.js";
import { byId } from "./dom.js";
import { iconEl } from "./icon-el.js";
import { installDropZone } from "./drop-zone.js";
import { installComposerPaste } from "./composer-paste.js";
import { screenUploads, uploadLimitHint, UPLOADS_DIR } from "./upload-policy.js";
import * as toast from "./toast.js";
import { $ } from "./dom.js";

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

  // The limit used to be discoverable only as a server 413, which drop and
  // paste reach without the user consciously choosing a file. Stated where the
  // choice is made rather than in a banner nobody reads.
  attachBtn.dataset["tooltip"] = `Attach file (${uploadLimitHint().toLowerCase()})`;

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
      el("span", { className: "chat-drop-limit" }, uploadLimitHint()),
    ) as HTMLDivElement;
    chatView.style.position = "relative";
    chatView.appendChild(overlay);
    return overlay;
  };

  const uploadAndAttach = (files: FileList): void => {
    // Pre-flight before any bytes leave: an over-cap file can only end in a
    // 413, and a dropped folder is a batch nobody chose. Rejections are said
    // out loud, because a silently shortened batch reads as a lost file.
    const screened = screenUploads(files);
    if (screened.skipped !== "") {
      toast.error(screened.skipped);
    }
    if (screened.files === null) {
      return;
    }
    void upload.dispatch(
      { files: screened.files, targetDir: UPLOADS_DIR },
      {
        onSuccess: (paths) => {
          for (const p of paths) {
            attachPathToActiveChat(p);
          }
        },
        onError: (err) => {
          // A partial batch is not rolled back, so attach what landed. The
          // action's own toast already names the failure.
          for (const p of partialUploadOf(err.cause)) {
            attachPathToActiveChat(p);
          }
        },
      },
    );
  };

  installDropZone({
    container: chatView,
    get overlay() {
      return getOverlay();
    },
    onDrop: uploadAndAttach,
  });

  // A pasted screenshot, file or oversized text block is the same journey as a
  // dropped one: upload to the uploads folder, attach the path. The composer is
  // where the cursor is when the user pastes, so the listener lives on the
  // textarea rather than the document (a document listener would also fire for
  // the shell and the editor).
  installComposerPaste($.promptInput, uploadAndAttach);
}
