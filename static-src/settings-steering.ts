// ---------------------------------------------------------------------------
// Settings → Instructions: the global-instructions document, auto-saved.
//
// Two entry points with different LIFETIMES, which is why they are two: the
// listeners must exist before the panel can be typed into, the read must not (it
// was a boot-path file read for a panel nobody had opened). settings.ts wires
// `initSteeringEditor` from `initUI` and `loadSteeringDoc` into the tab loader map.
// ---------------------------------------------------------------------------

import { apiGet } from "./api-client.js";
import { $ } from "./dom.js";
import { showSaving, showSaved, showError, STEERING_SAVE_KEY } from "./save-indicator.js";
import { saveSteering } from "./actions/settings.js";
import { registerCleanup, debouncedDispatch, subscribeByName } from "./actions/index.js";

/** Whether the user has typed into the textarea. The reason `loadSteeringDoc` may
 *  not assign unconditionally: the read is issued by the same tab activation that
 *  makes the textarea typeable, so keystrokes can land while it is in flight — and
 *  they are already in a debounced save. Assigning over them would show the
 *  server's document while the pending save carried the user's, and the next
 *  keystroke would dispatch the server text back. */
let dirty = false;

/** Read the document into its textarea. Fired once, on the Instructions panel's
 *  first activation, via the settings-tabs loader map. */
export function loadSteeringDoc(): void {
  const textarea = $.steeringInput;
  void apiGet<{ content?: string }>("/api/steering").then((d) => {
    if (dirty || d?.content === undefined) {
      return;
    }
    textarea.value = d.content;
  });
}

/** Wire the auto-save. Called from `initUI`, before any panel can be opened. */
export function initSteeringEditor(): void {
  const textarea = $.steeringInput;
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
    dirty = true;
    showSaving(STEERING_SAVE_KEY);
    debouncedSave({ content: textarea.value });
  });

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

/** Drop the typed-into state, so one test's keystrokes do not decide the next
 *  test's read. Production never needs it: the flag's lifetime is the page. */
export function _resetSteeringForTest(): void {
  dirty = false;
}
