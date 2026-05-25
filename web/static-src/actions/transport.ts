// transportAction: factory for transport.send-backed actions. Use for
// SSE-streaming intents (prompt, resolve_pending_change, restore_checkpoint,
// etc.) that flow through the bridge instead of plain HTTP.
//
//   const sendPrompt = transportAction({
//     name: "chat.send_prompt",
//     command: ({ chatID, text }) => ({
//       type: "prompt", chat_id: chatID, payload: { text },
//     }),
//     // No success toast — the agent's response stream IS the feedback.
//     error: "Couldn't send prompt",
//   });
//
//   await sendPrompt.dispatch({ chatID, text });
//
// transport.send returns SendResult { ok, status, error }; transportAction
// converts !ok into a thrown ActionError so the dispatcher's normal
// error branch fires (toast + rollback).
// ---------------------------------------------------------------------------

import { send as transportSend } from "../transport.js";
import type { Command, TypedCommand } from "../transport.js";
import { defineAction } from "./define.js";
import { ActionError } from "./error.js";
import type {
  Action,
  ActionContext,
  ActionDefinition,
} from "./types.js";

/** Caller-facing shape of a transportAction definition. Differs from
 *  the raw ActionDefinition in that `command` replaces `run`. The
 *  result is `void` because transport.send does not return a payload
 *  (the response arrives later via SSE events). */
export interface TransportActionDefinition<TArgs, TOp = unknown>
  extends Omit<ActionDefinition<TArgs, void, TOp>, "run"> {
  /** Build the typed command (or untyped Command for legacy intents)
   *  for this dispatch. Re-evaluated per-dispatch with current args. */
  command: (args: TArgs) => TypedCommand | Command;
}

/**
 * Build an Action from a transport.send command descriptor. The generated
 * `run()` calls transport.send and throws {@link ActionError} on `!r.ok`,
 * so the dispatcher's error branch (toast + rollback) fires consistently.
 *
 * @param def - Transport action definition where `command` replaces `run`.
 * @returns An {@link Action} backed by the SSE transport bridge.
 */
export function transportAction<TArgs, TOp = unknown>(
  def: TransportActionDefinition<TArgs, TOp>,
): Action<TArgs, void> {
  const { command, ...rest } = def;
  return defineAction<TArgs, void, TOp>({
    ...rest,
    run: async (args, signal, ctx?: ActionContext) => {
      const raw = command(args);
      const cmd: Command = ctx?.idempotencyKey !== undefined
        ? { ...raw, payload: { ...("payload" in raw && raw.payload != null ? raw.payload : undefined), idempotency_key: ctx.idempotencyKey } }
        : raw;
      // reportSendState: false — the action framework owns the error
      // surface via toast. Letting transport.send also call
      // setLastError would block the prompt send button for actions
      // unrelated to prompt sending (e.g. respond_permission).
      const r = await transportSend(cmd, { signal, reportSendState: false });
      if (!r.ok) {
        if (signal.aborted || r.code === "cancelled") {
          throw new ActionError("cancelled", { code: "cancelled" });
        }
        if (r.code === "timeout") {
          throw new ActionError(r.error ?? "Request timed out", { status: r.status, code: "timeout" });
        }
        if (r.code === "network") {
          throw new ActionError(r.error ?? "network error", { status: r.status, code: "network" });
        }
        throw new ActionError(r.error ?? `send failed (${String(r.status)})`, {
          status: r.status,
        });
      }
      return undefined;
    },
  });
}
