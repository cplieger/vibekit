// ---------------------------------------------------------------------------
// Confirm dialog: reusable confirmation for destructive actions.
// Uses the native <dialog> element with focus trap and @starting-style.
//
// Usage:
//   const ok = await confirm("Delete this file?", "Delete", "destructive");
//   if (ok) doDelete();
// ---------------------------------------------------------------------------

import { trapFocus } from "./focus-trap.js";

let dialogEl: HTMLDialogElement | null = null;
let prevResolve: ((v: boolean) => void) | null = null;
let prevAC: AbortController | null = null;
let prevRelease: (() => void) | null = null;

function ensureDialog(): HTMLDialogElement {
  if (dialogEl !== null) {
    return dialogEl;
  }
  dialogEl = document.createElement("dialog");
  dialogEl.className = "vk-confirm-dialog";
  // The <dialog> element gets implicit role="dialog". For destructive
  // variants we upgrade to role="alertdialog" at show-time so AT
  // announces the message with the urgency it deserves.
  dialogEl.innerHTML = `
    <p class="vk-confirm-msg" id="vk-confirm-msg"></p>
    <div class="vk-confirm-actions">
      <button type="button" class="vk-confirm-cancel btn-small">Cancel</button>
      <button type="button" class="vk-confirm-ok btn-small confirm-danger">Confirm</button>
    </div>`;
  // aria-labelledby links the dialog accname to the message paragraph
  // so SR users hear the message when the dialog opens.
  dialogEl.setAttribute("aria-labelledby", "vk-confirm-msg");
  document.body.appendChild(dialogEl);
  return dialogEl;
}

/** Show a confirmation dialog. Returns true if the user confirms. */
export function confirm(
  message: string,
  confirmLabel = "Confirm",
  variant: "destructive" | "normal" = "normal",
): Promise<boolean> {
  return new Promise((resolve) => {
    const d = ensureDialog();
    const msg = d.querySelector(".vk-confirm-msg")!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
    const okBtn = d.querySelector(".vk-confirm-ok")!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
    const cancelBtn = d.querySelector<HTMLButtonElement>(".vk-confirm-cancel")!; // eslint-disable-line @typescript-eslint/no-non-null-assertion

    // Upgrade to alertdialog for destructive prompts so screen readers
    // treat them as urgent / interruptive.
    if (variant === "destructive") {
      d.setAttribute("role", "alertdialog");
    } else {
      d.removeAttribute("role");
    }

    msg.textContent = message;
    okBtn.textContent = confirmLabel;
    okBtn.className =
      variant === "destructive"
        ? "vk-confirm-ok btn-small confirm-danger"
        : "vk-confirm-ok btn-small confirm-allow";

    // Preempt any prior confirmation — treat as cancelled.
    if (prevRelease) {
      prevRelease();
      prevRelease = null;
    }
    if (prevResolve) {
      prevResolve(false);
      prevResolve = null;
    }
    if (prevAC) {
      prevAC.abort();
      prevAC = null;
    }

    if (d.open) {
      d.close();
    }
    d.showModal();
    const release = trapFocus(d);
    // WAI-ARIA alertdialog: focus the least-destructive action so
    // keyboard users don't accidentally confirm a dangerous operation.
    if (variant === "destructive") {
      cancelBtn.focus();
    }
    const ac = new AbortController();
    const { signal } = ac;
    prevResolve = resolve;
    prevAC = ac;
    prevRelease = release;

    function close(result: boolean): void {
      ac.abort();
      release();
      d.close();
      prevResolve = null;
      prevAC = null;
      prevRelease = null;
      resolve(result);
    }

    okBtn.addEventListener(
      "click",
      () => {
        close(true);
      },
      { signal },
    );
    cancelBtn.addEventListener(
      "click",
      () => {
        close(false);
      },
      { signal },
    );
    d.addEventListener(
      "keydown",
      (e: KeyboardEvent) => {
        if (e.key === "Escape") {
          e.preventDefault();
          close(false);
        }
      },
      { signal },
    );
  });
}
