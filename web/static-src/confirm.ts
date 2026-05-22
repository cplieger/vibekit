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

function ensureDialog(): HTMLDialogElement {
  if (dialogEl !== null) return dialogEl;
  dialogEl = document.createElement("dialog");
  dialogEl.className = "vk-confirm-dialog";
  dialogEl.innerHTML = `
    <p class="vk-confirm-msg"></p>
    <div class="vk-confirm-actions">
      <button type="button" class="vk-confirm-cancel btn-small">Cancel</button>
      <button type="button" class="vk-confirm-ok btn-small confirm-danger">Confirm</button>
    </div>`;
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
    const msg = d.querySelector(".vk-confirm-msg") as HTMLParagraphElement;
    const okBtn = d.querySelector(".vk-confirm-ok") as HTMLButtonElement;
    const cancelBtn = d.querySelector(".vk-confirm-cancel") as HTMLButtonElement;

    msg.textContent = message;
    okBtn.textContent = confirmLabel;
    okBtn.className = variant === "destructive"
      ? "vk-confirm-ok btn-small confirm-danger"
      : "vk-confirm-ok btn-small confirm-allow";

    d.showModal();
    const release = trapFocus(d);
    const ac = new AbortController();
    const { signal } = ac;

    function close(result: boolean): void {
      ac.abort();
      release();
      d.close();
      resolve(result);
    }

    okBtn.addEventListener("click", () => close(true), { signal });
    cancelBtn.addEventListener("click", () => close(false), { signal });
    d.addEventListener("keydown", (e: KeyboardEvent) => {
      if (e.key === "Escape") { e.preventDefault(); close(false); }
    }, { signal });
  });
}
