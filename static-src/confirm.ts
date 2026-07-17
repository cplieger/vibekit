// ---------------------------------------------------------------------------
// Confirm dialog — adopted from @cplieger/ui-primitives.
//
// The hand-rolled native-<dialog> confirmation was replaced by the library's
// `ask` primitive (boolean shape), wrapped here to preserve vibekit's
// positional signature `confirm(message, confirmLabel?, variant?)` so the
// ~dozen call sites (editor-core, files, tools, git-*, forge-auth, mcp-ui,
// tabs, …) are unchanged.
//
// Behavior is preserved: a reused native <dialog> via showModal() (platform
// focus trap + focus restore), Escape / backdrop / preemption-by-a-newer-call
// all resolve `false`, and the destructive variant upgrades to
// role="alertdialog" and focuses Cancel so a keyboard user can't confirm by
// accident. Visuals live in the .uip-ask skin (css/04-uip-skin.css), ported
// 1:1 from the old .vk-confirm-dialog (buttons reproduce .btn-small +
// .confirm-allow / .confirm-danger).
//
// Usage:
//   const ok = await confirm("Delete this file?", "Delete", "destructive");
//   if (ok) doDelete();
// ---------------------------------------------------------------------------

import { ask } from "@cplieger/ui-primitives/ask";

/** Show a confirmation dialog. Resolves true if the user confirms, false on
 *  cancel / Escape / backdrop click / preemption by a later confirm(). */
export function confirm(
  message: string,
  confirmLabel = "Confirm",
  variant: "destructive" | "normal" = "normal",
): Promise<boolean> {
  return ask(message, { confirmLabel, variant });
}
