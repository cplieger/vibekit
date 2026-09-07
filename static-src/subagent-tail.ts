// ---------------------------------------------------------------------------
// subagent-tail: ONE delegate's last few output lines, projected out of the
// launching chat's messages.
//
// The card's tail answers "which of these delegates is progressing" while they
// work. It used to be a DOM MIRROR: the transcript rendered the delegate's whole
// output inside the card, a MutationObserver on that body harvested the last
// three lines per animation frame, and the render existed for nothing else —
// N parallel delegates meant N live markdown streams plus N per-frame backwards
// DOM walks. The transcript renders none of it now (`messages-blocks.ts`
// `placeBlock` drops a delegate's blocks, the same way it drops a workflow
// step's), so the tail is derived from the blocks themselves.
//
// `subagent-slice.ts` is the precedent and this is its cheap sibling: the same
// query over the same `agent_subtask_id` stamp, answering three lines instead of
// a whole transcript. Pure and DOM-free over `readonly Message[]`, so the caller
// owns how it reads the store.
//
// TWO COSTS ARE DELIBERATE, because neither has a cheaper honest form:
//
//   - The walk runs BACKWARDS and stops as soon as it has enough, so its cost is
//     the tail rather than the delegate's whole output. It runs per streamed
//     delta, and a delegate's report can be tens of kilobytes.
//   - `bindSubagentTail` subscribes to the chat's transcript version as well as
//     to the delegate's own block signals, because a NEW block arriving is the
//     one change no per-block signal can carry — the signal for a block nobody
//     has read yet does not exist. So a sibling delegate's delta does re-run this
//     walk; what it does not do is repaint, which the binding's own guard stops.
// ---------------------------------------------------------------------------

import type { Block, Message, ToolCall } from "./types.js";
import { effect, touch } from "@cplieger/reactive";
import { get, messagesVersionOf } from "./store.js";
import { ensureBlockTextSig, ensureBlockThinkingSig } from "./store-signals.js";
import { isInternalToolTitle, isSubagentInvocation } from "./tool-schema.js";

/** How many trailing lines a card's tail shows. */
export const TAIL_LINES = 3;

/** One block's per-(message, block-index) streaming signal, for a caller that
 *  wants the delegate's prose to arrive in the tick its delta does. */
export interface TailSource {
  readonly messageID: string;
  readonly blockIndex: number;
  /** Whether the growth lands in `blockThinkingSigs` rather than `blockTextSigs`. */
  readonly thinking: boolean;
  /** The block's text as of this read, so a signal minted from it starts truthful. */
  readonly full: string;
}

export interface SubagentTail {
  /** The trailing lines, oldest first, at most `want` of them. */
  readonly lines: string[];
  /** The blocks the lines came FROM, which are exactly the blocks whose growth can
   *  change them: the walk stops as soon as it has `want` lines, so a block above
   *  the ones it took can never reach the tail. */
  readonly sources: TailSource[];
}

/** The last `want` non-empty lines of `s`, in order.
 *
 *  Backwards from the end, so a 40KB report costs its last three lines rather
 *  than a `split("\n")` over the whole of it on every delta. */
function tailOf(s: string, want: number): string[] {
  const out: string[] = [];
  let end = s.length;
  while (end > 0 && out.length < want) {
    const nl = s.lastIndexOf("\n", end - 1);
    const line = s
      .slice(nl + 1, end)
      .replace(/\s+/gu, " ")
      .trim();
    if (line !== "") {
      out.unshift(line);
    }
    end = nl;
  }
  return out;
}

/** The line a TOOL block contributes: its title, and nothing else.
 *
 *  The card's claim line (`Grep Search spaghetti`) is not reproduced here on
 *  purpose — that string is `tool-card.ts`'s presentation of a call's input, and a
 *  second derivation of it would be a second owner. The title is what moves as the
 *  delegate works, which is the whole question the tail answers.
 *
 *  Empty for the delegate's own INVOCATION, which the card's header already names,
 *  and for a call the transcript itself renders nothing for. */
function toolLine(block: Block, tools: readonly ToolCall[]): string[] {
  const tc = tools.find((c) => c.id === block.tool_call_id);
  if (tc === undefined || isSubagentInvocation(tc) || isInternalToolTitle(tc.title)) {
    return [];
  }
  return tailOf(tc.title, 1);
}

/** Project one delegate's trailing output out of a conversation.
 *
 *  Walks EVERY message backwards rather than stopping at the newest one holding
 *  the delegate, because a turn split by a mid-turn model switch puts one
 *  delegate's blocks in two assistant messages. An empty subtask id is nobody's
 *  delegate and answers nothing. */
export function subagentTail(
  messages: readonly Message[],
  subtaskID: string,
  want: number = TAIL_LINES,
): SubagentTail {
  const lines: string[] = [];
  const sources: TailSource[] = [];
  if (subtaskID === "" || want <= 0) {
    return { lines, sources };
  }
  for (let mi = messages.length - 1; mi >= 0 && lines.length < want; mi--) {
    const m = messages[mi];
    if (m === undefined) {
      continue;
    }
    const blocks = m.blocks ?? [];
    for (let i = blocks.length - 1; i >= 0 && lines.length < want; i--) {
      const block = blocks[i];
      if (block === undefined || (block.agent_subtask_id ?? "") !== subtaskID) {
        continue;
      }
      const thinking = block.type === "thinking";
      if (block.type === "tool_use") {
        lines.unshift(...toolLine(block, m.tool_calls ?? []));
        continue;
      }
      const full = (thinking ? block.thinking : block.text) ?? "";
      sources.unshift({ messageID: m.id, blockIndex: i, thinking, full });
      lines.unshift(...tailOf(full, want - lines.length));
    }
  }
  return { lines, sources };
}

/** Repaint ONE delegate's tail whenever its own output grows. Returns the
 *  disposer.
 *
 *  `ensure` rather than `get` on the block signals, which is the opposite of what
 *  `subagent-view.ts` does and for the reason stated there: minting a signal
 *  silences `store.appendChunk`'s full-repaint fallback, which would freeze a
 *  MOUNTED bubble reading the same block. Nothing mounts a delegate's blocks in
 *  the transcript any more, so minting them here is what makes a delta arrive at
 *  all — without it the store has no fine-grained sink and no reason to repaint.
 *
 *  The guard is what keeps a sibling's delta from touching this card: the walk
 *  re-runs, the paint does not. */
export function bindSubagentTail(
  chatID: string,
  subtaskID: string,
  paint: (lines: string[]) => void,
): () => void {
  let painted: string | undefined;
  return effect(() => {
    touch(messagesVersionOf(chatID));
    const { lines, sources } = subagentTail(get(chatID)?.messages ?? [], subtaskID);
    for (const s of sources) {
      touch(
        s.thinking
          ? ensureBlockThinkingSig(s.messageID, s.blockIndex, s.full)
          : ensureBlockTextSig(s.messageID, s.blockIndex, s.full),
      );
    }
    const next = lines.join("\n");
    if (next === painted) {
      return;
    }
    painted = next;
    paint(lines);
  });
}
