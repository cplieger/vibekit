// ---------------------------------------------------------------------------
// The activity dot on a SUBAGENT's tab row: one effect, over every open subagent
// tab, painting the delegate's own state.
//
// A delegate was the last kind of work in this app with no activity signal on the
// strip at all. A chat row has `tabStatusFor`, a run row has `runStatusFor` and
// `run-dots.ts`, and a subagent sub-tab had neither — so `createTabEl` painted it
// `""` and `12-tabs.css`'s reservation rule left 16px of hidden slot between the
// nesting arrow and the name, forever. Measured on the shipped stylesheet before
// this existed: 24px arrow-to-name against 8px with the dot removed.
//
// THE FACT IS THE INVOCATION TOOL CALL'S STATUS, and three properties are what
// make it the right input rather than the nearest one:
//
//   - It is a closed union with `completed` and `failed` as distinct members, so a
//     failed delegate is reportable rather than collapsing into one settled state.
//   - It is PERSISTED with its `agent_subtask_id`, so it survives replay and a
//     delegate that finished weeks ago still resolves.
//   - Its frames are ingested with NO active-chat gate (`handlers/messages.ts`
//     answers `tool_call` / `tool_call_update` for any chat with a store row, and
//     every branch of `upsertToolCall` bumps that chat's transcript version), which
//     is what makes the dot correct for a delegate the reader is not looking at —
//     the whole reason the strip carries state.
//
// THE DOT DESCRIBES THE LEAF THE TAB NAMES, never a pipeline roll-up. The row's
// LABEL comes from that same invocation (`tab-materialize.ts` `subagentTabName`),
// so the dot has to agree with the words beside it: a stage's row saying its
// pipeline's state would contradict its own name. That is why this needs
// `findSubagentInvocation` alone — no `groupOf` walk, no `subagentToExec`.
//
// NO `input` STATE, deliberately. A delegate's permission asks are filed under its
// LAUNCHING CHAT (`decision-dock.ts` keys the queue by chat id), and that chat is
// this sub-tab's PARENT row, which already paints `input`. `runPendingAsks` exists
// for a run precisely because a parentless run has no chat row to carry its ask; a
// delegate always has one. A different surface's job, not a gap here.
//
// NO FACTORY SEED, which is the one place this differs from the chat kind rather
// than from the run kind. A chat row's leading dot slot is NOT reserved, so an
// unseeded chat dot leaves the row a frame narrower and shifts its name — hence
// `createTabEl`'s `idle` floor. A sub-tab's slot IS reserved by `12-tabs.css`, so
// the pre-effect frame costs no shift and the seed would only widen
// `SubagentTabOpener` and the seven files that construct one.
//
// IT KEEPS NO STATE OF ITS OWN, and that is the second difference from
// `run-dots.ts`. That module needs a tracked SET because its members arrive from
// SSE frames for runs whose tab may not exist yet; here the tab set IS the
// membership, enumerated per pass, so a closed tab is simply not visited and there
// is nothing to sweep. Measured cost of the scan the pass does per tab, over the
// fattest chat on the live volume (2,745 tool calls in 38 messages, node 26):
// 0.0172 ms for a full miss, 0.0040 ms for an early hit. At three open subagent
// tabs and 60 transcript bumps a second that is ~0.3% of one core, so the single
// effect is a recorded measurement rather than an assumption. If that ever stops
// holding, the escalation is the per-tab registry `chat.ts` already uses.
// ---------------------------------------------------------------------------

import { effect } from "@cplieger/reactive";
import { openSubagentRefs, setTabStatus, tabIdFor } from "./tabs.js";
import { get, messagesVersionOf, subagentStatusFor } from "./store.js";
import { findSubagentInvocation } from "./subagent-slice.js";
import { parseSubagentRef } from "./tab-materialize.js";

function repaint(): void {
  for (const ref of openSubagentRefs()) {
    const { chatID, subtaskID } = parseSubagentRef(ref);
    if (chatID === "") {
      // A malformed ref, which a persisted collection can hand us on any device.
      // Nothing will ever make it paint and no transcript names it, so there is
      // no signal worth subscribing to for it — the tab-set dependency covers the
      // only thing that can change about it, which is the row going away.
      continue;
    }
    // TRACKED, and read before anything below can bail: this is the dependency
    // that repaints a `working` delegate as `done` when its own
    // `tool_call_update` lands, with no tab mutation behind it. A pass that finds
    // nothing resident must stay subscribed, or the delegate's first frame would
    // never reach the row.
    void messagesVersionOf(chatID).value;
    // Both this and `openSubagentRefs` above walk the projection's own array in
    // one synchronous pass, so the id resolves for every ref that pass produced.
    // `setTabStatus` is a no-op for an unknown id in any case, which is why there
    // is no branch here for a state that cannot arrive.
    const id = tabIdFor("subagent", ref);
    const invocation = findSubagentInvocation(get(chatID)?.messages ?? [], subtaskID);
    setTabStatus(id, subagentStatusFor(invocation?.status));
  }
}

/** Wire the effect. Called from the composition root, not at import: an effect
 *  running at module load would paint against a tab strip that has not been
 *  restored yet, and the tab-set dependency is what picks a row up once it
 *  exists. */
export function installSubagentDotSubscriber(): void {
  effect(() => {
    // Subscribes to the tab SET (openSubagentRefs) and, inside the loop, to each
    // launching chat's own transcript version. Those two are the whole input: a
    // subagent tab landing or leaving, and the delegate's invocation changing.
    repaint();
  });
}
