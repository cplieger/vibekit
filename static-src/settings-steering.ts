// ---------------------------------------------------------------------------
// Settings → Instructions: the global-instructions document, auto-saved.
//
// Two entry points with different LIFETIMES: the listeners must exist before the
// panel can be typed into, the read must not (it was a boot-path file read for a
// panel nobody had opened). settings.ts wires `initSteeringEditor` from `initUI`
// and `loadSteeringDoc` into the tab loader map.
//
// THE BOX IS READ-ONLY UNTIL IT HOLDS THE DOCUMENT: a save PUTs `textarea.value` as
// the WHOLE document and the server REMOVES the file when it trims to empty, so a
// keystroke into an unloaded box commits the fragment as the whole thing.
// ---------------------------------------------------------------------------

import { apiGet } from "./api-client.js";
import { $ } from "./dom.js";
import { showSaving, showSaved, showError, STEERING_SAVE_KEY } from "./save-indicator.js";
import { saveSteering } from "./actions/settings.js";
import { registerCleanup, debouncedDispatch, subscribeByName } from "./actions/index.js";

/** Where the document's read has got to. All three states are consumed:
 *  `unread` starts a read — and is where a FAILED read returns, which is what
 *  makes the focus retry below possible — `reading` makes a concurrent call a
 *  no-op, and `loaded` is terminal, so nothing can overwrite what the reader is
 *  now typing. */
let read: "unread" | "reading" | "loaded" = "unread";

/** Read the document into its textarea and hand the box over to the reader.
 *  Fired on the Instructions panel's activation via the settings-tabs loader map,
 *  and again by a focus on a box a failed read left locked. */
export function loadSteeringDoc(): void {
  if (read !== "unread") {
    return;
  }
  read = "reading";
  const textarea = $.steeringInput;
  void apiGet<{ content?: string }>("/api/steering").then((d) => {
    if (typeof d?.content !== "string") {
      // The box stays locked, because an empty box is not an empty document and
      // the save cannot tell them apart. Back to `unread` so a focus retries.
      read = "unread";
      showError(STEERING_SAVE_KEY);
      return;
    }
    textarea.value = d.content;
    textarea.readOnly = false;
    read = "loaded";
  });
}

/** Wire the auto-save. Called from `initUI`, before any panel can be opened. */
export function initSteeringEditor(): void {
  const textarea = $.steeringInput;
  // Locked here rather than in the markup so the module that owns the save owns
  // the lock too: `loadSteeringDoc` is the only thing that opens the box.
  textarea.readOnly = true;
  // debouncedDispatch coalesces rapid keystrokes into a single trailing
  // dispatch after the quiet window (replaces the manual clearTimeout +
  // setTimeout(600) + saveGen pattern). saveGen is no longer needed: the
  // action has scope:"settings", so dispatches serialize (ordered
  // resolution), and the indicator is driven by the action's own
  // lifecycle events below rather than a per-dispatch .then().
  const debouncedSave = debouncedDispatch(saveSteering, { wait: 600 });

  const unsub = subscribeByName("settings.save_steering", (inst) => {
    if (inst.status === "success") {
      showSaved(STEERING_SAVE_KEY);
    } else if (inst.status === "error") {
      showError(STEERING_SAVE_KEY);
    }
  });

  textarea.addEventListener("input", () => {
    showSaving(STEERING_SAVE_KEY);
    debouncedSave({ content: textarea.value });
  });

  // The retry for a read that failed: a focus is the reader's own next attempt to
  // type, and it is the one event a read-only textarea still fires.
  textarea.addEventListener("focus", loadSteeringDoc);

  registerCleanup(() => {
    // Stop touching the indicator (mirrors the original cleanup, which
    // flushed without updating it), then flush any pending edit so an
    // unsaved change still persists on teardown.
    unsub();
    if (debouncedSave.isPending()) {
      void debouncedSave.flush({ content: textarea.value });
    }
  });
}

/** Drop the read state, so one test's document is not the next test's answer.
 *  Production never needs it: the state's lifetime is the page. */
export function _resetSteeringForTest(): void {
  read = "unread";
}
