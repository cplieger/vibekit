// Actions for the shell panel.
// ---------------------------------------------------------------------------

import { apiAction } from "./index.js";

/**
 * shell.restart — kill the PTY and install a fresh one.
 *
 * The panel's only server-side mutation. It exists because terminal.Handler is
 * single-use on the server: a child that exits, or a foreground process that
 * wedges, leaves a session that can never start again, and the screen clear this
 * button replaced could not help with either.
 *
 * No success toast: the terminal repainting with a new prompt is the feedback.
 * The caller confirms first, since this destroys whatever was running.
 */
// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void as the args type for an action taking none, per notify.ts
export const restartShell = apiAction<void, { ok?: boolean }>({
  name: "shell.restart",
  request: () => ({ method: "POST", path: "/api/shell/restart" }),
  success: false,
  error: "Shell restart failed",
});
