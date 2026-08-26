// ---------------------------------------------------------------------------
// Chat-input drag-drop and paste: hand files to the chat view and they upload
// into the workspace uploads folder, with their paths auto-attached to the
// prompt input.
//
// The THIRD door — an explicit "attach a file" click — is not here any more. It
// was a standalone paperclip pill in the composer row; it is now a row in the
// chat-actions menu (chat-options.ts), which calls the same openFilePicker.
// Drop and paste stay, because they are gestures on the chat view and the
// textarea rather than a control in the pill row.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { upload, partialUploadOf } from "./actions/files.js";
import { attachPathsToActiveChat } from "./chat.js";
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
  const chatView = byId<HTMLDivElement>("chat-view");

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
          // DETACHED: an upload callback with nothing after it that reads the chat.
          void attachPathsToActiveChat(paths);
        },
        onError: (err) => {
          // A partial batch is not rolled back, so attach what landed. The
          // action's own toast already names the failure.
          void attachPathsToActiveChat(partialUploadOf(err.cause));
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
