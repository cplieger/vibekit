// ---------------------------------------------------------------------------
// Composer resize: drag the message box taller, persist it, double-click to
// reset.
//
// The mechanism is the shell panel's, copied deliberately rather than
// reinvented: one pointer-capture path for mouse and touch, `e.isPrimary` so a
// second finger cannot hijack the drag, `hasPointerCapture` as the move/end
// guard instead of a boolean flag, both `pointerup` AND `pointercancel` wired to
// the same end, a persist on END rather than per move, a re-clamp on restore, a
// `role="separator"` keyboard path, and `touch-action: none` plus a ~24px hit
// target in CSS. Those are the five details a from-scratch version gets wrong.
//
// Three things are NOT the shell's, each for a reason:
//
//   - It moves a CEILING, not a height. `[id="prompt-input"]` auto-grows between
//     min-height and max-height via `field-sizing: content`, so writing a height
//     would make a one-line message occupy the dragged size. The drag writes
//     --composer-h, which max-height consumes. `resize: none` stays on the
//     textarea: the native grabber cannot be clamped or persisted, so this
//     replaces it rather than sitting beside it.
//   - The upper clamp is measured against the CHAT AREA, not the viewport. The
//     shell's `innerHeight * 0.8` works because nothing shares its axis; the
//     composer shares the bottom bar with the decision dock and two pill rows,
//     and sits above whatever the shell panel is taking. Measuring the space the
//     bar does not already use, and reserving a floor for the transcript, is the
//     honest bound — a viewport fraction would let a tall composer plus an open
//     elicitation form push the transcript to nothing.
//   - Double-click resets, and so does Home. The shell has neither; a
//     mouse-only reset would be an affordance a keyboard user cannot reach, and
//     Home is the conventional reset for a role=separator.
//
// The clamp is recomputed at every interaction and at restore rather than
// watched: a persisted ceiling set on a desktop is re-clamped against the phone
// it is next opened on, which is where the mismatch actually shows up. The one
// exception is a boot-time restore against a view that has no layout yet, where
// there is nothing to clamp against and no interaction to wait for — that parks
// the value and watches the chat area until it can measure, once.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import * as uiState from "./ui-state.js";

/** Floor for the box: one row of text plus its padding, matching the
 *  textarea's own min-height (2.5rem). Below this the box cannot show what is
 *  being typed. */
const COMPOSER_MIN_H = 40;

/** Transcript that must survive the tallest composer (8rem): enough for a turn
 *  header and the start of its answer. Without a floor the drag can hide the
 *  conversation it is a reply to. */
const TRANSCRIPT_FLOOR = 128;

/** Keyboard resize step (2rem) per ArrowUp/ArrowDown, same as the shell's. */
const COMPOSER_KEY_STEP = 32;

/** Upper clamp: the space the bottom bar does not already need, less the
 *  transcript floor.
 *
 *  `formH - inputH` is the bar's other content measured rather than assumed —
 *  the dock, the attachment row, the steer chip row and the pill row — so a
 *  composer cannot be dragged over an open elicitation form. Returns 0 when the
 *  chat area has no layout yet (a hidden view at boot), which callers read as
 *  "unknown, leave the value alone" rather than clamping to the floor. */
function composerMaxH(): number {
  const areaH = $.chatArea.getBoundingClientRect().height;
  if (areaH <= 0) {
    return 0;
  }
  const formH = $.promptForm.getBoundingClientRect().height;
  const inputH = $.promptInput.getBoundingClientRect().height;
  const barChrome = Math.max(formH - inputH, 0);
  return Math.max(areaH - barChrome - TRANSCRIPT_FLOOR, COMPOSER_MIN_H);
}

/** Clamp `h` between the floor and `maxH`. A `maxH` of 0 is UNKNOWN, not zero:
 *  the requested ceiling passes through with only the floor applied.
 *
 *  Substituting the CSS default here was a bug, and it contradicted the
 *  measurement's own contract: a 500px ceiling restored while the chat view is
 *  still hidden at boot became 200, silently, and nothing measured again
 *  afterwards — every later interaction starts from the box's rendered height, so
 *  the persisted value was gone for the session. What an unknown ceiling earns is
 *  a re-clamp once the area reports a size (see reclampWhenMeasurable), not a
 *  guess that overwrites the user's number. */
function clampComposerH(h: number, maxH: number): number {
  const floored = Math.max(h, COMPOSER_MIN_H);
  return maxH > 0 ? Math.min(floored, maxH) : floored;
}

/** A ceiling applied against an unknown measurement, waiting for a real one.
 *  0 = nothing pending. */
let pendingH = 0;
let pendingObserver: ResizeObserver | undefined;

/** Watch the chat area until it has layout, then re-clamp the parked ceiling.
 *
 *  The module otherwise measures at every interaction and at restore rather than
 *  watching, and that stays true for the case the stance was written for — a
 *  ceiling set on a desktop being read on a phone re-clamps on the next
 *  interaction. It cannot answer THIS case: a boot-time restore against a hidden
 *  view has no interaction to wait for and no measurement to clamp against, so
 *  the one question left is "when does the area first have a size", which is
 *  exactly what a ResizeObserver answers. One-shot: it disconnects as soon as it
 *  can measure. */
function reclampWhenMeasurable(): void {
  if (pendingObserver !== undefined || typeof ResizeObserver === "undefined") {
    return;
  }
  pendingObserver = new ResizeObserver(() => {
    if (composerMaxH() <= 0) {
      return; // still no layout — keep watching
    }
    const want = pendingH;
    dropPending();
    if (want > 0) {
      applyComposerH(want);
    }
  });
  pendingObserver.observe($.chatArea);
}

function dropPending(): void {
  pendingH = 0;
  pendingObserver?.disconnect();
  pendingObserver = undefined;
}

/** Clamp (and round) a ceiling, then apply it via the --composer-h custom
 *  property the textarea's max-height consumes. Returns the applied value. */
export function applyComposerH(h: number): number {
  const maxH = composerMaxH();
  const clamped = Math.round(clampComposerH(h, maxH));
  if (maxH > 0) {
    // Measured, so this value is final and supersedes anything parked.
    dropPending();
  } else {
    pendingH = clamped;
    reclampWhenMeasurable();
  }
  $.promptBox.style.setProperty("--composer-h", `${String(clamped)}px`);
  return clamped;
}

/** Drop back to the CSS default (12.5rem) and forget the persisted value.
 *  Removing the property rather than writing the default keeps one definition of
 *  that number, in the stylesheet. */
function resetComposerH(): void {
  // Ahead of the write: a parked ceiling that outlived the reset would be applied
  // by the re-clamp and undo it.
  dropPending();
  $.promptBox.style.removeProperty("--composer-h");
  uiState.save({ composer_h: 0 });
}

let initialized = false;

/** Wire the handle. Called once from app.ts, beside the composer's other two
 *  halves (see setupInput's note on why they are peers). */
export function initComposerResize(): void {
  if (initialized) {
    return;
  }
  initialized = true;
  const resizeEl = $.composerResize;

  // The markup ships a bare div; the a11y contract is set here so index.html
  // stays untouched. Same split as the shell handle's.
  resizeEl.setAttribute("role", "separator");
  resizeEl.setAttribute("aria-orientation", "horizontal");
  resizeEl.setAttribute("aria-label", "Resize the message box");
  resizeEl.tabIndex = 0;

  let startY = 0;
  let startH = 0;
  let lastH = 0;

  resizeEl.addEventListener("pointerdown", (e: PointerEvent) => {
    if (!e.isPrimary) {
      return;
    }
    startY = e.clientY;
    // The CURRENT rendered height, not the stored ceiling: with a short message
    // the box sits well below its ceiling, and starting from the ceiling would
    // make the box jump on the first pointermove.
    startH = $.promptInput.getBoundingClientRect().height;
    lastH = Math.round(startH);
    resizeEl.setPointerCapture(e.pointerId);
    resizeEl.classList.add("dragging");
    // Suspend the box's transitions so it tracks the pointer 1:1 rather than
    // easing behind it (see .prompt-box.resizing).
    $.promptBox.classList.add("resizing");
    e.preventDefault();
  });

  resizeEl.addEventListener("pointermove", (e: PointerEvent) => {
    if (!resizeEl.hasPointerCapture(e.pointerId)) {
      return;
    }
    // The handle is on the composer's TOP edge and the bar is bottom-docked, so
    // dragging up (a smaller clientY) makes the box taller.
    lastH = applyComposerH(startH + (startY - e.clientY));
  });

  const end = (e: PointerEvent): void => {
    if (!resizeEl.hasPointerCapture(e.pointerId)) {
      return;
    }
    resizeEl.releasePointerCapture(e.pointerId);
    resizeEl.classList.remove("dragging");
    $.promptBox.classList.remove("resizing");
    uiState.save({ composer_h: lastH });
  };
  resizeEl.addEventListener("pointerup", end);
  resizeEl.addEventListener("pointercancel", end);

  // Double-click resets. Not on the shell handle, so it is genuinely new here;
  // Home is its keyboard equal below.
  resizeEl.addEventListener("dblclick", (e: MouseEvent) => {
    e.preventDefault();
    resetComposerH();
  });

  resizeEl.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key === "Home") {
      e.preventDefault();
      resetComposerH();
      return;
    }
    if (e.key !== "ArrowUp" && e.key !== "ArrowDown") {
      return;
    }
    e.preventDefault();
    const cur = $.promptInput.getBoundingClientRect().height;
    const next = applyComposerH(
      cur + (e.key === "ArrowUp" ? COMPOSER_KEY_STEP : -COMPOSER_KEY_STEP),
    );
    uiState.save({ composer_h: next });
  });

  restoreComposerH();
}

/** Re-apply the persisted ceiling, re-clamped against the CURRENT layout: the
 *  value may have been set on a wider window or another device. */
function restoreComposerH(): void {
  const saved = uiState.load().composer_h;
  if (saved > 0) {
    applyComposerH(saved);
  }
}
