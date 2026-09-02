// ---------------------------------------------------------------------------
// PR merge dialog: the confirmation step of Merge and Merge-when-green,
// carrying the squash/rebase chooser. It replaced a bare confirm() that
// restated the button's own label while the backend defaulted to a merge
// commit — a method most repos here disallow, so a green PR's merge failed
// with nothing in the dialog to change.
//
// The default selection is the last method picked, read from the server's
// settings (last_merge_method) so it follows the user across devices, and
// persisted on confirm. Falls back to rebase when nothing was ever picked.
// ---------------------------------------------------------------------------

import { createDialog, type DialogController } from "@cplieger/ui-primitives/dialog";

import { loadSettings } from "./persist.js";
import { patchAppSettings } from "./actions/settings.js";
import type { MergeMethod } from "./actions/git-prs.js";

/** What the caller renders into the dialog chrome. */
export interface MergeDialogOpts {
  title: string;
  message: string;
  confirmLabel: string;
}

function isMergeMethod(v: unknown): v is MergeMethod {
  return v === "squash" || v === "rebase";
}

/** Resolve the method to preselect: the server-remembered last pick, else
 *  rebase (the method every repo this instance manages allows). */
async function defaultMethod(): Promise<MergeMethod> {
  const s = await loadSettings();
  const v = s?.last_merge_method;
  return isMergeMethod(v) ? v : "rebase";
}

// Dialog controller, created once per dialog ELEMENT and reused across opens
// so the backdrop/Escape listeners aren't stacked (same shape as the New PR
// dialog's controller in git-prs-tab.ts). Keyed to the element rather than a
// bare module-level slot: a stale controller drives a detached dialog, whose
// close event then never reaches the live one.
const dialogCtls = new WeakMap<HTMLDialogElement, DialogController>();

function controllerFor(dlg: HTMLDialogElement): DialogController {
  let ctl = dialogCtls.get(dlg);
  if (ctl === undefined) {
    ctl = createDialog(dlg, { closeOnBackdrop: true, closeOnEscape: true });
    dialogCtls.set(dlg, ctl);
  }
  return ctl;
}

/** Open the merge dialog. Resolves the chosen method on confirm, null on
 *  cancel / Escape / backdrop. Persists a changed choice as the next
 *  default before resolving. */
export async function openMergeMethodDialog(opts: MergeDialogOpts): Promise<MergeMethod | null> {
  const dlg = document.getElementById("pr-merge-dialog") as HTMLDialogElement | null;
  if (dlg === null) {
    return null;
  }
  const ctl = controllerFor(dlg);

  const title = document.getElementById("pr-merge-title");
  const message = document.getElementById("pr-merge-message");
  const confirmBtn = document.getElementById("pr-merge-confirm-btn") as HTMLButtonElement | null;
  if (title === null || message === null || confirmBtn === null) {
    console.error("merge dialog missing required elements");
    return null;
  }

  title.textContent = opts.title;
  message.textContent = opts.message;

  const initial = await defaultMethod();
  const radios = [...dlg.querySelectorAll<HTMLInputElement>('input[name="pr-merge-method"]')];
  for (const r of radios) {
    r.checked = r.value === initial;
  }

  return await new Promise<MergeMethod | null>((resolve) => {
    let settled = false;
    const settle = (value: MergeMethod | null): void => {
      if (settled) {
        return;
      }
      settled = true;
      resolve(value);
    };

    // Drop any prior open's listeners by cloning the buttons (the New PR
    // dialog's convention for a reused static <dialog>).
    const freshConfirm = confirmBtn.cloneNode(true) as HTMLButtonElement;
    confirmBtn.replaceWith(freshConfirm);
    freshConfirm.textContent = opts.confirmLabel;
    freshConfirm.addEventListener("click", () => {
      const picked = radios.find((r) => r.checked)?.value;
      const method = isMergeMethod(picked) ? picked : initial;
      if (method !== initial) {
        // Remember the pick as the next default. Fire-and-forget: a failed
        // save costs the memory, never the merge.
        void patchAppSettings.dispatch({ body: { last_merge_method: method } });
      }
      settle(method);
      ctl.close();
    });
    for (const btn of dlg.querySelectorAll<HTMLButtonElement>("[data-pr-merge-close]")) {
      const fresh = btn.cloneNode(true) as HTMLButtonElement;
      btn.replaceWith(fresh);
      fresh.addEventListener("click", () => {
        ctl.close();
      });
    }
    // Escape / backdrop / any close path resolves null unless the confirm
    // already settled.
    dlg.addEventListener(
      "close",
      () => {
        settle(null);
      },
      { once: true },
    );

    ctl.open();
  });
}
